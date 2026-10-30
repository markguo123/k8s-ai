package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// dirPerm/dirFilePerm 收紧到组可读：根目录默认落在主机盘或 PVC 上，装的是巡检报告
	// 与附件，不需要对外全局可读。
	dirPerm     = 0o750
	dirFilePerm = 0o640
	// dirCopyBuffer 是流式拷贝的缓冲：64KiB 足够让本地盘写入不被 syscall 数量卡住，
	// 又不会给每个 Put 挂上大缓冲。
	dirCopyBuffer = 64 << 10
)

// dirStore 是 driver=dir 后端：正文落在本地目录，key 直接映射成根目录下的相对路径
// （方案 15 §10）。它面向单机 appliance、离线交付与 CLI 缓存，不跨机共享，因此 Ensure
// 会在根目录上抢一把进程间独占锁，抢不到就起不来（§11.3）。
//
// 除 key 之外不存任何旁路元数据：体积取文件长度，摘要取 key 尾段的前 64 位十六进制
// （内容寻址不变量，ValidateKey 保证），media type 由尾段扩展名推导。要改 media type
// 声明值请走 db/s3 后端的元数据列。
type dirStore struct {
	root     string // 已 Abs+Clean 的根目录，路径包含性检查的基准
	prefix   string // 已 NormalizePrefix
	maxBytes int64
	lock     *dirLock
}

var (
	_ Store      = (*dirStore)(nil)
	_ Maintainer = (*dirStore)(nil)
	_ Probe      = (*dirStore)(nil)
)

// NewDir 构造目录后端，不碰文件系统：建目录与抢锁都在 Ensure（启动期 fail-fast，
// §11.2），这样配置校验与单测都能拿到一个「未启用」的后端而不产生副作用。
//
// root 为空是配置错误（ErrKeyInvalid 家族，装配层能直接点名 platform.blobs.dir）；
// prefix 为空取 DefaultPrefix；maxBytes <= 0 取 DefaultMaxObjectBytes。
func NewDir(root, prefix string, maxBytes int64) (Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("%w: dir backend needs platform.blobs.dir", ErrKeyInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve dir root %q: %w", ErrKeyInvalid, root, err)
	}
	normalized, err := NormalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxObjectBytes
	}
	return &dirStore{root: filepath.Clean(abs), prefix: normalized, maxBytes: maxBytes}, nil
}

// Driver 返回 dir。
func (d *dirStore) Driver() string { return DriverDir }

// Ensure 建根目录与临时目录，并抢根目录独占锁。重复调用安全（锁已持有即直接返回）。
func (d *dirStore) Ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(d.root, dirTempSubdir), dirPerm); err != nil {
		return fmt.Errorf("%w: prepare dir root %s: %w", ErrUnavailable, d.root, err)
	}
	if d.lock != nil {
		return nil
	}
	lock, err := lockDir(d.root)
	if err != nil {
		return err
	}
	d.lock = lock
	return nil
}

// Probe 只回答「现在能不能读这个根目录」，不建、不写、不抢锁（§12 对账与存储健康端点用）。
func (d *dirStore) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Stat(d.root)
	if err != nil {
		return fmt.Errorf("%w: stat dir root %s: %w", ErrUnavailable, d.root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: dir root %s is not a directory", ErrUnavailable, d.root)
	}
	return nil
}

// Close 释放根目录锁。对象文件不动。
func (d *dirStore) Close() error {
	if d.lock == nil {
		return nil
	}
	err := d.lock.Unlock()
	d.lock = nil
	return err
}

// Put 流式写入：临时文件 + fsync + rename 提交，任何一步失败都不留半成品。
//
// 临时文件必须与正文同盘（放在根目录下的 .tmp），否则 rename 退化成跨盘拷贝，既不是
// 原子操作也留不下「要么完整要么没有」的保证。key 依赖实际摘要，所以只能先流式落临时
// 名、读完再 rename 到内容寻址名；同内容重复 Put 落到同一个名字上，天然幂等。
func (d *dirStore) Put(ctx context.Context, spec Spec, r io.Reader) (Ref, error) {
	ext, err := validateSpecHead(spec)
	if err != nil {
		return Ref{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	tmp, err := os.CreateTemp(filepath.Join(d.root, dirTempSubdir), "put-")
	if err != nil {
		return Ref{}, fmt.Errorf("%w: create temp file under %s: %w", ErrUnavailable, d.root, err)
	}
	tempName := tmp.Name()
	fail := func(ferr error) (Ref, error) {
		_ = tmp.Close()
		_ = os.Remove(tempName)
		return Ref{}, ferr
	}

	stream := NewStreamHash(r, d.maxBytes)
	buf := make([]byte, dirCopyBuffer)
	for {
		// 每轮都看一次 ctx：客户端断开或超时后要立刻停手，而不是把 32MiB 拷完再报错。
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return fail(fmt.Errorf("%w: write temp file: %w", ErrUnavailable, werr))
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			// ErrTooLarge 从这里原样透出：体积闸门在读到超出的那一刻就断，不先收满内存或磁盘。
			return fail(rerr)
		}
	}
	sha, size := stream.SHA256(), stream.Size()

	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("%w: sync temp file: %w", ErrUnavailable, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("%w: close temp file: %w", ErrUnavailable, err))
	}
	key, err := BuildKey(d.prefix, spec.Category, spec.Scope, sha, ext)
	if err != nil {
		_ = os.Remove(tempName)
		return Ref{}, err
	}
	target, err := d.pathFor(key)
	if err != nil {
		_ = os.Remove(tempName)
		return Ref{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
		_ = os.Remove(tempName)
		return Ref{}, fmt.Errorf("%w: create object directory: %w", ErrUnavailable, err)
	}
	if err := os.Rename(tempName, target); err != nil {
		_ = os.Remove(tempName)
		return Ref{}, fmt.Errorf("%w: commit object %s: %w", ErrUnavailable, key, err)
	}
	ref := d.refOf(key, size, time.Time{})
	if fi, serr := os.Stat(target); serr == nil {
		ref.UpdatedAt = fi.ModTime()
	}
	return ref, nil
}

// Get 打开正文流；media type/体积/摘要都从磁盘与 key 现推。
func (d *dirStore) Get(ctx context.Context, key string) (io.ReadCloser, Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, Ref{}, err
	}
	path, err := d.pathFor(key)
	if err != nil {
		return nil, Ref{}, err
	}
	fi, err := d.objectInfo(ctx, path, key)
	if err != nil {
		return nil, Ref{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Ref{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, Ref{}, fmt.Errorf("%w: open object %s: %w", ErrUnavailable, key, err)
	}
	return f, d.refOf(key, fi.Size(), fi.ModTime()), nil
}

// Stat 只取元数据（不打开正文）。正文不在位时返回 ErrNotFound，好让下载路径把元数据行
// 标成 missing（§7）；根目录不可用返回 ErrUnavailable，绝不能被误判成 missing。
func (d *dirStore) Stat(ctx context.Context, key string) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	path, err := d.pathFor(key)
	if err != nil {
		return Ref{}, err
	}
	fi, err := d.objectInfo(ctx, path, key)
	if err != nil {
		return Ref{}, err
	}
	return d.refOf(key, fi.Size(), fi.ModTime()), nil
}

// Delete 删正文。不存在返回 nil（幂等）：GC 在「删内容」与「删元数据行」之间被杀之后，
// 重跑必须能继续往下走。
func (d *dirStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := d.pathFor(key)
	if err != nil {
		return err
	}
	if _, err := d.objectInfo(ctx, path, key); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: remove object %s: %w", ErrUnavailable, key, err)
	}
	return nil
}

// List 按前缀流式列举（GC 的桶扫描差集用）。前缀必须过 ValidateListPrefix：目录后端
// 的穿越入口就是这里，放过一个 "../" 等于把整个盘交出去。
//
// 只认「key 规则拼得出来且是普通文件」的条目：崩溃残留的临时文件、别人塞进根目录的
// 杂物、非内容寻址的文件一律跳过，否则 GC 会把它们当成孤儿对象。
func (d *dirStore) List(ctx context.Context, prefix string, fn func(Ref) error) error {
	if err := ValidateListPrefix(prefix); err != nil {
		return err
	}
	// 根目录不可用必须在开口之前失败：把「读不到盘」当成「盘里没有对象」，GC 就会把
	// 全部对象判成孤儿，这是本方案最贵的一类误判（§11.1、§12）。
	if err := d.Probe(ctx); err != nil {
		return err
	}
	body := strings.TrimSuffix(prefix, "/")
	segments := strings.Split(body, "/")
	base := d.root
	for _, seg := range segments[:len(segments)-1] {
		base = filepath.Join(base, filepath.FromSlash(seg))
	}
	return filepath.WalkDir(base, func(path string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				// 前缀指向的目录还没长出来 = 这里没有对象；根目录缺失已在 Probe 报错了。
				return nil
			}
			return fmt.Errorf("%w: scan %s: %w", ErrUnavailable, base, werr)
		}
		if entry.IsDir() {
			return nil
		}
		key, err := d.keyOf(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		if err := ValidateKey(key); err != nil {
			return nil
		}
		fi, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("%w: stat object %s: %w", ErrUnavailable, key, err)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		return fn(d.refOf(key, fi.Size(), fi.ModTime()))
	})
}

// pathFor 把 key 映射成根目录内的路径。ValidateKey 已从字符集层面排除了 ..、反斜杠、
// 绝对路径与 URL 编码残留，这里仍做一次拼接后的包含性检查：根目录本身可能被配成相对
// 路径或指向别处的软链接，纵深防御的成本只有一次字符串比较。
func (d *dirStore) pathFor(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	path := filepath.Join(d.root, filepath.FromSlash(key))
	if !withinRoot(d.root, path) {
		return "", fmt.Errorf("%w: %q resolves outside dir root %s", ErrKeyInvalid, key, d.root)
	}
	return path, nil
}

// keyOf 是 pathFor 的反向：把磁盘路径还原成 key（List 用）。
func (d *dirStore) keyOf(path string) (string, error) {
	rel, err := filepath.Rel(d.root, path)
	if err != nil {
		return "", fmt.Errorf("%w: relativize %s against %s: %w", ErrUnavailable, path, d.root, err)
	}
	return filepath.ToSlash(rel), nil
}

// objectInfo 判定「这个 key 的正文在位」。用 Lstat 而不是 Stat：符号链接一律当成「这不是
// 我们写的对象」——key 规则保证正文只可能由 Put 的 rename 产生，根目录里出现的软链接要么
// 是别人放的要么是攻击面，两种都不该顺着读出去。
//
// 非普通文件报 ErrUnavailable 而不是 ErrNotFound：那是后端状态异常，不能顺着下载路径把
// 元数据行判成 missing（§7）。
//
// ENOENT 还要再看一眼根目录：整块盘/整个目录不见了是后端故障（ErrUnavailable），不是
// 「这个对象不存在」。这次额外 stat 只在 not-found 分支上付，命中对象的正常路径不受影响。
func (d *dirStore) objectInfo(ctx context.Context, path, key string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if rootErr := d.rootErr(ctx, key); rootErr != nil {
				return nil, rootErr
			}
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("%w: stat object %s: %w", ErrUnavailable, key, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular object file", ErrUnavailable, key)
	}
	return fi, nil
}

// rootErr 在 not-found 之前先确认根目录还在位。判定顺序很重要：先问「盘还在吗」，
// 再问「对象在吗」。
func (d *dirStore) rootErr(ctx context.Context, key string) error {
	if err := d.Probe(ctx); err != nil {
		return fmt.Errorf("%w (object %s)", err, key)
	}
	return nil
}

// refOf 从 key 与文件信息拼出自描述元数据。摘要不重算：内容寻址不变量已由 ValidateKey
// 卡住尾段，为 Stat 重扫一遍正文是白付一次 IO。
func (d *dirStore) refOf(key string, size int64, mtime time.Time) Ref {
	return Ref{
		Key:       key,
		SHA256:    shaOfKey(key),
		Size:      size,
		MediaType: GuessMediaType(extOfKey(key)),
		UpdatedAt: mtime,
	}
}

// withinRoot 判定 path 严格落在 root 之内（root 自身不算）。
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

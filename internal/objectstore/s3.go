// 本文件是 internal/objectstore 里唯一允许 import 对象存储厂商 SDK 的文件
// （tests/arch/objectstore_test.go 按文件名 objectstore/s3.go 钉死这条边界）。SDK
// 类型一律不外露：向上只给 Store 接口与本包哨兵错误，将来换 SDK 改动面就是这一个文件。
//
// 只用 S3 通用子集（HeadBucket / CreateBucket / PutObject / StatObject / GetObject /
// RemoveObject / ListObjects / PresignedGetObject），不碰 MinIO 私有 admin API，因此
// MinIO、Ceph RGW、AWS S3 与国产兼容实现走同一条代码路径（方案 15 §17）。

package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/k8s-ai/k8s-ai/internal/security"
)

const (
	// MaxPresignTTL 是预签名 URL 的运行期上限（§13.2）：配置面另有 24h 硬上限
	// （config.maxBlobPresignTTL），这里是第二道夹刀——预签名 URL 一旦泄露，桶侧没有
	// 任何补救手段（收不回、也无需 STS 就公开可读），所以 TTL 只能往短了收。
	MaxPresignTTL = 15 * time.Minute

	// s3CopyBuffer 与 dir 后端同口径：64KiB 够让落盘中转不被 syscall 数量卡住。
	s3CopyBuffer = 64 << 10
	// s3TempPattern 是上传中转文件的命名前缀。崩溃残留靠它认出来是谁家的（前缀里带
	// 项目名），但它落在系统临时目录而不是对象目录里，不会被任何 List 扫到。
	s3TempPattern = "k8sai-put-"
	// s3TempDirPerm 收紧到仅属主可访问：中转文件里是还没进桶的正文（可能是附件）。
	s3TempDirPerm = 0o750

	s3DialTimeout           = 10 * time.Second
	s3TLSHandshakeTimeout   = 10 * time.Second
	s3ResponseHeaderTimeout = 30 * time.Second
	s3IdleConnTimeout       = 90 * time.Second

	// s3ExpiredTokenCode 没被 SDK 导出成常量（s3-error.go 的常量表里没有它），但
	// 用 STS 凭据时它是会真实出现的错误码，且必须按「凭据故障」而不是「对象不存在」处理。
	s3ExpiredTokenCode = "ExpiredToken"
)

// bucketNameRe 是 S3 桶名的形状约束：3–63 字符，小写字母数字开头结尾，中间允许 . 与 -。
// SDK 自己也会校验，但它把失败留到第一次请求；放在构造期是为了和 NewDir 同口径——
// 「配错了当场点名」，而不是等启动期 Ensure 报一句不可达让人去猜是网络还是名字。
var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// S3Config 是 S3 兼容后端的构造参数，与 platform.blobs.* 一一对应（§14）。
//
// AccessKey/SecretKey 只应来自环境变量或 Secret 注入；本结构体的字段值永不进日志，
// 错误串里的凭据由 security.Redactor 兜底（§13.3）。
type S3Config struct {
	// Endpoint 是 host:port，不含 scheme（SDK 的约定）；scheme 由 UseTLS 决定。
	Endpoint string
	// Region 是签名区域。留空时 SDK 会去做一次 GetBucketLocation 探测——那需要网络，
	// 也会让「离线构造 + 纯本地预签名」的用例变成联网用例，所以现场与测试都应显式给值。
	Region string
	// UseTLS 走 https。自签证书的 CA 必须进容器信任链：配置面没有 ca_bundle 键。
	UseTLS bool
	// PathStyle 用路径式寻址（bucket 进 path）。MinIO/Ceph RGW 必须 true。
	PathStyle bool
	Bucket    string
	AccessKey string
	SecretKey string
	// Prefix 是 key 顶层命名空间，空取 DefaultPrefix。
	Prefix string
	// TmpDir 是上传落盘中转目录（platform.blobs.tmp_dir），空取 os.TempDir()。
	TmpDir string
	// MaxBytes 是单对象上限，<=0 取 DefaultMaxObjectBytes。
	MaxBytes int64
	// CreateBucket 允许 Ensure 幂等建桶；无建桶权限的现场置 false 并预建桶。
	CreateBucket bool
	// PresignTTL 是 PresignGET 未指定 ttl 时的默认值，运行期再夹到 MaxPresignTTL。
	PresignTTL time.Duration
}

// s3Store 是 driver=s3 后端：正文落 S3 兼容桶，元数据登记表恒在 MySQL（§6.1）。
type s3Store struct {
	client    *minio.Client
	transport *http.Transport // 只为 Close 能收走空闲连接而留着
	bucket    string
	region    string
	prefix    string
	tmpBase   string
	// maxBytes 在构造期收敛（<=0 → DefaultMaxObjectBytes），Put 不再判 0。
	maxBytes     int64
	presignTTL   time.Duration
	createBucket bool
	redactor     *security.Redactor
}

var (
	_ Store      = (*s3Store)(nil)
	_ Maintainer = (*s3Store)(nil)
	_ Probe      = (*s3Store)(nil)
	// s3 是 Presigner 的唯一实现者：dir/db 走服务端流式下载端点，不签发 URL（§13.2）。
	_ Presigner = (*s3Store)(nil)
)

// NewS3 构造 S3 兼容后端，**不联网**：桶是否存在、凭据是否可用一律留给 Ensure
// （§11.2 的启动期 fail-fast 位置），与 NewDir「构造不碰文件系统」同口径。这样配置
// validate 与单测拿到「未启用」后端时零副作用，也不会因为校验就把一次重试风暴打在
// 一个不可达的桶上。
//
// 构造期的必填项校验复用 ErrKeyInvalid 家族：这些是「配置形状不对」，装配层能直接
// 点名 platform.blobs.* 哪个键没给，不必为它新增哨兵错误。
func NewS3(cfg S3Config) (Store, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: s3 backend needs platform.blobs.endpoint (host:port, no scheme)", ErrKeyInvalid)
	}
	if strings.Contains(endpoint, "://") {
		return nil, fmt.Errorf("%w: platform.blobs.endpoint %q must not contain a scheme; use platform.blobs.use_tls", ErrKeyInvalid, endpoint)
	}
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("%w: s3 backend needs platform.blobs.bucket", ErrKeyInvalid)
	}
	if !bucketNameRe.MatchString(bucket) {
		// 文案必须点名配置键：SDK 自己的报错只说桶名不合法，运维对着 yaml 不知道该改哪一行。
		return nil, fmt.Errorf("%w: bucket name %q is not a valid S3 bucket name (platform.blobs.bucket)",
			ErrKeyInvalid, bucket)
	}
	if strings.TrimSpace(cfg.AccessKey) == "" {
		return nil, fmt.Errorf("%w: s3 backend needs platform.blobs.access_key (inject it via Secret)", ErrKeyInvalid)
	}
	if strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, fmt.Errorf("%w: s3 backend needs platform.blobs.secret_key (inject it via Secret)", ErrKeyInvalid)
	}
	prefix, err := NormalizePrefix(cfg.Prefix)
	if err != nil {
		// 叠一层配置键：NormalizePrefix 只说「这个前缀不合法」，不说它来自哪个键。多段前缀
		// （k8sai/reports）是合法的，这里真正会拦下来的是穿越段。
		return nil, fmt.Errorf("%w (platform.blobs.prefix)", err)
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxObjectBytes
	}
	tmpBase := strings.TrimSpace(cfg.TmpDir)
	if tmpBase == "" {
		tmpBase = os.TempDir()
	}
	transport := newS3Transport()
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStatic(cfg.AccessKey, cfg.SecretKey, "", credentials.SignatureV4),
		Secure: cfg.UseTLS,
		Region: strings.TrimSpace(cfg.Region),
		// MinIO/Ceph 一律 path style 才通（虚拟主机式要求桶名能进 DNS）。
		BucketLookup: bucketLookup(cfg.PathStyle),
		Transport:    transport,
		// 1 = 关闭 SDK 内部重试（SDK 注释：Set to 1 to disable retries）。§11.3 明确
		// 不制造重试风暴：一次业务请求里的重试由调用方的超时兜着，不由 SDK 翻倍。
		MaxRetries: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: platform.blobs.endpoint %q: %s", ErrKeyInvalid, endpoint,
			security.NewRedactor().Redact(err.Error()))
	}
	return &s3Store{
		client:       client,
		transport:    transport,
		bucket:       bucket,
		region:       strings.TrimSpace(cfg.Region),
		prefix:       prefix,
		tmpBase:      tmpBase,
		maxBytes:     maxBytes,
		presignTTL:   clampPresignTTL(cfg.PresignTTL),
		createBucket: cfg.CreateBucket,
		redactor:     security.NewRedactor(),
	}, nil
}

// bucketLookup 把 path_style 配置翻成 SDK 的寻址方式。
func bucketLookup(pathStyle bool) minio.BucketLookupType {
	if pathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

// clampPresignTTL 把任意请求值夹进 (0, MaxPresignTTL]：<=0（含未配置）取上限而不是
// 直接报错——预签名是读路径上的便利件，因为调用方忘传 ttl 就失败它，代价落在用户身上。
func clampPresignTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 || ttl > MaxPresignTTL {
		return MaxPresignTTL
	}
	return ttl
}

// newS3Transport 显式建传输层而不是复用 minio.DefaultTransport：内网 MinIO 常见「配了
// 地址但连不上」，没有拨号与响应头超时的话，一次 Stat 能挂到内核 TCP 超时（分钟级），
// 把下载请求的 ctx 拖成摆设。代理仍走环境变量（企业出网场景）。
func newS3Transport() *http.Transport {
	dialer := &net.Dialer{Timeout: s3DialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   s3TLSHandshakeTimeout,
		ResponseHeaderTimeout: s3ResponseHeaderTimeout,
		IdleConnTimeout:       s3IdleConnTimeout,
		ExpectContinueTimeout: 10 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
	}
}

// Driver 返回 s3。
func (s *s3Store) Driver() string { return DriverS3 }

// Close 只收走连接池里的空闲连接。
//
// 说清楚它做不到什么：minio.Client 没有 Close 语义，也没有后台 goroutine，运行中的
// 请求由各自的 ctx 管。把它写成「关闭了与桶的连接」是假话。
func (s *s3Store) Close() error {
	s.transport.CloseIdleConnections()
	return nil
}

// Ensure 做两件启动期必须成的事：确认桶在位（必要时建桶）、确认中转目录可建。
// 幂等，可重复调用。
//
// 这里失败即拒绝启动（§11.2）：配了桶却连不上/建不了，比「跑起来了但每个下载都 503」
// 更难排查，也和 dir 后端「根目录不可写就起不来」同口径。
func (s *s3Store) Ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.tmpBase, s3TempDirPerm); err != nil {
		return fmt.Errorf("%w: prepare upload temp dir %s: %w", ErrUnavailable, s.tmpBase, err)
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return s.classify("head bucket", s.bucket, err)
	}
	if exists {
		return nil
	}
	if !s.createBucket {
		return fmt.Errorf("%w: bucket %s does not exist; create it out of band or set platform.blobs.create_bucket=true",
			ErrUnavailable, s.bucket)
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		// 并发启动时另一个副本刚建好：这不是故障。
		if minio.ToErrorResponse(err).Code == minio.BucketAlreadyOwnedByYou {
			return nil
		}
		return s.classify("create bucket", s.bucket, err)
	}
	return nil
}

// Probe 只回答「现在能不能用这个桶」：不建桶、不写对象、不改任何状态（§11.1）。
//
// 桶不存在按不可用而不是「对象不存在」：桶没了是后端故障，报成 NotFound 会让 §12 对账
// 把全部对象判成孤儿。
func (s *s3Store) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return s.classify("head bucket", s.bucket, err)
	}
	if !exists {
		return fmt.Errorf("%w: bucket %s not found", ErrUnavailable, s.bucket)
	}
	return nil
}

// Put 把正文流式中转进本地临时文件（边读边算 sha256、边判体积上限），再用实际摘要组
// key，单次 PUT 上传。
//
// 为什么要落盘中转：key 依赖实际摘要，而 S3 的签名与 Content-Length 都要求**上传之前**
// 就知道确切长度。直接对 io.Reader 调 PutObject 会退成分片上传（§23.2 明确不做分片），
// 分片后的 ETag 是 <md5>-N，§12.2 的校验就彻底没法抄近路。32MiB 上限让中转成本可控。
//
// 去重靠先 Stat 再 PUT：内容寻址下同 key 就是同内容，重复 PUT 只是白付一次上行流量。
func (s *s3Store) Put(ctx context.Context, spec Spec, r io.Reader) (Ref, error) {
	ext, err := validateSpecHead(spec)
	if err != nil {
		return Ref{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	tmp, err := os.CreateTemp(s.tmpBase, s3TempPattern)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: create upload temp file under %s: %w", ErrUnavailable, s.tmpBase, err)
	}
	// 中转文件在所有出口都必须消失：Put 的返回分支比 dir 多（去重命中、PUT 失败、回读
	// 失败），用 defer 统一收尾比每条分支各写一次 Remove 更难漏。
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	stream := NewStreamHash(r, s.maxBytes)
	buf := make([]byte, s3CopyBuffer)
	for {
		// 每轮看一次 ctx：客户端断开或超时就立刻停手，不把 32MiB 拷完再报错。
		if err := ctx.Err(); err != nil {
			return Ref{}, err
		}
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return Ref{}, fmt.Errorf("%w: write upload temp file: %w", ErrUnavailable, werr)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			// ErrTooLarge 从这里原样透出：体积闸门在读到超出的那一刻断掉。
			return Ref{}, rerr
		}
	}
	sha, size := stream.SHA256(), stream.Size()
	if err := tmp.Sync(); err != nil {
		return Ref{}, fmt.Errorf("%w: sync upload temp file: %w", ErrUnavailable, err)
	}
	key, err := BuildKey(s.prefix, spec.Category, spec.Scope, sha, ext)
	if err != nil {
		return Ref{}, err
	}
	if existing, serr := s.stat(ctx, key); serr == nil {
		// 同 key 却体积不同 = 后端把别的对象放在这个内容寻址名上，属于必须停机看的异常。
		if existing.Size != size {
			return Ref{}, fmt.Errorf("%w: %s already holds %d bytes but computed %d",
				ErrChecksum, key, existing.Size, size)
		}
		return existing, nil
	} else if !errors.Is(serr, ErrNotFound) {
		// 不可达时不去覆盖写：把「连不上」当成「对象不存在」再 PUT，等于在一次网络抖动里
		// 制造第二次不确定写入（§4.3）。
		return Ref{}, serr
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return Ref{}, fmt.Errorf("%w: rewind upload temp file: %w", ErrUnavailable, err)
	}
	_, err = s.client.PutObject(ctx, s.bucket, key, tmp, size, minio.PutObjectOptions{
		ContentType:      ResolveMediaType(spec.MediaType, ext),
		DisableMultipart: true,
		// 明文 HTTP（use_tls=false）下 minio-go 对 PUT 体一律用 STREAMING-AWS4-HMAC-SHA256-PAYLOAD
		// 逐块签名（api.go 的 `case metadata.streamSha256 && !c.secure`），SDK 不再算整包摘要，
		// 服务端手上就没有可比对的校验值——上行位翻转只能等下次对账才发现。补一层 Content-MD5：
		// 读的是已落盘的 *os.File，SDK 走 SectionReader 边读边算（api-put-object-streaming.go），
		// 不会把 32MiB 上限的正文整个读进内存；MinIO/Ceph RGW/AWS 都认这个头，摘要不符直接回
		// BadDigest。use_tls=true 时 SDK 本来就会带上 SHA-256，这个头只是多一道冗余。
		SendContentMd5: true,
	})
	if err != nil {
		return Ref{}, s.classify("put", key, err)
	}
	ref := s.refOf(key, size, time.Time{})
	// 回读一次拿服务端时间戳。失败不影响写成功的结论——PUT 已经返回 200，
	// 而 UpdatedAt 只是展示性元数据，权威时间仍在 MySQL 的 objects 行上。
	if served, serr := s.stat(ctx, key); serr == nil {
		ref.UpdatedAt = served.UpdatedAt
	}
	return ref, nil
}

// Get 打开正文流。
func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, Ref, error) {
	if err := ValidateKey(key); err != nil {
		return nil, Ref{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, Ref{}, s.classify("get", key, err)
	}
	// GetObject 是**懒执行**的：它只起了一个 goroutine，真正的 HTTP 请求要等第一次
	// Read/Stat。不在这里立刻 Stat，404 就会推迟到调用方读第一块时炸在 io 边界上，
	// ErrNotFound / ErrUnavailable 的分流失效（§4.3）。这一次 HEAD 与 dir 后端
	// 「先 Stat 再 Open」的代价同形。
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, Ref{}, s.classify("get", key, err)
	}
	return obj, s.refOf(key, info.Size, info.LastModified), nil
}

// Stat 只取元数据（不拉正文）。正文不在位返回 ErrNotFound，好让下载路径把元数据行标成
// missing（§7）；桶不可达返回 ErrUnavailable。
func (s *s3Store) Stat(ctx context.Context, key string) (Ref, error) {
	if err := ValidateKey(key); err != nil {
		return Ref{}, err
	}
	return s.stat(ctx, key)
}

// stat 是已过 key 校验的 HEAD 实现，Put 的去重回读与 Stat 共用。
func (s *s3Store) stat(ctx context.Context, key string) (Ref, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Ref{}, s.classify("stat", key, err)
	}
	return s.refOf(key, info.Size, info.LastModified), nil
}

// Delete 删正文。S3 的 DeleteObject 本身幂等，这里只把「对象不存在」的错误码也当成成功，
// 让 GC 在「删内容」与「删元数据行」之间被杀之后能重跑到底（§9.3）。
func (s *s3Store) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		mapped := s.classify("delete", key, err)
		if errors.Is(mapped, ErrNotFound) {
			return nil
		}
		return mapped
	}
	return nil
}

// List 按前缀流式列举（GC 的桶扫描差集用，§9.3）。
//
// 只回调「key 规则拼得出来」的条目：同一个桶里可能有别的系统、别的项目的对象，还有
// 控制台建目录时顺手写的 "/" 结尾占位对象。放过它们，GC 的差集就会把别人的对象当孤儿
// 去删——这是 §21「桶内含他系统对象时不误删」那条用例的前提。
func (s *s3Store) List(ctx context.Context, prefix string, fn func(Ref) error) error {
	if err := ValidateListPrefix(prefix); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var failure error
	for item := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:       prefix,
		Recursive:    true,
		WithMetadata: false,
	}) {
		if item.Err != nil {
			failure = s.classify("list", prefix, item.Err)
			cancel()
			// 不能 break：SDK 的列举 goroutine 靠「channel 被抽干到 close」收尾，
			// 提前 break 会把它泄在那里，每次中断泄漏一个 goroutine。
			continue
		}
		if ValidateKey(item.Key) != nil {
			continue
		}
		if err := fn(s.refOf(item.Key, item.Size, item.LastModified)); err != nil {
			failure = err
			cancel()
			continue
		}
	}
	return failure
}

// PresignGET 签发限时下载 URL（§13.2）。
//
// 返回的 URL 在 TTL 内等同公开可读，只允许出现在那一个响应体里：不写库、不发 SSE、
// 不写审计、不打日志；万一它出现在错误串里，由 security.Redactor 兜底（§13.3）。
// ttl <= 0 取构造期配置值；两者都过 clampPresignTTL 夹到 MaxPresignTTL。
func (s *s3Store) PresignGET(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = s.presignTTL
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, clampPresignTTL(ttl), nil)
	if err != nil {
		return "", s.classify("presign", key, err)
	}
	return u.String(), nil
}

// refOf 组装自描述元数据。摘要不重算：内容寻址不变量已由 ValidateKey 卡住尾段，为了
// 一次 Stat 再扫一遍正文是白付一次下行流量。
//
// media type 由 key 扩展名推导（与 dir 后端同口径），不读 x-amz-meta-*：那样
// List（WithMetadata:false）、Stat、Get、Put 四条路径才必然给出同一个值，而 contract
// 只要求同一后端内自洽（§4.2）。要改声明值请改 Ext。
func (s *s3Store) refOf(key string, size int64, updatedAt time.Time) Ref {
	return Ref{
		Key:       key,
		SHA256:    shaOfKey(key),
		Size:      size,
		MediaType: GuessMediaType(extOfKey(key)),
		UpdatedAt: updatedAt,
	}
}

// classify 把 SDK/HTTP 错误折叠成本包的哨兵错误，是唯一一处认识 minio.ErrorResponse
// 的地方（§4.3）。
//
// 判定表与它的理由：
//   - ctx 取消/超时原样透出：那是调用方的意图，不是后端故障，别把它算进存储健康度。
//   - NoSuchKey → ErrNotFound。
//   - NoSuchBucket / AccessDenied / InvalidAccessKeyId / SignatureDoesNotMatch /
//     AllAccessDisabled / ExpiredToken → ErrUnavailable。**配错桶与凭据失效必须报故障**：
//     把它们报成 ErrNotFound，§12 的对账就会在一次配置漂移后把全库对象判成 missing，
//     而 missing 是不可逆的结论。
//   - 其余按 HTTP 状态码兜：404 → NotFound，401/403 → Unavailable。
//   - 默认一律 ErrUnavailable：认不准就判故障是安全侧——它只会让下载返回 503，不会误删
//     或误判任何对象。
//
// 向上抛的 SDK 文本一律经 Redactor 且以 %s 拼入（不带 %w）：SDK 错误串里可能带完整 URL
// 与查询参数，走 Unwrap 链就绕过了脱敏（§13.3）。
func (s *s3Store) classify(op, target string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	message := s.redactor.Redact(err.Error())
	code := ""
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		code = resp.Code
	}
	switch code {
	case minio.NoSuchKey:
		return fmt.Errorf("%w: %s", ErrNotFound, target)
	case minio.NoSuchBucket, minio.AccessDenied, minio.InvalidAccessKeyID,
		minio.SignatureDoesNotMatch, minio.AllAccessDisabled, s3ExpiredTokenCode:
		return fmt.Errorf("%w: %s %s: s3 code %s: %s", ErrUnavailable, op, target, code, message)
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, target)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s %s: http %d: %s", ErrUnavailable, op, target, resp.StatusCode, message)
	}
	return fmt.Errorf("%w: %s %s: %s", ErrUnavailable, op, target, message)
}

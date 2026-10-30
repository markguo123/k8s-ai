package objectstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// 目录后端的保留名字。它们都不会被 key 规则拼出来，也不会被 List 收进结果：
// ".tmp/put-xxx" 只有两段（不足 prefix/category/hash 三段），".lock" 只有尾段且不是
// 内容寻址段，List 一律按 ValidateKey 过滤掉。
const (
	dirTempSubdir = ".tmp"
	dirLockFile   = ".lock"
)

// dirLock 是目录后端根目录上的进程间独占锁（方案 15 §11.3）。
//
// 锁的权威是内核（flock / LockFileEx），不是锁文件内容：进程崩溃或被 kill 后内核自动
// 释放，因此不存在「陈旧锁文件要人肉删」这种运维陷阱。锁文件里写的 pid 与主机名只服务
// 于人工排查「是谁把这个目录锁住了」，不参与任何判定。
type dirLock struct{ f *os.File }

// lockDir 在 root 上抢独占锁，不等待：已被别的进程持有就返回包装 ErrLocked 的错误。
//
// dir 后端必须单副本：两个进程写同一个根目录时，「同内容同 key」的幂等假设会变成两个
// 临时文件互相覆盖，GC 也会把对方刚写入的对象当成孤儿。扩副本前必须切 s3。
func lockDir(root string) (*dirLock, error) {
	path := filepath.Join(root, dirLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, dirFilePerm)
	if err != nil {
		return nil, fmt.Errorf("%w: open lock file %s: %w", ErrUnavailable, path, err)
	}
	if err := lockFileNonblocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	host, _ := os.Hostname()
	// 先写后截断：锁文件可能留着上一个持有者的身份信息，按新内容的长度截一次才不会让
	// 排查的人看到两行 pid。
	ident := "pid=" + strconv.Itoa(os.Getpid()) + " host=" + host + "\n"
	if _, err := f.WriteAt([]byte(ident), 0); err != nil {
		_ = unlockFile(f)
		_ = f.Close()
		return nil, fmt.Errorf("%w: write lock file %s: %w", ErrUnavailable, path, err)
	}
	if err := f.Truncate(int64(len(ident))); err != nil {
		_ = unlockFile(f)
		_ = f.Close()
		return nil, fmt.Errorf("%w: truncate lock file %s: %w", ErrUnavailable, path, err)
	}
	return &dirLock{f: f}, nil
}

// Unlock 释放锁并关闭句柄；重复调用安全。
//
// 句柄必须真的关掉，只解字节锁不够：Windows 上只要句柄还开着，锁文件就删不掉，PVC/临时
// 目录的清理会被卡住（Linux 上则是泄漏一个 fd）。解锁失败也要继续关，且优先把解锁的错误
// 报出去——那才是「锁可能还没放」这个更需要人知道的事实。
func (l *dirLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	unlockErr := unlockFile(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

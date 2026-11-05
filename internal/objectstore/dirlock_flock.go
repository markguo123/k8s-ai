//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris

package objectstore

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// flock 的互斥粒度正好是 dir 后端要的：防的是「另一个进程写同一根目录」，而不是两个
// 线程抢同一段字节。锁随文件描述符关闭（含进程退出）自动释放。
func lockFileNonblocking(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: another process is using %s", ErrLocked, f.Name())
		}
		return fmt.Errorf("%w: flock %s: %w", ErrUnavailable, f.Name(), err)
	}
	return nil
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

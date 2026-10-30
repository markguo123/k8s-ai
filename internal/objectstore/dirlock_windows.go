//go:build windows

package objectstore

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockByteCount 锁锁一个字节就够：Windows 的字节范围锁在同一句柄上互斥，范围大小不
// 影响「同一目录只能有一个持有者」这个语义，而锁文件本身永远只写一行身份信息。
const lockByteCount = 1

// FAIL_IMMEDIATELY 的作用等同 unix 的 LOCK_NB：拿不到锁立刻返回 ERROR_LOCK_VIOLATION，
// 绝不在启动路径上阻塞等待——服务起不来要好过起来了却写坏数据。
func lockFileNonblocking(f *os.File) error {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockByteCount, 0, &ov)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return fmt.Errorf("%w: another process is using %s", ErrLocked, f.Name())
		}
		return fmt.Errorf("%w: LockFileEx %s: %w", ErrUnavailable, f.Name(), err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	var ov windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockByteCount, 0, &ov)
	if err != nil && !errors.Is(err, windows.ERROR_NOT_LOCKED) {
		return err
	}
	return nil
}

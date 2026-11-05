//go:build !windows && !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris)

package objectstore

import (
	"fmt"
	"os"
)

// 没有建议锁原语的平台就不假装能锁：宁可在装配期报 ErrUnsupported，也不能让 dir 后端
// 在「以为锁住了」的前提下并发写同一根目录。这些平台请改用 db 后端。
func lockFileNonblocking(f *os.File) error {
	return fmt.Errorf("%w: driver=%s needs advisory file locks, unsupported on this platform", ErrUnsupported, DriverDir)
}

func unlockFile(*os.File) error { return nil }

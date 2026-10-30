package objectstore

import (
	"context"
	"io"
)

// Store.Driver 的取值。driver 值同时落进 objects.driver（写入时的后端，是混合后端
// 下读取路由的判据：改配置不清库，旧对象仍要按原后端读）。
const (
	DriverDB  = "db"
	DriverDir = "dir"
	DriverS3  = "s3"
	DriverOff = "off"
)

// NewDisabled 返回 driver=off 后端：未配置对象存储时装配它。
//
// 它实现同一个接口但一律返回 ErrDisabled（含读与删：后端不存在时，声称「对象已删除」
// 或「对象不存在」都是假话）。调用方据此把附件/报告端点降级为明确的未启用错误，
// 不静默丢弃、也不影响不依赖文件存储的基础业务（§2.2 口径 4）。
func NewDisabled() Store { return disabledStore{} }

type disabledStore struct{}

func (disabledStore) Driver() string { return DriverOff }

func (disabledStore) Close() error { return nil }

func (disabledStore) Put(_ context.Context, spec Spec, _ io.Reader) (Ref, error) {
	return Ref{}, fmtDisabled("put category " + spec.Category)
}

func (disabledStore) Get(_ context.Context, key string) (io.ReadCloser, Ref, error) {
	return nil, Ref{}, fmtDisabled("get " + key)
}

func (disabledStore) Stat(_ context.Context, key string) (Ref, error) {
	return Ref{}, fmtDisabled("stat " + key)
}

func (disabledStore) Delete(_ context.Context, key string) error {
	return fmtDisabled("delete " + key)
}

func (disabledStore) List(_ context.Context, prefix string, _ func(Ref) error) error {
	return fmtDisabled("list prefix " + prefix)
}

// Ensure 让 off 后端在启动期探针里明确表态：不是故障，是未启用。
func (disabledStore) Ensure(context.Context) error { return ErrDisabled }

// Probe 同上；off 不计入故障告警，只作状态展示。
func (disabledStore) Probe(context.Context) error { return ErrDisabled }

func fmtDisabled(op string) error {
	return &DisabledError{Op: op}
}

// DisabledError 携带操作名，便于端点把「对象存储未启用」的回话写清楚。
// errors.Is(err, ErrDisabled) 仍然成立。
type DisabledError struct {
	Op string
}

func (e *DisabledError) Error() string {
	return "objectstore: backend disabled (driver=off), refused " + e.Op
}

// Is 让哨兵判定覆盖整个类型，调用方只需 errors.Is(err, ErrDisabled)。
func (e *DisabledError) Is(target error) bool { return target == ErrDisabled }

// 编译期确认 off 后端三个接口都满足（Presigner 刻意不实现：off 不签发 URL）。
var (
	_ Store      = disabledStore{}
	_ Maintainer = disabledStore{}
	_ Probe      = disabledStore{}
)

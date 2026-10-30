// Package objectstore 是四期对象存储的统一抽象（方案 15 §4）：业务模块只依赖本包
// 接口，S3 兼容后端、目录后端与 db 后端都实现同一组契约，任何人不得直接 import
// 存储厂商 SDK。
//
// 边界（§4.4，由 tests/arch/objectstore_test.go 门禁钉死）：
//   - 本包不 import internal/store、不写任何 SQL、不认识 MySQL；对象元数据登记表
//     objects 的 SQL 单源在 internal/store（正文 payload 与元数据同表，拆成两处
//     SQL 必然漂移，把 *sql.DB 泄漏进抽象层同样失败）。
//   - minio-go 只允许出现在本包的 s3.go（P8.4 落地）。
//
// 三后端共用同一套 key 规则（keys.go）与同一组哨兵错误，调用方一律用 errors.Is
// 分流，禁止字符串匹配。
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// 哨兵错误（§4.3）。调用方按 errors.Is 分流。
//
// ErrUnavailable 与 ErrNotFound 的区分是 §11 降级与 §12 对账的前提：不可达绝不能
// 被误判成「对象不存在」并标成 missing，否则一次网络抖动就把健康对象判死。
var (
	ErrNotFound    = errors.New("objectstore: object not found")
	ErrTooLarge    = errors.New("objectstore: object exceeds limit")
	ErrKeyInvalid  = errors.New("objectstore: invalid key")
	ErrDisabled    = errors.New("objectstore: backend disabled")
	ErrChecksum    = errors.New("objectstore: sha256 mismatch")
	ErrState       = errors.New("objectstore: invalid object state transition")
	ErrLocked      = errors.New("objectstore: directory backend already in use")
	ErrUnavailable = errors.New("objectstore: backend unavailable")
	ErrUnsupported = errors.New("objectstore: operation not supported by backend")
)

// Ref 是对象的自描述元数据。审计、SSE 事件、日志只允许携带 Ref，不携带内容。
type Ref struct {
	Key       string    `json:"key"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	MediaType string    `json:"mediaType"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Spec 描述「要写一个什么对象」，key 由实现端按实际 sha256 组装。
//
// 早期草案让调用方自己拼带 sha 的 key，等于要求调用方先把整个对象读进内存算摘要，
// 32MiB 附件就是 32MiB 常驻内存，且「调用方拼错 key」只能靠写完再比摘要兜住；改由
// 实现端组装后，流式写入边读边算、边校验 max_object_size。
type Spec struct {
	// Category 是对象类别，必须在 Categories() 白名单内（§5）。
	Category string
	// Scope 是命名空间段，只允许内部生成的 ID（userID/sessionID/clusterID/scanID）。
	// 用户可控原文（文件名、集群显示名）禁止进入：展示名只进 MySQL 元数据列。
	Scope []string
	// Ext 是尾段扩展名（".md"/".json"/".png"），只参与 key 可读性，不参与去重判定。
	Ext string
	// MediaType 为空时按 Ext 推断，再空回落 application/octet-stream。
	MediaType string
	// CreatedBy 是触发写入的用户 ID；空表示系统任务（落库记 objectstore.SystemActor）。
	// objects.created_by 是报告归属的唯一依据（进程内 job 没有 owner 可继承）。
	CreatedBy string
}

// Store 是对象内容后端的唯一抽象。db/dir/s3 三后端实现同一契约，并由
// contract_test.go 的共享用例锁定等价行为；off 后端只保证「写必报 ErrDisabled」。
type Store interface {
	// Put 流式写入：边读边算 sha256，超 max_object_size 立即返回 ErrTooLarge。
	// 同内容重复 Put 必须返回同一 key（内容寻址幂等，§9.1）。
	Put(ctx context.Context, spec Spec, r io.Reader) (Ref, error)
	// Get 打开正文流，调用方必须 Close。key 非法 ErrKeyInvalid，内容不存在
	// ErrNotFound，后端不可达 ErrUnavailable（不得把不可达报成不存在）。
	Get(ctx context.Context, key string) (io.ReadCloser, Ref, error)
	// Stat 只取元数据：Put 去重与下载前探用。内容与元数据行分离的后端（db）在
	// 正文已删除时必须返回 ErrNotFound，好让下载路径把行标成 missing（§7）。
	Stat(ctx context.Context, key string) (Ref, error)
	// Delete 删内容。db 后端只把 payload 置 NULL 并保留元数据行，行由 Forget 删。
	Delete(ctx context.Context, key string) error
	// List 按前缀流式回调，回调返回 error 即中止；禁止把结果全量读进内存。
	List(ctx context.Context, prefix string, fn func(Ref) error) error
	// Driver 返回后端标识："db" | "dir" | "s3" | "off"。
	Driver() string

	io.Closer
}

// Presigner 仅 s3 后端实现；dir/db 走服务端流式下载端点（§13.2）。
type Presigner interface {
	// PresignGET 生成限时下载 URL。URL 含签名，必经脱敏后才能进日志。
	PresignGET(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Maintainer 由 db/dir/s3 三后端实现：启动期就绪保障（失败即 fail-fast，§11.2）。
//
// GC 刻意不在本接口里声明：它与 blobs 命令组同批交付（P8.9），提前声明只会得到一个
// 没有实现的接口方法（AGENTS.md 禁止占位实现）。
type Maintainer interface {
	Ensure(ctx context.Context) error
}

// Probe 只读探测，供存储健康端点使用：只回答「现在能不能用」，不写、不建、不改状态。
// 对象存储不进 /readyz（§11.1）：它故障时基础业务必须继续可用。
type Probe interface {
	Probe(ctx context.Context) error
}

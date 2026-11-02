package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// BlobGateway 是对象存储的装配期门面（P8.5，方案 15 §11.2 两段式启动）。
//
// 它存在的理由只有一条：对象存储是**可降级依赖**。MySQL 是平台核心依赖，连不上就不该
// 启动；桶不可达时聊天、审批、审计、扫描必须照常跑，只有文件相关端点返回 503。如果让
// 业务模块各自持有 objectstore.Store，「现在到底能不能用」这个问题就会有第五个答案，
// 而恢复策略会散成五份。这里把它收成一个：
//
//   - 构造期错误（配置形状非法、driver 拼错）由装配层直接拒绝启动，见 internal/cli；
//   - 就绪期错误（Ensure 失败：桶不可达/不存在且无权创建/凭据被拒/dir 抢不到独占锁）
//     记成「未就绪 + 最近错误类别」，进程照常启动，对象类请求一律拿到原始哨兵错误，
//     由 server 层翻成 503 + Retry-After；
//   - 恢复靠**懒重试**：对象类请求进来时才按 retryEvery 节流地再跑一次 Ensure，不常驻
//     后台 goroutine、不排队、不碰写路径。桶修好之后不需要重启进程。
//
// driver=db 的内容就在平台库里，它随 MySQL 一起降级（不单独降级）；off 的 ErrDisabled
// 是「功能未开启」而不是故障，Health 报 disabled 而不是 degraded。
type BlobGateway struct {
	backend    objectstore.Store
	maintainer objectstore.Maintainer
	probe      objectstore.Probe
	presigner  objectstore.Presigner
	counter    ObjectCounter
	driver     string

	mu          sync.Mutex
	ready       bool
	lastErr     error
	lastAttempt time.Time
	inflight    bool
	retryEvery  time.Duration
	// now 便于测试注入（节流窗口是秒级，用真时钟测会白等）。
	now func() time.Time
}

// defaultBlobRetryInterval 是懒恢复的最小重试间隔。30s 是个折中：桶抖一下不至于被每个
// HTTP 请求各打一次 HEAD 探测，用户把配置修好后也只需等半分钟。
const defaultBlobRetryInterval = 30 * time.Second

// ObjectCounter 是本包需要的统计窄接口（store.ObjectStore 满足它）。走窄接口而不是直接
// 依赖 store.ObjectStore，是为了让网关的单测不必为「统计」这个词拉起一个数据库。
type ObjectCounter interface {
	ObjectStats(ctx context.Context) ([]store.ObjectDriverStat, error)
}

// BlobHealth 是存储健康端点的响应体（方案 15 §13.1）。字段刻意不含任何桶名/端点/凭据。
type BlobHealth struct {
	// Driver 是当前装配的后端：db | dir | s3 | off。
	Driver string `json:"driver"`
	// Status 是总体结论：ready（可用）| degraded（未就绪，对象类 API 返 503）|
	// disabled（driver=off，功能未开启，不是故障）。
	Status string `json:"status"`
	// Ready 是「现在能不能读写对象」。
	Ready bool `json:"ready"`
	// ProbeError 是最近一次失败的原因摘要（不含凭据）；就绪时为空。
	ProbeError string `json:"probeError,omitempty"`
	// Objects / Bytes 是存活对象的总量（排除正在 GC 的行），跨后端合计。
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
	// DriverMismatch 是「内容还在别的后端」的对象数，按原 driver 分组：非空说明换过
	// driver 而没跑 blobs migrate，历史对象此刻读不到（方案 15 §10.3）。
	DriverMismatch map[string]int64 `json:"driverMismatch,omitempty"`
	// ObservedAt 是本次观测时间。
	ObservedAt time.Time `json:"observedAt"`
}

// Health 状态取值。
const (
	BlobStatusReady     = "ready"
	BlobStatusDegraded  = "degraded"
	BlobStatusDisabled  = "disabled"
)

// NewBlobGateway 包装已构造好的后端。backend 为 nil 时无网关可用，调用方不该传 nil
// （装配层在 objectstore.New 失败时直接返回错误，不会走到这里）。
func NewBlobGateway(backend objectstore.Store, counter ObjectCounter) *BlobGateway {
	g := &BlobGateway{
		backend:    backend,
		counter:    counter,
		retryEvery: defaultBlobRetryInterval,
		now:        time.Now,
	}
	if backend != nil {
		g.driver = backend.Driver()
		if m, ok := backend.(objectstore.Maintainer); ok {
			g.maintainer = m
		}
		if p, ok := backend.(objectstore.Probe); ok {
			g.probe = p
		}
		if p, ok := backend.(objectstore.Presigner); ok {
			g.presigner = p
		}
	}
	return g

}

// Driver 返回装配的后端标识（db | dir | s3 | off）。
func (g *BlobGateway) Driver() string { return g.driver }

// Available 报告当前是否已就绪（不触发探测，给日志与启动期判定用）。
func (g *BlobGateway) Available() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ready
}

// ErrDisabled 会原样从 EnsureReady/Put/Get 抛出，调用方用 errors.Is(err,
// objectstore.ErrDisabled) 区分「没开这个功能」与「后端坏了」。
var errNoBackend = errors.New("service: object storage backend is not wired")

// EnsureReady 确保后端就绪：已就绪直接返回 nil；未就绪则按 retryEvery 节流重跑 Ensure。
//
// 单飞由 inflight 保证：Ensure 打的是网络，十个并发请求一起跑只会在一个坏掉的桶上排出
// 十个超时，比它们各自的请求更慢。正在探测时后来的请求立刻拿最近一次错误回，不排队等待。
func (g *BlobGateway) EnsureReady(ctx context.Context) error {
	if g.backend == nil {
		return errNoBackend
	}
	if g.driver == objectstore.DriverOff {
		return fmt.Errorf("%w: platform.blobs.driver=off", objectstore.ErrDisabled)
	}
	g.mu.Lock()
	if g.ready {
		g.mu.Unlock()
		return nil
	}
	if g.inflight {
		err := g.lastErr
		g.mu.Unlock()
		if err == nil {
			return fmt.Errorf("%w: object storage readiness check in flight", objectstore.ErrUnavailable)
		}
		return err
	}
	if !g.lastAttempt.IsZero() && g.now().Sub(g.lastAttempt) < g.retryEvery {
		err := g.lastErr
		g.mu.Unlock()
		if err != nil {
			return err
		}
		// 未就绪却没有留下错误：不该发生（Ensure 成功即置 ready），真走到了也不能放行——
		// 「不知道好没好」在这里必须按坏的处理，松手就把请求送进一个没准备好的后端。
		return fmt.Errorf("%w: object storage is not ready", objectstore.ErrUnavailable)
	}
	g.inflight = true
	g.lastAttempt = g.now()
	g.mu.Unlock()

	err := g.runEnsure(ctx)

	g.mu.Lock()
	g.inflight = false
	g.lastAttempt = g.now()
	if err != nil {
		g.ready = false
		g.lastErr = err
	} else {
		g.ready = true
		g.lastErr = nil
	}
	g.mu.Unlock()
	return err
}

// runEnsure 跑一次就绪保障。后端没实现 Maintainer 时视作「无需保障动作」：
// 一个不提供 Ensure 的后端能给出的唯一诚实答案就是「我没有要检查的东西」。
func (g *BlobGateway) runEnsure(ctx context.Context) error {
	if g.maintainer == nil {
		return nil
	}
	return g.maintainer.Ensure(ctx)
}

// guard 是每个对象类入口的第一道闸门：未就绪时触发一次懒恢复。
func (g *BlobGateway) guard(ctx context.Context) error { return g.EnsureReady(ctx) }

// markUnavailable 在后端现场报错时把就绪位打回未就绪，并刷新重试窗口起点——
// 否则下一个请求会立刻再试，一个坏桶会被每个请求各探测一次。
func (g *BlobGateway) markUnavailable(err error) {
	if !errors.Is(err, objectstore.ErrUnavailable) && !errors.Is(err, objectstore.ErrLocked) {
		return
	}
	g.markNotReady(err)
}

// markNotReady 无条件把就绪位打回未就绪（ErrNotFound 这类正常答复不走这里）。
func (g *BlobGateway) markNotReady(err error) {
	g.mu.Lock()
	g.ready = false
	g.lastErr = err
	g.lastAttempt = g.now()
	g.mu.Unlock()
}

// Put 写入对象。返回的 Ref 是唯一的 key 来源（内容寻址，同内容必同 key）。
func (g *BlobGateway) Put(ctx context.Context, spec objectstore.Spec, r io.Reader) (objectstore.Ref, error) {
	if err := g.guard(ctx); err != nil {
		return objectstore.Ref{}, err
	}
	ref, err := g.backend.Put(ctx, spec, r)
	if err != nil {
		g.markUnavailable(err)
		return objectstore.Ref{}, err
	}
	return ref, nil
}

// Get 打开对象正文流，调用方必须 Close。
func (g *BlobGateway) Get(ctx context.Context, key string) (io.ReadCloser, objectstore.Ref, error) {
	if err := g.guard(ctx); err != nil {
		return nil, objectstore.Ref{}, err
	}
	rc, ref, err := g.backend.Get(ctx, key)
	if err != nil {
		g.markUnavailable(err)
		return nil, objectstore.Ref{}, err
	}
	return rc, ref, nil
}

// Stat 只取元数据（下载前探、去重判定）。
func (g *BlobGateway) Stat(ctx context.Context, key string) (objectstore.Ref, error) {
	if err := g.guard(ctx); err != nil {
		return objectstore.Ref{}, err
	}
	ref, err := g.backend.Stat(ctx, key)
	if err != nil {
		g.markUnavailable(err)
		return objectstore.Ref{}, err
	}
	return ref, nil
}

// Presign 生成限时下载 URL。只有 s3 后端支持，dir/db 返回包装了 ErrUnsupported 的错误
// （由 server 层翻成 400，而不是让前端以为链接坏了）。
func (g *BlobGateway) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := g.guard(ctx); err != nil {
		return "", err
	}
	if g.presigner == nil {
		return "", fmt.Errorf("%w: driver %s serves downloads through the API endpoint, use mode=stream",
			objectstore.ErrUnsupported, g.driver)
	}
	url, err := g.presigner.PresignGET(ctx, key, ttl)
	if err != nil {
		g.markUnavailable(err)
		return "", err
	}
	return url, nil
}

// Health 汇总一次存储观测（GET /api/v1/storage/health 的唯一数据源，方案 15 §13.1）。
//
// 单返回值是刻意的：这个端点唯一的失败原因是「后端不可达」，而那本身就是它要报告的
// 内容，再配一个 error 返回值只会让 handler 多一条永远不会走到的分支。所有探测与统计
// 失败都折进 ProbeError。
//
// 它顺带触发懒恢复：这是运维点进来那一下顺手把桶重新拉起来的地方，也是唯一一个恢复
// 触发点之外的补充入口。/healthz 与 /readyz 都不看这里（§11.1）。
func (g *BlobGateway) Health(ctx context.Context) BlobHealth {
	h := BlobHealth{Driver: g.driver, ObservedAt: g.now().UTC(), Status: BlobStatusDegraded}
	if g.backend == nil {
		h.ProbeError = errNoBackend.Error()
		return h
	}
	if g.driver == objectstore.DriverOff {
		h.Status = BlobStatusDisabled
		h.ProbeError = fmt.Sprintf("%v: platform.blobs.driver=off", objectstore.ErrDisabled)
		return h
	}

	if err := g.EnsureReady(ctx); err != nil {
		h.ProbeError = err.Error()
	}
	g.mu.Lock()
	ready := g.ready
	lastErr := g.lastErr
	g.mu.Unlock()
	h.Ready = ready
	if ready {
		h.Status = BlobStatusReady
	}
	if h.ProbeError == "" && lastErr != nil {
		h.ProbeError = lastErr.Error()
	}

	if g.probe != nil {
		if err := g.probe.Probe(ctx); err != nil {
			h.Ready = false
			h.Status = BlobStatusDegraded
			// 探针失败说明后端此刻答不上话，无论错误类别都要把就绪位打回——
			// 响应里报 degraded 而网关内部仍认为 ready，比两边一起报 degraded 更糟。
			g.markNotReady(err)
			if h.ProbeError == "" {
				h.ProbeError = err.Error()
			}
		}
	}

	if g.counter != nil {
		stats, err := g.counter.ObjectStats(ctx)
		if err != nil {
			if h.ProbeError == "" {
				h.ProbeError = err.Error()
			}
		} else {
			for _, s := range stats {
				h.Objects += s.Objects
				h.Bytes += s.Bytes
				if s.Driver != g.driver {
					if h.DriverMismatch == nil {
						h.DriverMismatch = map[string]int64{}
					}
					h.DriverMismatch[s.Driver] += s.Objects
				}
			}
		}
	}
	return h
}

// Close 关闭后端（dir 释放独占锁与句柄）。进程退出路径调用。
func (g *BlobGateway) Close() error {
	if g.backend == nil {
		return nil
	}
	return g.backend.Close()
}

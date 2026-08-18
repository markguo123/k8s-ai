package scanner

import (
	"bytes"
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/security"
)

// hardLogLineCap 单行硬上限（与 kubernetes 包同值）：超过才截断防 OOM。
const hardLogLineCap = 1 << 20 // 1 MiB

// redactor 是采集边界使用的默认脱敏器（ADR-006，不可变，线程安全）。
var redactor = security.NewRedactor()

// logFetcher 是 Phase2 唯一需要的 Kubernetes 能力（便于测试注入）。
type logFetcher interface {
	GetPodLogs(ctx context.Context, namespace, pod, container string, opts model.LogOptions) ([]byte, error)
}

// Phase2 只对 targets（规则筛选出的异常 Pod）取 current/previous logs。
// 单个任务失败只记 collection_errors，不中断整体（FR-004）。
func (c *collector) Phase2(ctx context.Context, snapshot *model.ClusterSnapshot, targets []model.ResourceRef, opts model.ScanOptions) error {
	if !opts.CollectLogs {
		return nil // 配置关闭日志采集
	}
	concurrency := opts.Phase2Concurrency
	if concurrency <= 0 {
		concurrency = 4 // 默认 Phase2 并发（FR-024）
	}
	podIndex := make(map[string]*model.PodInfo, len(snapshot.Pods))
	for i := range snapshot.Pods {
		p := &snapshot.Pods[i]
		podIndex[refKey(p.Ref)] = p
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex // 保护 snapshot.CollectionErrors
	for _, target := range targets {
		pod, ok := podIndex[refKey(target)]
		if !ok {
			continue // target 不在快照中（可能已被删除），跳过
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				if err := c.collectPodLogs(ctx, pod, opts); err != nil {
					mu.Lock()
					snapshot.CollectionErrors = append(snapshot.CollectionErrors, model.CollectionError{
						Resource:  model.ResourceRef{Kind: "Pod", Namespace: pod.Ref.Namespace, Name: pod.Ref.Name},
						Operation: "get_logs",
						Message:   err.Error(),
						Time:      time.Now().UTC().Format(time.RFC3339),
					})
					mu.Unlock()
				}
			case <-ctx.Done():
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// collectPodLogs 对单个 Pod 的所有容器取日志；单 Pod 有独立超时（FR-022）。
func (c *collector) collectPodLogs(ctx context.Context, pod *model.PodInfo, opts model.ScanOptions) error {
	if pod.Logs == nil {
		pod.Logs = make(map[string]model.CollectedLog)
	}
	podTimeout := opts.PodLogsTimeout
	if podTimeout <= 0 {
		podTimeout = 30 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, podTimeout)
	defer cancel()

	var firstErr error
	for _, container := range pod.Containers {
		cl := model.CollectedLog{}
		current, err := c.fetchLogs(pctx, pod, container.Name, false, opts)
		if err != nil {
			firstErr = firstOr(firstErr, err)
			cl.Error = err.Error()
		} else {
			cl.Current = current
		}
		if opts.CollectPreviousLogs {
			previous, err := c.fetchLogs(pctx, pod, container.Name, true, opts)
			switch {
			case err == nil:
				cl.Previous = previous
			case apierrors.IsNotFound(err):
				// 容器没有 previous logs 时静默跳过（P2.2），不算错误
			default:
				firstErr = firstOr(firstErr, err)
				cl.Error = err.Error()
			}
		}
		pod.Logs[container.Name] = cl
	}
	return firstErr
}

// fetchLogs 拉取并脱敏单个容器的日志；脱敏后再次截断以保证上限不被撑破。
func (c *collector) fetchLogs(ctx context.Context, pod *model.PodInfo, container string, previous bool, opts model.ScanOptions) ([]byte, error) {
	lo := model.LogOptions{
		Container:    container,
		Previous:     previous,
		MaxBytes:     opts.MaxLogBytes,
		MaxLineBytes: opts.MaxLogLineBytes,
	}
	if opts.MaxLogLines > 0 {
		tail := int64(opts.MaxLogLines)
		lo.TailLines = &tail
	}
	if opts.Since > 0 {
		sec := int64(opts.Since.Seconds())
		lo.SinceSeconds = &sec
	}
	raw, err := c.logs.GetPodLogs(ctx, pod.Ref.Namespace, pod.Ref.Name, container, lo)
	if err != nil {
		return nil, err
	}
	// 先脱敏（替换敏感内容为 [REDACTED]），再按上限截断（ADR-006）。
	return truncateLogs(redactor.RedactBytes(raw), lo.MaxBytes, lo.MaxLineBytes), nil
}

// truncateLogs 与 kubernetes 包同策略：按行保留、长行完整（仅 >1MiB 截断），
// 总字节上限按行边界控制（P0 优化）。
func truncateLogs(raw []byte, maxBytes, maxLineBytes int) []byte {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	_ = maxLineBytes
	out := make([]byte, 0, len(raw))
	for len(raw) > 0 {
		nl := bytes.IndexByte(raw, '\n')
		var line []byte
		if nl >= 0 {
			line, raw = raw[:nl], raw[nl+1:]
		} else {
			line, raw = raw, nil
		}
		if len(line) > hardLogLineCap {
			line = line[:hardLogLineCap]
		}
		if len(out) > 0 && len(out)+len(line)+1 > maxBytes {
			break
		}
		out = append(out, line...)
		if nl >= 0 {
			out = append(out, '\n')
		}
	}
	return out
}

func firstOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// refKey 生成快照内资源索引键（namespace/name）。
func refKey(r model.ResourceRef) string {
	return r.Namespace + "/" + r.Name
}

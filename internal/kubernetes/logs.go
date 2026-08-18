package kubernetes

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// GetPodLogs fetches capped log bytes for one container (FR-022).
func (c *Client) GetPodLogs(ctx context.Context, namespace, pod, container string, opts model.LogOptions) ([]byte, error) {
	po := &corev1.PodLogOptions{
		Container:    opts.Container,
		TailLines:    opts.TailLines,
		SinceSeconds: opts.SinceSeconds,
		Previous:     opts.Previous,
	}
	raw, err := c.clientset.CoreV1().Pods(namespace).GetLogs(pod, po).Do(ctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("get logs %s/%s/%s: %w", namespace, pod, container, err)
	}
	return truncateLogs(raw, opts.MaxBytes, opts.MaxLineBytes), nil
}

// hardLogLineCap 单行硬上限：超过该值的超长行才截断（防 OOM）；
// 不超过上限的行完整保留，保证错误日志信息完整（P0 优化）。
const hardLogLineCap = 1 << 20 // 1 MiB

// truncateLogs 按行保留日志：优先按行数（TailLines 由服务端处理），
// 单行不按 maxLineBytes 截断；总字节上限按行边界控制，
// 允许首条超长行（≤ hardLogLineCap）独占配额。
func truncateLogs(raw []byte, maxBytes, maxLineBytes int) []byte {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	_ = maxLineBytes // 兼容参数：单行不再按此截断
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

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

// truncateLogs caps total bytes and per-line bytes while preserving newlines.
func truncateLogs(raw []byte, maxBytes, maxLineBytes int) []byte {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	if maxLineBytes <= 0 {
		maxLineBytes = 1024
	}
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
	}
	out := make([]byte, 0, len(raw))
	for len(raw) > 0 {
		nl := bytes.IndexByte(raw, '\n')
		var line []byte
		if nl >= 0 {
			line, raw = raw[:nl], raw[nl+1:]
		} else {
			line, raw = raw, nil
		}
		if len(line) > maxLineBytes {
			line = line[:maxLineBytes]
		}
		out = append(out, line...)
		if nl >= 0 {
			out = append(out, '\n')
		}
	}
	return out
}

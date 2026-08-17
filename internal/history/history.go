// Package history 提供跨扫描历史对比（1.2，ADR-019）。
// 产出结构化的新增/持续/恢复差异数据：人读日报为次要产出，
// 主要消费者是二期 Agent（scan_cluster Tool 自动携带 latest.json 历史记忆）。
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// LoadPrevious 读取报告目录下的上一份 latest.json；文件不存在返回 (nil, nil)。
func LoadPrevious(dir string) (*model.ScanResult, error) {
	if dir == "" {
		return nil, nil
	}
	path := filepath.Join(dir, "latest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var prev model.ScanResult
	if err := json.Unmarshal(raw, &prev); err != nil {
		return nil, fmt.Errorf("parse previous report: %w", err)
	}
	return &prev, nil
}

// Compare 按 Finding.ID（稳定指纹）对比上轮与本轮，输出新增/持续/恢复。
// 指纹基于 kind/ns/name/rule/证据签名（ADR-003），Pod 归属 workload 归一化。
func Compare(prev *model.ScanResult, current []model.Finding) *model.HistoryDiff {
	diff := &model.HistoryDiff{
		Added:     []model.FindingRef{},
		Continued: []model.FindingRef{},
		Recovered: []model.FindingRef{},
	}
	if prev == nil {
		for _, f := range current {
			diff.Added = append(diff.Added, refOf(f))
		}
		return diff
	}
	diff.PreviousScanAt = prev.Meta.ScanEndedAt
	prevByID := map[string]model.Finding{}
	for _, f := range prev.Findings {
		prevByID[f.ID] = f
	}
	curByID := map[string]bool{}
	for _, f := range current {
		curByID[f.ID] = true
		if _, ok := prevByID[f.ID]; ok {
			diff.Continued = append(diff.Continued, refOf(f))
		} else {
			diff.Added = append(diff.Added, refOf(f))
		}
	}
	for _, f := range prev.Findings {
		if !curByID[f.ID] {
			diff.Recovered = append(diff.Recovered, refOf(f))
		}
	}
	return diff
}

func refOf(f model.Finding) model.FindingRef {
	return model.FindingRef{ID: f.ID, Rule: f.Rule, Severity: f.Severity, Title: f.Title, Resource: f.Resource}
}

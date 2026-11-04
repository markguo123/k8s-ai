package arch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P8.12.5：容器内 CLI 示例必须带 --config。
//
// Pod 里没有默认的 $HOME/.k8s-ai/config.yaml，漏掉 --config 就回落到默认 sqlite（一个全新空库），
// 报的是 platform schema is at version 0——运维会当成「真库没迁移」去跑 db migrate。真机踩过一次
// （2026-11-03），源头是手册与示例里少写这个参数。

func TestInPodCLIExamplesCarryConfig(t *testing.T) {
	for _, rel := range []string{
		"docs/03-运维与部署/01-部署与安装.md",
		"deploy/README.md",
		"deploy/example/object-storage-s3.yaml",
		"deploy/install.env.example",
		"deploy/install.sh",
		"deploy/upgrade.sh",
	} {
		content := readPodCLIRepoFile(t, rel)
		for idx, line := range strings.Split(content, "\n") {
			if !strings.Contains(line, "kubectl") {
				continue
			}
			if !strings.Contains(line, "k8s-ai") {
				continue
			}
			if !strings.Contains(line, "blobs ") && !strings.Contains(line, "db migrate") {
				continue
			}
			if !strings.Contains(line, "--config") {
				t.Errorf("%s 第 %d 行在 Pod 内跑碰库命令却没带 --config：%s", rel, idx+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestManualExplainsTheEmptySchemaFalseAlarm 手册要把这个假象讲透：光在示例里加个参数，
// 下次换个命令还会踩。
func TestManualExplainsTheEmptySchemaFalseAlarm(t *testing.T) {
	manual := readPodCLIRepoFile(t, "docs/03-运维与部署/01-部署与安装.md")
	if !strings.Contains(manual, "platform schema is at version 0") {
		t.Error("部署手册必须点名 platform schema is at version 0 这个假象并给 --config 写法")
	}
}

func readPodCLIRepoFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.HasPrefix(content, "\ufeff") {
		t.Fatalf("%s must not have a BOM", rel)
	}
	return content
}

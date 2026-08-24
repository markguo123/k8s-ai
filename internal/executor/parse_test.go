package executor

import (
	"strings"
	"testing"
)

func TestParseCommandValid(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"kubectl get pods -n default", []string{"kubectl", "get", "pods", "-n", "default"}},
		{"kubectl logs web-0 -n default --tail=50", []string{"kubectl", "logs", "web-0", "-n", "default", "--tail=50"}},
		{`kubectl patch deployment/d --patch '{"spec":{"replicas":3}}'`, []string{"kubectl", "patch", "deployment/d", "--patch", `{"spec":{"replicas":3}}`}},
		{`kubectl delete pod "web 0"`, []string{"kubectl", "delete", "pod", "web 0"}},
		// 流式命令不再在解析层拒绝，改由 executor 超时采样。
		{"kubectl logs -f pod -n default", []string{"kubectl", "logs", "-f", "pod", "-n", "default"}},
		{"kubectl get pods -w", []string{"kubectl", "get", "pods", "-w"}},
		// 扩充的写动词（autoscale/expose/taint/certificate）。
		{"kubectl autoscale deployment api --min=2 --max=5", []string{"kubectl", "autoscale", "deployment", "api", "--min=2", "--max=5"}},
	}
	for _, c := range cases {
		got, err := ParseCommand(c.cmd)
		if err != nil {
			t.Errorf("%s: %v", c.cmd, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v", c.cmd, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: token %d = %q, want %q", c.cmd, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseCommandRejects(t *testing.T) {
	rejects := []string{
		"kubectl get pods | grep x",
		"kubectl get pods && kubectl get nodes",
		"kubectl get pods > out.txt",
		"kubectl get pods $FOO",
		"kubectl get pods `id`",
		"kubectl get pods; kubectl get nodes",
		"get pods",
		"kubectl edit deployment/d",
		"kubectl attach pod",
		"kubectl port-forward svc/x 80:80",
		"kubectl exec -it pod -- ls",
		"kubectl exec -i pod -- ls",
		"kubectl frobnicate pods",
		`kubectl get pods "unbalanced`,
		"",
		"kubectl " + strings.Repeat("x", 3000),
	}
	for _, cmd := range rejects {
		if _, err := ParseCommand(cmd); err == nil {
			t.Errorf("应拒绝: %q", cmd)
		}
	}
}

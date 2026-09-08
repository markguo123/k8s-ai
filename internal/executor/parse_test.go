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

func TestParseCommandShellDetected(t *testing.T) {
	// shell 元字符不再被拒绝，而是标记 isShell=true。
	shellCmds := []string{
		"kubectl get pods | grep x",
		"kubectl get pods && kubectl get nodes",
		"kubectl get pods > out.txt",
		"kubectl get pods $FOO",
		"kubectl get pods; kubectl get nodes",
		"cat <<'EOF' | kubectl apply -f -",
		"kubectl delete pod x 2>/dev/null || true",
	}
	for _, cmd := range shellCmds {
		pc, err := ParseCommandDetail(cmd)
		if err != nil {
			t.Errorf("shell 命令不应拒绝: %q: %v", cmd, err)
			continue
		}
		if !pc.IsShell {
			t.Errorf("shell 命令应标记 isShell=true: %q", cmd)
		}
	}
}

func TestParseCommandGlobalFlagBeforeVerb(t *testing.T) {
	// 动词前的全局 flag 及其独立值不应被误当成 verb。
	cases := []string{
		"kubectl -n default get pods",
		"kubectl --namespace default get pods",
		"kubectl --context prod get pods",
		"kubectl -o yaml get pods",
	}
	for _, cmd := range cases {
		pc, err := ParseCommandDetail(cmd)
		if err != nil {
			t.Errorf("%q: %v", cmd, err)
			continue
		}
		if pc.IsShell {
			t.Errorf("%q 不应标记为 shell", cmd)
		}
		if len(pc.Argv) < 2 || pc.Argv[0] != "kubectl" {
			t.Errorf("%q: argv = %v", cmd, pc.Argv)
		}
	}
}

func TestParseCommandRejects(t *testing.T) {
	rejects := []string{
		"get pods",
		"kubectl edit deployment/d",
		"kubectl attach pod",
		"kubectl port-forward svc/x 80:80",
		"kubectl exec -it pod -- ls",
		"kubectl exec -i pod -- ls",
		"kubectl frobnicate pods",
		`kubectl get pods "unbalanced`,
		"",
		"kubectl " + strings.Repeat("x", 5000),
	}
	for _, cmd := range rejects {
		if _, err := ParseCommand(cmd); err == nil {
			t.Errorf("应拒绝: %q", cmd)
		}
	}
}

func TestParseCommandRejectsInteractiveShell(t *testing.T) {
	rejects := []string{
		"kubectl edit deployment/d && echo ok",
		"kubectl attach pod; echo ok",
		"kubectl port-forward svc/x 80:80 && echo ok",
		"kubectl exec -it pod -- sh && echo ok",
		"kubectl exec -i pod -- sh; echo ok",
		"echo ok | kubectl edit deployment/d",
	}
	for _, cmd := range rejects {
		if _, err := ParseCommandDetail(cmd); err == nil {
			t.Errorf("应拒绝 shell 模式下的交互式命令: %q", cmd)
		}
	}
}

// TestParseCommandNonShell 验证普通命令不误判为 shell。
func TestParseCommandNonShell(t *testing.T) {
	plainCmds := []string{
		"kubectl get pods -n default",
		"kubectl logs web-0 -n default --tail=50",
		`kubectl patch deployment/d --patch '{"spec":{"replicas":3}}'`,
		"kubectl delete pod web-0 -n default",
	}
	for _, cmd := range plainCmds {
		pc, err := ParseCommandDetail(cmd)
		if err != nil {
			t.Errorf("%s: %v", cmd, err)
			continue
		}
		if pc.IsShell {
			t.Errorf("普通命令不应标记 isShell: %q", cmd)
		}
	}
}

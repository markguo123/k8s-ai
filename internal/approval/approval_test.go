package approval

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestIsReadOnlyCommandShellNeverReadOnly(t *testing.T) {
	// 含 shell 语法的命令永不归类为只读（即使动词是 get）。
	shellReadOnlyCmds := []string{
		"kubectl get pods | grep crash",
		"kubectl get pods && kubectl get nodes",
		"kubectl get pods > out.txt",
		"kubectl get pods; kubectl get nodes",
		"kubectl get pods $FOO",
		"cat <<'EOF' | kubectl apply -f -",
		"kubectl get pods 2>/dev/null || true",
	}
	for _, cmd := range shellReadOnlyCmds {
		if IsReadOnlyCommand(cmd) {
			t.Errorf("含 shell 语法的命令不应为只读: %q", cmd)
		}
	}
	// 引号内的管道符号不应误判为 shell（虽然实际 kubectl 不会有引号内管道，但逻辑需正确）。
	if !IsReadOnlyCommand(`kubectl get pods -n "default"`) {
		t.Fatal("引号内不含 shell 语法，应为只读")
	}
}

func TestIsForbiddenCommand(t *testing.T) {
	forbidden := []string{
		"kubectl get secret -n default",                          // get secret
		"kubectl get secrets",                                    // 复数
		"kubectl get secret/db-password -o yaml",                 // secret/name
		"kubectl describe secret db-password -n production",      // describe
		"kubectl delete secret db-password",                      // 写也拦（输出/审计不落敏感数据）
		"kubectl get --raw /api/v1/namespaces/default/secrets/x", // --raw 绕过
		"kubectl get --raw=/api/v1/pods",                         // --raw= 形式
		"kubectl get pods -n SECRET",                             // 大小写不敏感（命名空间极端例，宁严勿漏）
		`kubectl get sec"ret" -o yaml`,                           // 引号混淆 secret
		"kubectl get $(echo secret) -o yaml",                     // 命令替换混淆 secret
		`kubectl get '--raw' /api/v1/pods`,                       // 引号混淆 --raw
	}
	for _, cmd := range forbidden {
		if !IsForbiddenCommand(cmd) {
			t.Errorf("IsForbiddenCommand(%q) = false, want true", cmd)
		}
	}
	allowed := []string{
		"kubectl get pods -n default",
		"kubectl get deployment web -o yaml",
		"kubectl logs pod-1 -n default --tail=50",
		"kubectl describe pod web-0",
		"kubectl get configmap my-secret-config -n default", // 名字含 secret 但资源是 configmap
		"kubectl get pods -l app=secret-app",                // label 值含 secret
		"kubectl get pod my-secret-pod -n default",          // 资源名含 secret 但不是 secret 资源
		"kubectl top pods -n production",
		"kubectl delete pod web-0 -n default",
	}
	for _, cmd := range allowed {
		if IsForbiddenCommand(cmd) {
			t.Errorf("IsForbiddenCommand(%q) = true, want false", cmd)
		}
	}
}

func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want model.RiskLevel
	}{
		{"kubectl get pods -n default", model.RiskSafe},
		{"kubectl -n default get pods", model.RiskSafe},
		{"kubectl --context prod get pods", model.RiskSafe},
		{"kubectl logs pod-1 -n default", model.RiskSafe},
		{"kubectl scale deployment/payment-api --replicas=5", model.RiskMedium},
		{"kubectl rollout restart deployment/payment-api", model.RiskMedium},
		{"kubectl rollout undo deployment/payment-api", model.RiskHigh},
		{"kubectl patch deployment/payment-api --patch '{}'", model.RiskHigh},
		{"kubectl delete pod web-0 -n default", model.RiskHigh},
		{"kubectl delete deployment/payment-api -n production", model.RiskCritical},
		{"kubectl delete pvc data -n production", model.RiskCritical},
		{"kubectl delete service web -n production", model.RiskCritical},
		{"kubectl delete -n production service web", model.RiskCritical},
		{"kubectl delete namespace production", model.RiskCritical},
		{"kubectl exec pod-1 -- ls", model.RiskHigh},
		{"kubectl edit deployment/payment-api", model.RiskCritical},
		// rollout history/status 只读。
		{"kubectl rollout history deployment/payment-api", model.RiskSafe},
		{"kubectl rollout status deployment/payment-api", model.RiskSafe},
		// --dry-run 的已知写动词降级为只读。
		{"kubectl apply -f x.yaml --dry-run=client", model.RiskSafe},
		{"kubectl scale deployment/api --replicas=3 --dry-run=server", model.RiskSafe},
		// delete --dry-run 同样不实际变更（delete 走特殊分支，需单独覆盖）。
		{"kubectl delete pod x --dry-run=client", model.RiskSafe},
		{"kubectl delete namespace production --dry-run=server", model.RiskSafe},
		// 扩充写动词分类。
		{"kubectl autoscale deployment api --min=2 --max=5", model.RiskMedium},
		{"kubectl expose deployment api --port=80", model.RiskMedium},
		{"kubectl taint node n key=value:NoSchedule", model.RiskMedium},
		{"kubectl certificate approve my-csr", model.RiskHigh},
	}
	for _, c := range cases {
		if got := ClassifyCommand(c.cmd); got != c.want {
			t.Errorf("%s: risk = %s, want %s", c.cmd, got, c.want)
		}
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	if !IsReadOnlyCommand("kubectl get pods") {
		t.Fatal("get 应为只读")
	}
	if !IsReadOnlyCommand("kubectl -n default get pods") {
		t.Fatal("动词前带 -n 的 get 应为只读")
	}
	if IsReadOnlyCommand("kubectl delete pod x") {
		t.Fatal("delete 不应为只读")
	}
	// rollout history/status 只读，无需审批。
	if !IsReadOnlyCommand("kubectl rollout history deployment/api") {
		t.Fatal("rollout history 应为只读")
	}
	if !IsReadOnlyCommand("kubectl rollout status deployment/api") {
		t.Fatal("rollout status 应为只读")
	}
	if IsReadOnlyCommand("kubectl rollout restart deployment/api") {
		t.Fatal("rollout restart 不应为只读")
	}
	// --dry-run 的写动词降级为只读。
	if !IsReadOnlyCommand("kubectl apply -f x.yaml --dry-run=client") {
		t.Fatal("apply --dry-run 应为只读")
	}
	if !IsReadOnlyCommand("kubectl delete pod x --dry-run=client") {
		t.Fatal("delete --dry-run 应为只读")
	}
}

func TestPolicyRequiresApproval(t *testing.T) {
	p := Policy{RequireApproval: true}
	if !p.RequiresApproval() {
		t.Fatal("默认应要求审批")
	}
	skip := Policy{RequireApproval: true, SkipPermissions: true}
	if skip.RequiresApproval() {
		t.Fatal("skip-permissions 应免审批")
	}
	off := Policy{}
	if off.RequiresApproval() {
		t.Fatal("require_approval=false 应免审批")
	}
}

func TestIsDenied(t *testing.T) {
	if !IsDenied([]string{"delete namespace"}, "kubectl delete namespace production") {
		t.Fatal("应命中 deny_ops")
	}
	if IsDenied(nil, "kubectl get pods") {
		t.Fatal("空 deny_ops 不应拦截")
	}
	if IsDenied([]string{"delete namespace"}, "kubectl get namespaces") {
		t.Fatal("不应误伤只读命令")
	}
}

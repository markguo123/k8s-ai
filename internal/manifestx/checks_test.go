package manifestx

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const privilegedDeployment = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: privileged-web
  namespace: d
spec:
  replicas: 1
  template:
    spec:
      hostNetwork: true
      hostPID: true
      automountServiceAccountToken: true
      containers:
        - name: app
          image: nginx:1.27.0
          securityContext:
            privileged: true
            allowPrivilegeEscalation: true
          ports:
            - containerPort: 8080
              hostPort: 8080
      volumes:
        - name: host-root
          hostPath:
            path: /
`

func TestDangerousFieldScan(t *testing.T) {
	objs := mustParse(t, privilegedDeployment, testOpts())
	o := objs[0]
	want := map[string]bool{
		DangerPrivileged:          true,
		DangerHostNetwork:         true,
		DangerHostPID:             true,
		DangerHostPath:            true,
		DangerHostPort:            true,
		DangerAutomountToken:      true,
		DangerPrivilegeEscalation: true,
	}
	got := map[string]bool{}
	for _, code := range o.Dangerous {
		got[code] = true
	}
	for code := range want {
		if !got[code] {
			t.Fatalf("危险字段 %s 未识别，实际命中 %v", code, o.Dangerous)
		}
	}
	// 危险字段提升风险但不阻断创建（由审批环节放行）。
	if o.HasErrors() {
		t.Fatalf("危险字段不应直接阻断: %+v", o.Errors())
	}
	for _, f := range o.Warnings() {
		if f.Code == CodeDangerousField && f.Field == "" {
			t.Fatal("危险字段结论必须带字段路径")
		}
	}
}

func TestBenignWorkloadHasNoDanger(t *testing.T) {
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: d
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: app
          image: nginx:1.27.0
          securityContext:
            allowPrivilegeEscalation: false
`
	objs := mustParse(t, manifest, testOpts())
	if len(objs[0].Dangerous) != 0 {
		t.Fatalf("良性清单不应命中危险字段: %v", objs[0].Dangerous)
	}
}

func TestImageTagFinding(t *testing.T) {
	objs := mustParse(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: d
spec:
  template:
    spec:
      containers:
        - name: a
          image: nginx
        - name: b
          image: redis:latest
        - name: c
          image: ghcr.io/acme/app@sha256:aaaa
`, testOpts())
	var fields []string
	for _, f := range objs[0].Findings {
		if f.Code == CodeImageUnpinned {
			fields = append(fields, f.Field)
		}
	}
	if len(fields) != 2 {
		t.Fatalf("应命中 2 条未固定 tag 结论，实际 %v", fields)
	}
}

func TestFingerprintStableAcrossFieldOrder(t *testing.T) {
	a := mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: d\ndata:\n  A: '1'\n  B: '2'\n", testOpts())
	b := mustParse(t, "kind: ConfigMap\napiVersion: v1\ndata:\n  B: '2'\n  A: '1'\nmetadata:\n  namespace: d\n  name: c\n", testOpts())
	if a[0].Fingerprint == "" || a[0].Fingerprint != b[0].Fingerprint {
		t.Fatalf("同语义 manifest 指纹应一致: %s vs %s", a[0].Fingerprint, b[0].Fingerprint)
	}
	c := mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: d\ndata:\n  A: '1'\n  B: '3'\n", testOpts())
	if a[0].Fingerprint == c[0].Fingerprint {
		t.Fatal("内容不同的指纹不应一致")
	}
	// 服务端回填字段不参与指纹。
	d := mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: d\n  resourceVersion: \"123\"\n  uid: abc\ndata:\n  A: '1'\n  B: '2'\n", testOpts())
	if d[0].Fingerprint != a[0].Fingerprint {
		t.Fatalf("服务端字段不应影响指纹: %s vs %s", d[0].Fingerprint, a[0].Fingerprint)
	}
	if got := Fingerprints(a); len(got) != 1 || got[0] != a[0].Fingerprint {
		t.Fatalf("Fingerprints 结果错误: %v", got)
	}
}

func TestSecretGenerateChannel(t *testing.T) {
	objs := mustParse(t, `
apiVersion: v1
kind: Secret
metadata:
  name: db-creds
  namespace: d
type: Opaque
stringData:
  password: "@generate"
`, testOpts())
	o := objs[0]
	if o.HasErrors() {
		t.Fatalf("@generate 通道应放行: %+v", o.Errors())
	}
	if len(o.SecretKeys) != 1 || o.SecretKeys[0].Key != "password" || !o.SecretKeys[0].Generated {
		t.Fatalf("Secret 指纹错误: %+v", o.SecretKeys)
	}
}

func TestSecretLiteralValueRejected(t *testing.T) {
	const secretValue = "Sup3r-S3cret-Value"
	manifest := `
apiVersion: v1
kind: Secret
metadata:
  name: db-creds
  namespace: d
type: Opaque
stringData:
  password: ` + secretValue + `
`
	objs := mustParse(t, manifest, testOpts())
	o := objs[0]
	if !hasFinding(objs, CodeSecretLiteralValue) {
		t.Fatalf("明文 Secret 值必须被拒绝: %+v", o.Findings)
	}
	if !o.HasErrors() {
		t.Fatal("明文 Secret 值必须阻断创建")
	}
	if len(o.SecretKeys) != 1 || o.SecretKeys[0].Length != len(secretValue) || o.SecretKeys[0].SHA256 == "" {
		t.Fatalf("应产出 key/长度/指纹: %+v", o.SecretKeys)
	}
	// 值不得出现在展示 manifest、结构化结果与审计可见字段里。
	if strings.Contains(o.Manifest, secretValue) {
		t.Fatal("展示 manifest 泄露明文值")
	}
	raw, err := json.Marshal(o)
	if err != nil || strings.Contains(string(raw), secretValue) {
		t.Fatalf("Object 序列化泄露明文值: %v", err)
	}
	if !strings.Contains(o.Manifest, "<redacted length=") {
		t.Fatalf("展示 manifest 应保留脱敏标记: %s", o.Manifest)
	}
}

func TestSecretDataAndErrors(t *testing.T) {
	opts := testOpts()
	// data 里的 base64 值：产出指纹（按解码后字节）并拒绝明文。
	encoded := base64.StdEncoding.EncodeToString([]byte("token-value"))
	objs := mustParse(t, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: d\ndata:\n  token: "+encoded+"\n", opts)
	if len(objs[0].SecretKeys) != 1 || objs[0].SecretKeys[0].Length != len("token-value") {
		t.Fatalf("data 指纹应按解码后长度: %+v", objs[0].SecretKeys)
	}
	if !hasFinding(objs, CodeSecretLiteralValue) {
		t.Fatal("data 明文值同样拒绝")
	}
	// 非法 base64。
	objs = mustParse(t, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: d\ndata:\n  token: \"!!!not-base64!!!\"\n", opts)
	if !hasFinding(objs, CodeSecretInvalidData) {
		t.Fatalf("非法 base64 应报错: %+v", objs[0].Findings)
	}
	// 占位符只能出现在 stringData。
	objs = mustParse(t, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: d\ndata:\n  token: \""+PlaceholderGenerate+"\"\n", opts)
	if !hasFinding(objs, CodeSecretBadDataPlaceh) {
		t.Fatalf("data 中的占位符应报错: %+v", objs[0].Findings)
	}
	// 空 Secret 只给提示。
	objs = mustParse(t, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: d\n", opts)
	if !hasFinding(objs, CodeSecretNoKeys) || objs[0].HasErrors() {
		t.Fatalf("空 Secret 应只给 warn: %+v", objs[0].Findings)
	}
}

func TestNonSecretUnaffectedBySecretRules(t *testing.T) {
	objs := mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: d\ndata:\n  password: plain-text-is-fine\n", testOpts())
	if objs[0].HasErrors() || len(objs[0].SecretKeys) != 0 {
		t.Fatalf("ConfigMap 不应触发 Secret 规则: %+v %+v", objs[0].Errors(), objs[0].SecretKeys)
	}
}

func TestDisplayManifestDropsServerFields(t *testing.T) {
	content := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "c", "uid": "u", "resourceVersion": "9"},
		"status":     map[string]any{"phase": "Active"},
	}
	view := DisplayManifest(content, nil)
	for _, unwanted := range []string{"uid", "resourceVersion", "status"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("展示 manifest 不应包含 %s:\n%s", unwanted, view)
		}
	}
	if Fingerprint(content) != Fingerprint(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "c"},
	}) {
		t.Fatal("指纹应忽略服务端字段")
	}
}

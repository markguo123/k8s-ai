// 红线回归：Secret 口令、kubeconfig 私钥、模型 API key 三类敏感信息在「API 响应、
// 审计记录、SQLite 落库字节、会话历史」四处都不得以明文出现。
// 对应 AGENTS.md 的 LLM Safety 红线与四期 P3.13 验收第二条。
package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// redactedMark 是 security.Redactor 的替换标记。
const redactedMark = "[REDACTED]"

// waitUntil 轮询条件直到成立：Chat 在 goroutine 内落库，202 返回后需要等一轮。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// denyPlaintext 断言若干文本都不含敏感串；where 标明检查的位置，便于定位泄漏出口。
func denyPlaintext(t *testing.T, sentinel string, texts map[string]string) {
	t.Helper()
	for where, text := range texts {
		if strings.Contains(text, sentinel) {
			t.Errorf("%s 泄漏明文 %q", where, sentinel)
		}
	}
}

// TestRedlineSessionSecretsRedactedAtRest 覆盖会话这条最容易泄漏的通道：owner 把
// 口令和 bearer 直接写进对话，落库、列表、审计、改名都必须只剩 [REDACTED]。
func TestRedlineSessionSecretsRedactedAtRest(t *testing.T) {
	h := newUserHarness(t)
	sessions := "/api/v1/sessions"
	const secretValue = "SENTINEL-secretvalue-4f9c1a"
	const bearerValue = "SENTINEL-bearerstring-77ab99"

	var created struct {
		ID string `json:"id"`
	}
	h.must(h.do(http.MethodPost, sessions, h.ownerToken, `{}`), http.StatusOK).plain(t, &created)

	query := `帮我看下这个 Deployment，运维口令 password: ` + secretValue + ` 接口调试用 bearer ` + bearerValue
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}
	h.must(h.do(http.MethodPost, sessions+"/"+created.ID+"/send-message", h.ownerToken, string(body)), http.StatusAccepted)

	ctx := context.Background()
	waitUntil(t, "会话消息落库", func() bool {
		sess, err := h.st.Sessions().Get(ctx, created.ID)
		return err == nil && len(sess.Messages) > 0
	})

	// 改名同样不能把凭据带进库里。
	const nameToken = "SENTINEL-nametoken-2c8d55"
	h.must(h.do(http.MethodPost, sessions+"/"+created.ID+"/rename", h.ownerToken, `{"name":"排障 token: `+nameToken+`"}`), http.StatusOK)

	sess, err := h.st.Sessions().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	stored, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("序列化会话失败: %v", err)
	}
	if !strings.Contains(string(stored), redactedMark) {
		t.Errorf("会话内容未脱敏，缺少 %s 标记: %s", redactedMark, stored)
	}

	var listed string
	if r := h.do(http.MethodGet, sessions, h.ownerToken, ""); r.status == http.StatusOK {
		listed = r.body
	}
	adminList := h.do(http.MethodGet, sessions, h.adminToken, "")

	var auditText strings.Builder
	for _, e := range h.auditEvents() {
		auditText.WriteString(e.Action)
		auditText.WriteString(e.Payload)
	}

	for _, sentinel := range []string{secretValue, bearerValue, nameToken} {
		denyPlaintext(t, sentinel, map[string]string{
			"落库会话":   string(stored),
			"SQLite 文件": string(h.dbBytes()),
			"owner 会话列表": listed,
			"admin 会话列表": adminList.body,
			"审计记录":   auditText.String(),
		})
	}
}

// TestRedlineAPIKeyStoredAsCiphertext 覆盖模型 API key：配置中心以 v1: 密文落库，
// 响应、审计、库文件三处都取不到明文。
func TestRedlineAPIKeyStoredAsCiphertext(t *testing.T) {
	h := newHarness(t)
	admin := h.adminToken()
	const apiKey = "sk-liveSENTINELKEY0123456789"

	h.must(h.do(http.MethodPatch, "/api/v1/settings/model", admin, `{"api_key":"`+apiKey+`"}`), http.StatusOK)

	raw, err := h.st.Settings().Get(context.Background(), "model", "api_key")
	if err != nil {
		t.Fatalf("读取配置行失败: %v", err)
	}
	if !strings.HasPrefix(raw.Value, "v1:") {
		t.Errorf("api_key 应以 v1: 版本前缀密文落库，实得前缀 %q", firstRunes(raw.Value, 8))
	}
	if !raw.Sensitive {
		t.Error("api_key 应标记为 sensitive")
	}

	var auditText strings.Builder
	for _, e := range h.auditEvents() {
		auditText.WriteString(e.Action)
		auditText.WriteString(e.Payload)
	}
	denyPlaintext(t, apiKey, map[string]string{
		"配置响应":   h.do(http.MethodGet, "/api/v1/settings/model", admin, "").body,
		"SQLite 文件": string(h.dbBytes()),
		"审计记录":   auditText.String(),
	})
}

// TestRedlineKubeconfigEncryptedAtRest 覆盖集群 kubeconfig：入库存密文，任何读取
// 出口与库文件都不含私钥串。
func TestRedlineKubeconfigEncryptedAtRest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminToken()
	const keyBlob = "SENTINEL-PRIVATEKEYBLOB-1d7f33"
	kubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: prod\n  cluster:\n    server: https://kube.example.internal:6443\nusers:\n- name: admin\n  user:\n    client-key-data: " + keyBlob + "\n"

	reqBody, err := json.Marshal(map[string]string{"name": "redline-cluster", "kubeconfig": kubeconfig})
	if err != nil {
		t.Fatalf("构造集群请求失败: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	h.must(h.do(http.MethodPost, "/api/v1/clusters", admin, string(reqBody)), http.StatusCreated).data(t, &created)

	cred, err := h.st.Credentials().GetByCluster(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取集群凭据失败: %v", err)
	}
	if !strings.HasPrefix(cred.KubeconfigSecret, "v1:") {
		t.Errorf("kubeconfig 应以 v1: 密文落库，实得前缀 %q", firstRunes(cred.KubeconfigSecret, 8))
	}

	var auditText strings.Builder
	for _, e := range h.auditEvents() {
		auditText.WriteString(e.Action)
		auditText.WriteString(e.Payload)
	}
	denyPlaintext(t, keyBlob, map[string]string{
		"集群列表响应": h.do(http.MethodGet, "/api/v1/clusters", admin, "").body,
		"集群详情响应": h.do(http.MethodGet, "/api/v1/clusters/"+created.ID, admin, "").body,
		"当前集群响应": h.do(http.MethodGet, "/api/v1/clusters/current", admin, "").body,
		"用户列表响应": h.do(http.MethodGet, "/api/v1/users", admin, "").body,
		"SQLite 文件": string(h.dbBytes()),
		"审计记录":   auditText.String(),
	})
}

// TestRedactJSONPayloadCoversPlatformSensitiveKeys 直接校验统一出站脱敏入口：
// Secret 值、kubeconfig、api_key 三类键整值替换，非敏感内容保持不变。
func TestRedactJSONPayloadCoversPlatformSensitiveKeys(t *testing.T) {
	const secretB64 = "UwMgU0VOVElORUxTIHZhbHVl"
	const keyBlob = "SENTINEL-KEYBLOB-0x99"

	jsonPayload := `{"name":"db-secret","data":{"password":"` + secretB64 + `"},"kubeconfig":"apiVersion: v1\nclient-key-data: ` + keyBlob + `","spec":{"template":{"api_key":"sk-SENTINELKEY00001111"}},"note":"read the logs please"}`
	out := store.RedactJSONPayload(jsonPayload)
	for _, leak := range []string{secretB64, keyBlob, "sk-SENTINELKEY00001111"} {
		if strings.Contains(out, leak) {
			t.Errorf("JSON 负载泄漏 %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, redactedMark) {
		t.Errorf("JSON 负载应出现脱敏标记: %s", out)
	}
	if !strings.Contains(out, "read the logs please") {
		t.Errorf("非敏感字段被误伤: %s", out)
	}

	// 非敏感键名里的自由文本走兜底模式。
	if text := store.RedactJSONPayload(`{"note":"口令 password: SENTINEL-PLAINTEXT-77"}`); strings.Contains(text, "SENTINEL-PLAINTEXT-77") {
		t.Errorf("非 JSON 键名场景未兜底脱敏: %s", text)
	}
	// 非法 JSON 退回纯文本脱敏。
	if text := store.RedactJSONPayload(`token=SENTINEL-PLAINTEXT-88`); strings.Contains(text, "SENTINEL-PLAINTEXT-88") {
		t.Errorf("非 JSON 负载未兜底脱敏: %s", text)
	}
}

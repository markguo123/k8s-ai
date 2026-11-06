package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

type secretValueReaderStub struct {
	namespace string
	name      string
	keys      []string
	values    map[string][]byte
}

func (s *secretValueReaderStub) GetSecretValues(_ context.Context, namespace, name string, keys []string) (map[string][]byte, error) {
	s.namespace = namespace
	s.name = name
	s.keys = append([]string(nil), keys...)
	return s.values, nil
}

func TestSecretValueToolSeparatesModelAndSafeData(t *testing.T) {
	const plain = "db-password-42"
	reader := &secretValueReaderStub{values: map[string][]byte{"password": []byte(plain)}}
	tool := &secretValueTool{reader: reader}
	if tool.IsReadOnly(nil) {
		t.Fatal("Secret 按需读取必须每次审批，不能归为自动只读")
	}
	if tool.Risk(nil) != model.RiskHigh {
		t.Fatalf("risk = %s, want HIGH", tool.Risk(nil))
	}
	if !tool.RequiresHumanApproval(nil) {
		t.Fatal("Secret 读取必须每次人工审批")
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"namespace":"prod",
		"name":"db-secret",
		"keys":["password"],
		"reason":"验证应用使用的数据库口令是否为旧值"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if reader.namespace != "prod" || reader.name != "db-secret" || len(reader.keys) != 1 || reader.keys[0] != "password" {
		t.Fatalf("reader request = %s/%s %v", reader.namespace, reader.name, reader.keys)
	}
	data, _ := json.Marshal(result.Data)
	if strings.Contains(string(data), plain) || strings.Contains(result.Text, plain) {
		t.Fatal("安全结果不得包含 Secret 明文")
	}
	serializedResult, _ := json.Marshal(result)
	if strings.Contains(string(serializedResult), plain) {
		t.Fatal("ToolResult JSON 序列化不得包含 ModelOnly/SensitiveValues")
	}
	modelOnly, _ := json.Marshal(result.ModelOnly)
	if !strings.Contains(string(modelOnly), plain) {
		t.Fatalf("ModelOnly 应包含当前轮诊断值: %s", modelOnly)
	}
	if !containsString(result.SensitiveValues, plain) {
		t.Fatalf("SensitiveValues 缺少原值: %v", result.SensitiveValues)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSecretValueToolApprovalPlanHasReasonAndNoValue(t *testing.T) {
	tool := &secretValueTool{reader: &secretValueReaderStub{}}
	plan := tool.PreviewApproval(json.RawMessage(`{
		"namespace":"prod",
		"name":"db-secret",
		"keys":["password","username"],
		"reason":"核对数据库认证配置"
	}`))
	if plan.Title != "读取 Secret 值" || plan.Target.Namespace != "prod" || plan.Target.Name != "db-secret" {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.Contains(plan.Summary, "核对数据库认证配置") || !strings.Contains(plan.Summary, "password") {
		t.Fatalf("summary 缺少原因或 key: %q", plan.Summary)
	}
	if len(plan.Checks) == 0 || plan.Checks[0].Name != "敏感数据" {
		t.Fatalf("缺少敏感数据提示: %+v", plan.Checks)
	}
}

func TestSecretValueToolRejectsTooManyKeys(t *testing.T) {
	tool := &secretValueTool{reader: &secretValueReaderStub{}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"namespace":"prod",
		"name":"db-secret",
		"keys":["a","b","c","d","e","f"],
		"reason":"test"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != tools.ErrorInvalidArgument {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, tools.ErrorInvalidArgument)
	}
}

func TestSecretValueToolRejectsOversizedValue(t *testing.T) {
	tool := &secretValueTool{reader: &secretValueReaderStub{values: map[string][]byte{
		"password": bytes.Repeat([]byte("x"), maxSecretValueBytes+1),
	}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"namespace":"prod","name":"db-secret","keys":["password"],"reason":"test"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != tools.ErrorInvalidArgument {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, tools.ErrorInvalidArgument)
	}
}

func TestSecretValueToolRejectsOversizedBatch(t *testing.T) {
	tool := &secretValueTool{reader: &secretValueReaderStub{values: map[string][]byte{
		"a": bytes.Repeat([]byte("x"), maxSecretValueBytes),
		"b": bytes.Repeat([]byte("y"), maxSecretValueBytes),
		"c": []byte("z"),
	}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"namespace":"prod","name":"db-secret","keys":["a","b","c"],"reason":"test"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != tools.ErrorInvalidArgument {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, tools.ErrorInvalidArgument)
	}
}

type secretCapableReader struct {
	Reader
	*secretValueReaderStub
}

func TestRegisterAllSecretModeControlsTool(t *testing.T) {
	base := testReader()
	reader := &secretCapableReader{Reader: base, secretValueReaderStub: &secretValueReaderStub{}}
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{mode: "off", want: false},
		{mode: "create_only", want: false},
		{mode: "read_on_demand", want: true},
	} {
		reg := tools.NewRegistry()
		if err := RegisterAllWithOptions(reg, reader, RegisterOptions{SecretAccess: tc.mode}); err != nil {
			t.Fatal(err)
		}
		_, got := reg.Lookup("get_secret_value")
		if got != tc.want {
			t.Errorf("mode=%s get_secret_value present=%t, want %t", tc.mode, got, tc.want)
		}
	}
}

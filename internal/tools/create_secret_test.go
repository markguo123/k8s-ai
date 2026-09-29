package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
)

// fakeSecretOutcome 复刻 executor.materializeSecret 的可审计结论：
// $GENERATE 总是产出指纹，$ASK 只在真的拿到用户值时产出（dry run 用临时值，不记录）。
func fakeSecretOutcome(req executor.ApplyRequest) (keys, generated []string, statuses []executor.SecretKeyStatus) {
	stringData, ok := req.Object["stringData"].(map[string]any)
	if !ok {
		return nil, nil, nil
	}
	names := make([]string, 0, len(stringData))
	for k := range stringData {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, _ := stringData[name].(string)
		var value, source string
		switch raw {
		case manifestx.PlaceholderGenerate:
			value, source = "generated-by-fixture", executor.SecretValueSourceGenerate
			generated = append(generated, name)
		case manifestx.PlaceholderAsk:
			userValue := strings.TrimSpace(req.SecretValues[name])
			if userValue == "" {
				continue
			}
			value, source = userValue, executor.SecretValueSourceUser
		default:
			continue
		}
		keys = append(keys, name)
		statuses = append(statuses, executor.FingerprintSecretValue(name, value, source))
	}
	return keys, generated, statuses
}

const secretAskManifest = `apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: default
type: Opaque
stringData:
  password: $ASK
`

const secretGenerateManifest = `apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: default
type: Opaque
stringData:
  password: $GENERATE
`

func TestCreateToolApplyWithoutSecretValueSubmitsNothing(t *testing.T) {
	runner := newFakeCreateRunner()
	tool := newTestCreateTool(runner, NewProposalStore())
	if _, err := tool.Execute(context.Background(), createInput(t, secretAskManifest, CreateModeDryRun, nil)); err != nil {
		t.Fatal(err)
	}

	tr, err := tool.Execute(context.Background(), createInput(t, secretAskManifest, CreateModeApply, nil))
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	if data["code"] != errCreateSecretValueRequired {
		t.Fatalf("缺少用户值时应报 secret_value_required，实际 %v", data["error"])
	}
	// 只有 dry run 那一次提交，且必须是 dry run：缺值不能留下半成品对象。
	if len(runner.requests) != 1 || !runner.requests[0].DryRun {
		t.Fatalf("缺值时不能真实提交，实际请求 %+v", runner.requests)
	}
}

func TestCreateToolApplyPassesSecretValuesOutOfBand(t *testing.T) {
	const plain = "user-typed-password"
	runner := newFakeCreateRunner()
	tool := newTestCreateTool(runner, NewProposalStore())
	ctx := context.Background()
	if _, err := tool.Execute(ctx, createInput(t, secretAskManifest, CreateModeDryRun, nil)); err != nil {
		t.Fatal(err)
	}

	ctx = WithSecretValues(ctx, map[SecretValueKey]string{
		{Kind: "Secret", Namespace: "default", Name: "db", Key: "password"}: plain,
	})
	tr, err := tool.Execute(ctx, createInput(t, secretAskManifest, CreateModeApply, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("提交次数 = %d, want 2", len(runner.requests))
	}
	if got := runner.requests[1].SecretValues["password"]; got != plain {
		t.Fatalf("执行器收到的 SecretValues = %v", runner.requests[1].SecretValues)
	}

	data := dataOf(t, tr)
	touches, ok := data[DataSecretTouches].([]SecretTouch)
	if !ok || len(touches) != 1 || touches[0].Name != "db" || len(touches[0].Keys) != 1 {
		t.Fatalf("缺少 Secret 指纹结论: %#v", data[DataSecretTouches])
	}
	if touches[0].Keys[0].Source != executor.SecretValueSourceUser || touches[0].Keys[0].SHA256 == "" {
		t.Fatalf("指纹结论不完整: %+v", touches[0].Keys[0])
	}
	// 红线：明文既不进 Text，也不进给模型/会话/审计的 Data。
	if encoded, err := json.Marshal(tr.Data); err != nil {
		t.Fatal(err)
	} else if strings.Contains(tr.Text, plain) || strings.Contains(string(encoded), plain) {
		t.Fatal("工具结果泄露了 Secret 明文")
	}
}

func TestCreateToolPendingSecretValuesOnlyInApplyMode(t *testing.T) {
	tool := newTestCreateTool(newFakeCreateRunner(), NewProposalStore())
	planner, ok := tool.(SecretValuePlanner)
	if !ok {
		t.Fatal("create_resource 必须实现 SecretValuePlanner")
	}
	if pending, need := planner.PendingSecretValues(createInput(t, secretAskManifest, CreateModeDryRun, nil)); need || len(pending) != 0 {
		t.Fatalf("dry_run 不该向用户索取值: need=%v pending=%v", need, pending)
	}
	if _, err := tool.Execute(context.Background(), createInput(t, secretGenerateManifest, "", nil)); err != nil {
		t.Fatal(err)
	}
	if pending, need := planner.PendingSecretValues(createInput(t, secretGenerateManifest, CreateModeApply, nil)); need || len(pending) != 0 {
		t.Fatalf("$GENERATE 无需用户取值: need=%v pending=%v", need, pending)
	}

	if _, err := tool.Execute(context.Background(), createInput(t, secretAskManifest, "", nil)); err != nil {
		t.Fatal(err)
	}

	pending, need := planner.PendingSecretValues(createInput(t, secretAskManifest, CreateModeApply, nil))
	if !need || len(pending) != 1 {
		t.Fatalf("apply 应申报 1 个待取值，实际 need=%v pending=%v", need, pending)
	}
	if got := (SecretValueKey{Kind: "Secret", Namespace: "default", Name: "db", Key: "password"}); pending[0].Key != got {
		t.Fatalf("待取值定位 = %+v, want %+v", pending[0].Key, got)
	}
	if !strings.Contains(pending[0].Prompt, "password") || strings.Contains(pending[0].Prompt, manifestx.PlaceholderAsk) {
		t.Fatalf("提问文案要能定位 key 且不泄露占位符细节: %q", pending[0].Prompt)
	}
}

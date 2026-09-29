package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// $ASK 通道（三期 R4.5）：值由用户在审批环节提供，执行器只把它写进深拷贝后的对象，
// 对外只暴露 key/长度/SHA-256/来源。
func TestApplySecretAskUsesUserValueAndOnlyReportsFingerprint(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	const userValue = "user-typed-password"
	obj := u("v1", "Secret", "web", "s1", map[string]any{
		"type":       "Opaque",
		"stringData": map[string]any{"password": SecretPlaceholderAsk, "token": SecretPlaceholder},
	})

	res, err := r.Apply(context.Background(), ApplyRequest{
		Object:       obj.Object,
		SecretValues: map[string]string{"password": userValue},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 集群里落的是用户原值（不是占位符，也不是随机值）。
	got := getUnstructured(t, fake, gvr("", "v1", "secrets"), "web", "s1")
	stringData, found, _ := unstructured.NestedStringMap(got.Object, "stringData")
	if !found || stringData["password"] != userValue {
		t.Fatalf("集群里的 password 应是用户提供的值: %+v", stringData)
	}

	// 结论按 key 名排序：password（用户提供）、token（执行器生成）。
	want := []SecretKeyStatus{
		FingerprintSecretValue("password", userValue, SecretValueSourceUser),
		FingerprintSecretValue("token", stringData["token"], SecretValueSourceGenerate),
	}
	if len(res.SecretKeyStatuses) != 2 || res.SecretKeyStatuses[0] != want[0] || res.SecretKeyStatuses[1] != want[1] {
		t.Fatalf("指纹结论不符: %+v want %+v", res.SecretKeyStatuses, want)
	}
	if strings.Join(res.SecretKeys, ",") != "password,token" {
		t.Fatalf("SecretKeys = %v", res.SecretKeys)
	}
	if strings.Join(res.GeneratedKeys, ",") != "token" {
		t.Fatalf("用户提供的值不应算作生成值: %v", res.GeneratedKeys)
	}

	// 红线：结果序列化后不得出现明文；调用方的 map 也不得被就地改写。
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), userValue) {
		t.Fatal("ApplyResult 泄露了用户提供的 Secret 值")
	}
	if passed, _, _ := unstructured.NestedStringMap(obj.Object, "stringData"); passed["password"] != SecretPlaceholderAsk {
		t.Fatal("Apply 不应改写调用方的对象")
	}
}

func TestApplySecretAskWithoutValueIsRejected(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	obj := u("v1", "Secret", "web", "s1", map[string]any{"stringData": map[string]any{"password": SecretPlaceholderAsk}})

	for name, values := range map[string]map[string]string{
		"缺键":  nil,
		"空白值": {"password": "   "},
	} {
		if _, err := r.Apply(context.Background(), ApplyRequest{Object: obj.Object, SecretValues: values}); err == nil {
			t.Fatalf("%s：真实提交缺少用户值时必须失败", name)
		} else if !strings.Contains(err.Error(), "password") {
			t.Fatalf("%s：错误应指明缺值的 key，实际: %v", name, err)
		}
	}
	// 失败必须发生在提交之前：集群里不能留下半个对象。
	if _, err := fake.Resource(gvr("", "v1", "secrets")).Namespace("web").Get(context.Background(), "s1", metav1.GetOptions{}); err == nil {
		t.Fatal("取值失败时不应把 Secret 写进集群")
	}
}

func TestApplySecretAskInDryRunNeedsNoValueAndIsNotFingerprinted(t *testing.T) {
	// dry run 只是让 apiserver 校验清单：临时值既不是最终值，就不该产出可审计指纹，
	// 否则审计会记下一个集群里并不存在的值。
	r, fake := newSSARunner(t, Options{})
	obj := u("v1", "Secret", "web", "s1", map[string]any{
		"stringData": map[string]any{"password": SecretPlaceholderAsk, "token": SecretPlaceholder},
	})

	res, err := r.Apply(context.Background(), ApplyRequest{Object: obj.Object, DryRun: true})
	if err != nil {
		t.Fatalf("dry run 缺用户值不应失败: %v", err)
	}
	if len(res.SecretKeyStatuses) != 1 || res.SecretKeyStatuses[0].Key != "token" {
		t.Fatalf("$ASK 在 dry run 下不应产出指纹: %+v", res.SecretKeyStatuses)
	}
	if strings.Join(res.SecretKeys, ",") != "token" {
		t.Fatalf("SecretKeys = %v, want 只有 token", res.SecretKeys)
	}
	if _, err := fake.Resource(gvr("", "v1", "secrets")).Namespace("web").Get(context.Background(), "s1", metav1.GetOptions{}); err == nil {
		t.Fatal("dry run 不应落盘")
	}
}

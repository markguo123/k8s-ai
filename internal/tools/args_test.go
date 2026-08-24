package tools

import (
	"encoding/json"
	"testing"
)

func TestParseArgs(t *testing.T) {
	args, err := ParseArgs(json.RawMessage(`{"namespace":"prod","tailLines":100,"flag":true}`), "namespace")
	if err != nil {
		t.Fatal(err)
	}
	if args["namespace"] != "prod" || args["tailLines"] != "100" || args["flag"] != "true" {
		t.Fatalf("args = %v", args)
	}
}

func TestParseArgsMissingRequired(t *testing.T) {
	if _, err := ParseArgs(json.RawMessage(`{"namespace":"prod"}`), "name"); err == nil {
		t.Fatal("缺少必填字段应报错")
	}
}

func TestParseArgsInvalidJSON(t *testing.T) {
	if _, err := ParseArgs(json.RawMessage(`{bad`), "name"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestParseArgsEmpty(t *testing.T) {
	args, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("空入参应为空 map: %v", args)
	}
}

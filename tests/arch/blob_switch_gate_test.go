// Package arch_test 的对象存储换后端闸门用例：deploy/install.sh 的 blobs_gate_backend_switch。
//
// 为什么要有这条真跑的用例：闸门只比 driver 名字时，「空库第一次接 S3」这种零风险改动也被拒，
// 现场唯一能往前走的路是 --allow-driver-mismatch，而那个开关的语义是「历史对象可以丢」——
// 用它绕过一个空后端，等于把真实语义用废，下次真有对象时也没人记得先迁移。
// 2026-11-03 真机接 MinIO 就撞在这条上，所以这里把「先数对象再决定」钉成可执行契约：
// 0 个放行、有对象拒绝、数不出来按最保守处理、显式绕行仍生效、同后端不去碰容器。
//
// 用例不复制脚本内容：两个函数直接从 install.sh 里抠出来 eval，改脚本就是改被测对象。
package arch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// blobGateFuncBody 取 install.sh 里一个函数的源码：函数名从行首开始，结束于下一个行首 "}"。
func blobGateFuncBody(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\n"+name+"() {")
	if start < 0 {
		t.Fatalf("deploy/install.sh 里找不到函数 %s()", name)
	}
	rest := script[start+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("函数 %s() 没有正常收尾", name)
	}
	return rest[:end+3]
}

func TestBlobBackendSwitchGateCountsObjects(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过换后端闸门可执行用例")
	}
	// 被测对象就是脚本本体：函数体从 install.sh 里现取，改脚本等于改被测。
	script := readRepoFile(t, "deploy", "install.sh")
	countFn := blobGateFuncBody(t, script, "blobs_current_object_count")
	gateFn := blobGateFuncBody(t, script, "blobs_gate_backend_switch")

	const sevenObjects = "driver=db\ntotal objects=7 bytes=4194304 (4.0 MB)\n"

	cases := []struct {
		name       string
		cur        string // 集群现生效的 driver
		want       string // 本次入参的 driver
		statOut    string // 容器里 blobs stat 的输出
		statExit   string // 容器里 blobs stat 的退出码
		allow      string
		wantOut    []string
		wantDie    bool
		wantProbed bool // 闸门是否应该去容器里数对象
	}{
		{
			name: "旧后端零对象时直接放行", cur: "db", want: "s3",
			statOut: "driver=db\ntotal objects=0 bytes=0 (0 B)\n", statExit: "0", allow: "false",
			wantOut: []string{"OK: 对象存储后端从 db 切到 s3", "RESULT=PASS"}, wantProbed: true,
		},
		{
			name: "旧后端有对象必须拒绝", cur: "db", want: "s3",
			statOut: sevenObjects, statExit: "0", allow: "false",
			wantOut: []string{"DIE: ", "7 个历史对象"}, wantDie: true, wantProbed: true,
		},
		{
			name: "数不出对象数按最保守处理", cur: "db", want: "s3",
			statOut: "", statExit: "1", allow: "false",
			wantOut: []string{"DIE: ", "数不出"}, wantDie: true, wantProbed: true,
		},
		{
			name: "显式绕行开关仍然生效", cur: "db", want: "s3",
			statOut: sevenObjects, statExit: "0", allow: "true",
			wantOut: []string{"WARN: 对象存储后端从 db 切到 s3", "RESULT=PASS"}, wantProbed: true,
		},
		{
			name: "后端没换就不去碰容器", cur: "s3", want: "s3",
			statOut: "", statExit: "1", allow: "false",
			wantOut: []string{"OK: 对象存储后端沿用 s3", "RESULT=PASS"}, wantProbed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			posixDir, hostDir := bashTempDir(t, bash)
			statFile := filepath.Join(hostDir, "stat.out")
			if err := os.WriteFile(statFile, []byte(tc.statOut), 0o600); err != nil {
				t.Fatalf("写 blobs stat 桩输出失败：%v", err)
			}
			probed := filepath.Join(hostDir, "probed")
			// 桩 kubectl 顺手留个标记文件，用来证明「同后端」那条路径真的没进容器。
			fake := "#!/usr/bin/env bash\n: \"${TOUCH:?}\" && printf x > \"$TOUCH\"\ncat \"${STAT_FILE:-/dev/null}\" 2>/dev/null\nexit \"${STAT_EXIT:-0}\"\n"
			kubectl := filepath.Join(hostDir, "kubectl-stub.sh")
			if err := os.WriteFile(kubectl, []byte(fake), 0o755); err != nil {
				t.Fatalf("写 kubectl 桩失败：%v", err)
			}
			harness := strings.Join([]string{
				"#!/usr/bin/env bash",
				"set -uo pipefail",
				"NAMESPACE=testns",
				"BLOBS_BACKEND=\"${GATE_WANT:-s3}\"",
				"ALLOW_DRIVER_MISMATCH=\"${GATE_ALLOW:-false}\"",
				"POD_PLATFORM_CONFIG=/etc/aegisops/config.yaml",
				"ok()     { printf 'OK: %s\\n' \"$*\"; }",
				"warn()   { printf 'WARN: %s\\n' \"$*\"; }",
				"detail() { printf 'DETAIL: %s\\n' \"$*\"; }",
				"die()    { printf 'DIE: %s\\n' \"$1\"; exit 42; }",
				"is_true() { case \"$1\" in true|1|yes) return 0 ;; *) return 1 ;; esac; }",
				"blobs_current_backend() { printf '%s' \"${GATE_CUR:-db}\"; }",
				countFn,
				gateFn,
				"blobs_gate_backend_switch",
				"echo RESULT=PASS",
				"",
			}, "\n")
			harnessPath := filepath.Join(hostDir, "gate-harness.sh")
			if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
				t.Fatalf("写闸门 harness 失败：%v", err)
			}

			cmd := exec.Command(bash, posixDir+"/gate-harness.sh")
			cmd.Env = append(os.Environ(),
				"TMPDIR=/tmp",
				"KUBECTL_BIN="+posixDir+"/kubectl-stub.sh",
				"STAT_FILE="+posixDir+"/stat.out",
				"STAT_EXIT="+tc.statExit,
				"TOUCH="+posixDir+"/probed",
				"GATE_CUR="+tc.cur,
				"GATE_WANT="+tc.want,
				"GATE_ALLOW="+tc.allow,
			)
			combined, err := cmd.CombinedOutput()
			out := string(combined)

			if tc.wantDie && err == nil {
				t.Errorf("该拒绝的切换却放行了\n输出：\n%s", out)
			}
			if !tc.wantDie && err != nil {
				t.Errorf("闸门不该失败（exit err）：%v\n输出：\n%s", err, out)
			}
			for _, needle := range tc.wantOut {
				if !strings.Contains(out, needle) {
					t.Errorf("输出缺少 %q\n输出：\n%s", needle, out)
				}
			}
			if _, err := os.Stat(probed); (err == nil) != tc.wantProbed {
				t.Errorf("是否进容器数对象不符：期望 probed=%v\n输出：\n%s", tc.wantProbed, out)
			}
		})
	}
}

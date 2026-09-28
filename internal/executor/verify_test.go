package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// --- R2.6 就绪判定策略表 ---------------------------------------------------

func TestObserveReadinessPolicies(t *testing.T) {
	cases := []struct {
		name       string
		obj        *unstructured.Unstructured
		ready      bool
		wantReason string
		generation int64 // 非 0 时写入 metadata.generation（模拟已发生过一次变更）
		noFields   bool  // 无策略 Kind 只确认存在，没有状态摘要
	}{
		{
			name: "Deployment 副本全部就绪",
			obj: u("apps/v1", "Deployment", "web", "api", map[string]any{
				"spec":   map[string]any{"replicas": int64(2)},
				"status": map[string]any{"readyReplicas": int64(2), "updatedReplicas": int64(2), "observedGeneration": int64(1)},
			}),
			ready:      true,
			wantReason: "2/2 副本可用",
		},
		{
			name: "Deployment 副本未就绪",
			obj: u("apps/v1", "Deployment", "web", "api", map[string]any{
				"spec":   map[string]any{"replicas": int64(2)},
				"status": map[string]any{"readyReplicas": int64(1), "updatedReplicas": int64(2)},
			}),
			ready:      false,
			wantReason: "ready=1/2",
		},
		{
			name: "Deployment 控制器还没观察到新 generation",
			obj: u("apps/v1", "Deployment", "web", "api", map[string]any{
				"spec":   map[string]any{"replicas": int64(1)},
				"status": map[string]any{"readyReplicas": int64(1), "updatedReplicas": int64(1), "observedGeneration": int64(1)},
			}),
			ready:      false,
			wantReason: "observedGeneration=1",
			generation: 2,
		},
		{
			name: "StatefulSet revision 未收敛",
			obj: u("apps/v1", "StatefulSet", "web", "db", map[string]any{
				"spec": map[string]any{"replicas": int64(1)},
				"status": map[string]any{
					"readyReplicas": int64(1), "updatedReplicas": int64(1), "observedGeneration": int64(1),
					"currentRevision": "db-1", "updateRevision": "db-2",
				},
			}),
			ready:      false,
			wantReason: "滚动更新未完成",
		},
		{
			name: "StatefulSet revision 收敛后就绪",
			obj: u("apps/v1", "StatefulSet", "web", "db", map[string]any{
				"spec": map[string]any{"replicas": int64(1)},
				"status": map[string]any{
					"readyReplicas": int64(1), "updatedReplicas": int64(1), "observedGeneration": int64(1),
					"currentRevision": "db-2", "updateRevision": "db-2",
				},
			}),
			ready:      true,
			wantReason: "就绪",
		},
		{
			name: "DaemonSet 部分节点未就绪",
			obj: u("apps/v1", "DaemonSet", "web", "agent", map[string]any{
				"status": map[string]any{"desiredNumberScheduled": int64(3), "numberReady": int64(2), "updatedNumberScheduled": int64(3)},
			}),
			ready:      false,
			wantReason: "ready=2/3",
		},
		{
			name:       "DaemonSet 刚创建时期望调度数为 0",
			obj:        u("apps/v1", "DaemonSet", "web", "agent", map[string]any{"status": map[string]any{}}),
			ready:      false,
			wantReason: "ready=0/0",
		},
		{
			name: "Job 完成条件为 True",
			obj: u("batch/v1", "Job", "web", "migrate", map[string]any{
				"status": map[string]any{"succeeded": int64(1), "conditions": []any{
					map[string]any{"type": "Complete", "status": "True", "reason": "Succeeded"},
				}},
			}),
			ready:      true,
			wantReason: "已完成",
		},
		{
			name: "Job 失败条件优先于 succeeded",
			obj: u("batch/v1", "Job", "web", "migrate", map[string]any{
				"status": map[string]any{"succeeded": int64(1), "conditions": []any{
					map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"},
				}},
			}),
			ready:      false,
			wantReason: "BackoffLimitExceeded",
		},
		{
			name: "Job 仍在运行",
			obj: u("batch/v1", "Job", "web", "migrate", map[string]any{
				"status": map[string]any{"active": int64(1)},
			}),
			ready:      false,
			wantReason: "仍在运行",
		},
		{
			name: "CronJob 只确认已创建",
			obj: u("batch/v1", "CronJob", "web", "nightly", map[string]any{
				"spec": map[string]any{"schedule": "0 3 * * *"},
			}),
			ready:      true,
			wantReason: "schedule=0 3 * * *",
		},
		{
			name: "Pod Running 且 Ready",
			obj: u("v1", "Pod", "web", "web-1", map[string]any{
				"status": map[string]any{"phase": "Running", "conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
				}},
			}),
			ready:      true,
			wantReason: "正在运行且 Ready",
		},
		{
			name: "Pod Running 但容器未 Ready",
			obj: u("v1", "Pod", "web", "web-1", map[string]any{
				"status": map[string]any{"phase": "Running"},
			}),
			ready:      false,
			wantReason: "容器未 Ready",
		},
		{
			name: "Pod Pending",
			obj: u("v1", "Pod", "web", "web-1", map[string]any{
				"status": map[string]any{"phase": "Pending", "reason": "Unschedulable"},
			}),
			ready:      false,
			wantReason: "Unschedulable",
		},
		{
			name:       "Pod 已成功结束",
			obj:        u("v1", "Pod", "web", "web-1", map[string]any{"status": map[string]any{"phase": "Succeeded"}}),
			ready:      true,
			wantReason: "已成功结束",
		},
		{
			name:       "PVC 已 Bound",
			obj:        u("v1", "PersistentVolumeClaim", "web", "data", map[string]any{"status": map[string]any{"phase": "Bound"}}),
			ready:      true,
			wantReason: "已 Bound",
		},
		{
			name:       "PVC 未绑定",
			obj:        u("v1", "PersistentVolumeClaim", "web", "data", map[string]any{"status": map[string]any{"phase": "Pending"}}),
			ready:      false,
			wantReason: "未绑定",
		},
		{
			name: "Service 已分配 ClusterIP",
			obj: u("v1", "Service", "web", "web", map[string]any{
				"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.9"},
			}),
			ready:      true,
			wantReason: "10.0.0.9",
		},
		{
			name: "LoadBalancer Service 还没拿到地址",
			obj: u("v1", "Service", "web", "web", map[string]any{
				"spec": map[string]any{"type": "LoadBalancer"},
			}),
			ready:      false,
			wantReason: "负载均衡器地址",
		},
		{
			name: "LoadBalancer Service 已拿到地址",
			obj: u("v1", "Service", "web", "web", map[string]any{
				"spec":   map[string]any{"type": "LoadBalancer"},
				"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "1.2.3.4"}}}},
			}),
			ready:      true,
			wantReason: "负载均衡器地址",
		},
		{
			name: "headless Service 直接算就绪",
			obj: u("v1", "Service", "web", "web", map[string]any{
				"spec": map[string]any{"type": "ClusterIP", "clusterIP": "None"},
			}),
			ready:      true,
			wantReason: "headless",
		},
		{
			name:       "Ingress 还没回写入口地址",
			obj:        u("networking.k8s.io/v1", "Ingress", "web", "web"),
			ready:      false,
			wantReason: "等待入口地址",
		},
		{
			name:       "Namespace Terminating 不算就绪",
			obj:        u("v1", "Namespace", "", "web", map[string]any{"status": map[string]any{"phase": "Terminating"}}),
			ready:      false,
			wantReason: "Terminating",
		},
		{
			name:       "无策略 Kind 以对象存在为就绪",
			obj:        u("v1", "ConfigMap", "web", "cm1", map[string]any{"data": map[string]any{"a": "b"}}),
			ready:      true,
			wantReason: "仅确认对象已存在",
			noFields:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.generation > 0 {
				tc.obj.SetGeneration(tc.generation)
			}
			obs := observe(tc.obj)
			if obs.ready != tc.ready {
				t.Fatalf("ready = %v，期望 %v（reason=%s）", obs.ready, tc.ready, obs.reason)
			}
			if !strings.Contains(obs.reason, tc.wantReason) {
				t.Fatalf("reason = %q，期望包含 %q", obs.reason, tc.wantReason)
			}
			if !tc.noFields && len(obs.fields) == 0 {
				t.Fatal("观测摘要不应为空")
			}
		})
	}
}

// --- R2.6/R2.7 Verify 轮询 -------------------------------------------------

func TestVerifyReadyImmediately(t *testing.T) {
	dep := u("apps/v1", "Deployment", "web", "web", map[string]any{
		"spec":   map[string]any{"replicas": int64(1)},
		"status": map[string]any{"readyReplicas": int64(1), "updatedReplicas": int64(1), "observedGeneration": int64(1)},
	})
	r, _ := newSSARunner(t, Options{}, dep)

	res, err := r.Verify(context.Background(), VerifyRequest{
		Ref:     ObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "web", Name: "web"},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Ready || res.TimedOut {
		t.Fatalf("应立即可绪: %+v", res)
	}
	if res.Reason == "" || res.Observations["readyReplicas"] == nil {
		t.Fatalf("结论缺少原因或状态摘要: %+v", res)
	}
	if len(res.Events) != 0 {
		t.Fatalf("就绪时不应附带事件: %v", res.Events)
	}
}

func TestVerifyWaitsUntilReady(t *testing.T) {
	pending := u("v1", "Pod", "web", "web-1", map[string]any{"status": map[string]any{"phase": "Pending"}})
	running := u("v1", "Pod", "web", "web-1", map[string]any{
		"status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	})
	r, fake := newSSARunner(t, Options{}, pending)
	var gets int
	fake.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 2 {
			return false, nil, nil // 走 tracker：Pending
		}
		return true, running, nil
	})

	started := time.Now()
	res, err := r.Verify(context.Background(), VerifyRequest{
		Ref:     ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: "web", Name: "web-1"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Ready {
		t.Fatalf("最终应就绪: %+v", res)
	}
	if gets < 2 {
		t.Fatalf("应至少轮询两次，实际 %d", gets)
	}
	if elapsed := time.Since(started); elapsed < DefaultVerifyInterval {
		t.Fatalf("轮询间隔未生效，耗时 %v", elapsed)
	}
}

func TestVerifyTimeoutSamplesEvents(t *testing.T) {
	pending := u("v1", "Pod", "web", "web-1", map[string]any{"status": map[string]any{"phase": "Pending"}})
	objects := []runtime.Object{pending}
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		objects = append(objects, eventObject("web", "Pod", "web-1", "FailedScheduling", "Warning",
			"0/3 nodes are available, password: hunter2", int64(i+1), base.Add(time.Duration(i)*time.Minute)))
	}
	// 同 ns 但不同对象：field selector 在 fake 里不生效，必须靠客户端二次过滤剔除。
	objects = append(objects, eventObject("web", "Pod", "other-pod", "Pulled", "Normal", "image pulled", 1, base))
	// 另一个 ns：不应出现在结果里。
	objects = append(objects, eventObject("other", "Pod", "web-1", "Other", "Warning", "别的命名空间", 1, base))
	r, _ := newSSARunner(t, Options{}, objects...)

	res, err := r.Verify(context.Background(), VerifyRequest{
		Ref:     ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: "web", Name: "web-1"},
		Timeout: DefaultVerifyInterval + 200*time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Ready || !res.TimedOut {
		t.Fatalf("超时应给出未就绪结论: %+v", res)
	}
	if len(res.Events) != maxEventSamples {
		t.Fatalf("事件应截断到 %d 条，实际 %d: %v", maxEventSamples, len(res.Events), res.Events)
	}
	if !res.Truncated {
		t.Fatal("7 条事件被截断，应标记 Truncated")
	}
	joined := strings.Join(res.Events, "\n")
	if strings.Contains(joined, "hunter2") || !strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("事件必须脱敏: %s", joined)
	}
	if strings.Contains(joined, "other-pod") || strings.Contains(joined, "别的命名空间") {
		t.Fatalf("事件必须只属于目标对象: %s", joined)
	}
	if !strings.Contains(joined, "FailedScheduling") {
		t.Fatalf("事件应保留 reason/type: %s", joined)
	}
}

func TestVerifyMissingObject(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	res, err := r.Verify(context.Background(), VerifyRequest{
		Ref:     ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web", Name: "ghost"},
		Timeout: DefaultVerifyInterval + 200*time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Ready || !res.TimedOut || !strings.Contains(res.Reason, "集群里还没有该对象") {
		t.Fatalf("缺对象应给出可读结论: %+v", res)
	}
}

func TestVerifyCanceledContextReportsCancel(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := r.Verify(ctx, VerifyRequest{
		Ref: ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web", Name: "ghost"},
	})
	if err != nil {
		t.Fatalf("取消不应返回错误: %v", err)
	}
	if res.Ready || !res.TimedOut || !strings.Contains(res.Reason, "校验被取消") {
		t.Fatalf("取消应给出可读结论: %+v", res)
	}
}

func TestVerifyRequiresKindAndName(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	if _, err := r.Verify(context.Background(), VerifyRequest{Ref: ObjectRef{Kind: "Pod", Namespace: "web"}}); err == nil {
		t.Fatal("缺少 name 应报错")
	}
}

func TestVerifyWithoutChannelGivesActionableError(t *testing.T) {
	r := NewRunner(Options{})
	_, err := r.Verify(context.Background(), VerifyRequest{
		Ref: ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web", Name: "cm1"},
	})
	if err == nil || !strings.Contains(err.Error(), "写通道未装配") {
		t.Fatalf("应给出可行动错误，实际 %v", err)
	}
}

// --- R2.8 截断 ------------------------------------------------------------

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("创建失败", 10); got != "创建失败" {
		t.Fatalf("未超限不应改动: %q", got)
	}
	got := truncateRunes("创建失败：镜像拉取失败", 4)
	if got != "创建失败…(截断)" {
		t.Fatalf("按字符截断错误: %q", got)
	}
}

// eventObject 构造一个 core/v1 Event（involvedObject 指向 ref）。
func eventObject(ns, kind, name, reason, eventType, message string, count int64, ts time.Time) *unstructured.Unstructured {
	return u("v1", "Event", ns, name+"-"+reason+strings.Repeat("x", int(count)), map[string]any{
		"involvedObject": map[string]any{"kind": kind, "name": name, "namespace": ns},
		"reason":         reason,
		"type":           eventType,
		"message":        message,
		"count":          count,
		"lastTimestamp":  ts.Format(time.RFC3339),
	})
}

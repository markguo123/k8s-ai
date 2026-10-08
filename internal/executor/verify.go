// 执行后就绪校验（R2.6/R2.7/R2.8）：按 Kind 的就绪判定策略 + 最近事件采样。
//
// 策略表刻意保守：没有策略的 Kind 只把"对象存在"当作就绪信号，
// 未就绪时附带 Events 摘要（脱敏 + 截断），让模型能继续排查而不是瞎猜。
package executor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// DefaultVerifyTimeout 是就绪等待的默认上限。
	DefaultVerifyTimeout = 30 * time.Second
	// DefaultVerifyInterval 是轮询间隔（避免高频打 apiserver）。
	DefaultVerifyInterval = 500 * time.Millisecond

	maxEventSamples = 5
	maxEventRunes   = 220

	// 就绪策略名（R5.8：post_checks 按策略名引用，Kind→策略由 readinessKinds 数据驱动）。
	StrategyObjectExists  = "object_exists"
	StrategyReplicaWait   = "replica_wait_ready"
	StrategyReplicaFollow = "replica_follow_revision"
	StrategyDaemonSet     = "daemonset_ready"
	StrategyJob           = "job_ready"
	StrategyCronJob       = "cronjob_object"
	StrategyPod           = "pod_ready"
	StrategyPVC           = "pvc_bound"
	StrategyService       = "service_ready"
	StrategyIngress       = "ingress_ready"
	StrategyNamespace     = "namespace_active"
)

var eventsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}

// observation 是一次状态读取的结论：关键字段 + 是否就绪 + 人话原因。
type observation struct {
	fields map[string]any
	ready  bool
	reason string
}

// Verify 轮询对象状态直到就绪或超时。
// 返回值的 error 只表示"没能完成校验"（对象不存在、集群错误）；
// 超时未就绪是正常结论（Ready=false, TimedOut=true）。
func (r *Runner) Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	ref := req.Ref
	if strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Kind) == "" {
		return VerifyResult{}, errors.New("就绪校验需要 kind/name")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultVerifyTimeout
	}
	deadline := time.Now().Add(timeout)

	var last observation
	for {
		obj, _, err := r.getObject(ctx, ref)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				// 真实 client 在 ctx 取消时把取消当成错误返回：这里归入"未就绪"结论，
				// 而不是让调用方拿到一个裸 error（创建事实不能因为等待超时而丢失）。
				return r.verifyResult(ref, cancelObservation(last), true), nil
			case apierrors.IsNotFound(err):
				if time.Now().After(deadline) {
					return r.verifyResult(ref, observation{ready: false, reason: "集群里还没有该对象（可能被准入控制拒绝或已被删除）"}, true), nil
				}
			default:
				return VerifyResult{}, err
			}
		} else {
			last = observe(obj)
			if last.ready {
				return r.verifyResult(ref, last, false), nil
			}
		}
		if time.Now().After(deadline) {
			if last.reason == "" {
				last = observation{ready: false, reason: "集群里还没有该对象（可能被准入控制拒绝或已被删除）"}
			}
			return r.verifyResult(ref, last, true), nil
		}
		select {
		case <-ctx.Done():
			res := r.verifyResult(ref, cancelObservation(last), true)
			return res, nil
		case <-time.After(DefaultVerifyInterval):
		}
	}
}

// cancelObservation 在没有读到任何状态时给出"被取消"的原因，否则保留最后一次观察结论。
func cancelObservation(last observation) observation {
	if last.reason == "" {
		return observation{ready: false, reason: "校验被取消"}
	}
	return last
}

// verifyResult 组装结论：未就绪时附带最近事件（脱敏 + 截断）。
func (r *Runner) verifyResult(ref ObjectRef, obs observation, timedOut bool) VerifyResult {
	out := VerifyResult{
		APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name,
		Ready: obs.ready, Reason: obs.reason, TimedOut: timedOut, Observations: obs.fields,
	}
	if obs.ready {
		return out
	}
	events, truncated := r.recentEvents(context.Background(), ref)
	out.Events = events
	out.Truncated = truncated
	return out
}

// recentEvents 取该对象最近的 Warning/Normal 事件摘要（最多 maxEventSamples 条）。
// 事件内容属于不可信数据：只做脱敏与截断，绝不当作指令执行。
func (r *Runner) recentEvents(ctx context.Context, ref ObjectRef) ([]string, bool) {
	ch, err := r.channel()
	if err != nil {
		return nil, false
	}
	ns := ref.Namespace
	if ns == "" {
		ns = metav1.NamespaceDefault
	}
	list, err := ch.dyn.Resource(eventsGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.kind=%s,involvedObject.name=%s", ref.Kind, ref.Name),
	})
	if err != nil {
		return nil, false
	}
	type event struct {
		ts   time.Time
		text string
	}
	var found []event
	for i := range list.Items {
		item := &list.Items[i]
		if !sameInvolvedObject(item, ref) {
			continue
		}
		reason, _, _ := unstructured.NestedString(item.Object, "reason")
		eventType, _, _ := unstructured.NestedString(item.Object, "type")
		message, _, _ := unstructured.NestedString(item.Object, "message")
		count, _, _ := unstructured.NestedInt64(item.Object, "count")
		suffix := ""
		if count > 1 {
			suffix = fmt.Sprintf(" (x%d)", count)
		}
		ts := item.GetCreationTimestamp().Time
		if last, ok, _ := unstructured.NestedString(item.Object, "lastTimestamp"); ok {
			if parsed, perr := time.Parse(time.RFC3339, last); perr == nil {
				ts = parsed
			}
		}
		found = append(found, event{ts: ts, text: fmt.Sprintf("%s %s%s: %s", eventType, reason, suffix, truncateRunes(redactor.Redact(message), maxEventRunes))})
	}
	if len(found) == 0 {
		return nil, false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ts.After(found[j].ts) })
	truncated := len(found) > maxEventSamples
	if truncated {
		found = found[:maxEventSamples]
	}
	// 先按时间倒序取最近若干条，再转回时间正序输出，便于阅读。
	sort.Slice(found, func(i, j int) bool { return found[i].ts.Before(found[j].ts) })
	out := make([]string, 0, len(found))
	for _, e := range found {
		out = append(out, e.text)
	}
	return out, truncated
}

func sameInvolvedObject(item *unstructured.Unstructured, ref ObjectRef) bool {
	kind, _, _ := unstructured.NestedString(item.Object, "involvedObject", "kind")
	name, _, _ := unstructured.NestedString(item.Object, "involvedObject", "name")
	return strings.EqualFold(kind, ref.Kind) && name == ref.Name
}

// observe 按 Kind 读取关键状态并判定是否就绪。
func observe(obj *unstructured.Unstructured) observation {
	policy, ok := readinessStrategies[ReadinessStrategyFor(obj.GetKind())]
	if ok {
		return policy(obj)
	}
	return observeObjectExists(obj)
}

// ReadinessStrategyFor 返回 Kind 对应的就绪策略名（R5.8 数据驱动；未映射 Kind
// 回退 object_exists）。skills post_checks 通过这个命名引用策略表。
func ReadinessStrategyFor(kind string) string {
	if s, ok := readinessKinds[kind]; ok {
		return s
	}
	return StrategyObjectExists
}

func observeObjectExists(obj *unstructured.Unstructured) observation {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	fields := map[string]any{}
	if phase != "" {
		fields["phase"] = phase
	}
	return observation{fields: fields, ready: true,
		reason: fmt.Sprintf("%s %s 已创建（无专门就绪判定策略，仅确认对象已存在）", obj.GetKind(), obj.GetName())}
}

// readinessStrategies 是策略名 → 就绪判定函数集合（R5.8：verify 的 post_checks
// 引用策略名，skills/执行器共用同一张表，不再按 Kind 硬编码扩散到业务包）。
var readinessStrategies = map[string]func(*unstructured.Unstructured) observation{
	StrategyObjectExists:  observeObjectExists,
	StrategyReplicaWait:   observeReplicaWorkload("Deployment", false),
	StrategyReplicaFollow: observeReplicaWorkload("StatefulSet", true),
	StrategyDaemonSet:     observeDaemonSet,
	StrategyJob:           observeJob,
	StrategyCronJob:       observeCronJob,
	StrategyPod:           observePod,
	StrategyPVC:           observePVC,
	StrategyService:       observeService,
	StrategyIngress:       observeIngress,
	StrategyNamespace:     observeNamespace,
}

// readinessKinds 是 Kind → 策略名的映射（数据驱动主表）。
var readinessKinds = map[string]string{
	"Deployment":            StrategyReplicaWait,
	"StatefulSet":           StrategyReplicaFollow,
	"ReplicaSet":            StrategyReplicaWait,
	"DaemonSet":             StrategyDaemonSet,
	"Job":                   StrategyJob,
	"CronJob":               StrategyCronJob,
	"Pod":                   StrategyPod,
	"PersistentVolumeClaim": StrategyPVC,
	"Service":               StrategyService,
	"Ingress":               StrategyIngress,
	"Namespace":             StrategyNamespace,
}

// observeReplicaWorkload 生成 Deployment/ReplicaSet/StatefulSet 的就绪判定。
// followRevision 为 true 时额外要求 currentRevision == updateRevision（StatefulSet：
// revision 不一致说明滚动更新还没完成，旧版本 Pod 仍在服务）。
func observeReplicaWorkload(kind string, followRevision bool) func(*unstructured.Unstructured) observation {
	return func(obj *unstructured.Unstructured) observation {
		desired, found, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
		if !found {
			desired = 1
		}
		ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
		updated, _, _ := unstructured.NestedInt64(obj.Object, "status", "updatedReplicas")
		unavailable, _, _ := unstructured.NestedInt64(obj.Object, "status", "unavailableReplicas")
		observed, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
		fields := map[string]any{"replicas": desired, "readyReplicas": ready, "updatedReplicas": updated, "unavailableReplicas": unavailable}
		converged := ready == desired && updated == desired && unavailable == 0 && observed >= obj.GetGeneration()
		detail := ""
		if followRevision {
			current, _, _ := unstructured.NestedString(obj.Object, "status", "currentRevision")
			update, _, _ := unstructured.NestedString(obj.Object, "status", "updateRevision")
			fields["currentRevision"], fields["updateRevision"] = current, update
			if current != update {
				converged = false
				detail = fmt.Sprintf("，currentRevision=%s 与 updateRevision=%s 不一致（滚动更新未完成）", current, update)
			}
		}
		if converged {
			return observation{fields: fields, ready: true, reason: fmt.Sprintf("%s %s 就绪：%d/%d 副本可用", kind, obj.GetName(), ready, desired)}
		}
		return observation{fields: fields, ready: false,
			reason: fmt.Sprintf("%s %s 尚未就绪：ready=%d/%d，updated=%d，unavailable=%d（observedGeneration=%d，generation=%d）%s",
				kind, obj.GetName(), ready, desired, updated, unavailable, observed, obj.GetGeneration(), detail)}
	}
}

func observeDaemonSet(obj *unstructured.Unstructured) observation {
	desired, _, _ := unstructured.NestedInt64(obj.Object, "status", "desiredNumberScheduled")
	ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "numberReady")
	updated, _, _ := unstructured.NestedInt64(obj.Object, "status", "updatedNumberScheduled")
	fields := map[string]any{"desiredNumberScheduled": desired, "numberReady": ready, "updatedNumberScheduled": updated}
	if desired > 0 && ready == desired && updated == desired {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("DaemonSet %s 就绪：%d/%d 节点可用", obj.GetName(), ready, desired)}
	}
	return observation{fields: fields, ready: false,
		reason: fmt.Sprintf("DaemonSet %s 尚未就绪：ready=%d/%d，updated=%d", obj.GetName(), ready, desired, updated)}
}

func observeJob(obj *unstructured.Unstructured) observation {
	succeeded, _, _ := unstructured.NestedInt64(obj.Object, "status", "succeeded")
	failed, _, _ := unstructured.NestedInt64(obj.Object, "status", "failed")
	active, _, _ := unstructured.NestedInt64(obj.Object, "status", "active")
	fields := map[string]any{"succeeded": succeeded, "failed": failed, "active": active}
	if conditionStatus(obj, "Failed") == "True" {
		reason := conditionReason(obj, "Failed")
		if reason == "" {
			reason = "Failed 条件为 True"
		}
		fields["failedCondition"] = reason
		return observation{fields: fields, ready: false, reason: fmt.Sprintf("Job %s 失败：%s", obj.GetName(), reason)}
	}
	if conditionStatus(obj, "Complete") == "True" || succeeded > 0 {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("Job %s 已完成（succeeded=%d）", obj.GetName(), succeeded)}
	}
	return observation{fields: fields, ready: false, reason: fmt.Sprintf("Job %s 仍在运行（active=%d）", obj.GetName(), active)}
}

func observeCronJob(obj *unstructured.Unstructured) observation {
	schedule, _, _ := unstructured.NestedString(obj.Object, "spec", "schedule")
	fields := map[string]any{"schedule": schedule}
	if last, ok, _ := unstructured.NestedString(obj.Object, "status", "lastScheduleTime"); ok {
		fields["lastScheduleTime"] = last
	}
	return observation{fields: fields, ready: true,
		reason: fmt.Sprintf("CronJob %s 已创建（schedule=%s，首次执行由控制器按调度触发）", obj.GetName(), schedule)}
}

func observePod(obj *unstructured.Unstructured) observation {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	reason, _, _ := unstructured.NestedString(obj.Object, "status", "reason")
	fields := map[string]any{"phase": phase}
	if reason != "" {
		fields["reason"] = reason
	}
	switch phase {
	case "Succeeded":
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("Pod %s 已成功结束", obj.GetName())}
	case "Running":
		if conditionStatus(obj, "Ready") == "True" {
			return observation{fields: fields, ready: true, reason: fmt.Sprintf("Pod %s 正在运行且 Ready", obj.GetName())}
		}
		return observation{fields: fields, ready: false, reason: fmt.Sprintf("Pod %s 运行中但容器未 Ready", obj.GetName())}
	case "Failed", "Unknown":
		return observation{fields: fields, ready: false, reason: fmt.Sprintf("Pod %s 状态异常：%s %s", obj.GetName(), phase, reason)}
	default:
		return observation{fields: fields, ready: false, reason: fmt.Sprintf("Pod %s 尚未运行（phase=%s %s）", obj.GetName(), phase, reason)}
	}
}

func observePVC(obj *unstructured.Unstructured) observation {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	fields := map[string]any{"phase": phase}
	if phase == "Bound" {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("PersistentVolumeClaim %s 已 Bound", obj.GetName())}
	}
	return observation{fields: fields, ready: false, reason: fmt.Sprintf("PersistentVolumeClaim %s 未绑定（phase=%s）", obj.GetName(), phase)}
}

func observeService(obj *unstructured.Unstructured) observation {
	clusterIP, _, _ := unstructured.NestedString(obj.Object, "spec", "clusterIP")
	svcType, _, _ := unstructured.NestedString(obj.Object, "spec", "type")
	fields := map[string]any{"type": svcType, "clusterIP": clusterIP}
	if strings.EqualFold(svcType, "LoadBalancer") {
		ingress, ok, _ := unstructured.NestedSlice(obj.Object, "status", "loadBalancer", "ingress")
		fields["loadBalancerIngress"] = len(ingress)
		if ok && len(ingress) > 0 {
			return observation{fields: fields, ready: true, reason: fmt.Sprintf("Service %s 已获得负载均衡器地址", obj.GetName())}
		}
		return observation{fields: fields, ready: false, reason: fmt.Sprintf("Service %s 仍在等待负载均衡器地址", obj.GetName())}
	}
	if clusterIP == "" || clusterIP == "None" {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("Service %s 已创建（headless/无 ClusterIP）", obj.GetName())}
	}
	return observation{fields: fields, ready: true, reason: fmt.Sprintf("Service %s 已分配 ClusterIP %s", obj.GetName(), clusterIP)}
}

func observeIngress(obj *unstructured.Unstructured) observation {
	ingress, _, _ := unstructured.NestedSlice(obj.Object, "status", "loadBalancer", "ingress")
	fields := map[string]any{"loadBalancerIngress": len(ingress)}
	if len(ingress) > 0 {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("Ingress %s 已获得入口地址", obj.GetName())}
	}
	return observation{fields: fields, ready: false, reason: fmt.Sprintf("Ingress %s 仍在等待入口地址（controller 未回写 status）", obj.GetName())}
}

func observeNamespace(obj *unstructured.Unstructured) observation {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	fields := map[string]any{"phase": phase}
	if phase == "Active" || phase == "" {
		return observation{fields: fields, ready: true, reason: fmt.Sprintf("Namespace %s 已就绪", obj.GetName())}
	}
	return observation{fields: fields, ready: false, reason: fmt.Sprintf("Namespace %s 状态异常：%s", obj.GetName(), phase)}
}

// conditionStatus 返回 status.conditions 中指定类型的 status（True/False/Unknown；条件不存在为空串）。
func conditionStatus(obj *unstructured.Unstructured, want string) string {
	cond, ok := findCondition(obj, want)
	if !ok {
		return ""
	}
	status, _ := cond["status"].(string)
	return status
}

// conditionReason 返回 status.conditions 中指定类型的 reason + message（用于回报失败原因）。
func conditionReason(obj *unstructured.Unstructured, want string) string {
	cond, ok := findCondition(obj, want)
	if !ok {
		return ""
	}
	reason, _ := cond["reason"].(string)
	message, _ := cond["message"].(string)
	return strings.TrimSpace(reason + " " + message)
}

func findCondition(obj *unstructured.Unstructured, want string) (map[string]any, bool) {
	conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !ok {
		return nil, false
	}
	for _, raw := range conds {
		cond, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		if condType, _ := cond["type"].(string); condType == want {
			return cond, true
		}
	}
	return nil, false
}

// truncateRunes 按字符数截断（避免多字节字符被切坏）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(截断)"
}

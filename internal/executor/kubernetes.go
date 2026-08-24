package executor

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ScaleDeployment 通过 client-go 更新副本数（结构化写路径）。
func (r *Runner) ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) (Result, error) {
	ns := r.nsOr(namespace)
	dep, err := r.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}
	// 直接更新 spec.replicas 与 /scale 语义等价（fake 客户端对 /scale 子资源支持有限）。
	dep.Spec.Replicas = &replicas
	if _, err := r.cs.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("update deployment %s/%s replicas: %w", ns, name, err)
	}
	return r.verifyDeployment(ctx, ns, name, fmt.Sprintf("scaled deployment %s/%s to %d replicas", ns, name, replicas))
}

// PatchDeployment 通过 client-go strategic merge patch 修改 Deployment。
func (r *Runner) PatchDeployment(ctx context.Context, namespace, name, patch string) (Result, error) {
	ns := r.nsOr(namespace)
	if _, err := r.cs.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patch deployment %s/%s: %w", ns, name, err)
	}
	return r.verifyDeployment(ctx, ns, name, fmt.Sprintf("patched deployment %s/%s", ns, name))
}

// RestartRollout 通过注入 restartedAt 注解触发滚动重启。
func (r *Runner) RestartRollout(ctx context.Context, namespace, name string) (Result, error) {
	ns := r.nsOr(namespace)
	dep, err := r.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
	if _, err := r.cs.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("restart rollout %s/%s: %w", ns, name, err)
	}
	return r.verifyDeployment(ctx, ns, name, fmt.Sprintf("restarted rollout deployment %s/%s", ns, name))
}

// LabelResource 给资源打标签（deployment/statefulset/daemonset/pod/service）。
func (r *Runner) LabelResource(ctx context.Context, kind, namespace, name, key, value string) (Result, error) {
	ns := r.nsOr(namespace)
	switch kind {
	case "deployment", "deployments":
		obj, err := r.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		setLabel(&obj.Labels, key, value)
		if _, err := r.cs.AppsV1().Deployments(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return Result{}, err
		}
	case "statefulset", "statefulsets":
		obj, err := r.cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		setLabel(&obj.Labels, key, value)
		if _, err := r.cs.AppsV1().StatefulSets(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return Result{}, err
		}
	case "daemonset", "daemonsets":
		obj, err := r.cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		setLabel(&obj.Labels, key, value)
		if _, err := r.cs.AppsV1().DaemonSets(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return Result{}, err
		}
	case "pod", "pods":
		obj, err := r.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		setLabel(&obj.Labels, key, value)
		if _, err := r.cs.CoreV1().Pods(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return Result{}, err
		}
	case "service", "services":
		obj, err := r.cs.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		setLabel(&obj.Labels, key, value)
		if _, err := r.cs.CoreV1().Services(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, fmt.Errorf("unsupported kind for label: %s", kind)
	}
	return Result{
		Command: fmt.Sprintf("kubectl -n %s label %s %s %s=%s", ns, kind, name, key, value),
		Stdout:  fmt.Sprintf("labeled %s/%s %s=%s", kind, name, key, value),
	}, nil
}

// PatchConfigMap 通过 client-go strategic merge patch 修改 ConfigMap（配置修复）。
func (r *Runner) PatchConfigMap(ctx context.Context, namespace, name, patch string) (Result, error) {
	ns := r.nsOr(namespace)
	if _, err := r.cs.CoreV1().ConfigMaps(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return Result{}, fmt.Errorf("patch configmap %s/%s: %w", ns, name, err)
	}
	return Result{
		Command: fmt.Sprintf("kubectl -n %s patch configmap %s --patch '%s'", ns, name, patch),
		Stdout:  fmt.Sprintf("patched configmap %s/%s", ns, name),
	}, nil
}

// DeletePod 删除 Pod（Deployment 会自动重建，用于应用新配置）。
func (r *Runner) DeletePod(ctx context.Context, namespace, name string, gracePeriodSeconds *int64) (Result, error) {
	ns := r.nsOr(namespace)
	opts := metav1.DeleteOptions{}
	if gracePeriodSeconds != nil {
		opts.GracePeriodSeconds = gracePeriodSeconds
	}
	if err := r.cs.CoreV1().Pods(ns).Delete(ctx, name, opts); err != nil {
		return Result{}, fmt.Errorf("delete pod %s/%s: %w", ns, name, err)
	}
	return Result{
		Command: fmt.Sprintf("kubectl -n %s delete pod %s", ns, name),
		Stdout:  fmt.Sprintf("deleted pod %s/%s", ns, name),
	}, nil
}

func (r *Runner) nsOr(ns string) string {
	if ns != "" {
		return ns
	}
	return r.ns
}

func setLabel(labels *map[string]string, key, value string) {
	if *labels == nil {
		*labels = map[string]string{}
	}
	(*labels)[key] = value
}

// verifyDeployment 执行写操作后核对 Deployment 状态（需求规格 §14.1）。
func (r *Runner) verifyDeployment(ctx context.Context, ns, name, summary string) (Result, error) {
	dep, err := r.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{Command: summary, Stdout: summary + "（验证失败：无法读取 Deployment）"}, nil
	}
	conditions := make([]string, 0)
	for _, c := range dep.Status.Conditions {
		conditions = append(conditions, string(c.Type)+"="+string(c.Status))
	}
	return Result{
		Command: summary,
		Stdout:  summary,
		Verification: map[string]any{
			"replicas":          derefI32p(dep.Spec.Replicas),
			"readyReplicas":     dep.Status.ReadyReplicas,
			"updatedReplicas":   dep.Status.UpdatedReplicas,
			"availableReplicas": dep.Status.AvailableReplicas,
			"conditions":        conditions,
		},
	}, nil
}

func derefI32p(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

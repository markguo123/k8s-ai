// Package scanner will host the Phase 1 / Phase 2 collector. It defines the
// read-only Kubernetes port consumed by the collector (API.md §2).
package scanner

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Reader is the read-only Kubernetes facade implemented by
// internal/kubernetes.Client. It is the only Kubernetes access path in the
// application (SECURITY.md §4).
type Reader interface {
	ServerVersion(ctx context.Context) (string, error)

	ListNamespaces(ctx context.Context) ([]corev1.Namespace, error)
	ListPods(ctx context.Context, namespace string) ([]corev1.Pod, error)
	ListNodes(ctx context.Context) ([]corev1.Node, error)
	ListServices(ctx context.Context, namespace string) ([]corev1.Service, error)
	ListEndpointSlices(ctx context.Context, namespace string) ([]discoveryv1.EndpointSlice, error)
	ListDeployments(ctx context.Context, namespace string) ([]appsv1.Deployment, error)
	ListReplicaSets(ctx context.Context, namespace string) ([]appsv1.ReplicaSet, error)
	ListStatefulSets(ctx context.Context, namespace string) ([]appsv1.StatefulSet, error)
	ListDaemonSets(ctx context.Context, namespace string) ([]appsv1.DaemonSet, error)
	ListJobs(ctx context.Context, namespace string) ([]batchv1.Job, error)
	ListCronJobs(ctx context.Context, namespace string) ([]batchv1.CronJob, error)
	ListPersistentVolumeClaims(ctx context.Context, namespace string) ([]corev1.PersistentVolumeClaim, error)
	ListPersistentVolumes(ctx context.Context) ([]corev1.PersistentVolume, error)
	ListStorageClasses(ctx context.Context) ([]storagev1.StorageClass, error)
	ListVolumeAttachments(ctx context.Context) ([]storagev1.VolumeAttachment, error)
	ListIngresses(ctx context.Context, namespace string) ([]networkingv1.Ingress, error)
	ListNetworkPolicies(ctx context.Context, namespace string) ([]networkingv1.NetworkPolicy, error)
	ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error)

	// Targeted reads are reserved for Phase 2 tools; Phase 1 does not use them.
	GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	GetNode(ctx context.Context, name string) (*corev1.Node, error)
	GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error)
	GetService(ctx context.Context, namespace, name string) (*corev1.Service, error)

	// GetPodLogs returns capped log bytes for a container (FR-022).
	GetPodLogs(ctx context.Context, namespace, pod, container string, opts model.LogOptions) ([]byte, error)
}

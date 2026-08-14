// Package kubernetes implements the read-only Kubernetes facade
// (scanner.Reader). It must never expose mutating operations (SECURITY.md).
package kubernetes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Options configure cluster access.
type Options struct {
	Kubeconfig string
	Context    string
	InCluster  bool
	Timeout    time.Duration
	QPS        float32
	Burst      int
}

// Client implements scanner.Reader with a typed read-only clientset.
type Client struct {
	clientset kubernetes.Interface
}

// NewClient builds a read-only client from kubeconfig or in-cluster config
// (FR-003). InCluster is auto-detected when running inside a cluster.
func NewClient(opts Options) (*Client, error) {
	rc, err := buildRESTConfig(opts)
	if err != nil {
		return nil, err
	}
	applyLimits(rc, opts)
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	return &Client{clientset: cs}, nil
}

// NewClientWithClientset 用已有 clientset 构造只读客户端（测试注入用）。
func NewClientWithClientset(cs kubernetes.Interface) *Client {
	return &Client{clientset: cs}
}

func buildRESTConfig(opts Options) (*rest.Config, error) {
	if opts.InCluster || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		return rc, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		p, err := expandHome(opts.Kubeconfig)
		if err != nil {
			return nil, err
		}
		loadingRules.ExplicitPath = p
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return rc, nil
}

func applyLimits(rc *rest.Config, opts Options) {
	rc.QPS = 20
	rc.Burst = 40
	rc.Timeout = 30 * time.Second
	if opts.QPS > 0 {
		rc.QPS = opts.QPS
	}
	if opts.Burst > 0 {
		rc.Burst = opts.Burst
	}
	if opts.Timeout > 0 {
		rc.Timeout = opts.Timeout
	}
}

func expandHome(path string) (string, error) {
	if path == "" || path == "~" {
		return path, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func listOpts() metav1.ListOptions {
	// ResourceVersion "0" lets the API server serve from its cache (ADR-009).
	return metav1.ListOptions{ResourceVersion: "0"}
}

func allOrNs(namespace string) string {
	if namespace == "" {
		return metav1.NamespaceAll
	}
	return namespace
}

// ServerVersion returns the cluster server version.
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	info, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("server version: %w", err)
	}
	return info.GitVersion, nil
}

func (c *Client) ListNamespaces(ctx context.Context) ([]corev1.Namespace, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	list, err := c.clientset.CoreV1().Pods(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListNodes(ctx context.Context) ([]corev1.Node, error) {
	list, err := c.clientset.CoreV1().Nodes().List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListServices(ctx context.Context, namespace string) ([]corev1.Service, error) {
	list, err := c.clientset.CoreV1().Services(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListEndpointSlices(ctx context.Context, namespace string) ([]discoveryv1.EndpointSlice, error) {
	list, err := c.clientset.DiscoveryV1().EndpointSlices(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list endpoint slices: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListDeployments(ctx context.Context, namespace string) ([]appsv1.Deployment, error) {
	list, err := c.clientset.AppsV1().Deployments(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListReplicaSets(ctx context.Context, namespace string) ([]appsv1.ReplicaSet, error) {
	list, err := c.clientset.AppsV1().ReplicaSets(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list replica sets: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListStatefulSets(ctx context.Context, namespace string) ([]appsv1.StatefulSet, error) {
	list, err := c.clientset.AppsV1().StatefulSets(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list stateful sets: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListDaemonSets(ctx context.Context, namespace string) ([]appsv1.DaemonSet, error) {
	list, err := c.clientset.AppsV1().DaemonSets(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list daemon sets: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListJobs(ctx context.Context, namespace string) ([]batchv1.Job, error) {
	list, err := c.clientset.BatchV1().Jobs(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListCronJobs(ctx context.Context, namespace string) ([]batchv1.CronJob, error) {
	list, err := c.clientset.BatchV1().CronJobs(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListPersistentVolumeClaims(ctx context.Context, namespace string) ([]corev1.PersistentVolumeClaim, error) {
	list, err := c.clientset.CoreV1().PersistentVolumeClaims(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list pvcs: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListPersistentVolumes(ctx context.Context) ([]corev1.PersistentVolume, error) {
	list, err := c.clientset.CoreV1().PersistentVolumes().List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list pvs: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListStorageClasses(ctx context.Context) ([]storagev1.StorageClass, error) {
	list, err := c.clientset.StorageV1().StorageClasses().List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list storage classes: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListVolumeAttachments(ctx context.Context) ([]storagev1.VolumeAttachment, error) {
	list, err := c.clientset.StorageV1().VolumeAttachments().List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list volume attachments: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListIngresses(ctx context.Context, namespace string) ([]networkingv1.Ingress, error) {
	list, err := c.clientset.NetworkingV1().Ingresses(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list ingresses: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListNetworkPolicies(ctx context.Context, namespace string) ([]networkingv1.NetworkPolicy, error) {
	list, err := c.clientset.NetworkingV1().NetworkPolicies(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list network policies: %w", err)
	}
	return list.Items, nil
}

func (c *Client) ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	list, err := c.clientset.CoreV1().Events(allOrNs(namespace)).List(ctx, listOpts())
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", namespace, name, err)
	}
	return pod, nil
}

func (c *Client) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", name, err)
	}
	return node, nil
}

func (c *Client) GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error) {
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	return dep, nil
}

func (c *Client) GetService(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get service %s/%s: %w", namespace, name, err)
	}
	return svc, nil
}

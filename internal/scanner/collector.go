// Package scanner 负责集群采集（两阶段扫描的 Phase1/Phase2）。
// Phase1 只做全量轻量 list，不取日志；异常判定由 Rule Engine 完成（ADR-002）。
package scanner

import (
	"context"
	"errors"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Collector 定义两阶段采集器（API.md §3）。
type Collector interface {
	// Phase1 全量轻量采集：只 list，不取日志（FR-004）。
	Phase1(ctx context.Context, opts model.ScanOptions) (*model.ClusterSnapshot, error)
	// Phase2 只对异常 Pod 深度采集日志（FR-004/FR-022）。
	Phase2(ctx context.Context, snapshot *model.ClusterSnapshot, targets []model.ResourceRef, opts model.ScanOptions) error
}

type collector struct {
	reader Reader
	logs   logFetcher // Phase2 只依赖日志读取能力（便于测试注入）
}

// New 创建采集器，reader 为只读 Kubernetes 门面（scanner.Reader）。
func New(r Reader) Collector {
	return &collector{reader: r, logs: r}
}

// phase1Task 描述一个可并行的 Phase1 采集任务。
type phase1Task struct {
	operation string // 用于 collection_errors 的 Operation 字段
	kind      string // 资源类型
	namespace string // 资源所在 namespace（集群级资源为空）
	fn        func(ctx context.Context) error
}

// snapCollector 用互斥锁保护快照的并发写入。
type snapCollector struct {
	mu   sync.Mutex
	snap *model.ClusterSnapshot
}

func (s *snapCollector) addError(e model.CollectionError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.CollectionErrors = append(s.snap.CollectionErrors, e)
}

func (s *snapCollector) addNamespaces(items []corev1.Namespace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Namespaces = append(s.snap.Namespaces, normalizeNamespace(items[i]))
	}
}

func (s *snapCollector) addPods(items []corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Pods = append(s.snap.Pods, normalizePod(items[i]))
	}
}

func (s *snapCollector) addNodes(items []corev1.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Nodes = append(s.snap.Nodes, normalizeNode(items[i]))
	}
}

func (s *snapCollector) addServices(items []corev1.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Services = append(s.snap.Services, normalizeService(items[i]))
	}
}

func (s *snapCollector) addEndpointSlices(items []discoveryv1.EndpointSlice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.EndpointSlices = append(s.snap.EndpointSlices, normalizeEndpointSlice(items[i]))
	}
}

func (s *snapCollector) addDeployments(items []appsv1.Deployment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeDeployment(items[i]))
	}
}

func (s *snapCollector) addReplicaSets(items []appsv1.ReplicaSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeReplicaSet(items[i]))
	}
}

func (s *snapCollector) addStatefulSets(items []appsv1.StatefulSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeStatefulSet(items[i]))
	}
}

func (s *snapCollector) addDaemonSets(items []appsv1.DaemonSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeDaemonSet(items[i]))
	}
}

func (s *snapCollector) addJobs(items []batchv1.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeJob(items[i]))
	}
}

func (s *snapCollector) addCronJobs(items []batchv1.CronJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Workloads = append(s.snap.Workloads, normalizeCronJob(items[i]))
	}
}

func (s *snapCollector) addPVCs(items []corev1.PersistentVolumeClaim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Storage = append(s.snap.Storage, normalizePVC(items[i]))
	}
}

func (s *snapCollector) addPVs(items []corev1.PersistentVolume) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Storage = append(s.snap.Storage, normalizePV(items[i]))
	}
}

func (s *snapCollector) addStorageClasses(items []storagev1.StorageClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Storage = append(s.snap.Storage, normalizeStorageClass(items[i]))
	}
}

func (s *snapCollector) addVolumeAttachments(items []storagev1.VolumeAttachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Storage = append(s.snap.Storage, normalizeVolumeAttachment(items[i]))
	}
}

func (s *snapCollector) addIngresses(items []networkingv1.Ingress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.Ingresses = append(s.snap.Ingresses, normalizeIngress(items[i]))
	}
}

func (s *snapCollector) addNetworkPolicies(items []networkingv1.NetworkPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		s.snap.NetworkPolicies = append(s.snap.NetworkPolicies, normalizeNetworkPolicy(items[i]))
	}
}

// addEvents 将事件按 involvedObject.UID 写入本地索引（ADR-010），
// 后续规则与证据按 UID O(1) 查询，避免按 Pod 逐条请求 events。
func (s *snapCollector) addEvents(items []corev1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range items {
		ei := normalizeEvent(items[i])
		if ei.InvolvedObject.UID == "" {
			continue // 无 UID 的事件无法关联，跳过
		}
		s.snap.EventsIndex[ei.InvolvedObject.UID] = append(s.snap.EventsIndex[ei.InvolvedObject.UID], ei)
	}
}

// Phase1 执行全量轻量采集，返回 ClusterSnapshot。
func (c *collector) Phase1(ctx context.Context, opts model.ScanOptions) (*model.ClusterSnapshot, error) {
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 8 // 默认 Phase1 并发（FR-024）
	}
	snap := &model.ClusterSnapshot{EventsIndex: map[string][]model.EventInfo{}}
	collect := &snapCollector{snap: snap}

	// 1. 命名空间必须先拉取：它决定后续 events 的采集范围。
	namespaces, err := c.reader.ListNamespaces(ctx)
	if err != nil {
		collect.addError(model.CollectionError{
			Resource:  model.ResourceRef{Kind: "Namespace"},
			Operation: "list_namespaces",
			Message:   err.Error(),
			Time:      time.Now().UTC().Format(time.RFC3339),
		})
	} else {
		collect.addNamespaces(namespaces)
	}

	// 2. 组装 16 类资源 list 任务 + 每 namespace 一次 events 任务。
	tasks := c.resourceTasks(collect, opts)
	if opts.CollectEvents {
		for _, ns := range eventNamespaces(snap.Namespaces, opts.Namespace) {
			ns := ns
			tasks = append(tasks, phase1Task{
				operation: "list_events",
				kind:      "Event",
				namespace: ns,
				fn: func(ctx context.Context) error {
					events, err := c.reader.ListEvents(ctx, ns)
					if err != nil {
						return err
					}
					collect.addEvents(events)
					return nil
				},
			})
		}
	}

	// 3. 信号量限流的 worker pool：单任务失败只记 collection_errors，不中断整体扫描（FR-004/FR-034）。
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				if err := task.fn(ctx); err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					collect.addError(model.CollectionError{
						Resource:  model.ResourceRef{Kind: task.kind, Namespace: task.namespace},
						Operation: task.operation,
						Message:   err.Error(),
						Time:      time.Now().UTC().Format(time.RFC3339),
					})
				}
			case <-ctx.Done():
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return snap, err
	}

	// 4. 系统组件动态发现：仅全集群扫描时执行（FR-010）。
	if opts.Namespace == "" {
		snap.Components = detectComponents(snap.Pods)
	}
	return snap, nil
}

// resourceTasks 返回 16 类资源的 list 任务（不含 events）。
func (c *collector) resourceTasks(collect *snapCollector, opts model.ScanOptions) []phase1Task {
	ns := opts.Namespace
	return []phase1Task{
		{operation: "list_pods", kind: "Pod", fn: func(ctx context.Context) error {
			pods, err := c.reader.ListPods(ctx, ns)
			if err != nil {
				return err
			}
			collect.addPods(pods)
			return nil
		}},
		{operation: "list_nodes", kind: "Node", fn: func(ctx context.Context) error {
			nodes, err := c.reader.ListNodes(ctx)
			if err != nil {
				return err
			}
			collect.addNodes(nodes)
			return nil
		}},
		{operation: "list_services", kind: "Service", fn: func(ctx context.Context) error {
			items, err := c.reader.ListServices(ctx, ns)
			if err != nil {
				return err
			}
			collect.addServices(items)
			return nil
		}},
		{operation: "list_endpoint_slices", kind: "EndpointSlice", fn: func(ctx context.Context) error {
			items, err := c.reader.ListEndpointSlices(ctx, ns)
			if err != nil {
				return err
			}
			collect.addEndpointSlices(items)
			return nil
		}},
		{operation: "list_deployments", kind: "Deployment", fn: func(ctx context.Context) error {
			items, err := c.reader.ListDeployments(ctx, ns)
			if err != nil {
				return err
			}
			collect.addDeployments(items)
			return nil
		}},
		{operation: "list_replica_sets", kind: "ReplicaSet", fn: func(ctx context.Context) error {
			items, err := c.reader.ListReplicaSets(ctx, ns)
			if err != nil {
				return err
			}
			collect.addReplicaSets(items)
			return nil
		}},
		{operation: "list_stateful_sets", kind: "StatefulSet", fn: func(ctx context.Context) error {
			items, err := c.reader.ListStatefulSets(ctx, ns)
			if err != nil {
				return err
			}
			collect.addStatefulSets(items)
			return nil
		}},
		{operation: "list_daemon_sets", kind: "DaemonSet", fn: func(ctx context.Context) error {
			items, err := c.reader.ListDaemonSets(ctx, ns)
			if err != nil {
				return err
			}
			collect.addDaemonSets(items)
			return nil
		}},
		{operation: "list_jobs", kind: "Job", fn: func(ctx context.Context) error {
			items, err := c.reader.ListJobs(ctx, ns)
			if err != nil {
				return err
			}
			collect.addJobs(items)
			return nil
		}},
		{operation: "list_cron_jobs", kind: "CronJob", fn: func(ctx context.Context) error {
			items, err := c.reader.ListCronJobs(ctx, ns)
			if err != nil {
				return err
			}
			collect.addCronJobs(items)
			return nil
		}},
		{operation: "list_pvcs", kind: "PVC", fn: func(ctx context.Context) error {
			items, err := c.reader.ListPersistentVolumeClaims(ctx, ns)
			if err != nil {
				return err
			}
			collect.addPVCs(items)
			return nil
		}},
		{operation: "list_pvs", kind: "PV", fn: func(ctx context.Context) error {
			items, err := c.reader.ListPersistentVolumes(ctx)
			if err != nil {
				return err
			}
			collect.addPVs(items)
			return nil
		}},
		{operation: "list_storage_classes", kind: "StorageClass", fn: func(ctx context.Context) error {
			items, err := c.reader.ListStorageClasses(ctx)
			if err != nil {
				return err
			}
			collect.addStorageClasses(items)
			return nil
		}},
		{operation: "list_volume_attachments", kind: "VolumeAttachment", fn: func(ctx context.Context) error {
			items, err := c.reader.ListVolumeAttachments(ctx)
			if err != nil {
				return err
			}
			collect.addVolumeAttachments(items)
			return nil
		}},
		{operation: "list_ingresses", kind: "Ingress", fn: func(ctx context.Context) error {
			items, err := c.reader.ListIngresses(ctx, ns)
			if err != nil {
				return err
			}
			collect.addIngresses(items)
			return nil
		}},
		{operation: "list_network_policies", kind: "NetworkPolicy", fn: func(ctx context.Context) error {
			items, err := c.reader.ListNetworkPolicies(ctx, ns)
			if err != nil {
				return err
			}
			collect.addNetworkPolicies(items)
			return nil
		}},
	}
}

// eventNamespaces 决定 events 采集范围：指定 namespace 时只用该 ns，否则用全部命名空间。
func eventNamespaces(nss []model.NamespaceInfo, filter string) []string {
	if filter != "" {
		return []string{filter}
	}
	names := make([]string, 0, len(nss))
	for _, n := range nss {
		names = append(names, n.Ref.Name)
	}
	return names
}

package model

// ClusterSnapshot is the internal collection result of Phase 1.
// It never enters the LLM or the report directly (ADR-002).
type ClusterSnapshot struct {
	ServerVersion    string                 `json:"serverVersion"`
	CollectedAt      string                 `json:"collectedAt"`
	Namespaces       []NamespaceInfo        `json:"namespaces"`
	Pods             []PodInfo              `json:"pods"`
	Nodes            []NodeInfo             `json:"nodes"`
	Services         []ServiceInfo          `json:"services"`
	EndpointSlices   []EndpointSliceInfo    `json:"endpointSlices"`
	Workloads        []WorkloadInfo         `json:"workloads"`
	Storage          []StorageInfo          `json:"storage"`
	Ingresses        []IngressInfo          `json:"ingresses"`
	NetworkPolicies  []NetworkPolicyInfo    `json:"networkPolicies"`
	Components       []ComponentInfo        `json:"components"`
	EventsIndex      map[string][]EventInfo `json:"-"` // UID → events, internal only
	CollectionErrors []CollectionError      `json:"collectionErrors,omitempty"`
}

type NamespaceInfo struct {
	Ref    ResourceRef
	Phase  string
	Labels map[string]string // 用于生产/测试环境识别（严重级调整）
}

type ConditionInfo struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

type ContainerInfo struct {
	Name         string
	Image        string
	ImageID      string
	State        string
	Reason       string
	Message      string
	ExitCode     int32
	RestartCount int32
	LastState    string
	LastReason   string
	LastExitCode int32
	Ready        bool
	Requests     map[string]string
	Limits       map[string]string
}

type PodInfo struct {
	Ref         ResourceRef
	Phase       string
	NodeName    string
	StartTime   string
	Conditions  []ConditionInfo
	Containers  []ContainerInfo
	OwnerRefs   []ResourceRef
	PVCRefs     []ResourceRef // 引用的 PVC（Pod 卷声明）
	Labels      map[string]string
	Annotations map[string]string // redacted at the collection boundary
	QoSClass    string
	Logs        map[string]CollectedLog // Phase 2: container name → log
}

// CollectedLog holds capped, redacted log bytes for one container.
type CollectedLog struct {
	Current   []byte
	Previous  []byte
	Truncated bool
	Error     string
}

type NodeInfo struct {
	Ref           ResourceRef
	Conditions    []ConditionInfo
	Unschedulable bool
	Capacity      map[string]string
	Allocatable   map[string]string
	Taints        []string
	Labels        map[string]string
}

type ServiceInfo struct {
	Ref        ResourceRef
	Selector   map[string]string
	ClusterIPs []string
	Ports      []string
}

type EndpointSliceInfo struct {
	Ref         ResourceRef
	ServiceName string        // 所属 Service（kubernetes.io/service-name 标签）
	TargetPods  []ResourceRef // 端点对应的 Pod
	Addresses   []string
	Ports       []string
	Ready       int
	NotReady    int
}

type WorkloadInfo struct {
	Ref               ResourceRef
	OwnerRefs         []ResourceRef // 上级 owner（如 ReplicaSet → Deployment）
	DesiredReplicas   *int32
	ReadyReplicas     *int32
	AvailableReplicas *int32
	UpdatedReplicas   *int32
	Conditions        []ConditionInfo
	Selector          map[string]string
}

type StorageInfo struct {
	Ref              ResourceRef
	Kind             string // PVC | PV | StorageClass | VolumeAttachment
	Status           string
	Reason           string
	Capacity         string
	Driver           string
	VolumeName       string // PVC 绑定的 PV 名（PVC→PV 关联）
	StorageClassName string // PVC/PV 的 StorageClass 名（→SC 关联）
}

type IngressInfo struct {
	Ref      ResourceRef
	Backends []string
}

type NetworkPolicyInfo struct {
	Ref         ResourceRef
	PodSelector map[string]string
}

// ComponentInfo describes a detected system component (FR-010).
type ComponentInfo struct {
	Name      string
	Namespace string
	Present   bool
	Ready     bool
	Detail    string
}

type EventInfo struct {
	Reason         string
	Message        string
	Type           string
	Count          int32
	FirstTimestamp string
	LastTimestamp  string
	InvolvedObject ResourceRef
}

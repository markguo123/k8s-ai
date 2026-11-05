package model

// ClusterSnapshot is the internal collection result of Phase 1.
// It never enters the LLM or the report directly (ADR-002).
type ClusterSnapshot struct {
	ServerVersion       string                   `json:"serverVersion"`
	CollectedAt         string                   `json:"collectedAt"`
	Namespaces          []NamespaceInfo          `json:"namespaces"`
	Pods                []PodInfo                `json:"pods"`
	Nodes               []NodeInfo               `json:"nodes"`
	Services            []ServiceInfo            `json:"services"`
	EndpointSlices      []EndpointSliceInfo      `json:"endpointSlices"`
	Workloads           []WorkloadInfo           `json:"workloads"`
	Storage             []StorageInfo            `json:"storage"`
	Ingresses           []IngressInfo            `json:"ingresses"`
	NetworkPolicies     []NetworkPolicyInfo      `json:"networkPolicies"`
	Hpas                []HpaInfo                `json:"hpas"`
	Pdbs                []PdbInfo                `json:"pdbs"`
	Roles               []RoleInfo               `json:"roles"`
	RoleBindings        []RoleBindingInfo        `json:"roleBindings"`
	ClusterRoles        []ClusterRoleInfo        `json:"clusterRoles"`
	ClusterRoleBindings []ClusterRoleBindingInfo `json:"clusterRoleBindings"`
	ConfigMaps          []ResourceRef            `json:"configMaps,omitempty"` // 集群内已存在 ConfigMap（仅名称，不读数据，P7.6）
	Secrets             []SecretInfo             `json:"secrets,omitempty"`    // Secret 元数据/键集（不含内容值，P7.6）
	Components          []ComponentInfo          `json:"components"`
	EventsIndex         map[string][]EventInfo   `json:"-"` // UID → events, internal only
	CollectionErrors    []CollectionError        `json:"collectionErrors,omitempty"`
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
	Probes       []ProbeInfo
}

// ProbeInfo 是容器探针配置的可读摘要，只含路径/端口/命令与时序，不含环境变量值。
type ProbeInfo struct {
	Kind                string
	Action              string
	Path                string
	Port                string
	Host                string
	Scheme              string
	Command             string
	InitialDelaySeconds int32
	PeriodSeconds       int32
	TimeoutSeconds      int32
	FailureThreshold    int32
	SuccessThreshold    int32
}

// SecretInfo 描述一个 Secret 的元数据与键级信息（绝不包含 data/stringData 内容值，P7.6）；
// 扫描器只读 Secret 的名称/类型/键集合，内容值不进入证据/LLM/会话/审计（AGENTS.md LLM Safety）。
type SecretInfo struct {
	Ref       ResourceRef
	Type      string
	Keys      []string
	Immutable bool
}

type PodInfo struct {
	Ref               ResourceRef
	Phase             string
	Reason            string // Pod.Status.Reason（如 Evicted）
	RestartPolicy     string // Pod.Spec.RestartPolicy；解释退出码 0 为何仍会反复重建时需要
	NodeName          string
	StartTime         string
	CreationTimestamp string // RFC3339；用于 Pending/Terminating 等时间门槛判定
	DeletionTimestamp string // RFC3339；非空表示正在删除（卡 Terminating 判定）
	Finalizers        []string
	Conditions        []ConditionInfo
	Containers        []ContainerInfo
	InitContainers    []ContainerInfo // initContainerStatuses 归一化
	OwnerRefs         []ResourceRef
	PVCRefs           []ResourceRef // 引用的 PVC（Pod 卷声明）
	ConfigMaps        []ResourceRef // 引用的 ConfigMap（仅名称，不读取数据；供修复建议定位配置来源）
	SecretRefs        []ResourceRef // 引用的 Secret（仅名称，绝不读取数据；供修复建议定位凭据来源）
	Labels            map[string]string
	Annotations       map[string]string // redacted at the collection boundary
	QoSClass          string
	Logs              map[string]CollectedLog // Phase 2: container name → log
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
	Suspend           bool // CronJob: spec.suspend=true（P7.3）
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
	CreatedAt        string // VolumeAttachment 创建时间（RFC3339，P7.4）
}

type IngressInfo struct {
	Ref      ResourceRef
	Backends []string
	Rules    []IngressRuleInfo // 归一化 host/path/backend，供冲突检测（P7.3）
}

// IngressRuleInfo 是单个 Ingress 规则的归一化描述。
type IngressRuleInfo struct {
	Host    string
	Path    string
	Backend string
}

type NetworkPolicyInfo struct {
	Ref         ResourceRef
	PodSelector map[string]string
	PolicyTypes []string // spec.policyTypes（P7.3）
}

// HpaInfo 水平 Pod 自动伸缩（autoscaling/v2）归一化信息（P7.5）。
type HpaInfo struct {
	Ref             ResourceRef
	MinReplicas     *int32
	MaxReplicas     int32
	DesiredReplicas int32  // status.desiredReplicas
	CurrentReplicas int32  // status.currentReplicas
	ScaleTarget     string // scaleTargetRef 的 Kind/name
}

// PdbInfo Pod 驱逐预算（policy/v1）归一化信息（P7.5）。
type PdbInfo struct {
	Ref                ResourceRef
	MinAvailable       string
	MaxUnavailable     string
	Selector           map[string]string
	DisruptionsAllowed int32 // status.disruptionsAllowed
}

// ClusterRoleInfo RBAC ClusterRole（rbac/v1）归一化信息（P7.5，只读元数据）。
type ClusterRoleInfo struct {
	Ref   ResourceRef
	Rules []PolicyRuleInfo
}

// RoleInfo RBAC Role（rbac/v1）归一化信息，仅用于分析实际命名空间授权。
type RoleInfo struct {
	Ref   ResourceRef
	Rules []PolicyRuleInfo
}

// RoleBindingInfo 命名空间级角色绑定（rbac/v1，只读元数据）。
type RoleBindingInfo struct {
	Ref      ResourceRef
	RoleRef  RoleRefInfo
	Subjects []SubjectInfo
}

// ClusterRoleBindingInfo 集群级角色绑定（rbac/v1，只读元数据）。
type ClusterRoleBindingInfo struct {
	Ref      ResourceRef
	RoleRef  RoleRefInfo
	Subjects []SubjectInfo
}

// RoleRefInfo 描述绑定的角色引用。
type RoleRefInfo struct {
	APIGroup string
	Kind     string
	Name     string
}

// SubjectInfo 描述 RBAC 绑定主体；ServiceAccount 会保留 namespace。
type SubjectInfo struct {
	Kind      string
	APIGroup  string
	Name      string
	Namespace string
}

// PolicyRuleInfo 单条 RBAC 策略规则的归一化描述（P7.5）。
type PolicyRuleInfo struct {
	APIGroups       []string
	Resources       []string
	Verbs           []string
	ResourceNames   []string
	NonResourceURLs []string
}

// ComponentInfo describes a detected system component (FR-010).
type ComponentInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Present   bool   `json:"present"`
	Ready     bool   `json:"ready"`
	Detail    string `json:"detail,omitempty"`
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

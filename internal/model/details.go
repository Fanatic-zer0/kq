package model

import "time"

// ContainerStatus is a safe, display-ready summary of one container's state.
type ContainerStatus struct {
	Name         string `json:"name"`
	Image        string `json:"image,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
	State        string `json:"state,omitempty"`
}

// VolumeInfo summarizes one Pod volume without exposing Secret/ConfigMap data.
type VolumeInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Backing string `json:"backing,omitempty"`
}

type PodDetails struct {
	Phase               string            `json:"phase,omitempty"`
	Ready               string            `json:"ready,omitempty"`
	Restarts            int32             `json:"restarts"`
	QoSClass            string            `json:"qosClass,omitempty"`
	NodeName            string            `json:"nodeName,omitempty"`
	PodIP               string            `json:"podIP,omitempty"`
	HostIP              string            `json:"hostIP,omitempty"`
	ServiceAccountName  string            `json:"serviceAccountName,omitempty"`
	CreationTimestamp   time.Time         `json:"creationTimestamp,omitempty"`
	Containers          []ContainerStatus `json:"containers,omitempty"`
	Volumes             []VolumeInfo      `json:"volumes,omitempty"`
	ImagePullSecretRefs []string          `json:"imagePullSecretRefs,omitempty"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type NodeDetails struct {
	InternalIP        string          `json:"internalIP,omitempty"`
	ExternalIP        string          `json:"externalIP,omitempty"`
	KubeletVersion    string          `json:"kubeletVersion,omitempty"`
	Taints            []string        `json:"taints,omitempty"`
	Conditions        []NodeCondition `json:"conditions,omitempty"`
	Unschedulable     bool            `json:"unschedulable,omitempty"`
	CreationTimestamp time.Time       `json:"creationTimestamp,omitempty"`
}

type DeploymentDetails struct {
	Replicas           int32           `json:"replicas"`
	ReadyReplicas      int32           `json:"readyReplicas"`
	UpdatedReplicas    int32           `json:"updatedReplicas"`
	AvailableReplicas  int32           `json:"availableReplicas"`
	Generation         int64           `json:"generation"`
	ObservedGeneration int64           `json:"observedGeneration"`
	Selector           string          `json:"selector,omitempty"`
	Strategy           string          `json:"strategy,omitempty"`
	Conditions         []NodeCondition `json:"conditions,omitempty"`
	CreationTimestamp  time.Time       `json:"creationTimestamp,omitempty"`
}

type ReplicaSetDetails struct {
	Desired           int32     `json:"desired"`
	Ready             int32     `json:"ready"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
}

type StatefulSetDetails struct {
	Replicas          int32     `json:"replicas"`
	ReadyReplicas     int32     `json:"readyReplicas"`
	ServiceName       string    `json:"serviceName,omitempty"`
	UpdateStrategy    string    `json:"updateStrategy,omitempty"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
}

type DaemonSetDetails struct {
	DesiredNumberScheduled int32     `json:"desiredNumberScheduled"`
	NumberReady            int32     `json:"numberReady"`
	NumberAvailable        int32     `json:"numberAvailable"`
	CreationTimestamp      time.Time `json:"creationTimestamp,omitempty"`
}

type JobDetails struct {
	Active            int32                      `json:"active"`
	Succeeded         int32                      `json:"succeeded"`
	Failed            int32                      `json:"failed"`
	Completions       int32                      `json:"completions"`
	Parallelism       int32                      `json:"parallelism"`
	BackoffLimit      int32                      `json:"backoffLimit"`
	Containers        []WorkloadContainerDetails `json:"containers,omitempty"`
	StartTime         *time.Time                 `json:"startTime,omitempty"`
	CompletionTime    *time.Time                 `json:"completionTime,omitempty"`
	CreationTimestamp time.Time                  `json:"creationTimestamp,omitempty"`
}

type WorkloadContainerDetails struct {
	Name    string   `json:"name"`
	Type    string   `json:"type,omitempty"`
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type CronJobDetails struct {
	Schedule          string                     `json:"schedule,omitempty"`
	Suspend           bool                       `json:"suspend"`
	ConcurrencyPolicy string                     `json:"concurrencyPolicy,omitempty"`
	NextRunTime       *time.Time                 `json:"nextRunTime,omitempty"`
	ScheduleError     string                     `json:"scheduleError,omitempty"`
	Containers        []WorkloadContainerDetails `json:"containers,omitempty"`
	LastScheduleTime  *time.Time                 `json:"lastScheduleTime,omitempty"`
	CreationTimestamp time.Time                  `json:"creationTimestamp,omitempty"`
}

type ServiceDetails struct {
	Type              string            `json:"type,omitempty"`
	ClusterIP         string            `json:"clusterIP,omitempty"`
	Selector          map[string]string `json:"selector,omitempty"`
	Ports             []string          `json:"ports,omitempty"`
	CreationTimestamp time.Time         `json:"creationTimestamp,omitempty"`
}

type EndpointsDetails struct {
	ReadyAddresses    int `json:"readyAddresses"`
	NotReadyAddresses int `json:"notReadyAddresses"`
}

type EndpointSliceDetails struct {
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
}

type IngressRule struct {
	Host        string `json:"host,omitempty"`
	Path        string `json:"path,omitempty"`
	PathType    string `json:"pathType,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	ServicePort string `json:"servicePort,omitempty"`
}

type IngressTLS struct {
	SecretName string   `json:"secretName,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
}

type IngressDetails struct {
	IngressClassName      string            `json:"ingressClassName,omitempty"`
	Annotations           map[string]string `json:"annotations,omitempty"`
	DefaultBackend        *IngressRule      `json:"defaultBackend,omitempty"`
	Rules                 []IngressRule     `json:"rules,omitempty"`
	TLS                   []IngressTLS      `json:"tls,omitempty"`
	LoadBalancerAddresses []string          `json:"loadBalancerAddresses,omitempty"`
	CreationTimestamp     time.Time         `json:"creationTimestamp,omitempty"`
}

type PVCDetails struct {
	Phase             string         `json:"phase,omitempty"`
	StorageClassName  string         `json:"storageClassName,omitempty"`
	RequestedCapacity string         `json:"requestedCapacity,omitempty"`
	Capacity          string         `json:"capacity,omitempty"`
	AccessModes       []string       `json:"accessModes,omitempty"`
	VolumeMode        string         `json:"volumeMode,omitempty"`
	VolumeName        string         `json:"volumeName,omitempty"`
	Selector          string         `json:"selector,omitempty"`
	DataSource        string         `json:"dataSource,omitempty"`
	Conditions        []PVCCondition `json:"conditions,omitempty"`
	CreationTimestamp time.Time      `json:"creationTimestamp,omitempty"`
}

type PVCCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type PVDetails struct {
	Capacity          string    `json:"capacity,omitempty"`
	AccessModes       []string  `json:"accessModes,omitempty"`
	ReclaimPolicy     string    `json:"reclaimPolicy,omitempty"`
	Phase             string    `json:"phase,omitempty"`
	StorageClassName  string    `json:"storageClassName,omitempty"`
	VolumeMode        string    `json:"volumeMode,omitempty"`
	ClaimNamespace    string    `json:"claimNamespace,omitempty"`
	ClaimName         string    `json:"claimName,omitempty"`
	MountOptions      []string  `json:"mountOptions,omitempty"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
}

type StorageClassDetails struct {
	Provisioner   string `json:"provisioner,omitempty"`
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
}

type ConfigMapDetails struct {
	Keys              []string   `json:"keys,omitempty"`
	CreationTimestamp time.Time  `json:"creationTimestamp,omitempty"`
	LastUpdated       *time.Time `json:"lastUpdated,omitempty"`
}

// SecretDetails.Keys is populated only when the query explicitly opts in; values are never captured.
type SecretDetails struct {
	Type              string     `json:"type,omitempty"`
	Keys              []string   `json:"keys,omitempty"`
	CreationTimestamp time.Time  `json:"creationTimestamp,omitempty"`
	LastUpdated       *time.Time `json:"lastUpdated,omitempty"`
}

type ServiceAccountDetails struct {
	AutomountToken    *bool     `json:"automountToken,omitempty"`
	SecretRefs        []string  `json:"secretRefs,omitempty"`
	ImagePullSecrets  []string  `json:"imagePullSecrets,omitempty"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
}

type HPADetails struct {
	MinReplicas     int32  `json:"minReplicas"`
	MaxReplicas     int32  `json:"maxReplicas"`
	CurrentReplicas int32  `json:"currentReplicas"`
	TargetSummary   string `json:"targetSummary,omitempty"`
}

type PDBDetails struct {
	AllowedDisruptions int32 `json:"allowedDisruptions"`
	CurrentHealthy     int32 `json:"currentHealthy"`
	DesiredHealthy     int32 `json:"desiredHealthy"`
}

type RoleBindingDetails struct {
	RoleKind string   `json:"roleKind,omitempty"`
	RoleName string   `json:"roleName,omitempty"`
	Subjects []string `json:"subjects,omitempty"`
}

type RoleDetails struct {
	Rules []string `json:"rules,omitempty"`
}

type CRDVersion struct {
	Name    string `json:"name"`
	Served  bool   `json:"served"`
	Storage bool   `json:"storage"`
}

type CRDCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type CRDDetails struct {
	Group             string         `json:"group,omitempty"`
	Scope             string         `json:"scope,omitempty"`
	Kind              string         `json:"kind,omitempty"`
	Plural            string         `json:"plural,omitempty"`
	Singular          string         `json:"singular,omitempty"`
	Versions          []CRDVersion   `json:"versions,omitempty"`
	Conditions        []CRDCondition `json:"conditions,omitempty"`
	CreationTimestamp time.Time      `json:"creationTimestamp,omitempty"`
}

type CustomResourceField struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

type CustomResourceDetails struct {
	APIVersion        string                `json:"apiVersion,omitempty"`
	CreationTimestamp time.Time             `json:"creationTimestamp,omitempty"`
	Labels            map[string]string     `json:"labels,omitempty"`
	Annotations       map[string]string     `json:"annotations,omitempty"`
	Config            any                   `json:"config,omitempty"`
	Spec              any                   `json:"spec,omitempty"`
	Status            any                   `json:"status,omitempty"`
	Fields            []CustomResourceField `json:"-"`
	FieldsTruncated   bool                  `json:"fieldsTruncated,omitempty"`
}

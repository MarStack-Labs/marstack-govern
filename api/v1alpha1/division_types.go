package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DivisionPhase string

const (
	DivisionPending     DivisionPhase = "Pending"
	DivisionActive      DivisionPhase = "Active"
	DivisionSuspended   DivisionPhase = "Suspended"
	DivisionTerminating DivisionPhase = "Terminating"
)

const (
	ConditionNamespacesReady = "NamespacesReady"
	ConditionIsolationReady  = "IsolationReady"
	ConditionAccessReady     = "AccessReady"
	ConditionQuotaReady      = "QuotaReady"
)

type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
	RoleApprover Role = "approver"
)

type Quota struct {
	// +kubebuilder:validation:Required
	CPU resource.Quantity `json:"cpu"`

	// +kubebuilder:validation:Required
	Memory resource.Quantity `json:"memory"`

	// +optional
	Storage resource.Quantity `json:"storage,omitempty"`

	// +optional
	// +kubebuilder:validation:Minimum=0
	Pods int32 `json:"pods,omitempty"`
}

type Limits struct {
	// +optional
	DefaultRequestCPU resource.Quantity `json:"defaultRequestCpu,omitempty"`

	// +optional
	DefaultRequestMemory resource.Quantity `json:"defaultRequestMemory,omitempty"`

	// +optional
	MaxCPUPerPod resource.Quantity `json:"maxCpuPerPod,omitempty"`

	// +optional
	MaxMemoryPerPod resource.Quantity `json:"maxMemoryPerPod,omitempty"`
}

type Grant struct {
	// +kubebuilder:validation:Enum=viewer;operator;admin;approver
	Role Role `json:"role"`

	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`
}

type DivisionSpec struct {
	// +kubebuilder:validation:MinLength=1
	DisplayName string `json:"displayName"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Environments []string `json:"environments"`

	// +kubebuilder:validation:Required
	Quota Quota `json:"quota"`

	// +optional
	Limits Limits `json:"limits,omitempty"`

	// +optional
	Access []Grant `json:"access,omitempty"`

	// +optional
	// +kubebuilder:default=true
	DefaultDeny *bool `json:"defaultDeny,omitempty"`

	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

type DivisionStatus struct {
	// +optional
	Phase DivisionPhase `json:"phase,omitempty"`

	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// +optional
	QuotaBackend string `json:"quotaBackend,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=div
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.quota.cpu`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.quota.memory`
// +kubebuilder:printcolumn:name="Quota",type=string,JSONPath=`.status.quotaBackend`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Division struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DivisionSpec   `json:"spec,omitempty"`
	Status DivisionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DivisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Division `json:"items"`
}

func (d *Division) NamespaceFor(environment string) string {
	return d.Name + "-" + environment
}

func (d *Division) IsolationEnabled() bool {
	return d.Spec.DefaultDeny == nil || *d.Spec.DefaultDeny
}

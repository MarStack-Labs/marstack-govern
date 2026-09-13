package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EnvironmentPhase string

const (
	EnvironmentPending    EnvironmentPhase = "Pending"
	EnvironmentReady      EnvironmentPhase = "Ready"
	EnvironmentExpired    EnvironmentPhase = "Expired"
	EnvironmentReclaiming EnvironmentPhase = "Reclaiming"
	EnvironmentOrphaned   EnvironmentPhase = "Orphaned"
)

const (
	ConditionNamespaceReady = "NamespaceReady"
	ConditionWithinQuota    = "WithinQuota"
	ConditionLeaseValid     = "LeaseValid"
)

type ChangeRef struct {
	// +kubebuilder:validation:MinLength=1
	Repository string `json:"repository"`

	// +kubebuilder:validation:Minimum=1
	Number int32 `json:"number"`

	// +kubebuilder:validation:MinLength=1
	Branch string `json:"branch"`

	// +optional
	Revision string `json:"revision,omitempty"`
}

type EphemeralEnvironmentSpec struct {
	// +kubebuilder:validation:MinLength=1
	Division string `json:"division"`

	// +kubebuilder:validation:Required
	Change ChangeRef `json:"change"`

	// +kubebuilder:validation:Required
	Quota Quota `json:"quota"`

	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?$`
	// +kubebuilder:default="48h"
	TTL string `json:"ttl,omitempty"`

	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	Renewals []Renewal `json:"renewals,omitempty"`
}

type Renewal struct {
	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?$`
	Extend string `json:"extend"`

	// +kubebuilder:validation:MinLength=10
	Reason string `json:"reason"`

	// +kubebuilder:validation:MinLength=1
	GrantedBy string `json:"grantedBy"`

	// +kubebuilder:validation:Required
	GrantedAt metav1.Time `json:"grantedAt"`
}

type EphemeralEnvironmentStatus struct {
	// +optional
	Phase EnvironmentPhase `json:"phase,omitempty"`

	// +optional
	Namespace string `json:"namespace,omitempty"`

	// +optional
	LeaseStartedAt *metav1.Time `json:"leaseStartedAt,omitempty"`

	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// +optional
	GrantedTTL string `json:"grantedTtl,omitempty"`

	// +optional
	ReclaimedAt *metav1.Time `json:"reclaimedAt,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=env
// +kubebuilder:printcolumn:name="Division",type=string,JSONPath=`.spec.division`
// +kubebuilder:printcolumn:name="Change",type=integer,JSONPath=`.spec.change.number`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Requester",type=string,JSONPath=`.spec.requestedBy`
type EphemeralEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EphemeralEnvironmentSpec   `json:"spec,omitempty"`
	Status EphemeralEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EphemeralEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EphemeralEnvironment `json:"items"`
}

func (e *EphemeralEnvironment) Live() bool {
	switch e.Status.Phase {
	case EnvironmentPending, EnvironmentReady, "":
		return true
	default:
		return false
	}
}

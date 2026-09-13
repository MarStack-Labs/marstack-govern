package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Outcome string

const (
	OutcomeApproved         Outcome = "Approved"
	OutcomeRejected         Outcome = "Rejected"
	OutcomeChangesRequested Outcome = "ChangesRequested"
)

type RequestRef struct {
	// +kubebuilder:validation:Enum=QuotaRequest
	Kind string `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// +optional
	UID string `json:"uid,omitempty"`
}

type Evidence struct {
	// +optional
	Recommendation *Recommendation `json:"recommendation,omitempty"`

	// +optional
	Preflight *PreflightResult `json:"preflight,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Digest string `json:"digest"`

	// +kubebuilder:validation:Required
	CapturedAt metav1.Time `json:"capturedAt"`
}

type DecisionSpec struct {
	// +kubebuilder:validation:Required
	Request RequestRef `json:"request"`

	// +kubebuilder:validation:Enum=Approved;Rejected;ChangesRequested
	Outcome Outcome `json:"outcome"`

	// +kubebuilder:validation:MinLength=10
	Reason string `json:"reason"`

	// +kubebuilder:validation:Required
	Evidence Evidence `json:"evidence"`

	// +optional
	DecidedBy string `json:"decidedBy,omitempty"`

	// +optional
	GrantedUntil *metav1.Time `json:"grantedUntil,omitempty"`

	// +optional
	RevokedReason string `json:"revokedReason,omitempty"`
}

type DecisionStatus struct {
	// +optional
	Applied bool `json:"applied,omitempty"`

	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`

	// +optional
	Expired bool `json:"expired,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dec
// +kubebuilder:printcolumn:name="Request",type=string,JSONPath=`.spec.request.name`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.spec.outcome`
// +kubebuilder:printcolumn:name="Decider",type=string,JSONPath=`.spec.decidedBy`
// +kubebuilder:printcolumn:name="Until",type=string,JSONPath=`.spec.grantedUntil`
// +kubebuilder:printcolumn:name="Applied",type=boolean,JSONPath=`.status.applied`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Decision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DecisionSpec   `json:"spec,omitempty"`
	Status DecisionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DecisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Decision `json:"items"`
}

func (d *Decision) Lapsed(now metav1.Time) bool {
	if d.Spec.Outcome != OutcomeApproved || d.Spec.GrantedUntil == nil {
		return false
	}

	return now.After(d.Spec.GrantedUntil.Time)
}

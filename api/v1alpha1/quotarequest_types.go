package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RequestPhase string

const (
	RequestPending          RequestPhase = "Pending"
	RequestAwaitingDecision RequestPhase = "AwaitingDecision"
	RequestApproved         RequestPhase = "Approved"
	RequestRejected         RequestPhase = "Rejected"
	RequestChangesRequested RequestPhase = "ChangesRequested"
	RequestApplied          RequestPhase = "Applied"
	RequestExpired          RequestPhase = "Expired"
	RequestWithdrawn        RequestPhase = "Withdrawn"
)

const (
	ConditionRecommended = "Recommended"
	ConditionPreflight   = "Preflight"
	ConditionSimulated   = "Simulated"
	ConditionDecided     = "Decided"
	ConditionApplied     = "Applied"
)

type QuotaRequestSpec struct {
	// +kubebuilder:validation:MinLength=1
	Division string `json:"division"`

	// +kubebuilder:validation:Required
	Target Quota `json:"target"`

	// +kubebuilder:validation:MinLength=10
	Reason string `json:"reason"`

	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`

	// +optional
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// +optional
	Withdrawn bool `json:"withdrawn,omitempty"`
}

type Recommendation struct {
	// +optional
	Current Quota `json:"current,omitempty"`

	// +optional
	ObservedP95 Quota `json:"observedP95,omitempty"`

	// +optional
	ObservedP99 Quota `json:"observedP99,omitempty"`

	// +optional
	Proposed Quota `json:"proposed,omitempty"`

	// +optional
	Window string `json:"window,omitempty"`

	// +optional
	Basis string `json:"basis,omitempty"`

	// +optional
	ExhaustionAt *metav1.Time `json:"exhaustionAt,omitempty"`

	// +optional
	HeadroomPercent int32 `json:"headroomPercent,omitempty"`
}

type PreflightResult struct {
	Admitted bool `json:"admitted"`

	// +optional
	Findings []PreflightFinding `json:"findings,omitempty"`

	// +optional
	ClusterCommitmentPercent int32 `json:"clusterCommitmentPercent,omitempty"`

	// +optional
	EvaluatedAt *metav1.Time `json:"evaluatedAt,omitempty"`
}

type PreflightFinding struct {
	// +kubebuilder:validation:Enum=warn;block
	Severity string `json:"severity"`

	Check   string `json:"check"`
	Message string `json:"message"`

	// +optional
	Field string `json:"field,omitempty"`
}

type Simulation struct {
	Schedulable bool `json:"schedulable"`

	// +optional
	TypicalPodCPUMillicores int64 `json:"typicalPodCpuMillicores,omitempty"`

	// +optional
	TypicalPodMemoryBytes int64 `json:"typicalPodMemoryBytes,omitempty"`

	// +optional
	HeadroomPods int32 `json:"headroomPods,omitempty"`

	// +optional
	PlacedPods int32 `json:"placedPods,omitempty"`

	// +optional
	UnplacedPods int32 `json:"unplacedPods,omitempty"`

	// +optional
	NodesExhausted []string `json:"nodesExhausted,omitempty"`

	// +optional
	PendingNow []PendingPod `json:"pendingNow,omitempty"`

	// +optional
	CommitmentBeforePercent int32 `json:"commitmentBeforePercent,omitempty"`

	// +optional
	CommitmentAfterPercent int32 `json:"commitmentAfterPercent,omitempty"`

	// +optional
	Verdict string `json:"verdict,omitempty"`

	// +optional
	SimulatedAt *metav1.Time `json:"simulatedAt,omitempty"`
}

type PendingPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// +optional
	Reason string `json:"reason,omitempty"`
}

type QuotaRequestStatus struct {
	// +optional
	Phase RequestPhase `json:"phase,omitempty"`

	// +optional
	Recommendation *Recommendation `json:"recommendation,omitempty"`

	// +optional
	Preflight *PreflightResult `json:"preflight,omitempty"`

	// +optional
	Simulation *Simulation `json:"simulation,omitempty"`

	// +optional
	DecisionRef string `json:"decisionRef,omitempty"`

	// +optional
	EvidenceDigest string `json:"evidenceDigest,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=qr
// +kubebuilder:printcolumn:name="Division",type=string,JSONPath=`.spec.division`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.target.cpu`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.target.memory`
// +kubebuilder:printcolumn:name="Requester",type=string,JSONPath=`.spec.requestedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type QuotaRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QuotaRequestSpec   `json:"spec,omitempty"`
	Status QuotaRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []QuotaRequest `json:"items"`
}

func (r *QuotaRequest) Open() bool {
	switch r.Status.Phase {
	case "", RequestPending, RequestAwaitingDecision, RequestChangesRequested:
		return !r.Spec.Withdrawn
	default:
		return false
	}
}

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UnallocatedStrategy string

const (
	UnallocatedPlatform UnallocatedStrategy = "Platform"
	UnallocatedProRata  UnallocatedStrategy = "ProRata"
)

const ConditionEffective = "Effective"

type Rates struct {
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,2})?$`
	CPUCoreMonth string `json:"cpuCoreMonth"`

	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,2})?$`
	MemoryGiMonth string `json:"memoryGiMonth"`

	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,2})?$`
	StorageGiMonth string `json:"storageGiMonth,omitempty"`

	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,2})?$`
	LoadBalancerMonth string `json:"loadBalancerMonth,omitempty"`

	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]{1,2})?$`
	EgressGi string `json:"egressGi,omitempty"`
}

type PricingPolicySpec struct {
	// +optional
	// +kubebuilder:default=IDR
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=3
	Currency string `json:"currency,omitempty"`

	// +kubebuilder:validation:Required
	Rates Rates `json:"rates"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=date
	EffectiveFrom string `json:"effectiveFrom"`

	// +optional
	// +kubebuilder:validation:Format=date
	EffectiveTo string `json:"effectiveTo,omitempty"`

	// +kubebuilder:validation:Enum=Platform;ProRata
	// +kubebuilder:default=Platform
	Unallocated UnallocatedStrategy `json:"unallocated"`

	// +kubebuilder:validation:MinLength=1
	ApprovedBy string `json:"approvedBy"`

	// +kubebuilder:validation:Required
	ApprovedAt metav1.Time `json:"approvedAt"`
}

type PricingPolicyStatus struct {
	// +optional
	Effective bool `json:"effective,omitempty"`

	// +optional
	Revision int64 `json:"revision,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=price
// +kubebuilder:printcolumn:name="Currency",type=string,JSONPath=`.spec.currency`
// +kubebuilder:printcolumn:name="CPU/core/month",type=string,JSONPath=`.spec.rates.cpuCoreMonth`
// +kubebuilder:printcolumn:name="Memory/Gi/month",type=string,JSONPath=`.spec.rates.memoryGiMonth`
// +kubebuilder:printcolumn:name="From",type=string,JSONPath=`.spec.effectiveFrom`
// +kubebuilder:printcolumn:name="Effective",type=boolean,JSONPath=`.status.effective`
// +kubebuilder:printcolumn:name="Revision",type=integer,JSONPath=`.status.revision`
type PricingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PricingPolicySpec   `json:"spec,omitempty"`
	Status PricingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PricingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PricingPolicy `json:"items"`
}

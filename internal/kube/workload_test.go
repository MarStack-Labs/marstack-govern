package kube

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeploymentHealth(t *testing.T) {
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		want       Health
	}{
		{
			name:       "all replicas ready",
			deployment: deployment(2, 2, 1, 1),
			want:       HealthHealthy,
		},
		{
			name:       "some replicas ready",
			deployment: deployment(3, 1, 1, 1),
			want:       HealthProgressing,
		},
		{
			name:       "no replicas ready",
			deployment: deployment(2, 0, 1, 1),
			want:       HealthDegraded,
		},
		{
			name:       "scaled to zero on purpose",
			deployment: deployment(0, 0, 1, 1),
			want:       HealthHealthy,
		},
		{
			name:       "status not yet observed",
			deployment: deployment(2, 2, 1, 2),
			want:       HealthProgressing,
		},
		{
			name:       "progress deadline exceeded",
			deployment: withCondition(deployment(2, 2, 1, 1), appsv1.DeploymentProgressing, corev1.ConditionFalse, "ProgressDeadlineExceeded"),
			want:       HealthDegraded,
		},
		{
			name:       "replica failure",
			deployment: withCondition(deployment(2, 2, 1, 1), appsv1.DeploymentReplicaFailure, corev1.ConditionTrue, "FailedCreate"),
			want:       HealthDegraded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := WorkloadFromDeployment(test.deployment).Health
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorkloadCarriesRequests(t *testing.T) {
	d := deployment(1, 1, 1, 1)
	d.Spec.Template.Spec.Containers = []corev1.Container{{
		Name:  "api",
		Image: "registry.internal/api@sha256:abc",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}}

	workload := WorkloadFromDeployment(d)

	if len(workload.Containers) != 1 {
		t.Fatalf("got %d containers, want 1", len(workload.Containers))
	}

	container := workload.Containers[0]
	if container.CPURequestMillicores != 250 {
		t.Errorf("cpu request: got %d, want 250", container.CPURequestMillicores)
	}
	if container.CPULimitMillicores != 1000 {
		t.Errorf("cpu limit: got %d, want 1000", container.CPULimitMillicores)
	}
	if container.MemoryRequestBytes != 512*1024*1024 {
		t.Errorf("memory request: got %d, want %d", container.MemoryRequestBytes, 512*1024*1024)
	}
	if workload.PrimaryImage() != "registry.internal/api@sha256:abc" {
		t.Errorf("primary image: got %q", workload.PrimaryImage())
	}
}

func TestDivisionComesFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{name: "no labels", labels: nil, want: ""},
		{name: "govern label", labels: map[string]string{LabelDivision: "payments"}, want: "payments"},
		{name: "capsule tenant", labels: map[string]string{LabelCapsuleTenant: "erp"}, want: "erp"},
		{
			name:   "govern label wins",
			labels: map[string]string{LabelDivision: "payments", LabelCapsuleTenant: "erp"},
			want:   "payments",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := deployment(1, 1, 1, 1)
			d.Labels = test.labels

			if got := WorkloadFromDeployment(d).Division; got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func deployment(desired, ready int32, observedGeneration, generation int64) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			UID:             "11111111-1111-1111-1111-111111111111",
			Name:            "api",
			Namespace:       "payments-dev",
			Generation:      generation,
			ResourceVersion: "42",
		},
		Spec: appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:      ready,
			ObservedGeneration: observedGeneration,
		},
	}
}

func withCondition(d *appsv1.Deployment, conditionType appsv1.DeploymentConditionType, status corev1.ConditionStatus, reason string) *appsv1.Deployment {
	d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
		Type:   conditionType,
		Status: status,
		Reason: reason,
	})
	return d
}

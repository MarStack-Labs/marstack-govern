package kube

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	LabelDivision        = "govern.marstack.io/division"
	LabelCapsuleTenant   = "capsule.clastix.io/tenant"
	AnnotationSourceRepo = "app.kubernetes.io/source-repo"
)

type Health string

const (
	HealthHealthy     Health = "healthy"
	HealthProgressing Health = "progressing"
	HealthDegraded    Health = "degraded"
	HealthUnknown     Health = "unknown"
)

type Container struct {
	Name                 string
	Image                string
	CPURequestMillicores int64
	CPULimitMillicores   int64
	MemoryRequestBytes   int64
	MemoryLimitBytes     int64
}

type Workload struct {
	UID             string
	Namespace       string
	Name            string
	Kind            string
	APIVersion      string
	Division        string
	ReplicasDesired int32
	ReplicasReady   int32
	Health          Health
	Containers      []Container
	CreatedAt       time.Time
	ResourceVersion string
}

func (w Workload) PrimaryImage() string {
	if len(w.Containers) == 0 {
		return ""
	}
	return w.Containers[0].Image
}

func WorkloadFromDeployment(d *appsv1.Deployment) Workload {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}

	return Workload{
		UID:             string(d.UID),
		Namespace:       d.Namespace,
		Name:            d.Name,
		Kind:            "Deployment",
		APIVersion:      "apps/v1",
		Division:        divisionOf(d.ObjectMeta),
		ReplicasDesired: desired,
		ReplicasReady:   d.Status.ReadyReplicas,
		Health:          deploymentHealth(d, desired),
		Containers:      containersOf(d.Spec.Template.Spec.Containers),
		CreatedAt:       d.CreationTimestamp.Time,
		ResourceVersion: d.ResourceVersion,
	}
}

func WorkloadFromStatefulSet(s *appsv1.StatefulSet) Workload {
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}

	return Workload{
		UID:             string(s.UID),
		Namespace:       s.Namespace,
		Name:            s.Name,
		Kind:            "StatefulSet",
		APIVersion:      "apps/v1",
		Division:        divisionOf(s.ObjectMeta),
		ReplicasDesired: desired,
		ReplicasReady:   s.Status.ReadyReplicas,
		Health:          replicaHealth(s.Status.ReadyReplicas, desired, s.Status.ObservedGeneration, s.Generation),
		Containers:      containersOf(s.Spec.Template.Spec.Containers),
		CreatedAt:       s.CreationTimestamp.Time,
		ResourceVersion: s.ResourceVersion,
	}
}

func WorkloadFromDaemonSet(d *appsv1.DaemonSet) Workload {
	desired := d.Status.DesiredNumberScheduled

	return Workload{
		UID:             string(d.UID),
		Namespace:       d.Namespace,
		Name:            d.Name,
		Kind:            "DaemonSet",
		APIVersion:      "apps/v1",
		Division:        divisionOf(d.ObjectMeta),
		ReplicasDesired: desired,
		ReplicasReady:   d.Status.NumberReady,
		Health:          replicaHealth(d.Status.NumberReady, desired, d.Status.ObservedGeneration, d.Generation),
		Containers:      containersOf(d.Spec.Template.Spec.Containers),
		CreatedAt:       d.CreationTimestamp.Time,
		ResourceVersion: d.ResourceVersion,
	}
}

func divisionOf(meta metav1.ObjectMeta) string {
	if division := meta.Labels[LabelDivision]; division != "" {
		return division
	}
	return meta.Labels[LabelCapsuleTenant]
}

func containersOf(containers []corev1.Container) []Container {
	out := make([]Container, 0, len(containers))
	for _, c := range containers {
		out = append(out, Container{
			Name:                 c.Name,
			Image:                c.Image,
			CPURequestMillicores: c.Resources.Requests.Cpu().MilliValue(),
			CPULimitMillicores:   c.Resources.Limits.Cpu().MilliValue(),
			MemoryRequestBytes:   c.Resources.Requests.Memory().Value(),
			MemoryLimitBytes:     c.Resources.Limits.Memory().Value(),
		})
	}
	return out
}

func deploymentHealth(d *appsv1.Deployment, desired int32) Health {
	for _, condition := range d.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return HealthDegraded
		}
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return HealthDegraded
		}
	}

	return replicaHealth(d.Status.ReadyReplicas, desired, d.Status.ObservedGeneration, d.Generation)
}

func replicaHealth(ready, desired int32, observedGeneration, generation int64) Health {
	if observedGeneration < generation {
		return HealthProgressing
	}
	if desired == 0 {
		return HealthHealthy
	}
	if ready == desired {
		return HealthHealthy
	}
	if ready == 0 {
		return HealthDegraded
	}
	return HealthProgressing
}

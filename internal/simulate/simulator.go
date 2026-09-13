package simulate

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	fallbackPodCPU = 250
	fallbackPodMem = 512 * 1024 * 1024
	maxSimulated   = 500
)

type Compute struct {
	CPUMillicores int64
	MemoryBytes   int64
}

type PendingPod struct {
	Namespace string
	Name      string
	Reason    string
}

type Impact struct {
	QuotaBefore  Compute
	QuotaAfter   Compute
	Committed    Compute
	Allocatable  Compute
	CommitBefore float64
	CommitAfter  float64

	TypicalPod     Pod
	HeadroomPods   int
	PlacedPods     int
	UnplacedPods   int
	NodesExhausted []string
	PendingNow     []PendingPod

	Schedulable bool
	Verdict     string
	SimulatedAt time.Time
}

type Simulator struct {
	client.Client
	Now func() time.Time
}

func (s *Simulator) Impact(ctx context.Context, namespaces []string, before, after, committed Compute) (Impact, error) {
	nodes, allocatable, err := s.snapshot(ctx)
	if err != nil {
		return Impact{}, err
	}

	typical, err := s.typicalPod(ctx, namespaces)
	if err != nil {
		return Impact{}, err
	}

	pending, err := s.pendingNow(ctx, namespaces)
	if err != nil {
		return Impact{}, err
	}

	headroom := Compute{
		CPUMillicores: after.CPUMillicores - before.CPUMillicores,
		MemoryBytes:   after.MemoryBytes - before.MemoryBytes,
	}

	pods := fill(headroom, typical)
	placement := Place(nodes, pods)

	committedAfter := Compute{
		CPUMillicores: committed.CPUMillicores - before.CPUMillicores + after.CPUMillicores,
		MemoryBytes:   committed.MemoryBytes - before.MemoryBytes + after.MemoryBytes,
	}

	impact := Impact{
		QuotaBefore:    before,
		QuotaAfter:     after,
		Committed:      committedAfter,
		Allocatable:    allocatable,
		CommitBefore:   ratio(committed.CPUMillicores, allocatable.CPUMillicores),
		CommitAfter:    ratio(committedAfter.CPUMillicores, allocatable.CPUMillicores),
		TypicalPod:     typical,
		HeadroomPods:   len(pods),
		PlacedPods:     placement.Placed,
		UnplacedPods:   len(placement.Unplaced),
		NodesExhausted: placement.NodesExhausted,
		PendingNow:     pending,
		SimulatedAt:    s.now(),
	}

	impact.Schedulable = impact.UnplacedPods == 0
	impact.Verdict = verdictOf(impact)

	return impact, nil
}

func (s *Simulator) snapshot(ctx context.Context) ([]Node, Compute, error) {
	nodeList := &corev1.NodeList{}
	if err := s.List(ctx, nodeList); err != nil {
		return nil, Compute{}, fmt.Errorf("list nodes: %w", err)
	}

	podList := &corev1.PodList{}
	if err := s.List(ctx, podList); err != nil {
		return nil, Compute{}, fmt.Errorf("list pods: %w", err)
	}

	allocatedCPU := map[string]int64{}
	allocatedMem := map[string]int64{}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName == "" || terminal(pod) {
			continue
		}

		cpu, memory := requestsOf(pod)
		allocatedCPU[pod.Spec.NodeName] += cpu
		allocatedMem[pod.Spec.NodeName] += memory
	}

	nodes := make([]Node, 0, len(nodeList.Items))
	allocatable := Compute{}

	for i := range nodeList.Items {
		item := &nodeList.Items[i]

		node := Node{
			Name:           item.Name,
			AllocatableCPU: item.Status.Allocatable.Cpu().MilliValue(),
			AllocatableMem: item.Status.Allocatable.Memory().Value(),
			AllocatedCPU:   allocatedCPU[item.Name],
			AllocatedMem:   allocatedMem[item.Name],
			Schedulable:    true,
		}

		switch {
		case item.Spec.Unschedulable:
			node.Schedulable, node.UnschedulableBy = false, "cordoned"
		case !ready(item):
			node.Schedulable, node.UnschedulableBy = false, "not ready"
		case taintedNoSchedule(item):
			node.Schedulable, node.UnschedulableBy = false, "tainted"
		}

		if node.Schedulable {
			allocatable.CPUMillicores += node.AllocatableCPU
			allocatable.MemoryBytes += node.AllocatableMem
		}

		nodes = append(nodes, node)
	}

	return nodes, allocatable, nil
}

func (s *Simulator) typicalPod(ctx context.Context, namespaces []string) (Pod, error) {
	sizes := []Pod{}

	for _, namespace := range namespaces {
		podList := &corev1.PodList{}
		if err := s.List(ctx, podList, client.InNamespace(namespace)); err != nil {
			return Pod{}, fmt.Errorf("list pods in %s: %w", namespace, err)
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if terminal(pod) {
				continue
			}

			cpu, memory := requestsOf(pod)
			if cpu == 0 && memory == 0 {
				continue
			}

			sizes = append(sizes, Pod{Name: pod.Name, CPU: cpu, Mem: memory})
		}
	}

	if len(sizes) == 0 {
		return Pod{Name: "typical", CPU: fallbackPodCPU, Mem: fallbackPodMem}, nil
	}

	sort.Slice(sizes, func(i, j int) bool { return sizes[i].CPU < sizes[j].CPU })
	median := sizes[len(sizes)/2]

	return Pod{Name: "typical", CPU: max64(median.CPU, 1), Mem: max64(median.Mem, 1)}, nil
}

func (s *Simulator) pendingNow(ctx context.Context, namespaces []string) ([]PendingPod, error) {
	pending := []PendingPod{}

	for _, namespace := range namespaces {
		podList := &corev1.PodList{}
		if err := s.List(ctx, podList, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" {
				continue
			}

			pending = append(pending, PendingPod{
				Namespace: pod.Namespace,
				Name:      pod.Name,
				Reason:    unschedulableReason(pod),
			})
		}
	}

	return pending, nil
}

func (s *Simulator) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}

	return time.Now()
}

func fill(headroom Compute, typical Pod) []Pod {
	if headroom.CPUMillicores <= 0 && headroom.MemoryBytes <= 0 {
		return nil
	}

	byCPU := int64(0)
	if typical.CPU > 0 && headroom.CPUMillicores > 0 {
		byCPU = headroom.CPUMillicores / typical.CPU
	}

	byMemory := int64(0)
	if typical.Mem > 0 && headroom.MemoryBytes > 0 {
		byMemory = headroom.MemoryBytes / typical.Mem
	}

	count := byCPU
	if byMemory < count || count == 0 {
		count = byMemory
	}
	if count <= 0 {
		count = 1
	}
	if count > maxSimulated {
		count = maxSimulated
	}

	pods := make([]Pod, 0, count)
	for i := range count {
		pods = append(pods, Pod{
			Name: fmt.Sprintf("%s-%d", typical.Name, i+1),
			CPU:  typical.CPU,
			Mem:  typical.Mem,
		})
	}

	return pods
}

func verdictOf(impact Impact) string {
	if impact.HeadroomPods == 0 {
		return "the target does not add headroom, so nothing new has to be scheduled"
	}

	if impact.Schedulable {
		return fmt.Sprintf("all %d pods of the division's typical size (%dm cpu, %dMi) would find a node",
			impact.HeadroomPods, impact.TypicalPod.CPU, impact.TypicalPod.Mem/1024/1024)
	}

	return fmt.Sprintf("%d of %d pods of the division's typical size would not fit on any node, even though the quota allows them",
		impact.UnplacedPods, impact.HeadroomPods)
}

func requestsOf(pod *corev1.Pod) (cpu, memory int64) {
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		cpu += container.Resources.Requests.Cpu().MilliValue()
		memory += container.Resources.Requests.Memory().Value()
	}

	return cpu, memory
}

func terminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func ready(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return true
}

func taintedNoSchedule(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			return true
		}
	}

	return false
}

func unschedulableReason(pod *corev1.Pod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			if condition.Message != "" {
				return condition.Message
			}
			return condition.Reason
		}
	}

	return "waiting to be scheduled"
}

func ratio(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}

	return float64(part) / float64(whole)
}

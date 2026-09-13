package simulate_test

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marstack-labs/marstack-govern/internal/simulate"
)

const gibibyte = 1024 * 1024 * 1024

func TestSpareCapacityScatteredAcrossNodesDoesNotFit(t *testing.T) {
	objects := []client.Object{}

	for i := range 6 {
		name := fmt.Sprintf("node-%d", i)
		objects = append(objects,
			node(name, 4000, 8*gibibyte),
			runningPod(fmt.Sprintf("filler-%d", i), "other", name, 3300, 6*gibibyte),
		)
	}

	objects = append(objects, runningPod("api", "payments-dev", "node-0", 2000, 4*gibibyte))

	simulator := &simulate.Simulator{Client: newCluster(t, objects...)}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 8000, MemoryBytes: 16 * gibibyte},
		simulate.Compute{CPUMillicores: 12000, MemoryBytes: 24 * gibibyte},
		simulate.Compute{CPUMillicores: 8000, MemoryBytes: 16 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if impact.TypicalPod.CPU != 2000 {
		t.Fatalf("typical pod: got %dm, want 2000m", impact.TypicalPod.CPU)
	}
	if impact.HeadroomPods != 2 {
		t.Fatalf("headroom pods: got %d, want 2", impact.HeadroomPods)
	}

	if impact.Schedulable {
		t.Fatal("the simulation said pods would fit while every node has only 700m free")
	}
	if impact.UnplacedPods != 2 {
		t.Errorf("unplaced: got %d, want 2", impact.UnplacedPods)
	}
	if !strings.Contains(impact.Verdict, "would not fit") {
		t.Errorf("verdict: %q", impact.Verdict)
	}
}

func TestRoomOnOneNodeIsEnough(t *testing.T) {
	c := newCluster(t,
		node("node-0", 8000, 32*gibibyte),
		node("node-1", 8000, 32*gibibyte),
		runningPod("api", "payments-dev", "node-0", 2000, 4*gibibyte),
	)

	simulator := &simulate.Simulator{Client: c}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 8000, MemoryBytes: 16 * gibibyte},
		simulate.Compute{CPUMillicores: 12000, MemoryBytes: 24 * gibibyte},
		simulate.Compute{CPUMillicores: 8000, MemoryBytes: 16 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if !impact.Schedulable {
		t.Fatalf("pods that fit were reported unschedulable: %+v", impact)
	}
	if impact.PlacedPods != impact.HeadroomPods {
		t.Errorf("placed %d of %d", impact.PlacedPods, impact.HeadroomPods)
	}
	if !strings.Contains(impact.Verdict, "would find a node") {
		t.Errorf("verdict: %q", impact.Verdict)
	}
}

func TestCordonedAndTaintedNodesAreNotCounted(t *testing.T) {
	healthy := node("node-0", 8000, 32*gibibyte)

	cordoned := node("node-1", 8000, 32*gibibyte)
	cordoned.Spec.Unschedulable = true

	tainted := node("node-2", 8000, 32*gibibyte)
	tainted.Spec.Taints = []corev1.Taint{{
		Key:    "dedicated",
		Value:  "ingress",
		Effect: corev1.TaintEffectNoSchedule,
	}}

	broken := node("node-3", 8000, 32*gibibyte)
	broken.Status.Conditions = []corev1.NodeCondition{{
		Type:   corev1.NodeReady,
		Status: corev1.ConditionFalse,
	}}

	simulator := &simulate.Simulator{Client: newCluster(t, healthy, cordoned, tainted, broken)}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 1000, MemoryBytes: 1 * gibibyte},
		simulate.Compute{CPUMillicores: 2000, MemoryBytes: 2 * gibibyte},
		simulate.Compute{CPUMillicores: 1000, MemoryBytes: 1 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if impact.Allocatable.CPUMillicores != 8000 {
		t.Errorf("allocatable cpu: got %dm, want only the one healthy node's 8000m",
			impact.Allocatable.CPUMillicores)
	}
}

func TestADivisionWithoutPodsGetsAStatedDefault(t *testing.T) {
	simulator := &simulate.Simulator{Client: newCluster(t, node("node-0", 8000, 32*gibibyte))}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 4000, MemoryBytes: 8 * gibibyte},
		simulate.Compute{CPUMillicores: 5000, MemoryBytes: 10 * gibibyte},
		simulate.Compute{CPUMillicores: 4000, MemoryBytes: 8 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if impact.TypicalPod.CPU != 250 {
		t.Errorf("typical pod cpu: got %dm, want the stated default of 250m", impact.TypicalPod.CPU)
	}
	if impact.HeadroomPods != 4 {
		t.Errorf("headroom pods: got %d, want 4", impact.HeadroomPods)
	}
}

func TestPodsAlreadyWaitingAreReported(t *testing.T) {
	waiting := runningPod("worker", "payments-dev", "", 500, gibibyte)
	waiting.Status.Phase = corev1.PodPending
	waiting.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: "0/3 nodes are available: insufficient cpu",
	}}

	simulator := &simulate.Simulator{Client: newCluster(t, node("node-0", 8000, 32*gibibyte), waiting)}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 4000, MemoryBytes: 8 * gibibyte},
		simulate.Compute{CPUMillicores: 6000, MemoryBytes: 12 * gibibyte},
		simulate.Compute{CPUMillicores: 4000, MemoryBytes: 8 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if len(impact.PendingNow) != 1 {
		t.Fatalf("pending pods: got %v", impact.PendingNow)
	}
	if !strings.Contains(impact.PendingNow[0].Reason, "insufficient cpu") {
		t.Errorf("reason: got %q", impact.PendingNow[0].Reason)
	}
}

func TestCommitmentIsReportedBeforeAndAfter(t *testing.T) {
	simulator := &simulate.Simulator{Client: newCluster(t, node("node-0", 10000, 32*gibibyte))}

	impact, err := simulator.Impact(
		t.Context(),
		[]string{"payments-dev"},
		simulate.Compute{CPUMillicores: 4000, MemoryBytes: 8 * gibibyte},
		simulate.Compute{CPUMillicores: 6000, MemoryBytes: 12 * gibibyte},
		simulate.Compute{CPUMillicores: 6000, MemoryBytes: 12 * gibibyte},
	)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}

	if got := round(impact.CommitBefore); got != 0.6 {
		t.Errorf("commitment before: got %v, want 0.6", got)
	}
	if got := round(impact.CommitAfter); got != 0.8 {
		t.Errorf("commitment after: got %v, want 0.8", got)
	}
}

func TestPackingFillsTheTightestNodeFirst(t *testing.T) {
	nodes := []simulate.Node{
		{Name: "roomy", AllocatableCPU: 8000, AllocatableMem: 16 * gibibyte, Schedulable: true},
		{Name: "tight", AllocatableCPU: 2000, AllocatableMem: 4 * gibibyte, Schedulable: true},
	}

	placement := simulate.Place(nodes, []simulate.Pod{{Name: "a", CPU: 2000, Mem: 4 * gibibyte}})

	if placement.Placed != 1 {
		t.Fatalf("placed: got %d", placement.Placed)
	}
	if len(placement.NodesExhausted) != 1 || placement.NodesExhausted[0] != "tight" {
		t.Errorf("exhausted: got %v, want [tight]", placement.NodesExhausted)
	}
}

func newCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func node(name string, cpuMillis, memoryBytes int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

func runningPod(name, namespace, nodeName string, cpuMillis, memoryBytes int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
						corev1.ResourceMemory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func round(value float64) float64 {
	return float64(int(value*100+0.5)) / 100
}

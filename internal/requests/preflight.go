package requests

import (
	"context"
	"fmt"
	"math"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

const warnCommitmentPercent = 85

type Preflight struct {
	client.Client
}

func (p *Preflight) Evaluate(ctx context.Context, division string, target Compute) (*governv1alpha1.PreflightResult, error) {
	allocatable, err := p.allocatable(ctx)
	if err != nil {
		return nil, err
	}

	committed, current, err := p.committed(ctx, division)
	if err != nil {
		return nil, err
	}

	after := Compute{
		CPUMillicores: committed.CPUMillicores - current.CPUMillicores + target.CPUMillicores,
		MemoryBytes:   committed.MemoryBytes - current.MemoryBytes + target.MemoryBytes,
	}

	now := metav1.Now()
	result := &governv1alpha1.PreflightResult{Admitted: true, EvaluatedAt: &now}

	if target.CPUMillicores < current.CPUMillicores {
		result.Findings = append(result.Findings, governv1alpha1.PreflightFinding{
			Severity: "warn",
			Check:    "quota-shrinks",
			Field:    "spec.target.cpu",
			Message: fmt.Sprintf("the target of %dm is below the current quota of %dm, so running workloads may be evicted from their headroom",
				target.CPUMillicores, current.CPUMillicores),
		})
	}

	result.ClusterCommitmentPercent = percentOf(after.CPUMillicores, allocatable.CPUMillicores)

	if allocatable.CPUMillicores > 0 && after.CPUMillicores > allocatable.CPUMillicores {
		result.Admitted = false
		result.Findings = append(result.Findings, governv1alpha1.PreflightFinding{
			Severity: "block",
			Check:    "cluster-cpu",
			Field:    "spec.target.cpu",
			Message: fmt.Sprintf("granting this would commit %dm of cpu against %dm allocatable in the cluster",
				after.CPUMillicores, allocatable.CPUMillicores),
		})
	}

	if allocatable.MemoryBytes > 0 && after.MemoryBytes > allocatable.MemoryBytes {
		result.Admitted = false
		result.Findings = append(result.Findings, governv1alpha1.PreflightFinding{
			Severity: "block",
			Check:    "cluster-memory",
			Field:    "spec.target.memory",
			Message: fmt.Sprintf("granting this would commit %d bytes of memory against %d allocatable in the cluster",
				after.MemoryBytes, allocatable.MemoryBytes),
		})
	}

	if result.Admitted && result.ClusterCommitmentPercent >= warnCommitmentPercent {
		result.Findings = append(result.Findings, governv1alpha1.PreflightFinding{
			Severity: "warn",
			Check:    "cluster-headroom",
			Message: fmt.Sprintf("the cluster would be %d%% committed on cpu, leaving little room for another division",
				result.ClusterCommitmentPercent),
		})
	}

	return result, nil
}

func (p *Preflight) allocatable(ctx context.Context) (Compute, error) {
	nodes := &corev1.NodeList{}
	if err := p.List(ctx, nodes); err != nil {
		return Compute{}, fmt.Errorf("list nodes: %w", err)
	}

	total := Compute{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.Spec.Unschedulable {
			continue
		}

		if cpu, ok := node.Status.Allocatable[corev1.ResourceCPU]; ok {
			total.CPUMillicores += cpu.MilliValue()
		}
		if memory, ok := node.Status.Allocatable[corev1.ResourceMemory]; ok {
			total.MemoryBytes += memory.Value()
		}
	}

	return total, nil
}

func (p *Preflight) committed(ctx context.Context, division string) (total, current Compute, err error) {
	divisions := &governv1alpha1.DivisionList{}
	if err := p.List(ctx, divisions); err != nil {
		return Compute{}, Compute{}, fmt.Errorf("list divisions: %w", err)
	}

	for i := range divisions.Items {
		item := &divisions.Items[i]
		quota := quotaOf(item.Spec.Quota)

		total.CPUMillicores += quota.CPUMillicores
		total.MemoryBytes += quota.MemoryBytes

		if item.Name == division {
			current = quota
		}
	}

	return total, current, nil
}

func percentOf(part, whole int64) int32 {
	if whole <= 0 {
		return 0
	}

	value := math.Round(float64(part) / float64(whole) * 100)
	if value > math.MaxInt32 {
		return math.MaxInt32
	}

	return int32(value)
}

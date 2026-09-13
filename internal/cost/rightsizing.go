package cost

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

const (
	RightSizingWindow = 14 * 24 * time.Hour

	headroom           = 1.25
	minimumCPUMilli    = 50
	minimumMemoryBytes = 64 * 1024 * 1024
)

var ErrNoWorkloadUsage = errors.New("no per-workload usage was observed in the window, so no request can be questioned")

type Vector interface {
	Vector(ctx context.Context, query string) ([]metrics.Sample, error)
}

type Observed struct {
	Namespace     string
	Name          string
	CPUMillicores int64
	MemoryBytes   int64
}

type Proposal struct {
	WorkloadRequest
	ObservedCPUMillicores int64
	ObservedMemoryBytes   int64
	ProposedCPUMillicores int64
	ProposedMemoryBytes   int64
	MonthlySaving         Money
}

func (p Proposal) Worthwhile() bool {
	return p.ProposedCPUMillicores < p.CPUMillicores || p.ProposedMemoryBytes < p.MemoryBytes
}

func (p Proposal) Patch() string {
	return fmt.Sprintf(
		`{"spec":{"template":{"spec":{"containers":[{"name":%q,"resources":{"requests":{"cpu":"%dm","memory":"%dMi"}}}]}}}}`,
		p.Name, p.ProposedCPUMillicores, p.ProposedMemoryBytes/(1024*1024))
}

func (r *Reader) ObservedByWorkload(
	ctx context.Context,
	namespaces []string,
	window time.Duration,
) (map[string]Observed, error) {
	if !r.Available() {
		return nil, ErrNoUsageSource
	}
	if len(namespaces) == 0 {
		return map[string]Observed{}, nil
	}

	selector := namespaceSelector(namespaces)
	step := promWindow(window)

	cpu, err := r.quantiles(ctx, fmt.Sprintf(
		`quantile_over_time(0.95, sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{%s,container!=""}[5m]))[%s:5m])`,
		selector, step))
	if err != nil {
		return nil, err
	}

	memory, err := r.quantiles(ctx, fmt.Sprintf(
		`quantile_over_time(0.95, sum by (namespace, pod) (container_memory_working_set_bytes{%s,container!=""})[%s:5m])`,
		selector, step))
	if err != nil {
		return nil, err
	}

	if len(cpu) == 0 && len(memory) == 0 {
		return nil, ErrNoWorkloadUsage
	}

	out := map[string]Observed{}
	for key, cores := range cpu {
		observed := out[key]
		observed.Namespace, observed.Name = split(key)
		observed.CPUMillicores = int64(cores * 1000)
		out[key] = observed
	}
	for key, bytes := range memory {
		observed := out[key]
		observed.Namespace, observed.Name = split(key)
		observed.MemoryBytes = int64(bytes)
		out[key] = observed
	}

	return out, nil
}

func (r *Reader) quantiles(ctx context.Context, query string) (map[string]float64, error) {
	vector, ok := r.source.(Vector)
	if !ok {
		return nil, ErrNoUsageSource
	}

	samples, err := vector.Vector(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read per-workload usage: %w", err)
	}

	out := map[string]float64{}
	for _, sample := range samples {
		namespace := sample.Labels["namespace"]
		pod := sample.Labels["pod"]
		if namespace == "" || pod == "" {
			continue
		}

		name := workloadOf(pod)
		key := namespace + "/" + name

		if sample.Value > out[key] {
			out[key] = sample.Value
		}
	}

	return out, nil
}

func Propose(requested []WorkloadRequest, observed map[string]Observed, policy Policy) []Proposal {
	hourlyCPU := policy.CPUCore.Times(big.NewRat(1, hoursPerMonth))
	hourlyMemory := policy.MemoryGi.Times(big.NewRat(1, hoursPerMonth))
	month := big.NewRat(hoursPerMonth, 1)

	out := []Proposal{}

	for _, workload := range requested {
		seen, found := observed[workload.Namespace+"/"+workload.Name]
		if !found {
			continue
		}

		proposal := Proposal{
			WorkloadRequest:       workload,
			ObservedCPUMillicores: seen.CPUMillicores,
			ObservedMemoryBytes:   seen.MemoryBytes,
			ProposedCPUMillicores: propose(seen.CPUMillicores, workload.CPUMillicores, minimumCPUMilli),
			ProposedMemoryBytes:   propose(seen.MemoryBytes, workload.MemoryBytes, minimumMemoryBytes),
		}

		if !proposal.Worthwhile() {
			continue
		}

		freedCores := Ratio(workload.CPUMillicores-proposal.ProposedCPUMillicores, 1000)
		freedMemory := Ratio(workload.MemoryBytes-proposal.ProposedMemoryBytes, gibibyte)

		proposal.MonthlySaving = hourlyCPU.Times(new(big.Rat).Mul(freedCores, month)).
			Add(hourlyMemory.Times(new(big.Rat).Mul(freedMemory, month)))

		out = append(out, proposal)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].MonthlySaving.Rat().Cmp(out[j].MonthlySaving.Rat()) > 0
	})

	return out
}

func propose(observed, requested, floor int64) int64 {
	if observed <= 0 {
		return requested
	}

	proposed := int64(float64(observed) * headroom)
	if proposed < floor {
		proposed = floor
	}
	if proposed > requested {
		return requested
	}

	return proposed
}

func workloadOf(pod string) string {
	parts := strings.Split(pod, "-")
	if len(parts) < 3 {
		return pod
	}

	return strings.Join(parts[:len(parts)-2], "-")
}

func split(key string) (string, string) {
	namespace, name, _ := strings.Cut(key, "/")

	return namespace, name
}

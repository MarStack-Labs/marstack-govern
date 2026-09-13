package cost

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	gibibyte      = 1024 * 1024 * 1024
	hoursPerMonth = 730
)

type Scalar interface {
	Scalar(ctx context.Context, query string) (float64, bool, error)
	Available() bool
}

type Usage struct {
	CPUCoreHoursRequested  *big.Rat
	CPUCoreHoursUsed       *big.Rat
	MemoryGiHoursRequested *big.Rat
	MemoryGiHoursUsed      *big.Rat
	StorageGiHours         *big.Rat
	LoadBalancerHours      *big.Rat
	Window                 time.Duration
	Observed               bool
}

type Reader struct {
	source Scalar
}

func NewReader(source Scalar) *Reader {
	return &Reader{source: source}
}

func (r *Reader) Available() bool {
	return r != nil && r.source != nil && r.source.Available()
}

func (r *Reader) Usage(ctx context.Context, namespaces []string, window time.Duration) (Usage, error) {
	if !r.Available() {
		return Usage{}, ErrNoUsageSource
	}
	if len(namespaces) == 0 {
		return Usage{Window: window}, nil
	}

	selector := namespaceSelector(namespaces)
	step := promWindow(window)

	queries := map[string]string{
		"cpuRequested": fmt.Sprintf(
			`sum_over_time(sum(kube_pod_container_resource_requests{%s,resource="cpu"})[%s:1h])`, selector, step),
		"cpuUsed": fmt.Sprintf(
			`sum_over_time(sum(rate(container_cpu_usage_seconds_total{%s,container!=""}[5m]))[%s:1h])`, selector, step),
		"memoryRequested": fmt.Sprintf(
			`sum_over_time(sum(kube_pod_container_resource_requests{%s,resource="memory"})[%s:1h])`, selector, step),
		"memoryUsed": fmt.Sprintf(
			`sum_over_time(sum(container_memory_working_set_bytes{%s,container!=""})[%s:1h])`, selector, step),
		"storage": fmt.Sprintf(
			`sum_over_time(sum(kube_persistentvolumeclaim_resource_requests_storage_bytes{%s})[%s:1h])`, selector, step),
		"loadBalancers": fmt.Sprintf(
			`sum_over_time(count(kube_service_spec_type{%s,type="LoadBalancer"})[%s:1h])`, selector, step),
	}

	values := map[string]float64{}
	observed := false

	for name, query := range queries {
		value, found, err := r.source.Scalar(ctx, query)
		if err != nil {
			return Usage{}, fmt.Errorf("read %s: %w", name, err)
		}
		if found {
			observed = true
			values[name] = value
		}
	}

	return Usage{
		CPUCoreHoursRequested:  FromFloat(values["cpuRequested"]),
		CPUCoreHoursUsed:       FromFloat(values["cpuUsed"]),
		MemoryGiHoursRequested: bytesToGi(values["memoryRequested"]),
		MemoryGiHoursUsed:      bytesToGi(values["memoryUsed"]),
		StorageGiHours:         bytesToGi(values["storage"]),
		LoadBalancerHours:      FromFloat(values["loadBalancers"]),
		Window:                 window,
		Observed:               observed,
	}, nil
}

func bytesToGi(value float64) *big.Rat {
	return new(big.Rat).Quo(FromFloat(value), new(big.Rat).SetInt64(gibibyte))
}

func namespaceSelector(namespaces []string) string {
	escaped := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		escaped = append(escaped, strings.NewReplacer(`\`, `\\`, `|`, `\|`, `.`, `\.`).Replace(namespace))
	}

	return fmt.Sprintf(`namespace=~"%s"`, strings.Join(escaped, "|"))
}

func promWindow(window time.Duration) string {
	hours := int(window.Hours())
	if hours <= 0 {
		hours = 1
	}
	if hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}

	return fmt.Sprintf("%dh", hours)
}

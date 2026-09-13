package requests

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

const (
	DefaultWindow   = 30 * 24 * time.Hour
	trendWindow     = 7 * 24 * time.Hour
	defaultHeadroom = 0.30
	cpuStepMillis   = 500
	memoryStepBytes = 512 * 1024 * 1024
)

var ErrNoUsage = errors.New("the metrics source reports no usage for this division")

type Samples struct {
	CPUCurrentMillicores int64
	CPUp95Millicores     int64
	CPUp99Millicores     int64
	MemoryCurrentBytes   int64
	Memoryp95Bytes       int64
	Memoryp99Bytes       int64
	CPUSlopePerSecond    float64
	MemorySlopePerSecond float64
}

type Recommendation struct {
	Current         Compute
	ObservedP95     Compute
	ObservedP99     Compute
	Proposed        Compute
	Window          string
	Basis           string
	HeadroomPercent int32
	ExhaustionAt    *time.Time
}

type Compute struct {
	CPUMillicores int64
	MemoryBytes   int64
	StorageBytes  int64
	Pods          int32
}

type Scalar interface {
	Scalar(ctx context.Context, query string) (float64, bool, error)
	Available() bool
}

type Recommender struct {
	source   Scalar
	window   time.Duration
	headroom float64
	now      func() time.Time
}

func NewRecommender(source Scalar, window time.Duration) *Recommender {
	if window <= 0 {
		window = DefaultWindow
	}

	return &Recommender{
		source:   source,
		window:   window,
		headroom: defaultHeadroom,
		now:      time.Now,
	}
}

func (r *Recommender) Available() bool {
	return r.source != nil && r.source.Available()
}

func (r *Recommender) Recommend(ctx context.Context, namespaces []string, current Compute) (Recommendation, error) {
	if !r.Available() {
		return Recommendation{}, metrics.ErrUnavailable
	}
	if len(namespaces) == 0 {
		return Recommendation{}, ErrNoUsage
	}

	samples, err := r.sample(ctx, namespaces)
	if err != nil {
		return Recommendation{}, err
	}

	proposed := Compute{
		CPUMillicores: roundUpTo(withHeadroom(samples.CPUp99Millicores, r.headroom), cpuStepMillis),
		MemoryBytes:   roundUpTo(withHeadroom(samples.Memoryp99Bytes, r.headroom), memoryStepBytes),
		StorageBytes:  current.StorageBytes,
		Pods:          current.Pods,
	}

	if proposed.CPUMillicores < current.CPUMillicores {
		proposed.CPUMillicores = current.CPUMillicores
	}
	if proposed.MemoryBytes < current.MemoryBytes {
		proposed.MemoryBytes = current.MemoryBytes
	}

	recommendation := Recommendation{
		Current: Compute{
			CPUMillicores: samples.CPUCurrentMillicores,
			MemoryBytes:   samples.MemoryCurrentBytes,
		},
		ObservedP95: Compute{
			CPUMillicores: samples.CPUp95Millicores,
			MemoryBytes:   samples.Memoryp95Bytes,
		},
		ObservedP99: Compute{
			CPUMillicores: samples.CPUp99Millicores,
			MemoryBytes:   samples.Memoryp99Bytes,
		},
		Proposed:        proposed,
		Window:          r.window.String(),
		HeadroomPercent: int32(math.Round(r.headroom * 100)),
		Basis: fmt.Sprintf("p99 over %s plus %d%% headroom, rounded up to %dm cpu and %dMi memory",
			r.window.String(), int(math.Round(r.headroom*100)), cpuStepMillis, memoryStepBytes/1024/1024),
	}

	recommendation.ExhaustionAt = r.exhaustion(samples, current)

	return recommendation, nil
}

func (r *Recommender) sample(ctx context.Context, namespaces []string) (Samples, error) {
	selector := namespaceSelector(namespaces)
	window := promDuration(r.window)
	trend := promDuration(trendWindow)

	cpuSeries := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s,container!=""}[5m]))`, selector)
	memorySeries := fmt.Sprintf(`sum(container_memory_working_set_bytes{%s,container!=""})`, selector)

	queries := map[string]string{
		"cpuCurrent": cpuSeries,
		"cpuP95":     fmt.Sprintf(`quantile_over_time(0.95, %s[%s:5m])`, cpuSeries, window),
		"cpuP99":     fmt.Sprintf(`quantile_over_time(0.99, %s[%s:5m])`, cpuSeries, window),
		"cpuSlope":   fmt.Sprintf(`deriv(%s[%s:1h])`, cpuSeries, trend),
		"memCurrent": memorySeries,
		"memP95":     fmt.Sprintf(`quantile_over_time(0.95, %s[%s:5m])`, memorySeries, window),
		"memP99":     fmt.Sprintf(`quantile_over_time(0.99, %s[%s:5m])`, memorySeries, window),
		"memSlope":   fmt.Sprintf(`deriv(%s[%s:1h])`, memorySeries, trend),
	}

	values := map[string]float64{}
	seen := false

	for name, query := range queries {
		value, found, err := r.source.Scalar(ctx, query)
		if err != nil {
			return Samples{}, fmt.Errorf("sample %s: %w", name, err)
		}
		if found {
			seen = true
			values[name] = value
		}
	}

	if !seen {
		return Samples{}, ErrNoUsage
	}

	return Samples{
		CPUCurrentMillicores: cores(values["cpuCurrent"]),
		CPUp95Millicores:     cores(values["cpuP95"]),
		CPUp99Millicores:     cores(values["cpuP99"]),
		MemoryCurrentBytes:   int64(values["memCurrent"]),
		Memoryp95Bytes:       int64(values["memP95"]),
		Memoryp99Bytes:       int64(values["memP99"]),
		CPUSlopePerSecond:    values["cpuSlope"],
		MemorySlopePerSecond: values["memSlope"],
	}, nil
}

func (r *Recommender) exhaustion(samples Samples, current Compute) *time.Time {
	soonest := time.Time{}

	if seconds, ok := secondsUntil(float64(current.CPUMillicores-samples.CPUCurrentMillicores)/1000, samples.CPUSlopePerSecond); ok {
		soonest = r.now().Add(time.Duration(seconds) * time.Second)
	}

	if seconds, ok := secondsUntil(float64(current.MemoryBytes-samples.MemoryCurrentBytes), samples.MemorySlopePerSecond); ok {
		candidate := r.now().Add(time.Duration(seconds) * time.Second)
		if soonest.IsZero() || candidate.Before(soonest) {
			soonest = candidate
		}
	}

	if soonest.IsZero() {
		return nil
	}

	return &soonest
}

func secondsUntil(headroom, slopePerSecond float64) (float64, bool) {
	if slopePerSecond <= 0 || headroom <= 0 {
		return 0, false
	}

	seconds := headroom / slopePerSecond
	if math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds > float64(365*24*3600) {
		return 0, false
	}

	return seconds, true
}

func namespaceSelector(namespaces []string) string {
	escaped := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		escaped = append(escaped, regexpEscape(namespace))
	}

	return fmt.Sprintf(`namespace=~"%s"`, strings.Join(escaped, "|"))
}

func regexpEscape(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `.`, `\.`, `+`, `\+`, `*`, `\*`, `?`, `\?`,
		`(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`,
		`^`, `\^`, `$`, `\$`, `|`, `\|`,
	)

	return replacer.Replace(value)
}

func promDuration(window time.Duration) string {
	hours := int(window.Hours())
	if hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}

	return fmt.Sprintf("%dh", hours)
}

func cores(value float64) int64 {
	return int64(math.Round(value * 1000))
}

func withHeadroom(value int64, headroom float64) int64 {
	return int64(math.Ceil(float64(value) * (1 + headroom)))
}

func roundUpTo(value, step int64) int64 {
	if value <= 0 {
		return step
	}

	remainder := value % step
	if remainder == 0 {
		return value
	}

	return value + step - remainder
}

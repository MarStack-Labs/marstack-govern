package diagnostics

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultWindow     = 15 * time.Minute
	settleBeforeStart = time.Minute
)

type Metrics interface {
	ScalarAt(ctx context.Context, query string, at time.Time) (float64, bool, error)
	Available() bool
}

type Regression struct {
	Revision         string
	At               time.Time
	Image            string
	LatencyBefore    float64
	LatencyAfter     float64
	ErrorRatioBefore float64
	ErrorRatioAfter  float64
	Observed         bool
}

func (r Regression) Worse() bool {
	if !r.Observed {
		return false
	}

	return r.LatencyAfter > r.LatencyBefore*1.2 || r.ErrorRatioAfter > r.ErrorRatioBefore+0.005
}

type Correlator struct {
	Metrics       Metrics
	Window        time.Duration
	LatencyMetric string
	RequestMetric string
	ErrorSelector string
}

func (c *Correlator) Around(ctx context.Context, namespace, workload string, rollout Rollout) (Regression, error) {
	regression := Regression{Revision: rollout.Revision, At: rollout.At, Image: rollout.Image}

	if c.Metrics == nil || !c.Metrics.Available() || rollout.At.IsZero() {
		return regression, nil
	}

	window := c.Window
	if window <= 0 {
		window = defaultWindow
	}

	before := rollout.At.Add(-settleBeforeStart)
	after := rollout.At.Add(window)
	if after.After(time.Now()) {
		after = time.Now()
	}

	latency := c.latencyQuery(namespace, workload, window)
	errors := c.errorQuery(namespace, workload, window)

	samples := []struct {
		query  string
		at     time.Time
		target *float64
	}{
		{latency, before, &regression.LatencyBefore},
		{latency, after, &regression.LatencyAfter},
		{errors, before, &regression.ErrorRatioBefore},
		{errors, after, &regression.ErrorRatioAfter},
	}

	for _, sample := range samples {
		value, found, err := c.Metrics.ScalarAt(ctx, sample.query, sample.at)
		if err != nil {
			return regression, fmt.Errorf("read the series around rollout %s: %w", rollout.Revision, err)
		}
		if found {
			regression.Observed = true
			*sample.target = value
		}
	}

	return regression, nil
}

func (c *Correlator) latencyQuery(namespace, workload string, window time.Duration) string {
	metric := c.LatencyMetric
	if metric == "" {
		metric = "http_server_request_duration_seconds_bucket"
	}

	return fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(%s{namespace="%s",service=~"%s.*"}[%s])) by (le))`,
		metric, namespace, workload, promDuration(window))
}

func (c *Correlator) errorQuery(namespace, workload string, window time.Duration) string {
	metric := c.RequestMetric
	if metric == "" {
		metric = "http_server_request_duration_seconds_count"
	}

	selector := c.ErrorSelector
	if selector == "" {
		selector = `http_response_status_code=~"5.."`
	}

	return fmt.Sprintf(
		`sum(rate(%s{namespace="%s",service=~"%s.*",%s}[%s])) / clamp_min(sum(rate(%s{namespace="%s",service=~"%s.*"}[%s])), 0.001)`,
		metric, namespace, workload, selector, promDuration(window),
		metric, namespace, workload, promDuration(window))
}

func promDuration(window time.Duration) string {
	minutes := int(window.Minutes())
	if minutes <= 0 {
		minutes = 1
	}

	return fmt.Sprintf("%dm", minutes)
}

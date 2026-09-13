package cost_test

import (
	"context"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/cost"
)

func TestTheResolutionFollowsTheWindow(t *testing.T) {
	for _, item := range []struct {
		window time.Duration
		want   time.Duration
	}{
		{30 * 24 * time.Hour, time.Hour},
		{7 * 24 * time.Hour, time.Hour},
		{6 * time.Hour, 6 * time.Minute},
		{time.Hour, time.Minute},
		{10 * time.Minute, time.Minute},
	} {
		if got := cost.Resolution(item.window); got != item.want {
			t.Errorf("%s: got %s, want %s", item.window, got, item.want)
		}
	}
}

func TestCoreHoursAreTheSameHoweverOftenPrometheusIsSampled(t *testing.T) {
	const cores = 4.0

	for _, window := range []time.Duration{
		30 * 24 * time.Hour,
		24 * time.Hour,
		6 * time.Hour,
		time.Hour,
	} {
		resolution := cost.Resolution(window)
		samples := float64(window) / float64(resolution)

		reader := cost.NewReader(constantScalar{value: cores * samples})

		usage, err := reader.Usage(t.Context(), []string{"payments-dev"}, window)
		if err != nil {
			t.Fatalf("%s: %v", window, err)
		}

		want := cores * window.Hours()
		got := usage.CPUCoreHoursRequested

		if difference(got, want) > 0.01 {
			t.Errorf("%s sampled every %s: got %s core-hours, want %.2f",
				window, resolution, got.FloatString(2), want)
		}
	}
}

func difference(got *big.Rat, want float64) float64 {
	value, _ := got.Float64()

	return math.Abs(value - want)
}

type constantScalar struct {
	value float64
}

func (c constantScalar) Available() bool { return true }

func (c constantScalar) Scalar(context.Context, string) (float64, bool, error) {
	return c.value, true, nil
}

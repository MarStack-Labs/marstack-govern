package environments

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

const (
	DefaultTTL = 48 * time.Hour
	MaxTTL     = 7 * 24 * time.Hour
)

type Lease struct {
	StartedAt time.Time
	ExpiresAt time.Time
	Granted   time.Duration
	Clamped   bool
}

func (l Lease) Expired(now time.Time) bool {
	return !now.Before(l.ExpiresAt)
}

func (l Lease) Remaining(now time.Time) time.Duration {
	if l.Expired(now) {
		return 0
	}

	return l.ExpiresAt.Sub(now)
}

func LeaseOf(environment *governv1alpha1.EphemeralEnvironment) (Lease, error) {
	requested, err := parseTTL(environment.Spec.TTL, DefaultTTL)
	if err != nil {
		return Lease{}, fmt.Errorf("spec.ttl: %w", err)
	}

	granted := requested

	for _, renewal := range environment.Spec.Renewals {
		extension, err := parseTTL(renewal.Extend, 0)
		if err != nil {
			return Lease{}, fmt.Errorf("renewal granted by %s: %w", renewal.GrantedBy, err)
		}

		granted += extension
	}

	clamped := false
	if granted > MaxTTL {
		granted = MaxTTL
		clamped = true
	}

	started := environment.CreationTimestamp.Time
	if environment.Status.LeaseStartedAt != nil {
		started = environment.Status.LeaseStartedAt.Time
	}

	return Lease{
		StartedAt: started,
		ExpiresAt: started.Add(granted),
		Granted:   granted,
		Clamped:   clamped,
	}, nil
}

func FormatTTL(value time.Duration) string {
	minutes := int(value.Round(time.Minute).Minutes())
	if minutes <= 0 {
		minutes = 1
	}

	hours := minutes / 60
	minutes -= hours * 60

	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func parseTTL(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration", value)
	}

	if parsed <= 0 {
		return 0, fmt.Errorf("%q is not a lease anyone can use", value)
	}

	return parsed, nil
}

func stamp(at time.Time) *metav1.Time {
	value := metav1.NewTime(at)

	return &value
}

package api

import (
	"context"
	"testing"
	"time"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

func TestHubAssignsMonotonicCursors(t *testing.T) {
	hub := NewHub(8)

	for range 3 {
		hub.Publish(&governv1.StreamEvent{Type: governv1.StreamEvent_TYPE_HEARTBEAT})
	}

	events, complete := hub.Subscribe(t.Context(), "0", nil)
	if !complete {
		t.Fatal("replay from the start of the buffer was reported incomplete")
	}

	cursors := []string{}
	for range 3 {
		select {
		case event := <-events:
			cursors = append(cursors, event.GetCursor())
		case <-time.After(time.Second):
			t.Fatalf("timed out after %v", cursors)
		}
	}

	want := []string{"1", "2", "3"}
	for i, cursor := range cursors {
		if cursor != want[i] {
			t.Fatalf("cursors: got %v, want %v", cursors, want)
		}
	}
}

func TestHubDeliversToLiveSubscribers(t *testing.T) {
	hub := NewHub(8)

	events, _ := hub.Subscribe(t.Context(), "", nil)
	hub.Publish(&governv1.StreamEvent{Type: governv1.StreamEvent_TYPE_WORKLOAD_CHANGED})

	select {
	case event := <-events:
		if event.GetType() != governv1.StreamEvent_TYPE_WORKLOAD_CHANGED {
			t.Fatalf("got %v", event.GetType())
		}
	case <-time.After(time.Second):
		t.Fatal("the subscriber received nothing")
	}
}

func TestHubReportsAnUnreachableCursor(t *testing.T) {
	hub := NewHub(2)

	for range 5 {
		hub.Publish(&governv1.StreamEvent{Type: governv1.StreamEvent_TYPE_HEARTBEAT})
	}

	if _, complete := hub.Subscribe(t.Context(), "1", nil); complete {
		t.Fatal("a cursor older than the replay buffer was reported as replayable")
	}

	if _, complete := hub.Subscribe(t.Context(), "4", nil); !complete {
		t.Fatal("a cursor inside the replay buffer was reported as unreachable")
	}
}

func TestHubDropsSubscribersOnCancel(t *testing.T) {
	hub := NewHub(4)

	ctx, cancel := context.WithCancel(t.Context())
	_, _ = hub.Subscribe(ctx, "", nil)

	if hub.Subscribers() != 1 {
		t.Fatalf("got %d subscribers, want 1", hub.Subscribers())
	}

	cancel()

	deadline := time.After(2 * time.Second)
	for hub.Subscribers() != 0 {
		select {
		case <-deadline:
			t.Fatal("the subscriber was never released")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSubscribersOnlySeeWhatTheirFilterAllows(t *testing.T) {
	hub := NewHub(8)

	allowed := func(event *governv1.StreamEvent) bool {
		changed, ok := event.GetBody().(*governv1.StreamEvent_WorkloadChanged)
		if !ok {
			return true
		}
		return changed.WorkloadChanged.GetWorkload().GetNamespace() == "payments-dev"
	}

	events, _ := hub.Subscribe(t.Context(), "", allowed)

	for _, namespace := range []string{"erp-dev", "payments-dev"} {
		hub.Publish(&governv1.StreamEvent{
			Type: governv1.StreamEvent_TYPE_WORKLOAD_CHANGED,
			Body: &governv1.StreamEvent_WorkloadChanged{
				WorkloadChanged: &governv1.WorkloadChanged{
					Workload: &governv1.Workload{Namespace: namespace, Name: "api"},
				},
			},
		})
	}

	select {
	case event := <-events:
		changed := event.GetBody().(*governv1.StreamEvent_WorkloadChanged)
		if got := changed.WorkloadChanged.GetWorkload().GetNamespace(); got != "payments-dev" {
			t.Fatalf("a filtered subscriber received %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the allowed event never arrived")
	}

	select {
	case event := <-events:
		t.Fatalf("an event outside the scope leaked through: %v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

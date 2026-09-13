package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type eventStream struct {
	hub       *Hub
	logger    *slog.Logger
	heartbeat time.Duration
	sessions  *identity.Service
	guarded   bool
}

func (s *eventStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}

	actor, signedIn := identity.FromContext(r.Context())
	if s.guarded && !signedIn {
		http.Error(w, "sign in at /auth/login", http.StatusUnauthorized)
		return
	}

	allow, err := s.filterFor(r.Context(), actor, signedIn)
	if err != nil {
		s.logger.Error("scope the event stream", "error", err)
		http.Error(w, "cannot decide what you may see", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	from := r.Header.Get("Last-Event-ID")
	if from == "" {
		from = r.URL.Query().Get("cursor")
	}

	stream, complete := s.hub.Subscribe(r.Context(), from, allow)

	if !complete {
		if err := writeEvent(w, "resync", "", `{"reason":"the requested cursor is older than the replay buffer"}`); err != nil {
			return
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()

	started := time.Now()

	for {
		select {
		case <-r.Context().Done():
			return

		case event, open := <-stream:
			if !open {
				return
			}

			payload, err := protojson.Marshal(event)
			if err != nil {
				s.logger.Error("encode stream event", "error", err)
				continue
			}

			if err := writeEvent(w, eventName(event.GetType()), event.GetCursor(), string(payload)); err != nil {
				return
			}
			flusher.Flush()

		case <-ticker.C:
			beat := &governv1.StreamEvent{
				Type:      governv1.StreamEvent_TYPE_HEARTBEAT,
				EmittedAt: timestamppb.New(time.Now()),
				Body: &governv1.StreamEvent_Heartbeat{
					Heartbeat: &governv1.Heartbeat{ConnectedSeconds: int64(time.Since(started).Seconds())},
				},
			}

			payload, err := protojson.Marshal(beat)
			if err != nil {
				continue
			}

			if err := writeEvent(w, "heartbeat", "", string(payload)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *eventStream) filterFor(ctx context.Context, actor identity.Actor, signedIn bool) (Filter, error) {
	if s.sessions == nil || !signedIn {
		return nil, nil
	}

	scope, err := s.sessions.Scope(ctx, actor)
	if err != nil {
		return nil, err
	}

	if scope.AllowAll {
		return nil, nil
	}

	return func(event *governv1.StreamEvent) bool {
		switch body := event.GetBody().(type) {
		case *governv1.StreamEvent_WorkloadChanged:
			return scope.Allows(body.WorkloadChanged.GetWorkload().GetNamespace())
		case *governv1.StreamEvent_DivisionChanged:
			for _, namespace := range body.DivisionChanged.GetDivision().GetNamespaces() {
				if scope.Allows(namespace) {
					return true
				}
			}
			return false
		default:
			return true
		}
	}, nil
}

func writeEvent(w http.ResponseWriter, name, id, data string) error {
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	return err
}

func eventName(eventType governv1.StreamEvent_Type) string {
	switch eventType {
	case governv1.StreamEvent_TYPE_WORKLOAD_CHANGED:
		return "workload_changed"
	case governv1.StreamEvent_TYPE_DIVISION_CHANGED:
		return "division_changed"
	case governv1.StreamEvent_TYPE_REQUEST_CHANGED:
		return "request_changed"
	case governv1.StreamEvent_TYPE_DECISION_MADE:
		return "decision_made"
	case governv1.StreamEvent_TYPE_PROJECTION_DEGRADED:
		return "projection_degraded"
	case governv1.StreamEvent_TYPE_HEARTBEAT:
		return "heartbeat"
	default:
		return "message"
	}
}

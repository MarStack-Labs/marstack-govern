package catalog

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

type Publisher interface {
	Publish(event *governv1.StreamEvent)
}

type Projector struct {
	store     *Store
	events    <-chan kube.WorkloadEvent
	publisher Publisher
	logger    *slog.Logger
	cursor    string
}

func NewProjector(store *Store, events <-chan kube.WorkloadEvent, publisher Publisher, logger *slog.Logger) *Projector {
	if logger == nil {
		logger = slog.Default()
	}

	return &Projector{
		store:     store,
		events:    events,
		publisher: publisher,
		logger:    logger,
	}
}

func (p *Projector) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, open := <-p.events:
			if !open {
				return nil
			}
			p.handle(ctx, event)
		}
	}
}

func (p *Projector) handle(ctx context.Context, event kube.WorkloadEvent) {
	var err error

	switch event.Type {
	case kube.EventRemoved:
		err = p.store.DeleteWorkload(ctx, event.Workload.UID)
	default:
		err = p.store.UpsertWorkload(ctx, event.Workload)
	}

	if err != nil {
		p.logger.Error("project workload",
			"namespace", event.Workload.Namespace,
			"name", event.Workload.Name,
			"error", err)

		if markErr := p.store.MarkSourceDegraded(ctx, SourceKubernetes, err.Error()); markErr != nil {
			p.logger.Error("mark source degraded", "error", markErr)
		}
		return
	}

	if event.Workload.ResourceVersion > p.cursor {
		p.cursor = event.Workload.ResourceVersion
	}

	if err := p.store.MarkSourceHealthy(ctx, SourceKubernetes, p.cursor, event.At); err != nil {
		p.logger.Error("record projection cursor", "error", err)
	}

	p.publish(event)
}

func (p *Projector) publish(event kube.WorkloadEvent) {
	if p.publisher == nil {
		return
	}

	p.publisher.Publish(&governv1.StreamEvent{
		Type:      governv1.StreamEvent_TYPE_WORKLOAD_CHANGED,
		EmittedAt: timestamppb.New(time.Now()),
		Freshness: &governv1.Freshness{ResourceVersion: p.cursor, ObservedAt: timestamppb.New(event.At)},
		Body: &governv1.StreamEvent_WorkloadChanged{
			WorkloadChanged: &governv1.WorkloadChanged{
				Change:   changeOf(event.Type),
				Workload: protoWorkload(event.Workload),
			},
		},
	})
}

func changeOf(eventType kube.EventType) governv1.WorkloadChanged_Change {
	switch eventType {
	case kube.EventAdded:
		return governv1.WorkloadChanged_CHANGE_ADDED
	case kube.EventRemoved:
		return governv1.WorkloadChanged_CHANGE_REMOVED
	default:
		return governv1.WorkloadChanged_CHANGE_UPDATED
	}
}

func protoWorkload(workload kube.Workload) *governv1.Workload {
	requested := &governv1.Compute{}
	for _, container := range workload.Containers {
		requested.CpuMillicores += container.CPURequestMillicores
		requested.MemoryBytes += container.MemoryRequestBytes
	}

	return &governv1.Workload{
		Uid:             workload.UID,
		Division:        workload.Division,
		Namespace:       workload.Namespace,
		Name:            workload.Name,
		Kind:            workload.Kind,
		Health:          protoHealth(string(workload.Health)),
		ReplicasDesired: workload.ReplicasDesired,
		ReplicasReady:   workload.ReplicasReady,
		ImageRef:        workload.PrimaryImage(),
		Requested:       requested,
		CreatedAt:       timestamppb.New(workload.CreatedAt),
	}
}

func protoHealth(health string) governv1.Workload_Health {
	switch health {
	case string(kube.HealthHealthy):
		return governv1.Workload_HEALTH_HEALTHY
	case string(kube.HealthProgressing):
		return governv1.Workload_HEALTH_PROGRESSING
	case string(kube.HealthDegraded):
		return governv1.Workload_HEALTH_DEGRADED
	default:
		return governv1.Workload_HEALTH_UNSPECIFIED
	}
}

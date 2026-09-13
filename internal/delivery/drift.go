package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/audit"
)

type Mutations interface {
	List(ctx context.Context, query audit.Query) ([]audit.Record, error)
}

type Drift struct {
	argo      *Argo
	mutations Mutations
}

func NewDrift(argo *Argo, mutations Mutations) *Drift {
	return &Drift{argo: argo, mutations: mutations}
}

func (d *Drift) Of(ctx context.Context, namespace, kind, name string) (*governv1.GetDriftResponse, error) {
	application, found, err := d.argo.ForWorkload(ctx, namespace, kind, name)
	if errors.Is(err, ErrArgoAbsent) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fmt.Errorf("%s/%s is not managed by argo cd, so there is no desired state to compare against",
			namespace, name)
	}

	response := &governv1.GetDriftResponse{
		Application: fmt.Sprintf("%s (%s)", application.Name, revisionOf(application)),
	}

	for _, resource := range application.Resources {
		if !resource.Drifted() {
			continue
		}

		response.Drifted = true

		field := &governv1.DriftField{
			Path:    strings.TrimPrefix(resource.Group+"/", "/") + resource.Kind + "/" + resource.Name,
			Desired: "Synced",
			Live:    resource.Status,
		}

		if actor, at, ok := d.lastMutation(ctx, resource); ok {
			field.ChangedBy = actor
			field.ChangedAt = timestamppb.New(at)
		}

		response.Fields = append(response.Fields, field)
	}

	if !response.Drifted && application.SyncStatus != "" && application.SyncStatus != "Synced" {
		response.Drifted = true
		response.Fields = append(response.Fields, &governv1.DriftField{
			Path:    "status.sync.status",
			Desired: "Synced",
			Live:    application.SyncStatus,
		})
	}

	return response, nil
}

func (d *Drift) lastMutation(ctx context.Context, resource Resource) (string, time.Time, bool) {
	if d.mutations == nil {
		return "", time.Time{}, false
	}

	records, err := d.mutations.List(ctx, audit.Query{
		ObjectName: resource.Name,
		Namespace:  resource.Namespace,
		Limit:      20,
	})
	if err != nil || len(records) == 0 {
		return "", time.Time{}, false
	}

	for _, record := range records {
		switch record.Verb {
		case "update", "patch", "delete", "create":
			return record.Actor, record.EventAt, true
		}
	}

	return "", time.Time{}, false
}

func short(revision string) string {
	if len(revision) <= 12 {
		return revision
	}

	return revision[:12]
}

func revisionOf(application Application) string {
	if application.SyncedRevision == "" {
		return application.TargetRevision
	}

	return fmt.Sprintf("%s at %s", application.TargetRevision, short(application.SyncedRevision))
}

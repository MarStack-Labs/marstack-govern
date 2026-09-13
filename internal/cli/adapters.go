package cli

import (
	"context"
	"fmt"
	"sort"

	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/diagnostics"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/requests"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

type divisionAccess struct {
	store *tenancy.Store
}

func (d divisionAccess) DivisionAccess(ctx context.Context) ([]identity.DivisionAccess, error) {
	access, err := d.store.ListAccess(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]identity.DivisionAccess, 0, len(access))
	for _, entry := range access {
		converted := identity.DivisionAccess{
			Division:    entry.Division,
			DisplayName: entry.DisplayName,
			Namespaces:  entry.Namespaces,
		}
		for _, grant := range entry.Grants {
			converted.Grants = append(converted.Grants, identity.AccessGrant{
				Role:  grant.Role,
				Group: grant.Group,
			})
		}
		out = append(out, converted)
	}

	return out, nil
}

func impersonatingClients(base *rest.Config) identity.ClientFactory {
	return func(subject string, groups []string) (kubernetes.Interface, error) {
		return kube.Impersonate(base, subject, groups)
	}
}

func impersonatingRuntimeClients(base *rest.Config, scheme *runtime.Scheme) requests.ClientFactory {
	return func(actor identity.Actor) (client.Client, error) {
		return kube.ImpersonateRuntime(base, scheme, actor.Subject, actor.Groups)
	}
}

type workloadRequests struct {
	store *catalog.Store
}

func (w workloadRequests) RequestedByWorkload(ctx context.Context, namespaces []string) ([]cost.WorkloadRequest, error) {
	found, err := w.store.RequestedByWorkload(ctx, namespaces)
	if err != nil {
		return nil, err
	}

	out := make([]cost.WorkloadRequest, 0, len(found))
	for _, item := range found {
		out = append(out, cost.WorkloadRequest{
			UID:           item.UID,
			Namespace:     item.Namespace,
			Name:          item.Name,
			CPUMillicores: item.CPUMillicores,
			MemoryBytes:   item.MemoryBytes,
		})
	}

	return out, nil
}

type diagnoser struct {
	collector  *diagnostics.Collector
	correlator *diagnostics.Correlator
}

func (d diagnoser) Explain(ctx context.Context, workload catalog.Workload) (*governv1.FailureExplanation, error) {
	subject := diagnostics.Subject{
		Namespace:       workload.Namespace,
		Name:            workload.Name,
		Kind:            workload.Kind,
		ReplicasDesired: workload.ReplicasDesired,
		ReplicasReady:   workload.ReplicasReady,
	}

	pods, err := d.collector.Pods(ctx, workload.Namespace, types.UID(workload.UID))
	if err != nil {
		return nil, err
	}

	warnings, err := d.collector.Warnings(ctx, workload.Namespace, pods)
	if err != nil {
		return nil, err
	}

	explanation := diagnostics.Explain(subject, pods, warnings)

	out := &governv1.FailureExplanation{
		Cause:             explanation.Cause,
		Evidence:          explanation.Evidence,
		ReproduceCommands: explanation.Reproduce,
	}

	rollouts, err := d.collector.Rollouts(ctx, workload.Namespace, types.UID(workload.UID))
	if err != nil {
		return out, nil
	}
	if len(rollouts) == 0 {
		return out, nil
	}

	regression, err := d.correlator.Around(ctx, workload.Namespace, workload.Name, rollouts[0])
	if err != nil || !regression.Observed {
		return out, nil
	}

	out.Regression = &governv1.RegressionDelta{
		RolloutRevision:  regression.Revision,
		RolloutAt:        timestamppb.New(regression.At),
		LatencyP99Before: regression.LatencyBefore,
		LatencyP99After:  regression.LatencyAfter,
		ErrorRatioBefore: regression.ErrorRatioBefore,
		ErrorRatioAfter:  regression.ErrorRatioAfter,
	}

	return out, nil
}

func (d diagnoser) Timeline(ctx context.Context, workload catalog.Workload) ([]*governv1.TimelineEvent, error) {
	events := []*governv1.TimelineEvent{}

	rollouts, err := d.collector.Rollouts(ctx, workload.Namespace, types.UID(workload.UID))
	if err != nil {
		return nil, err
	}

	for _, rollout := range rollouts {
		events = append(events, &governv1.TimelineEvent{
			OccurredAt: timestamppb.New(rollout.At),
			Kind:       governv1.TimelineEvent_KIND_ROLLOUT,
			Source:     "kubernetes",
			Summary:    fmt.Sprintf("revision %s rolled out %s", rollout.Revision, rollout.Image),
			Detail:     map[string]string{"revision": rollout.Revision, "image": rollout.Image},
		})
	}

	pods, err := d.collector.Pods(ctx, workload.Namespace, types.UID(workload.UID))
	if err != nil {
		return nil, err
	}

	warnings, err := d.collector.Warnings(ctx, workload.Namespace, pods)
	if err != nil {
		return nil, err
	}

	for i := range warnings {
		warning := warnings[i]
		events = append(events, &governv1.TimelineEvent{
			OccurredAt: timestamppb.New(warning.LastTimestamp.Time),
			Kind:       governv1.TimelineEvent_KIND_KUBERNETES_EVENT,
			Source:     "kubernetes",
			Summary:    fmt.Sprintf("%s: %s", warning.Reason, warning.Message),
			Detail: map[string]string{
				"object": warning.InvolvedObject.Name,
				"count":  fmt.Sprintf("%d", warning.Count),
			},
		})
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].GetOccurredAt().AsTime().After(events[j].GetOccurredAt().AsTime())
	})

	return events, nil
}

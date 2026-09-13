package environments

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

type ClientFactory func(actor identity.Actor) (client.Client, error)

type Service struct {
	reader   client.Client
	asCaller ClientFactory
	now      func() time.Time
}

func NewService(reader client.Client, asCaller ClientFactory) *Service {
	return &Service{reader: reader, asCaller: asCaller, now: time.Now}
}

func (s *Service) ListEnvironments(
	ctx context.Context,
	req *connect.Request[governv1.ListEnvironmentsRequest],
) (*connect.Response[governv1.ListEnvironmentsResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	division := req.Msg.GetDivision()
	if division == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	list := &governv1alpha1.EphemeralEnvironmentList{}
	if err := s.reader.List(ctx, list); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	now := s.now()
	response := &governv1.ListEnvironmentsResponse{
		Freshness: &governv1.Freshness{
			ObservedAt:      timestamppb.New(now),
			ResourceVersion: list.ResourceVersion,
		},
	}

	for i := range list.Items {
		environment := &list.Items[i]
		if environment.Spec.Division != division {
			continue
		}
		if !req.Msg.GetIncludeReclaimed() && environment.Status.Phase == governv1alpha1.EnvironmentExpired {
			continue
		}

		response.Environments = append(response.Environments, protoEnvironment(environment, now))
	}

	sort.SliceStable(response.Environments, func(i, j int) bool {
		return response.Environments[i].GetChange().GetNumber() > response.Environments[j].GetChange().GetNumber()
	})

	return connect.NewResponse(response), nil
}

func (s *Service) RequestEnvironment(
	ctx context.Context,
	req *connect.Request[governv1.RequestEnvironmentRequest],
) (*connect.Response[governv1.RequestEnvironmentResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	change := req.Msg.GetChange()
	if change.GetRepository() == "" || change.GetNumber() <= 0 || change.GetBranch() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a preview needs the repository, the change number and the branch it previews"))
	}

	quota := req.Msg.GetQuota()
	if quota.GetCpuMillicores() <= 0 || quota.GetMemoryBytes() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a preview needs its own cpu and memory ceiling"))
	}

	ttl := strings.TrimSpace(req.Msg.GetTtl())
	if ttl == "" {
		ttl = DefaultTTL.String()
	}
	parsed, err := parseTTL(ttl, DefaultTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ttl = FormatTTL(parsed)

	name := fmt.Sprintf("%s-%d", division.Name, change.GetNumber())

	existing := &governv1alpha1.EphemeralEnvironment{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, existing); err == nil {
		return connect.NewResponse(&governv1.RequestEnvironmentResponse{
			Environment: protoEnvironment(existing, s.now()),
		}), nil
	}

	writer, err := s.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	environment := &governv1alpha1.EphemeralEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{kube.LabelDivision: division.Name},
		},
		Spec: governv1alpha1.EphemeralEnvironmentSpec{
			Division: division.Name,
			Change: governv1alpha1.ChangeRef{
				Repository: change.GetRepository(),
				Number:     change.GetNumber(),
				Branch:     change.GetBranch(),
				Revision:   change.GetRevision(),
			},
			Quota:       quotaFromProto(quota),
			TTL:         ttl,
			RequestedBy: actor.Subject,
		},
	}

	if err := writer.Create(ctx, environment); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("create the preview as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.RequestEnvironmentResponse{
		Environment: protoEnvironment(environment, s.now()),
	}), nil
}

func (s *Service) RenewEnvironment(
	ctx context.Context,
	req *connect.Request[governv1.RenewEnvironmentRequest],
) (*connect.Response[governv1.RenewEnvironmentResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	reason := strings.TrimSpace(req.Msg.GetReason())
	if len(reason) < 10 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a renewal needs a reason of at least ten characters"))
	}

	extension, err := parseTTL(strings.TrimSpace(req.Msg.GetExtend()), 0)
	if err != nil || extension <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("say how much longer the preview should live, as a duration"))
	}

	writer, err := s.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	environment := &governv1alpha1.EphemeralEnvironment{}
	if err := writer.Get(ctx, types.NamespacedName{Name: req.Msg.GetName()}, environment); err != nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("preview %s not found", req.Msg.GetName()))
	}

	if !environment.Live() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("preview %s is %s; a reclaimed namespace cannot be renewed, only requested again",
				environment.Name, strings.ToLower(string(environment.Status.Phase))))
	}

	patched := environment.DeepCopy()
	patched.Spec.Renewals = append(patched.Spec.Renewals, governv1alpha1.Renewal{
		Extend:    FormatTTL(extension),
		Reason:    reason,
		GrantedBy: actor.Subject,
		GrantedAt: metav1.NewTime(s.now()),
	})

	if err := writer.Patch(ctx, patched, client.MergeFrom(environment)); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("renew as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.RenewEnvironmentResponse{
		Environment: protoEnvironment(patched, s.now()),
	}), nil
}

func (s *Service) ReleaseEnvironment(
	ctx context.Context,
	req *connect.Request[governv1.ReleaseEnvironmentRequest],
) (*connect.Response[governv1.ReleaseEnvironmentResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	writer, err := s.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	environment := &governv1alpha1.EphemeralEnvironment{}
	if err := writer.Get(ctx, types.NamespacedName{Name: req.Msg.GetName()}, environment); err != nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("preview %s not found", req.Msg.GetName()))
	}

	if err := writer.Delete(ctx, environment); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("release as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.ReleaseEnvironmentResponse{
		Environment: protoEnvironment(environment, s.now()),
	}), nil
}

func (s *Service) division(ctx context.Context, name string) (*governv1alpha1.Division, error) {
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	division := &governv1alpha1.Division{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, division); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", name))
	}

	return division, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func protoEnvironment(
	environment *governv1alpha1.EphemeralEnvironment,
	now time.Time,
) *governv1.PreviewEnvironment {
	out := &governv1.PreviewEnvironment{
		Uid:      string(environment.UID),
		Name:     environment.Name,
		Division: environment.Spec.Division,
		Change: &governv1.Change{
			Repository: environment.Spec.Change.Repository,
			Number:     environment.Spec.Change.Number,
			Branch:     environment.Spec.Change.Branch,
			Revision:   environment.Spec.Change.Revision,
		},
		Phase:       protoPhase(environment.Status.Phase),
		Namespace:   environment.Status.Namespace,
		Quota:       protoQuota(environment.Spec.Quota),
		RequestedBy: environment.Spec.RequestedBy,
		GrantedTtl:  environment.Status.GrantedTTL,
	}

	if environment.Status.LeaseStartedAt != nil {
		out.LeaseStartedAt = timestamppb.New(environment.Status.LeaseStartedAt.Time)
	}
	if environment.Status.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(environment.Status.ExpiresAt.Time)

		remaining := environment.Status.ExpiresAt.Sub(now)
		if remaining < 0 {
			remaining = 0
		}
		out.RemainingSeconds = int64(remaining.Seconds())
	}

	for _, renewal := range environment.Spec.Renewals {
		out.Renewals = append(out.Renewals, &governv1.Renewal{
			Extend:    renewal.Extend,
			Reason:    renewal.Reason,
			GrantedBy: renewal.GrantedBy,
			GrantedAt: timestamppb.New(renewal.GrantedAt.Time),
		})
	}

	for _, condition := range environment.Status.Conditions {
		out.Conditions = append(out.Conditions, &governv1.Condition{
			Type:             condition.Type,
			Ok:               condition.Status == metav1.ConditionTrue,
			Reason:           condition.Reason,
			Message:          condition.Message,
			LastTransitionAt: timestamppb.New(condition.LastTransitionTime.Time),
		})
	}

	return out
}

func protoPhase(phase governv1alpha1.EnvironmentPhase) governv1.PreviewEnvironment_Phase {
	switch phase {
	case governv1alpha1.EnvironmentReady:
		return governv1.PreviewEnvironment_PHASE_READY
	case governv1alpha1.EnvironmentExpired:
		return governv1.PreviewEnvironment_PHASE_EXPIRED
	case governv1alpha1.EnvironmentReclaiming:
		return governv1.PreviewEnvironment_PHASE_RECLAIMING
	case governv1alpha1.EnvironmentOrphaned:
		return governv1.PreviewEnvironment_PHASE_ORPHANED
	case governv1alpha1.EnvironmentPending:
		return governv1.PreviewEnvironment_PHASE_PENDING
	default:
		return governv1.PreviewEnvironment_PHASE_UNSPECIFIED
	}
}

func protoQuota(quota governv1alpha1.Quota) *governv1.Compute {
	return &governv1.Compute{
		CpuMillicores: quota.CPU.MilliValue(),
		MemoryBytes:   quota.Memory.Value(),
		StorageBytes:  quota.Storage.Value(),
		Pods:          quota.Pods,
	}
}

func quotaFromProto(compute *governv1.Compute) governv1alpha1.Quota {
	return governv1alpha1.Quota{
		CPU:     *resource.NewMilliQuantity(compute.GetCpuMillicores(), resource.DecimalSI),
		Memory:  *resource.NewQuantity(compute.GetMemoryBytes(), resource.BinarySI),
		Storage: *resource.NewQuantity(compute.GetStorageBytes(), resource.BinarySI),
		Pods:    compute.GetPods(),
	}
}

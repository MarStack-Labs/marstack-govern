package tenancy

import (
	"context"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

const SourceKubernetes = "kubernetes"

type Publisher interface {
	Publish(event *governv1.StreamEvent)
}

type Projector struct {
	client.Client
	Store     *Store
	Publisher Publisher
}

func (p *Projector) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.Division{}).
		Owns(&corev1.Namespace{}).
		Named("division-projector").
		Complete(p)
}

func (p *Projector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	division := &governv1alpha1.Division{}
	if err := p.Get(ctx, req.NamespacedName, division); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, p.Store.DeleteDivisionByName(ctx, req.Name)
		}
		return ctrl.Result{}, err
	}

	namespaces, usage, err := p.observeNamespaces(ctx, division)
	if err != nil {
		return ctrl.Result{}, err
	}

	projected := Division{
		UID:             string(division.UID),
		Name:            division.Name,
		DisplayName:     division.Spec.DisplayName,
		Phase:           phaseOf(division),
		QuotaBackend:    division.Status.QuotaBackend,
		QuotaCPU:        division.Spec.Quota.CPU.MilliValue(),
		QuotaMemory:     division.Spec.Quota.Memory.Value(),
		QuotaStorage:    division.Spec.Quota.Storage.Value(),
		QuotaPods:       division.Spec.Quota.Pods,
		UsedCPU:         usage.cpuMillicores,
		UsedMemory:      usage.memoryBytes,
		UsedStorage:     usage.storageBytes,
		UsedPods:        usage.pods,
		CreatedAt:       division.CreationTimestamp.Time,
		ResourceVersion: division.ResourceVersion,
	}

	if err := p.Store.UpsertDivision(ctx, projected, namespaces); err != nil {
		return ctrl.Result{}, err
	}

	p.publish(projected, namespaces)

	return ctrl.Result{}, nil
}

type usageTotals struct {
	cpuMillicores int64
	memoryBytes   int64
	storageBytes  int64
	pods          int32
}

func (p *Projector) observeNamespaces(ctx context.Context, division *governv1alpha1.Division) ([]Namespace, usageTotals, error) {
	list := &corev1.NamespaceList{}
	if err := p.List(ctx, list, client.MatchingLabels{kube.LabelDivision: division.Name}); err != nil {
		return nil, usageTotals{}, fmt.Errorf("list namespaces of %s: %w", division.Name, err)
	}

	namespaces := make([]Namespace, 0, len(list.Items))
	totals := usageTotals{}

	for i := range list.Items {
		item := &list.Items[i]

		defaultDeny, err := p.exists(ctx, &networkingv1.NetworkPolicy{}, item.Name, DefaultDenyPolicyName)
		if err != nil {
			return nil, usageTotals{}, err
		}

		limitRange, err := p.exists(ctx, &corev1.LimitRange{}, item.Name, LimitRangeName)
		if err != nil {
			return nil, usageTotals{}, err
		}

		bindings := &rbacv1.RoleBindingList{}
		if err := p.List(ctx, bindings,
			client.InNamespace(item.Name),
			client.MatchingLabels{LabelManagedBy: ManagedBy, kube.LabelDivision: division.Name},
		); err != nil {
			return nil, usageTotals{}, fmt.Errorf("list role bindings in %s: %w", item.Name, err)
		}

		if err := p.addQuotaUsage(ctx, item.Name, &totals); err != nil {
			return nil, usageTotals{}, err
		}

		boundBindings := len(bindings.Items)
		if boundBindings > math.MaxInt32 {
			boundBindings = math.MaxInt32
		}

		namespaces = append(namespaces, Namespace{
			Name:               item.Name,
			Division:           division.Name,
			Environment:        item.Labels[LabelEnvironment],
			Phase:              string(item.Status.Phase),
			DefaultDenyPresent: defaultDeny,
			LimitRangePresent:  limitRange,
			RoleBindings:       int32(boundBindings),
			CreatedAt:          item.CreationTimestamp.Time,
		})
	}

	return namespaces, totals, nil
}

func (p *Projector) addQuotaUsage(ctx context.Context, namespace string, totals *usageTotals) error {
	quota := &corev1.ResourceQuota{}
	key := types.NamespacedName{Namespace: namespace, Name: ResourceQuotaName}

	if err := p.Get(ctx, key, quota); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read quota usage in %s: %w", namespace, err)
	}

	used := quota.Status.Used
	if cpu, ok := used[corev1.ResourceRequestsCPU]; ok {
		totals.cpuMillicores += cpu.MilliValue()
	}
	if memory, ok := used[corev1.ResourceRequestsMemory]; ok {
		totals.memoryBytes += memory.Value()
	}
	if storage, ok := used[corev1.ResourceRequestsStorage]; ok {
		totals.storageBytes += storage.Value()
	}
	if pods, ok := used[corev1.ResourcePods]; ok {
		count := pods.Value()
		if count > math.MaxInt32 {
			count = math.MaxInt32
		}
		totals.pods += int32(count)
	}

	return nil
}

func (p *Projector) exists(ctx context.Context, object client.Object, namespace, name string) (bool, error) {
	err := p.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, object)
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}

	return false, fmt.Errorf("read %s in %s: %w", name, namespace, err)
}

func (p *Projector) publish(division Division, namespaces []Namespace) {
	if p.Publisher == nil {
		return
	}

	names := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		names = append(names, namespace.Name)
	}

	p.Publisher.Publish(&governv1.StreamEvent{
		Type:      governv1.StreamEvent_TYPE_DIVISION_CHANGED,
		EmittedAt: timestamppb.New(time.Now()),
		Freshness: &governv1.Freshness{ResourceVersion: division.ResourceVersion},
		Body: &governv1.StreamEvent_DivisionChanged{
			DivisionChanged: &governv1.DivisionChanged{
				Division: &governv1.Division{
					Uid:         division.UID,
					Name:        division.Name,
					DisplayName: division.DisplayName,
					Phase:       protoPhase(division.Phase),
					Quota: &governv1.Compute{
						CpuMillicores: division.QuotaCPU,
						MemoryBytes:   division.QuotaMemory,
						StorageBytes:  division.QuotaStorage,
						Pods:          division.QuotaPods,
					},
					Used: &governv1.Compute{
						CpuMillicores: division.UsedCPU,
						MemoryBytes:   division.UsedMemory,
						StorageBytes:  division.UsedStorage,
						Pods:          division.UsedPods,
					},
					Namespaces: names,
					CreatedAt:  timestamppb.New(division.CreatedAt),
				},
			},
		},
	})
}

func phaseOf(division *governv1alpha1.Division) string {
	if division.Status.Phase == "" {
		return "pending"
	}

	switch division.Status.Phase {
	case governv1alpha1.DivisionActive:
		return "active"
	case governv1alpha1.DivisionSuspended:
		return "suspended"
	case governv1alpha1.DivisionTerminating:
		return "terminating"
	default:
		return "pending"
	}
}

func protoPhase(phase string) governv1.Division_Phase {
	switch phase {
	case "active":
		return governv1.Division_PHASE_ACTIVE
	case "suspended":
		return governv1.Division_PHASE_SUSPENDED
	case "terminating":
		return governv1.Division_PHASE_TERMINATING
	default:
		return governv1.Division_PHASE_PENDING
	}
}

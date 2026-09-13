package tenancy

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

const (
	QuotaBackendCapsule = "capsule"

	globalQuotaName = "govern-division-quota"
)

var globalQuotaGVK = schema.GroupVersionKind{
	Group:   "capsule.clastix.io",
	Version: "v1beta2",
	Kind:    "GlobalResourceQuota",
}

type GlobalQuota struct {
	Name           string
	NamespaceCount int64
	Hard           corev1.ResourceList
	Used           corev1.ResourceList
}

func capsuleInstalled(reader client.Client) bool {
	mapper := reader.RESTMapper()
	if mapper == nil {
		return false
	}

	_, err := mapper.RESTMapping(globalQuotaGVK.GroupKind(), globalQuotaGVK.Version)

	return err == nil
}

func (r *Reconciler) ensureGlobalQuota(ctx context.Context, division *governv1alpha1.Division) error {
	quota := &unstructured.Unstructured{}
	quota.SetGroupVersionKind(globalQuotaGVK)

	name := globalQuotaName + "-" + division.Name
	key := types.NamespacedName{Name: name}

	err := r.Get(ctx, key, quota)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("read the global quota for %s: %w", division.Name, err)
	}

	created := apierrors.IsNotFound(err)

	desired := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"namespaceSelectors": []any{
				map[string]any{
					"matchLabels": map[string]any{kube.LabelDivision: division.Name},
				},
			},
			"quota": map[string]any{"hard": hardLimits(division.Spec.Quota)},
		},
	}}

	quota.SetGroupVersionKind(globalQuotaGVK)
	quota.SetName(name)
	quota.SetLabels(map[string]string{
		LabelManagedBy:     ManagedBy,
		kube.LabelDivision: division.Name,
	})
	if err := unstructured.SetNestedField(quota.Object, desired.Object["spec"], "spec"); err != nil {
		return fmt.Errorf("build the global quota for %s: %w", division.Name, err)
	}

	if created {
		if err := r.Create(ctx, quota); err != nil {
			return fmt.Errorf("create the global quota for %s: %w", division.Name, err)
		}

		return nil
	}

	if err := r.Update(ctx, quota); err != nil {
		return fmt.Errorf("update the global quota for %s: %w", division.Name, err)
	}

	return nil
}

func (r *Reconciler) globalQuota(ctx context.Context, division *governv1alpha1.Division) (*GlobalQuota, error) {
	quota := &unstructured.Unstructured{}
	quota.SetGroupVersionKind(globalQuotaGVK)

	name := globalQuotaName + "-" + division.Name
	if err := r.Get(ctx, types.NamespacedName{Name: name}, quota); err != nil {
		return nil, err
	}

	count, _, err := unstructured.NestedInt64(quota.Object, "status", "namespaceCount")
	if err != nil {
		return nil, fmt.Errorf("read the namespace count: %w", err)
	}

	hard, err := resourceList(quota.Object, "status", "total", "hard")
	if err != nil {
		return nil, err
	}

	used, err := resourceList(quota.Object, "status", "total", "used")
	if err != nil {
		return nil, err
	}

	return &GlobalQuota{Name: name, NamespaceCount: count, Hard: hard, Used: used}, nil
}

func (r *Reconciler) dropNamespaceQuotas(ctx context.Context, namespaces []string) error {
	for _, namespace := range namespaces {
		leftover := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: ResourceQuotaName, Namespace: namespace},
		}

		if err := r.deleteIfPresent(ctx, leftover); err != nil {
			return fmt.Errorf("remove the per-namespace quota in %s: %w", namespace, err)
		}
	}

	return nil
}

func hardLimits(quota governv1alpha1.Quota) map[string]any {
	hard := map[string]any{
		string(corev1.ResourceRequestsCPU):    quota.CPU.String(),
		string(corev1.ResourceRequestsMemory): quota.Memory.String(),
	}

	if !quota.Storage.IsZero() {
		hard[string(corev1.ResourceRequestsStorage)] = quota.Storage.String()
	}
	if quota.Pods > 0 {
		hard[string(corev1.ResourcePods)] = fmt.Sprintf("%d", quota.Pods)
	}

	return hard
}

func resourceList(object map[string]any, fields ...string) (corev1.ResourceList, error) {
	raw, found, err := unstructured.NestedStringMap(object, fields...)
	if err != nil {
		return nil, fmt.Errorf("read %v: %w", fields, err)
	}
	if !found {
		return nil, nil
	}

	list := corev1.ResourceList{}
	for name, value := range raw {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("%s is reported as %q, which is not a quantity", name, value)
		}

		list[corev1.ResourceName(name)] = quantity
	}

	return list, nil
}

func (r *Reconciler) globalQuotaMessage(ctx context.Context, division *governv1alpha1.Division) string {
	quota, err := r.globalQuota(ctx, division)
	if err != nil {
		return "the division total is enforced by Capsule across its namespaces"
	}

	if quota.NamespaceCount == 0 {
		return "Capsule holds the division total, but has not yet counted any namespace into it"
	}

	cpu := quota.Used[corev1.ResourceRequestsCPU]
	ceiling := quota.Hard[corev1.ResourceRequestsCPU]

	return fmt.Sprintf("Capsule enforces the division total across %d namespaces; %s of %s cpu is committed",
		quota.NamespaceCount, cpu.String(), ceiling.String())
}

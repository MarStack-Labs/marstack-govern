package topology

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Verdict string

const (
	VerdictAllowedAndUsed     Verdict = "allowed and used"
	VerdictAllowedUnused      Verdict = "allowed but never observed"
	VerdictObservedNotAllowed Verdict = "observed but no policy allows it"
	VerdictDenied             Verdict = "denied"
)

type Path struct {
	From      string
	To        string
	Allowed   bool
	Observed  bool
	AllowedBy string
	Verdict   Verdict
}

type Reachability struct {
	client client.Client
}

func NewReachability(c client.Client) *Reachability {
	return &Reachability{client: c}
}

func (r *Reachability) Paths(ctx context.Context, namespaces []string, observed []Edge) ([]Path, error) {
	catalogue, err := r.namespaces(ctx)
	if err != nil {
		return nil, err
	}

	policies := &networkingv1.NetworkPolicyList{}
	if err := r.client.List(ctx, policies); err != nil {
		return nil, fmt.Errorf("list network policies: %w", err)
	}

	seen := map[string]bool{}
	for _, edge := range observed {
		seen[edge.Client+"\x00"+edge.Server] = true
	}

	paths := []Path{}

	for _, target := range namespaces {
		for source := range catalogue {
			if source == target {
				continue
			}

			allowed, by := r.allows(policies.Items, catalogue, source, target)
			observedHere := seen[source+"\x00"+target]

			paths = append(paths, Path{
				From:      source,
				To:        target,
				Allowed:   allowed,
				Observed:  observedHere,
				AllowedBy: by,
				Verdict:   verdictOf(allowed, observedHere),
			})
		}
	}

	sort.SliceStable(paths, func(i, j int) bool {
		if paths[i].To != paths[j].To {
			return paths[i].To < paths[j].To
		}
		return paths[i].From < paths[j].From
	})

	return paths, nil
}

func (r *Reachability) namespaces(ctx context.Context) (map[string]map[string]string, error) {
	list := &corev1.NamespaceList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	catalogue := map[string]map[string]string{}
	for i := range list.Items {
		item := &list.Items[i]

		known := map[string]string{"kubernetes.io/metadata.name": item.Name}
		for key, value := range item.Labels {
			known[key] = value
		}

		catalogue[item.Name] = known
	}

	return catalogue, nil
}

func (r *Reachability) allows(
	policies []networkingv1.NetworkPolicy,
	catalogue map[string]map[string]string,
	source, target string,
) (bool, string) {
	guarded := false

	for i := range policies {
		policy := &policies[i]
		if policy.Namespace != target || !guards(policy) {
			continue
		}

		guarded = true

		for _, rule := range policy.Spec.Ingress {
			if len(rule.From) == 0 {
				return true, policy.Name
			}

			for _, peer := range rule.From {
				if peer.NamespaceSelector == nil {
					continue
				}

				selector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
				if err != nil {
					continue
				}

				if selector.Matches(labels.Set(catalogue[source])) {
					return true, policy.Name
				}
			}
		}
	}

	return !guarded, ""
}

func guards(policy *networkingv1.NetworkPolicy) bool {
	if len(policy.Spec.PolicyTypes) == 0 {
		return true
	}

	for _, policyType := range policy.Spec.PolicyTypes {
		if policyType == networkingv1.PolicyTypeIngress {
			return true
		}
	}

	return false
}

func verdictOf(allowed, observed bool) Verdict {
	switch {
	case allowed && observed:
		return VerdictAllowedAndUsed
	case allowed:
		return VerdictAllowedUnused
	case observed:
		return VerdictObservedNotAllowed
	default:
		return VerdictDenied
	}
}

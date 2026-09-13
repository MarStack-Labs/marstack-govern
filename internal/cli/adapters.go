package cli

import (
	"context"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/kube"
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

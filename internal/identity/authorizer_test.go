package identity_test

import (
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/marstack-labs/marstack-govern/internal/identity"
)

func TestClusterWideAccessSeesEverything(t *testing.T) {
	factory, _ := reviewingClients(func(string) bool { return true })
	authorizer := identity.NewAuthorizer(factory, time.Minute)

	scope, err := authorizer.ScopeFor(t.Context(), actor(), []string{"payments-dev"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}

	if !scope.AllowAll {
		t.Fatal("a cluster-wide reader was not given the whole cluster")
	}
	if !scope.Allows("anything-at-all") {
		t.Error("AllowAll does not allow an arbitrary namespace")
	}
}

func TestOnlyNamespacesKubernetesAllowsAreReturned(t *testing.T) {
	factory, _ := reviewingClients(func(namespace string) bool {
		return namespace == "payments-dev"
	})
	authorizer := identity.NewAuthorizer(factory, time.Minute)

	scope, err := authorizer.ScopeFor(t.Context(), actor(), []string{"payments-dev", "payments-prod", "erp-dev"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}

	if scope.AllowAll {
		t.Fatal("a namespace-scoped reader was given the whole cluster")
	}
	if len(scope.Namespaces) != 1 || scope.Namespaces[0] != "payments-dev" {
		t.Fatalf("namespaces: got %v", scope.Namespaces)
	}
	if scope.Allows("payments-prod") {
		t.Error("a refused namespace is still allowed")
	}
}

func TestDecisionsAreCachedForTheirTtl(t *testing.T) {
	factory, calls := reviewingClients(func(namespace string) bool {
		return namespace == "payments-dev"
	})
	authorizer := identity.NewAuthorizer(factory, time.Minute)

	for range 3 {
		if _, err := authorizer.ScopeFor(t.Context(), actor(), []string{"payments-dev", "payments-prod"}); err != nil {
			t.Fatalf("scope: %v", err)
		}
	}

	if got := calls.Load(); got != 3 {
		t.Fatalf("asked kubernetes %d times, want 3", got)
	}
}

func TestADifferentSubjectIsAskedSeparately(t *testing.T) {
	factory, calls := reviewingClients(func(namespace string) bool {
		return namespace == "payments-dev"
	})
	authorizer := identity.NewAuthorizer(factory, time.Minute)

	if _, err := authorizer.ScopeFor(t.Context(), actor(), []string{"payments-dev"}); err != nil {
		t.Fatalf("scope: %v", err)
	}

	other := actor()
	other.Subject = "someone-else"

	if _, err := authorizer.ScopeFor(t.Context(), other, []string{"payments-dev"}); err != nil {
		t.Fatalf("scope: %v", err)
	}

	if got := calls.Load(); got != 4 {
		t.Fatalf("asked kubernetes %d times, want 4", got)
	}
}

func actor() identity.Actor {
	return identity.Actor{
		Subject: "dev@example.test",
		Groups:  []string{"payments-admins"},
	}
}

func reviewingClients(allow func(namespace string) bool) (identity.ClientFactory, *atomic.Int32) {
	calls := &atomic.Int32{}

	return func(subject string, groups []string) (kubernetes.Interface, error) {
		client := fake.NewSimpleClientset()

		client.PrependReactor("create", "selfsubjectaccessreviews",
			func(action k8stesting.Action) (bool, runtime.Object, error) {
				calls.Add(1)

				create, ok := action.(k8stesting.CreateAction)
				if !ok {
					return false, nil, nil
				}

				review, ok := create.GetObject().(*authorizationv1.SelfSubjectAccessReview)
				if !ok {
					return false, nil, nil
				}

				review.Status.Allowed = allow(review.Spec.ResourceAttributes.Namespace)

				return true, review, nil
			})

		return client, nil
	}, calls
}

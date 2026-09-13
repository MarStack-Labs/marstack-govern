package identity

import (
	"context"
	"fmt"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type ClientFactory func(subject string, groups []string) (kubernetes.Interface, error)

type Scope struct {
	AllowAll   bool
	Namespaces []string
}

func (s Scope) Allows(namespace string) bool {
	if s.AllowAll {
		return true
	}

	for _, allowed := range s.Namespaces {
		if allowed == namespace {
			return true
		}
	}

	return false
}

type Authorizer struct {
	factory ClientFactory
	ttl     time.Duration

	mu       sync.Mutex
	decided  map[string]decision
	nowFunc  func() time.Time
	resource authorizationv1.ResourceAttributes
}

type decision struct {
	allowed bool
	at      time.Time
}

func NewAuthorizer(factory ClientFactory, ttl time.Duration) *Authorizer {
	if ttl <= 0 {
		ttl = time.Minute
	}

	return &Authorizer{
		factory: factory,
		ttl:     ttl,
		decided: map[string]decision{},
		nowFunc: time.Now,
		resource: authorizationv1.ResourceAttributes{
			Verb:     "list",
			Group:    "apps",
			Resource: "deployments",
		},
	}
}

func (a *Authorizer) ScopeFor(ctx context.Context, actor Actor, candidates []string) (Scope, error) {
	allowAll, err := a.allowed(ctx, actor, "")
	if err != nil {
		return Scope{}, err
	}
	if allowAll {
		return Scope{AllowAll: true}, nil
	}

	scope := Scope{Namespaces: make([]string, 0, len(candidates))}
	for _, namespace := range candidates {
		allowed, err := a.allowed(ctx, actor, namespace)
		if err != nil {
			return Scope{}, err
		}
		if allowed {
			scope.Namespaces = append(scope.Namespaces, namespace)
		}
	}

	return scope, nil
}

func (a *Authorizer) allowed(ctx context.Context, actor Actor, namespace string) (bool, error) {
	key := actor.Subject + "|" + namespace

	if cached, ok := a.cached(key); ok {
		return cached, nil
	}

	client, err := a.factory(actor.Subject, actor.Groups)
	if err != nil {
		return false, fmt.Errorf("build a client for %s: %w", actor.Label(), err)
	}

	attributes := a.resource
	attributes.Namespace = namespace

	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attributes},
	}

	answer, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("ask kubernetes whether %s may list workloads in %q: %w", actor.Label(), namespace, err)
	}

	a.remember(key, answer.Status.Allowed)

	return answer.Status.Allowed, nil
}

func (a *Authorizer) cached(key string) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	entry, ok := a.decided[key]
	if !ok || a.nowFunc().Sub(entry.at) > a.ttl {
		return false, false
	}

	return entry.allowed, true
}

func (a *Authorizer) remember(key string, allowed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.decided[key] = decision{allowed: allowed, at: a.nowFunc()}
}

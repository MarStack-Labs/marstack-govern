package requests_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/requests"
	"github.com/marstack-labs/marstack-govern/internal/simulate"
)

func TestARequestIsFiledAsTheCallerAndArrivesWithANumber(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), node(40, 96))
	store := requests.NewStore(pool)
	service := newService(t, c, store)

	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "dev@example.test"})

	submitted, err := service.SubmitRequest(ctx, connect.NewRequest(&governv1.SubmitRequestRequest{
		Division: "payments",
		Kind:     governv1.ResourceRequest_KIND_QUOTA,
		Reason:   "onboarding two backends for the quarter",
		Quota: &governv1.QuotaSpec{
			Target: &governv1.Compute{CpuMillicores: 12000, MemoryBytes: 24 * gibibyte},
		},
	}))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	name := submitted.Msg.GetRequest().GetName()
	if name == "" {
		t.Fatal("the created request has no name")
	}
	if got := submitted.Msg.GetRequest().GetRequester().GetSubject(); got != "dev@example.test" {
		t.Errorf("requester: got %q", got)
	}
	if got := submitted.Msg.GetRequest().GetNamespace(); got != "payments-dev" {
		t.Errorf("namespace: got %q, want payments-dev", got)
	}

	reconcileNamed(t, &requests.RequestReconciler{
		Client:      c,
		Recommender: withMetrics(t),
		Preflight:   &requests.Preflight{Client: c},
		Simulator:   &simulate.Simulator{Client: c},
	}, "payments-dev", name)

	project(t, c, store, "payments-dev", name)

	listed, err := service.ListRequests(ctx, connect.NewRequest(&governv1.ListRequestsRequest{Division: "payments"}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Msg.GetRequests()) != 1 {
		t.Fatalf("got %d requests, want 1", len(listed.Msg.GetRequests()))
	}

	stored := listed.Msg.GetRequests()[0]
	if stored.GetRecommendation().GetProposed().GetCpuMillicores() != 9000 {
		t.Errorf("proposed cpu: got %d", stored.GetRecommendation().GetProposed().GetCpuMillicores())
	}
	if !stored.GetPreflight().GetAdmitted() {
		t.Errorf("preflight: got %+v", stored.GetPreflight())
	}
}

func TestADecisionAgainstStaleEvidenceIsRefused(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	store := requests.NewStore(pool)
	service := newService(t, c, store)
	decisions := requests.NewDecisionService(service)

	reconcileRequest(t, c, withMetrics(t))
	project(t, c, store, "payments-dev", "raise-quota")

	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})
	uid := storedUID(t, store)

	_, err := decisions.Decide(ctx, connect.NewRequest(&governv1.DecideRequest{
		RequestUid:     uid,
		Outcome:        governv1.Decision_OUTCOME_APPROVED,
		Reason:         "approved for the quarter",
		EvidenceDigest: "a-digest-from-five-minutes-ago",
	}))

	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("got %v, want aborted", err)
	}
}

func TestADecisionAgainstCurrentEvidenceIsRecorded(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	store := requests.NewStore(pool)
	service := newService(t, c, store)
	decisions := requests.NewDecisionService(service)

	reconcileRequest(t, c, withMetrics(t))
	project(t, c, store, "payments-dev", "raise-quota")

	request := readRequest(t, c)
	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})

	decided, err := decisions.Decide(ctx, connect.NewRequest(&governv1.DecideRequest{
		RequestUid:     storedUID(t, store),
		Outcome:        governv1.Decision_OUTCOME_APPROVED,
		Reason:         "approved for the quarter",
		GrantDuration:  "720h",
		EvidenceDigest: request.Status.EvidenceDigest,
	}))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}

	decision := decided.Msg.GetDecision()
	if decision.GetOutcome() != governv1.Decision_OUTCOME_APPROVED {
		t.Errorf("outcome: got %v", decision.GetOutcome())
	}
	if decision.GetDecider().GetSubject() != "lead@example.test" {
		t.Errorf("decider: got %q", decision.GetDecider().GetSubject())
	}
	if decision.GetGrantedUntil() == nil {
		t.Fatal("an approval carries no expiry")
	}
	if days := time.Until(decision.GetGrantedUntil().AsTime()).Hours() / 24; days < 29 || days > 31 {
		t.Errorf("grant lasts %.0f days, want 30", days)
	}
	if decision.GetEvidence().GetRecommendation() == nil {
		t.Error("the decision kept no recommendation as evidence")
	}
	if decision.GetEvidence().GetPreflight() == nil {
		t.Error("the decision kept no preflight result as evidence")
	}
}

func TestADecidedRequestCannotBeDecidedTwice(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	store := requests.NewStore(pool)
	service := newService(t, c, store)
	decisions := requests.NewDecisionService(service)

	reconcileRequest(t, c, withMetrics(t))
	decide(t, c, governv1alpha1.OutcomeApproved, ninetyDays())
	project(t, c, store, "payments-dev", "raise-quota")

	request := readRequest(t, c)
	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})

	_, err := decisions.Decide(ctx, connect.NewRequest(&governv1.DecideRequest{
		RequestUid:     storedUID(t, store),
		Outcome:        governv1.Decision_OUTCOME_REJECTED,
		Reason:         "changed my mind after the fact",
		EvidenceDigest: request.Status.EvidenceDigest,
	}))

	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v, want failed precondition", err)
	}
}

func TestTheQueueOnlyHoldsOpenRequests(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	store := requests.NewStore(pool)
	service := newService(t, c, store)
	decisions := requests.NewDecisionService(service)

	reconcileRequest(t, c, withMetrics(t))
	project(t, c, store, "payments-dev", "raise-quota")

	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})

	queue, err := decisions.ListQueue(ctx, connect.NewRequest(&governv1.ListQueueRequest{}))
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(queue.Msg.GetRequests()) != 1 {
		t.Fatalf("open queue: got %d, want 1", len(queue.Msg.GetRequests()))
	}

	withdrawn, err := service.WithdrawRequest(ctx, connect.NewRequest(&governv1.WithdrawRequestRequest{
		Uid:    storedUID(t, store),
		Reason: "no longer needed",
	}))
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if withdrawn.Msg.GetRequest() == nil {
		t.Fatal("withdrawing returned no request")
	}

	reconcileRequest(t, c, withMetrics(t))
	project(t, c, store, "payments-dev", "raise-quota")

	queue, err = decisions.ListQueue(ctx, connect.NewRequest(&governv1.ListQueueRequest{}))
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(queue.Msg.GetRequests()) != 0 {
		t.Fatalf("a withdrawn request stayed in the queue: %v", queue.Msg.GetRequests())
	}
}

func TestAnonymousCallersCannotFileRequests(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), node(40, 96))
	service := newService(t, c, requests.NewStore(pool))

	_, err := service.SubmitRequest(t.Context(), connect.NewRequest(&governv1.SubmitRequestRequest{
		Division: "payments",
		Kind:     governv1.ResourceRequest_KIND_QUOTA,
		Reason:   "onboarding two backends for the quarter",
	}))

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}

func TestAThinReasonIsRefused(t *testing.T) {
	pool := dbtest.Migrated(t)
	c := newCluster(t, division(8, 16), node(40, 96))
	service := newService(t, c, requests.NewStore(pool))

	ctx := identity.NewContext(t.Context(), identity.Actor{Subject: "dev@example.test"})

	_, err := service.SubmitRequest(ctx, connect.NewRequest(&governv1.SubmitRequestRequest{
		Division: "payments",
		Kind:     governv1.ResourceRequest_KIND_QUOTA,
		Reason:   "need it",
		Quota:    &governv1.QuotaSpec{Target: &governv1.Compute{CpuMillicores: 12000}},
	}))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want invalid argument", err)
	}
}

func newService(t *testing.T, c client.Client, store *requests.Store) *requests.Service {
	t.Helper()

	return requests.NewService(
		c,
		func(identity.Actor) (client.Client, error) { return c, nil },
		store,
		withMetrics(t),
		&requests.Preflight{Client: c},
	)
}

func project(t *testing.T, c client.Client, store *requests.Store, namespace, name string) {
	t.Helper()

	projector := &requests.Projector{Client: c, Store: store}
	reconcileNamed(t, projector, namespace, name)
}

func storedUID(t *testing.T, store *requests.Store) string {
	t.Helper()

	found, err := store.ListRequests(t.Context(), nil, "", false)
	if err != nil {
		t.Fatalf("list stored requests: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("nothing was projected")
	}

	return found[0].UID
}

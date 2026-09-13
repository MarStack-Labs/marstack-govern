package environments_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"connectrpc.com/connect"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/environments"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

func TestOnlyThisDivisionsPreviewsAreListed(t *testing.T) {
	mine := preview("48h")
	theirs := preview("48h")
	theirs.Name = "erp-99"
	theirs.Spec.Division = "erp"
	theirs.Spec.Change.Number = 99

	service, _ := newService(t, division(), mine, theirs)

	response, err := service.ListEnvironments(signedIn(t), connect.NewRequest(
		&governv1.ListEnvironmentsRequest{Division: "payments"},
	))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(response.Msg.GetEnvironments()) != 1 {
		t.Fatalf("got %d previews, want only this division's", len(response.Msg.GetEnvironments()))
	}
	if got := response.Msg.GetEnvironments()[0].GetChange().GetNumber(); got != 412 {
		t.Fatalf("change: got %d", got)
	}
}

func TestAReclaimedPreviewIsHiddenUnlessAskedFor(t *testing.T) {
	gone := preview("48h")
	gone.Status.Phase = governv1alpha1.EnvironmentExpired

	service, _ := newService(t, division(), gone)

	hidden, err := service.ListEnvironments(signedIn(t), connect.NewRequest(
		&governv1.ListEnvironmentsRequest{Division: "payments"},
	))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hidden.Msg.GetEnvironments()) != 0 {
		t.Fatalf("a reclaimed preview was listed as if it still existed")
	}

	shown, err := service.ListEnvironments(signedIn(t), connect.NewRequest(
		&governv1.ListEnvironmentsRequest{Division: "payments", IncludeReclaimed: true},
	))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(shown.Msg.GetEnvironments()) != 1 {
		t.Fatal("the history of reclaimed previews is not reachable")
	}
}

func TestRequestingTheSameChangeTwiceReturnsTheSamePreview(t *testing.T) {
	service, api := newService(t, division())

	ask := connect.NewRequest(&governv1.RequestEnvironmentRequest{
		Division: "payments",
		Change: &governv1.Change{
			Repository: "payments/ledger",
			Number:     412,
			Branch:     "feat/settlement-window",
		},
		Quota: &governv1.Compute{CpuMillicores: 2000, MemoryBytes: 4 << 30, Pods: 10},
		Ttl:   "24h",
	})

	first, err := service.RequestEnvironment(signedIn(t), ask)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	second, err := service.RequestEnvironment(signedIn(t), ask)
	if err != nil {
		t.Fatalf("request again: %v", err)
	}

	if first.Msg.GetEnvironment().GetName() != second.Msg.GetEnvironment().GetName() {
		t.Fatalf("two previews were created for one change: %q and %q",
			first.Msg.GetEnvironment().GetName(), second.Msg.GetEnvironment().GetName())
	}

	list := &governv1alpha1.EphemeralEnvironmentList{}
	if err := api.List(t.Context(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("got %d environments in the cluster, want 1", len(list.Items))
	}
}

func TestARenewalWithoutAReasonIsRefused(t *testing.T) {
	service, _ := newService(t, division(), preview("48h"))

	_, err := service.RenewEnvironment(signedIn(t), connect.NewRequest(
		&governv1.RenewEnvironmentRequest{Name: "payments-412", Extend: "24h", Reason: "later"},
	))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code: got %v, want a refusal", connect.CodeOf(err))
	}
}

func TestARenewalIsRecordedAgainstThePersonWhoGrantedIt(t *testing.T) {
	service, api := newService(t, division(), preview("48h"))

	_, err := service.RenewEnvironment(signedIn(t), connect.NewRequest(&governv1.RenewEnvironmentRequest{
		Name:   "payments-412",
		Extend: "24h",
		Reason: "the reviewer is on leave until Thursday",
	}))
	if err != nil {
		t.Fatalf("renew: %v", err)
	}

	stored := &governv1alpha1.EphemeralEnvironment{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: "payments-412"}, stored); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if len(stored.Spec.Renewals) != 1 {
		t.Fatalf("got %d renewals, want 1", len(stored.Spec.Renewals))
	}
	renewal := stored.Spec.Renewals[0]
	if renewal.GrantedBy != "dev@marstack.test" {
		t.Errorf("granted by: got %q", renewal.GrantedBy)
	}
	if renewal.Reason == "" || renewal.Extend != "24h" {
		t.Errorf("renewal: %+v", renewal)
	}
}

func TestEveryDurationWeWriteMatchesWhatTheCRDAccepts(t *testing.T) {
	accepted := regexp.MustCompile(`^([0-9]+h)?([0-9]+m)?$`)

	for _, value := range []time.Duration{
		30 * time.Second,
		90 * time.Second,
		45 * time.Minute,
		time.Hour,
		90 * time.Minute,
		24 * time.Hour,
		environments.MaxTTL,
	} {
		formatted := environments.FormatTTL(value)
		if !accepted.MatchString(formatted) || formatted == "" {
			t.Errorf("%s formats as %q, which the CRD pattern rejects", value, formatted)
		}
	}
}

func TestAReclaimedPreviewCannotBeRenewed(t *testing.T) {
	gone := preview("48h")
	gone.Status.Phase = governv1alpha1.EnvironmentExpired

	service, _ := newService(t, division(), gone)

	_, err := service.RenewEnvironment(signedIn(t), connect.NewRequest(&governv1.RenewEnvironmentRequest{
		Name:   "payments-412",
		Extend: "24h",
		Reason: "we would like it back for one more day",
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("code: got %v, want a refusal to resurrect a reclaimed namespace", connect.CodeOf(err))
	}
}

func TestSigningInIsRequired(t *testing.T) {
	service, _ := newService(t, division())

	_, err := service.ListEnvironments(t.Context(), connect.NewRequest(
		&governv1.ListEnvironmentsRequest{Division: "payments"},
	))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code: got %v", connect.CodeOf(err))
	}
}

func newService(t *testing.T, objects ...client.Object) (*environments.Service, client.Client) {
	t.Helper()

	_, api := newReconciler(t, created, objects...)

	return environments.NewService(api, func(identity.Actor) (client.Client, error) {
		return api, nil
	}), api
}

func signedIn(t *testing.T) context.Context {
	t.Helper()

	return identity.NewContext(t.Context(), identity.Actor{
		Subject:        "dev@marstack.test",
		ActiveDivision: "payments",
	})
}

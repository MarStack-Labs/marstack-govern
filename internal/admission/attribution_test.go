package admission_test

import (
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governadmission "github.com/marstack-labs/marstack-govern/internal/admission"
)

func TestAClaimedIdentityIsReplacedByTheAuthenticatedOne(t *testing.T) {
	request := quotaRequest()
	request.Spec.RequestedBy = "cfo@marstack.test"

	response := handle(t, "/attribute/quotarequest", admissionv1.Create, "dev@marstack.test", request, nil)

	if !response.Allowed {
		t.Fatalf("denied: %s", message(response))
	}

	patched := applyTo(t, request, response)
	if patched.Spec.RequestedBy != "dev@marstack.test" {
		t.Fatalf("requestedBy: got %q, want the authenticated user", patched.Spec.RequestedBy)
	}
}

func TestAnEmptyRequesterIsFilledInNotLeftBlank(t *testing.T) {
	request := quotaRequest()

	response := handle(t, "/attribute/quotarequest", admissionv1.Create, "dev@marstack.test", request, nil)

	patched := applyTo(t, request, response)
	if patched.Spec.RequestedBy != "dev@marstack.test" {
		t.Fatalf("requestedBy: got %q", patched.Spec.RequestedBy)
	}
}

func TestTheRequesterCannotBeRewrittenLater(t *testing.T) {
	previous := quotaRequest()
	previous.Spec.RequestedBy = "dev@marstack.test"

	next := previous.DeepCopy()
	next.Spec.RequestedBy = "cfo@marstack.test"

	response := handle(t, "/attribute/quotarequest", admissionv1.Update, "dev@marstack.test", next, previous)

	if response.Allowed {
		t.Fatal("the requester was rewritten after the fact")
	}
	if got := message(response); got == "" {
		t.Fatal("the refusal said nothing")
	}
}

func TestAnUnrelatedUpdateStillPasses(t *testing.T) {
	previous := quotaRequest()
	previous.Spec.RequestedBy = "dev@marstack.test"

	next := previous.DeepCopy()
	next.Spec.Withdrawn = true

	response := handle(t, "/attribute/quotarequest", admissionv1.Update, "dev@marstack.test", next, previous)

	if !response.Allowed {
		t.Fatalf("withdrawing was denied: %s", message(response))
	}
}

func TestAnApproverCannotSignAsSomeoneElse(t *testing.T) {
	decision := &governv1alpha1.Decision{
		ObjectMeta: metav1.ObjectMeta{Name: "d-1", Namespace: "payments-dev"},
		Spec: governv1alpha1.DecisionSpec{
			Outcome:   governv1alpha1.OutcomeApproved,
			Reason:    "the usage justifies the headroom",
			DecidedBy: "head-of-platform@marstack.test",
		},
	}

	response := handle(t, "/attribute/decision", admissionv1.Create, "intern@marstack.test", decision, nil)

	patched := &governv1alpha1.Decision{}
	applyInto(t, decision, response, patched)

	if patched.Spec.DecidedBy != "intern@marstack.test" {
		t.Fatalf("decidedBy: got %q, want the approval signed by whoever actually sent it",
			patched.Spec.DecidedBy)
	}
}

func TestARenewalIsSignedByWhoeverGrantedIt(t *testing.T) {
	previous := environment()
	next := previous.DeepCopy()
	next.Spec.Renewals = append(next.Spec.Renewals, governv1alpha1.Renewal{
		Extend:    "24h",
		Reason:    "the reviewer is on leave until Thursday",
		GrantedBy: "head-of-platform@marstack.test",
	})

	response := handle(t, "/attribute/ephemeralenvironment", admissionv1.Update,
		"lead@marstack.test", next, previous)

	if !response.Allowed {
		t.Fatalf("denied: %s", message(response))
	}

	patched := &governv1alpha1.EphemeralEnvironment{}
	applyInto(t, next, response, patched)

	if len(patched.Spec.Renewals) != 1 {
		t.Fatalf("got %d renewals", len(patched.Spec.Renewals))
	}
	if patched.Spec.Renewals[0].GrantedBy != "lead@marstack.test" {
		t.Fatalf("grantedBy: got %q, want the authenticated grantor",
			patched.Spec.Renewals[0].GrantedBy)
	}
	if patched.Spec.Renewals[0].GrantedAt.IsZero() {
		t.Fatal("the renewal carries no time")
	}
}

func TestAGrantedRenewalCannotBeEditedAway(t *testing.T) {
	previous := environment()
	previous.Spec.Renewals = []governv1alpha1.Renewal{{
		Extend:    "24h",
		Reason:    "the reviewer is on leave until Thursday",
		GrantedBy: "lead@marstack.test",
		GrantedAt: metav1.Now(),
	}}

	next := previous.DeepCopy()
	next.Spec.Renewals[0].Extend = "168h"

	response := handle(t, "/attribute/ephemeralenvironment", admissionv1.Update,
		"dev@marstack.test", next, previous)

	if response.Allowed {
		t.Fatal("a granted renewal was rewritten in place")
	}
}

func TestARenewalCannotBeDroppedToHideIt(t *testing.T) {
	previous := environment()
	previous.Spec.Renewals = []governv1alpha1.Renewal{{
		Extend:    "24h",
		Reason:    "the reviewer is on leave until Thursday",
		GrantedBy: "lead@marstack.test",
		GrantedAt: metav1.Now(),
	}}

	next := previous.DeepCopy()
	next.Spec.Renewals = nil

	response := handle(t, "/attribute/ephemeralenvironment", admissionv1.Update,
		"dev@marstack.test", next, previous)

	if response.Allowed {
		t.Fatal("the renewal history was erased")
	}
}

func TestAnUnauthenticatedRequestIsRefused(t *testing.T) {
	response := handle(t, "/attribute/quotarequest", admissionv1.Create, "", quotaRequest(), nil)

	if response.Allowed {
		t.Fatal("an unattributable request was admitted")
	}
}

func TestDeletingIsNotOurBusiness(t *testing.T) {
	response := handle(t, "/attribute/quotarequest", admissionv1.Delete,
		"dev@marstack.test", quotaRequest(), nil)

	if !response.Allowed {
		t.Fatalf("a delete was blocked by the attribution webhook: %s", message(response))
	}
}

func handle(
	t *testing.T,
	path string,
	operation admissionv1.Operation,
	user string,
	object client.Object,
	previous client.Object,
) admission.Response {
	t.Helper()

	var rule governadmission.Rule
	for _, candidate := range governadmission.Rules() {
		if candidate.Path == path {
			rule = candidate
		}
	}
	if rule.Path == "" {
		t.Fatalf("no rule registered at %s", path)
	}

	request := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			UserInfo:  authenticationv1.UserInfo{Username: user},
			Object:    runtime.RawExtension{Raw: encode(t, object)},
		},
	}
	if previous != nil {
		request.OldObject = runtime.RawExtension{Raw: encode(t, previous)}
	}

	return governadmission.NewAttribution(scheme(t), rule).Handle(t.Context(), request)
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	built := runtime.NewScheme()
	if err := governv1alpha1.AddToScheme(built); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	return built
}

func encode(t *testing.T, object client.Object) []byte {
	t.Helper()

	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	return raw
}

func applyTo(
	t *testing.T,
	original *governv1alpha1.QuotaRequest,
	response admission.Response,
) *governv1alpha1.QuotaRequest {
	t.Helper()

	patched := &governv1alpha1.QuotaRequest{}
	applyInto(t, original, response, patched)

	return patched
}

func applyInto(t *testing.T, original client.Object, response admission.Response, into any) {
	t.Helper()

	if !response.Allowed {
		t.Fatalf("denied: %s", message(response))
	}

	raw, err := json.Marshal(response.Patches)
	if err != nil {
		t.Fatalf("encode patches: %v", err)
	}

	patch, err := jsonpatch.DecodePatch(raw)
	if err != nil {
		t.Fatalf("decode patch: %v", err)
	}

	patched, err := patch.Apply(encode(t, original))
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}

	if err := json.Unmarshal(patched, into); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
}

func message(response admission.Response) string {
	if response.Result == nil {
		return ""
	}

	return response.Result.Message
}

func quotaRequest() *governv1alpha1.QuotaRequest {
	return &governv1alpha1.QuotaRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-1", Namespace: "payments-dev"},
		Spec: governv1alpha1.QuotaRequestSpec{
			Division: "payments",
			Target: governv1alpha1.Quota{
				CPU:    resource.MustParse("40"),
				Memory: resource.MustParse("80Gi"),
			},
			Reason: "the p95 has been above the ceiling for a fortnight",
		},
	}
}

func environment() *governv1alpha1.EphemeralEnvironment {
	return &governv1alpha1.EphemeralEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-412"},
		Spec: governv1alpha1.EphemeralEnvironmentSpec{
			Division: "payments",
			Change: governv1alpha1.ChangeRef{
				Repository: "payments/ledger",
				Number:     412,
				Branch:     "feat/settlement-window",
			},
			Quota: governv1alpha1.Quota{
				CPU:    resource.MustParse("2"),
				Memory: resource.MustParse("4Gi"),
			},
			TTL:         "48h",
			RequestedBy: "dev@marstack.test",
		},
	}
}

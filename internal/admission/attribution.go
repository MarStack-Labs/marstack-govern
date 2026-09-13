package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

type Rule struct {
	Path     string
	Field    string
	New      func() client.Object
	Owner    func(client.Object) string
	SetOwner func(client.Object, string)
	Stamp    func(next, previous client.Object, user string) error
}

type Attribution struct {
	rule    Rule
	decoder admission.Decoder
}

func NewAttribution(scheme *runtime.Scheme, rule Rule) *Attribution {
	return &Attribution{rule: rule, decoder: admission.NewDecoder(scheme)}
}

func Rules() []Rule {
	return []Rule{
		{
			Path:  "/attribute/quotarequest",
			Field: "spec.requestedBy",
			New:   func() client.Object { return &governv1alpha1.QuotaRequest{} },
			Owner: func(object client.Object) string {
				return object.(*governv1alpha1.QuotaRequest).Spec.RequestedBy
			},
			SetOwner: func(object client.Object, user string) {
				object.(*governv1alpha1.QuotaRequest).Spec.RequestedBy = user
			},
		},
		{
			Path:  "/attribute/decision",
			Field: "spec.decidedBy",
			New:   func() client.Object { return &governv1alpha1.Decision{} },
			Owner: func(object client.Object) string {
				return object.(*governv1alpha1.Decision).Spec.DecidedBy
			},
			SetOwner: func(object client.Object, user string) {
				object.(*governv1alpha1.Decision).Spec.DecidedBy = user
			},
		},
		{
			Path:  "/attribute/ephemeralenvironment",
			Field: "spec.requestedBy",
			New:   func() client.Object { return &governv1alpha1.EphemeralEnvironment{} },
			Owner: func(object client.Object) string {
				return object.(*governv1alpha1.EphemeralEnvironment).Spec.RequestedBy
			},
			SetOwner: func(object client.Object, user string) {
				object.(*governv1alpha1.EphemeralEnvironment).Spec.RequestedBy = user
			},
			Stamp: stampRenewals,
		},
	}
}

func Register(manager ctrl.Manager) error {
	server := manager.GetWebhookServer()

	for _, rule := range Rules() {
		server.Register(rule.Path, &webhook.Admission{
			Handler: NewAttribution(manager.GetScheme(), rule),
		})
	}

	return nil
}

func (a *Attribution) Handle(_ context.Context, request admission.Request) admission.Response {
	user := request.UserInfo.Username
	if user == "" {
		return admission.Denied("the request carries no authenticated user, so nothing can be attributed")
	}

	next := a.rule.New()
	if err := a.decoder.Decode(request, next); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	switch request.Operation {
	case admissionv1.Create:
		a.rule.SetOwner(next, user)

	case admissionv1.Update:
		previous := a.rule.New()
		if err := a.decoder.DecodeRaw(request.OldObject, previous); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		if owner := a.rule.Owner(previous); a.rule.Owner(next) != owner {
			return admission.Denied(fmt.Sprintf(
				"%s was recorded as %q when the object was created and cannot be rewritten",
				a.rule.Field, owner))
		}

		if a.rule.Stamp != nil {
			if err := a.rule.Stamp(next, previous, user); err != nil {
				return admission.Denied(err.Error())
			}
		}

	default:
		return admission.Allowed("")
	}

	encoded, err := json.Marshal(next)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(request.Object.Raw, encoded)
}

func stampRenewals(next, previous client.Object, user string) error {
	after := next.(*governv1alpha1.EphemeralEnvironment)
	before := previous.(*governv1alpha1.EphemeralEnvironment)

	if len(after.Spec.Renewals) < len(before.Spec.Renewals) {
		return fmt.Errorf("a renewal that was already granted cannot be removed")
	}

	for i := range before.Spec.Renewals {
		if after.Spec.Renewals[i] != before.Spec.Renewals[i] {
			return fmt.Errorf("renewal %d was granted by %s and cannot be rewritten",
				i, before.Spec.Renewals[i].GrantedBy)
		}
	}

	for i := len(before.Spec.Renewals); i < len(after.Spec.Renewals); i++ {
		after.Spec.Renewals[i].GrantedBy = user
		if after.Spec.Renewals[i].GrantedAt.IsZero() {
			after.Spec.Renewals[i].GrantedAt = metav1.Now()
		}
	}

	return nil
}

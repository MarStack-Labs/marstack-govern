package delivery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/delivery"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

func TestATemplateCarriesTheFieldsItNeeds(t *testing.T) {
	service := newDeliveryService(t, nil)

	response, err := service.ListTemplates(signedIn(t), connect.NewRequest(&governv1.ListTemplatesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(response.Msg.GetTemplates()) != 2 {
		t.Fatalf("got %d templates", len(response.Msg.GetTemplates()))
	}

	postgres := response.Msg.GetTemplates()[1]
	if postgres.GetName() != "postgresql" {
		t.Fatalf("templates are not sorted: %s", postgres.GetName())
	}

	required := 0
	for _, field := range postgres.GetFields() {
		if field.GetRequired() {
			required++
		}
	}
	if required == 0 {
		t.Fatal("a field with no default was not marked as needing an answer")
	}
}

func TestWithoutARegistryThereIsNothingToScaffoldFrom(t *testing.T) {
	service := delivery.NewService(newDeliveryCluster(t), delivery.NewCatalogue(nil))

	_, err := service.ListTemplates(signedIn(t), connect.NewRequest(&governv1.ListTemplatesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want a refusal rather than an empty catalogue", connect.CodeOf(err))
	}
}

func TestScaffoldingRendersAnApplicationForTheRightNamespace(t *testing.T) {
	service := newDeliveryService(t, nil)

	response, err := service.Scaffold(signedIn(t), connect.NewRequest(&governv1.ScaffoldRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
	}))
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	if len(response.Msg.GetFiles()) != 1 {
		t.Fatalf("got %d files", len(response.Msg.GetFiles()))
	}

	file := response.Msg.GetFiles()[0]
	if file.GetPath() != "payments/dev/ledger-db.yaml" {
		t.Errorf("path: got %s", file.GetPath())
	}

	for _, want := range []string{
		"namespace: payments-dev",
		"name: ledger-db",
		"chart: postgresql",
		`- name: auth.database`,
		`value: "ledger"`,
	} {
		if !strings.Contains(file.GetContent(), want) {
			t.Errorf("the manifest is missing %q:\n%s", want, file.GetContent())
		}
	}
}

func TestAnEnvironmentTheDivisionDoesNotHaveIsRefused(t *testing.T) {
	service := newDeliveryService(t, nil)

	_, err := service.Scaffold(signedIn(t), connect.NewRequest(&governv1.ScaffoldRequest{
		Division:    "payments",
		Environment: "production",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
	}))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want the unknown environment refused", connect.CodeOf(err))
	}
}

func TestAMissingRequiredValueIsNamed(t *testing.T) {
	service := newDeliveryService(t, nil)

	_, err := service.Scaffold(signedIn(t), connect.NewRequest(&governv1.ScaffoldRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
	}))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code: got %v", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "auth.database") {
		t.Fatalf("the refusal does not say which value is missing: %v", err)
	}
}

func TestANameKubernetesWouldRejectIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	service := newDeliveryService(t, nil)

	_, err := service.Scaffold(signedIn(t), connect.NewRequest(&governv1.ScaffoldRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "Ledger_DB",
		Values:      map[string]string{"auth.database": "ledger"},
	}))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want the name refused", connect.CodeOf(err))
	}
}

func TestWithoutAnAdmissionChainScaffoldSaysItWasNotChecked(t *testing.T) {
	service := newDeliveryService(t, nil)

	response, err := service.Scaffold(signedIn(t), connect.NewRequest(&governv1.ScaffoldRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
	}))
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	if response.Msg.GetAdmitted() {
		t.Fatal("an unchecked manifest was reported as admitted")
	}
	if len(response.Msg.GetFindings()) == 0 {
		t.Fatal("nothing said why it was not admitted")
	}
}

func TestAManifestTheClusterWouldRejectNeverReachesTheForge(t *testing.T) {
	forge := &recordingForge{available: true}
	service := newDeliveryService(t, forge).
		WithAdmission(refusing{}).
		WithForge(forge, func(string) string { return "marstack/payments-gitops" })

	_, err := service.OpenChange(signedIn(t), connect.NewRequest(&governv1.OpenChangeRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
		Reason:      "the ledger needs its own database",
	}))

	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v, want the change refused", connect.CodeOf(err))
	}
	if forge.opened {
		t.Fatal("a merge request was opened for a manifest the cluster would reject")
	}
	if !strings.Contains(err.Error(), "runAsRoot") {
		t.Fatalf("the refusal does not repeat the finding: %v", err)
	}
}

func TestOpeningAChangeWithoutAForgeSaysSo(t *testing.T) {
	service := newDeliveryService(t, nil)

	_, err := service.OpenChange(signedIn(t), connect.NewRequest(&governv1.OpenChangeRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
		Reason:      "the ledger needs its own database",
	}))

	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want unavailable", connect.CodeOf(err))
	}
}

func TestAChangeIsAttributedAndCarriesItsReason(t *testing.T) {
	forge := &recordingForge{available: true}
	service := newDeliveryService(t, forge).
		WithForge(forge, func(string) string { return "marstack/payments-gitops" })

	response, err := service.OpenChange(signedIn(t), connect.NewRequest(&governv1.OpenChangeRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
		Reason:      "the ledger needs its own database",
	}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if forge.repository != "marstack/payments-gitops" {
		t.Errorf("repository: got %s", forge.repository)
	}
	if forge.branch != "scaffold/dev-ledger-db" {
		t.Errorf("branch: got %s", forge.branch)
	}
	if !strings.Contains(forge.body, "the ledger needs its own database") {
		t.Errorf("the reason was dropped: %s", forge.body)
	}
	if !strings.Contains(forge.body, "dev@marstack.test") {
		t.Errorf("the change is not attributed: %s", forge.body)
	}
	if response.Msg.GetChange().GetUrl() == "" {
		t.Error("the caller was not told where the change is")
	}
}

func TestAChangeWithoutAReasonIsRefused(t *testing.T) {
	forge := &recordingForge{available: true}
	service := newDeliveryService(t, forge).
		WithForge(forge, func(string) string { return "marstack/payments-gitops" })

	_, err := service.OpenChange(signedIn(t), connect.NewRequest(&governv1.OpenChangeRequest{
		Division:    "payments",
		Environment: "dev",
		Template:    "postgresql",
		Name:        "ledger-db",
		Values:      map[string]string{"auth.database": "ledger"},
		Reason:      "because",
	}))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v", connect.CodeOf(err))
	}
	if forge.opened {
		t.Fatal("a change was opened with no reason recorded")
	}
}

func TestTheGitHubForgeOpensABranchACommitAndAPullRequest(t *testing.T) {
	seen := map[string]bool{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/repos/marstack/payments-gitops":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case strings.HasPrefix(r.URL.Path, "/repos/marstack/payments-gitops/git/ref/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "abc123"}})
		case r.URL.Path == "/repos/marstack/payments-gitops/git/refs":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case strings.HasPrefix(r.URL.Path, "/repos/marstack/payments-gitops/contents/"):
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})

				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.URL.Path == "/repos/marstack/payments-gitops/pulls":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 42, "title": "Add ledger-db", "state": "open",
				"html_url": "https://github.test/marstack/payments-gitops/pull/42",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	forge := delivery.NewGitHub(delivery.GitHubConfig{BaseURL: server.URL, Token: "t0ken"})

	change, err := forge.Open(t.Context(), "marstack/payments-gitops", "scaffold/dev-ledger-db",
		"Add ledger-db", "because", []delivery.File{{Path: "payments/dev/ledger-db.yaml", Content: "kind: Application\n"}},
		"dev@marstack.test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if change.ID != "42" || change.URL == "" {
		t.Fatalf("change: %+v", change)
	}

	for _, want := range []string{
		"GET /repos/marstack/payments-gitops",
		"POST /repos/marstack/payments-gitops/git/refs",
		"PUT /repos/marstack/payments-gitops/contents/payments/dev/ledger-db.yaml",
		"POST /repos/marstack/payments-gitops/pulls",
	} {
		if !seen[want] {
			t.Errorf("the forge never saw %s", want)
		}
	}
}

func TestAForgeWithoutATokenRefusesRatherThanCallingAnonymously(t *testing.T) {
	forge := delivery.NewGitHub(delivery.GitHubConfig{BaseURL: "https://api.github.test"})

	if forge.Available() {
		t.Fatal("a forge with no token claims to be usable")
	}

	if _, err := forge.Changes(t.Context(), "marstack/payments-gitops"); err == nil {
		t.Fatal("an anonymous call was made")
	}
}

type recordingForge struct {
	available  bool
	opened     bool
	repository string
	branch     string
	title      string
	body       string
	files      []delivery.File
}

func (f *recordingForge) Available() bool { return f.available }

func (f *recordingForge) Open(
	_ context.Context,
	repository, branch, title, body string,
	files []delivery.File,
	author string,
) (delivery.Change, error) {
	f.opened = true
	f.repository = repository
	f.branch = branch
	f.title = title
	f.body = body
	f.files = files

	return delivery.Change{
		ID:     "1",
		Title:  title,
		Branch: branch,
		URL:    "https://github.test/" + repository + "/pull/1",
		Author: author,
		State:  "open",
	}, nil
}

func (f *recordingForge) Changes(context.Context, string) ([]delivery.Change, error) {
	return nil, nil
}

type refusing struct{}

func (refusing) Admits(context.Context, string, string) (bool, []delivery.Finding, error) {
	return false, []delivery.Finding{{
		Severity: "block",
		Check:    "disallow-root",
		Message:  "the chart defaults to runAsRoot, which the cluster refuses",
	}}, nil
}

type charts struct{}

func (charts) Available() bool { return true }

func (charts) Charts(context.Context) ([]delivery.Chart, error) {
	return []delivery.Chart{
		{
			Name: "app", Version: "1.4.0", Description: "A plain deployment with a service",
			Reference: "registry.marstack.test/charts/app:1.4.0",
			Values:    map[string]string{"image.tag": "latest", "replicaCount": "2"},
		},
		{
			Name: "postgresql", Version: "16.2.1", Description: "A managed PostgreSQL",
			Reference: "registry.marstack.test/charts/postgresql:16.2.1",
			Values:    map[string]string{"auth.database": "", "primary.persistence.size": "10Gi"},
		},
	}, nil
}

func newDeliveryService(t *testing.T, _ delivery.Forge) *delivery.Service {
	t.Helper()

	return delivery.NewService(newDeliveryCluster(t), delivery.NewCatalogue(charts{}))
}

func newDeliveryCluster(t *testing.T) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("govern scheme: %v", err)
	}

	division := &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{Name: "payments"},
		Spec: governv1alpha1.DivisionSpec{
			DisplayName:  "Payments",
			Environments: []string{"dev", "staging"},
		},
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(division).Build()
}

func signedIn(t *testing.T) context.Context {
	t.Helper()

	return identity.NewContext(t.Context(), identity.Actor{
		Subject:        "dev@marstack.test",
		ActiveDivision: "payments",
	})
}

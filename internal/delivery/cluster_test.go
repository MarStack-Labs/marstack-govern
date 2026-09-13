package delivery_test

import (
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marstack-labs/marstack-govern/internal/delivery"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

const envContext = "GOVERN_TEST_KUBE_CONTEXT"

func TestAgainstARealArgo(t *testing.T) {
	kubeContext := os.Getenv(envContext)
	if kubeContext == "" {
		t.Skipf("set %s to a cluster running Argo CD to exercise this", envContext)
	}

	namespace := os.Getenv("GOVERN_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "payments-dev"
	}

	api := clusterClient(t, kubeContext)
	argo := delivery.NewArgo(api)

	if !argo.Available() {
		t.Fatal("Argo CD is installed but the platform cannot see its Application kind")
	}

	workload := os.Getenv("GOVERN_TEST_WORKLOAD")
	if workload == "" {
		workload = "guestbook-ui"
	}

	application, found, err := argo.ForWorkload(t.Context(), namespace, "Deployment", workload)
	if err != nil {
		t.Fatalf("look up %s/%s: %v", namespace, workload, err)
	}
	if !found {
		t.Fatalf("Argo manages %s/%s but the platform could not match it to an application",
			namespace, workload)
	}

	if application.Name == "" {
		t.Error("the application came back unnamed")
	}
	if application.SyncStatus == "" {
		t.Error("the application came back with no sync status")
	}
	if application.DestNamespace != namespace {
		t.Errorf("destination: got %s, want %s", application.DestNamespace, namespace)
	}
	if application.RepoURL == "" || application.SyncedRevision == "" {
		t.Errorf("the application cannot say where it came from: repo=%q revision=%q",
			application.RepoURL, application.SyncedRevision)
	}

	t.Logf("%s sync=%s health=%s target=%s revision=%s resources=%d",
		application.Name, application.SyncStatus, application.HealthStatus,
		application.TargetRevision, application.SyncedRevision, len(application.Resources))
}

func clusterClient(t *testing.T, kubeContext string) client.Client {
	t.Helper()

	scheme, err := tenancy.NewScheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}

	_, restConfig, err := kube.NewClient(kube.ClientConfig{Context: kubeContext})
	if err != nil {
		t.Fatalf("connect to %s: %v", kubeContext, err)
	}

	api, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	return api
}

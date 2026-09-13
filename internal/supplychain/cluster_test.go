package supplychain_test

import (
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/supplychain"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

const envContext = "GOVERN_TEST_KUBE_CONTEXT"

func TestAgainstARealTrivy(t *testing.T) {
	kubeContext := os.Getenv(envContext)
	if kubeContext == "" {
		t.Skipf("set %s to a cluster running the Trivy operator to exercise this", envContext)
	}

	namespace := os.Getenv("GOVERN_TEST_SCANNED_NAMESPACE")
	if namespace == "" {
		namespace = "kube-system"
	}

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

	scanner := supplychain.NewScanner(api)

	if !scanner.Available() {
		t.Fatal("the Trivy operator is installed but the platform cannot see its report kind")
	}

	found, err := scanner.Vulnerabilities(t.Context(), namespace, "")
	if err != nil {
		t.Fatalf("read vulnerabilities in %s: %v", namespace, err)
	}

	if !found.Found {
		t.Fatalf("the operator has written reports in %s but the platform counted none", namespace)
	}

	total := found.Critical + found.High + found.Medium + found.Low
	if total == 0 {
		t.Log("reports exist and every image in this namespace is clean")
	}
	if found.ScannedAt.IsZero() {
		t.Error("the counts came back without a scan time, so freshness cannot be shown")
	}

	t.Logf("%s: critical=%d high=%d medium=%d low=%d scanned=%s",
		namespace, found.Critical, found.High, found.Medium, found.Low,
		found.ScannedAt.Format("2006-01-02T15:04:05Z"))
}

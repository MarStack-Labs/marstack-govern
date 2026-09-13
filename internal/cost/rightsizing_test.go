package cost_test

import (
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-govern/internal/cost"
)

func TestAWorkloadAskingForTenTimesWhatItUsesIsFlagged(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "w-1", Namespace: "payments-dev", Name: "ledger",
				CPUMillicores: 4000, MemoryBytes: 8 << 30},
		},
		map[string]cost.Observed{
			"payments-dev/ledger": {CPUMillicores: 400, MemoryBytes: 1 << 30},
		},
		standardPolicy(t),
	)

	if len(proposals) != 1 {
		t.Fatalf("got %d proposals, want the oversized request questioned", len(proposals))
	}

	proposal := proposals[0]
	if proposal.ProposedCPUMillicores != 500 {
		t.Errorf("cpu: got %dm, want the p95 plus a quarter", proposal.ProposedCPUMillicores)
	}
	if proposal.ProposedMemoryBytes != int64(float64(1<<30)*1.25) {
		t.Errorf("memory: got %d", proposal.ProposedMemoryBytes)
	}
	if proposal.MonthlySaving.IsZero() {
		t.Error("a proposal was made with no money attached to it")
	}
}

func TestAWorkloadSizedCorrectlyIsLeftAlone(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "w-1", Namespace: "payments-dev", Name: "api",
				CPUMillicores: 500, MemoryBytes: 1 << 30},
		},
		map[string]cost.Observed{
			"payments-dev/api": {CPUMillicores: 450, MemoryBytes: 900 << 20},
		},
		standardPolicy(t),
	)

	if len(proposals) != 0 {
		t.Fatalf("a well-sized workload was told to shrink: %+v", proposals[0])
	}
}

func TestAWorkloadNeverObservedIsNotGuessedAt(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "w-1", Namespace: "payments-dev", Name: "batch",
				CPUMillicores: 4000, MemoryBytes: 8 << 30},
		},
		map[string]cost.Observed{},
		standardPolicy(t),
	)

	if len(proposals) != 0 {
		t.Fatal("a workload with no usage was told to shrink anyway")
	}
}

func TestAProposalNeverFallsBelowTheFloor(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "w-1", Namespace: "payments-dev", Name: "idle",
				CPUMillicores: 2000, MemoryBytes: 4 << 30},
		},
		map[string]cost.Observed{
			"payments-dev/idle": {CPUMillicores: 1, MemoryBytes: 1024},
		},
		standardPolicy(t),
	)

	if len(proposals) != 1 {
		t.Fatalf("got %d proposals", len(proposals))
	}
	if proposals[0].ProposedCPUMillicores < 50 {
		t.Errorf("cpu: got %dm, below the floor", proposals[0].ProposedCPUMillicores)
	}
	if proposals[0].ProposedMemoryBytes < 64*1024*1024 {
		t.Errorf("memory: got %d, below the floor", proposals[0].ProposedMemoryBytes)
	}
}

func TestTheBiggestSavingIsListedFirst(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "small", Namespace: "payments-dev", Name: "small",
				CPUMillicores: 1000, MemoryBytes: 2 << 30},
			{UID: "large", Namespace: "payments-dev", Name: "large",
				CPUMillicores: 16000, MemoryBytes: 32 << 30},
		},
		map[string]cost.Observed{
			"payments-dev/small": {CPUMillicores: 100, MemoryBytes: 256 << 20},
			"payments-dev/large": {CPUMillicores: 200, MemoryBytes: 512 << 20},
		},
		standardPolicy(t),
	)

	if len(proposals) != 2 {
		t.Fatalf("got %d proposals", len(proposals))
	}
	if proposals[0].Name != "large" {
		t.Fatalf("the smaller saving was listed first: %s", proposals[0].Name)
	}
}

func TestTheProposalCarriesThePatchThatAppliesIt(t *testing.T) {
	proposals := cost.Propose(
		[]cost.WorkloadRequest{
			{UID: "w-1", Namespace: "payments-dev", Name: "ledger",
				CPUMillicores: 4000, MemoryBytes: 8 << 30},
		},
		map[string]cost.Observed{
			"payments-dev/ledger": {CPUMillicores: 400, MemoryBytes: 1 << 30},
		},
		standardPolicy(t),
	)

	patch := proposals[0].Patch()
	for _, want := range []string{`"ledger"`, `"500m"`, `"requests"`} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch is missing %s: %s", want, patch)
		}
	}
}

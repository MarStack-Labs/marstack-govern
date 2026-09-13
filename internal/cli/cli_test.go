package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-govern/internal/version"
)

func TestVersionCommandPrintsVersion(t *testing.T) {
	var out bytes.Buffer

	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	got := strings.TrimSpace(out.String())
	if got != version.String() {
		t.Fatalf("got %q, want %q", got, version.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out bytes.Buffer

	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"nonexistent"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}

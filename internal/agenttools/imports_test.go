package agenttools_test

// The assistant's file tools reach a filesystem through the pane's own binding
// and nothing else (stage 10, ADR-anntnxdj §5). The mechanical half of that
// claim: neither internal/agenttools nor internal/assistant imports
// internal/filesystem/local. If either did, some path in the tool pipeline
// could name this machine's disk directly — which is exactly the leak the stage
// closes, and exactly what the constructors did before it.
//
// `go list -deps` without -test is deliberate: the TESTS here do use the local
// provider, as a real provider to stand behind a registry, and that is the
// fixture rather than a route the product has.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const localProvider = "github.com/shady2k/nocx/internal/filesystem/local"

func TestAgentPackagesDoNotImportTheLocalProvider(t *testing.T) {
	root := moduleRoot(t)
	for _, pkg := range []string{
		"github.com/shady2k/nocx/internal/agenttools",
		"github.com/shady2k/nocx/internal/assistant",
	} {
		cmd := exec.Command("go", "list", "-deps", pkg) // #nosec G204 — a literal and a fixed package path
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, output)
		}
		for _, dep := range strings.Fields(string(output)) {
			if dep == localProvider {
				t.Errorf("%s reaches %s; the file tools must act only through the pane's binding", pkg, localProvider)
			}
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD") // #nosec G204 — a fixed literal
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD returned %q", gomod)
	}
	return filepath.Dir(gomod)
}

package agenttools

// Which machine a file tool acts on is a property of the BINDING the transport
// minted for the run — never of the pane's kind (stage 10, ADR-anntnxdj §5).
// The constructor's own guarantee is the one left here: a run that carries no
// binding gets no capability, so nothing can fall back to this machine's disk.
//
// These tests are the constructor's half. That an SSH pane's binding reaches
// that server is asserted where the binding is minted and used
// (internal/transport's acceptance test) and end to end in
// e2e/assistant-ssh-files.spec.ts.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// TestFileToolsBuildOnAPaneWithABinding is the paired half of
// TestFileToolsBuildNoCapabilityWithoutAFileBinding: with a binding present the
// five build the capability they always did. Without it, "no capability" could
// be satisfied by a constructor that refuses everything.
func TestFileToolsBuildOnAPaneWithABinding(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "ordinary.txt")
	if err := os.WriteFile(file, []byte("here\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := expiryFixturePolicy(root).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	bound := RunContext{Files: newTestFileSource(t)}

	for _, name := range []string{"files.read", "files.list", "files.search", "files.edit", "files.create"} {
		t.Run(name, func(t *testing.T) {
			tool, ok := reg.Lookup(name)
			if !ok {
				t.Fatalf("%s is not in the assembled registry", name)
			}
			capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: file}}, bound)
			if err != nil {
				t.Fatalf("Narrow with the pane's binding: %v", err)
			}
			if capability == nil {
				t.Fatal("Narrow returned no capability with the pane's binding")
			}
		})
	}
}

// TestFileToolsBuildNoCapabilityWithoutAFileBinding is the fail-closed shape
// stage 10 keeps: a tool that cannot prove it has a route to the pane's files
// must not fall back to this machine's disk, which is exactly what the old
// local.New() constructors did.
func TestFileToolsBuildNoCapabilityWithoutAFileBinding(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "ordinary.txt")
	if err := os.WriteFile(file, []byte("here\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := expiryFixturePolicy(root).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	for _, name := range []string{"files.read", "files.list", "files.search", "files.edit", "files.create"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the assembled registry", name)
		}
		capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: file}}, RunContext{})
		if err == nil {
			t.Fatalf("%s built a capability with no file binding", name)
		}
		if capability != nil {
			t.Fatalf("%s returned a capability with no file binding", name)
		}
	}
}

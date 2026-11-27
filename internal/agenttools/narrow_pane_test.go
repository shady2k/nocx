package agenttools

// The file tools are built on THIS machine's filesystem, so a pane on another
// machine gets no capability at all (ADR-anntnxdj §5, decision 3: until stage
// 8 they refuse, say the remote file tools are coming, and stay declared). The
// constructor refusing is the guarantee — no capability object exists, so
// nothing can be read or written here — and ReachesLocalDisk is the same fact
// read by the guard that turns it into the model's answer. These tests hold
// the two together.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// paneEnvironment is where a run's pane is, as the admitting adapter derives
// it: this machine, or a host reached over ssh.
func thisMachine() content.Environment {
	return content.Environment{ID: content.EnvironmentIDFor(content.EnvLocal, ""), Kind: content.EnvLocal}
}

func anotherMachine() content.Environment {
	const endpoint = "deploy@build-01:22"
	host := endpoint
	return content.Environment{ID: content.EnvironmentIDFor(content.EnvSSH, endpoint), Kind: content.EnvSSH, Endpoint: &host}
}

// TestFileToolsRefuseAPaneThatIsNotThisMachine is the criterion at the
// constructor: each of the three refuses to build a capability, so there is
// nothing to read or write with.
func TestFileToolsRefuseAPaneThatIsNotThisMachine(t *testing.T) {
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
	remote := RunContext{Environment: anotherMachine()}

	for _, name := range []string{"files.read", "files.list", "files.search", "files.edit", "files.create"} {
		t.Run(name, func(t *testing.T) {
			tool, ok := reg.Lookup(name)
			if !ok {
				t.Fatalf("%s is not in the assembled registry", name)
			}
			capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: file}}, remote)
			if !errors.Is(err, ErrRemoteFileTools) {
				t.Fatalf("Narrow on a pane that is not this machine: err = %v, want ErrRemoteFileTools", err)
			}
			if capability != nil {
				t.Fatal("Narrow returned a capability for a pane that is not this machine")
			}
		})
	}
}

// And the paired half, so "refuses a remote pane" cannot be satisfied by a
// constructor that refuses everything: the same call on this machine, with the
// pane's file binding present, builds the capability it always did.
func TestFileToolsBuildOnThisMachine(t *testing.T) {
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
	here := RunContext{Environment: thisMachine(), Files: newTestFileSource(t)}

	for _, name := range []string{"files.read", "files.list", "files.search", "files.edit", "files.create"} {
		t.Run(name, func(t *testing.T) {
			tool, ok := reg.Lookup(name)
			if !ok {
				t.Fatalf("%s is not in the assembled registry", name)
			}
			capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: file}}, here)
			if err != nil {
				t.Fatalf("Narrow on this machine: %v", err)
			}
			if capability == nil {
				t.Fatal("Narrow returned no capability on this machine")
			}
		})
	}
}

// TestFileToolsBuildNoCapabilityWithoutAFileBinding is the other guard: a pane
// on this machine whose run carries no Files source gets no capability either.
// It is the fail-closed shape stage 10 keeps — a tool that cannot prove it has a
// route to the pane's files must not fall back to this machine's disk, which is
// exactly what the old local.New() constructors did.
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
	unbound := RunContext{Environment: thisMachine()}
	for _, name := range []string{"files.read", "files.list", "files.search", "files.edit", "files.create"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the assembled registry", name)
		}
		capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: file}}, unbound)
		if err == nil {
			t.Fatalf("%s built a capability with no file binding", name)
		}
		if capability != nil {
			t.Fatalf("%s returned a capability with no file binding", name)
		}
	}
}

// TestReachesLocalDiskAgreesWithTheConstructors keeps the guard's list and the
// constructors' refusals one fact. A file tool added later, or a constructor
// that starts acting on this machine, fails here rather than in the product:
// the model would be told a call ran that cannot run, or refused a call that
// would have worked.
func TestReachesLocalDiskAgreesWithTheConstructors(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := expiryFixturePolicy(t.TempDir()).AsGrant(nil)
	remote := RunContext{Environment: anotherMachine()}
	executable := 0
	for _, tool := range reg.All() {
		if tool.Narrow == nil {
			continue
		}
		executable++
		_, err := tool.Narrow(grant, nil, remote)
		if refused := errors.Is(err, ErrRemoteFileTools); refused != ReachesLocalDisk(tool.Name) {
			t.Errorf("%s: refused a pane that is not this machine = %t, ReachesLocalDisk = %t; the guard and the constructor must name the same tools",
				tool.Name, refused, ReachesLocalDisk(tool.Name))
		}
	}
	if executable == 0 {
		t.Fatal("no executable declaration assembled: the assertion above proved nothing")
	}
}

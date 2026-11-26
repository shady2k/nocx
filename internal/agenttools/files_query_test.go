package agenttools

// files.list and files.search are the read-side pair that arrived last in
// stage 10 (leaf .11.3): the assistant lists a directory and searches file
// CONTENTS, and both act through the pane's own binding like files.read.
//
// What is asserted here is the declaration itself, because a tool's effect,
// its resource and its output trust are the three facts the policy layer, the
// ledger and the frame marker read — and a row that named the wrong effect
// would be approved as an observation while it reached a file.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
)

func TestFilesQueryToolsDeclareTheObservationTheyPerform(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, name := range []string{"files.list", "files.search"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the assembled registry", name)
		}
		decl := tool.Declaration
		if !slices.Equal(decl.Effect, []content.Effect{content.EffectObserve}) {
			t.Errorf("%s effects = %v, want [observe]: a listing and a search change nothing", name, decl.Effect)
		}
		if decl.OutputTrust != OutputTrustUntrusted {
			t.Errorf("%s output trust = %q, want untrusted: the text it returns comes from files", name, decl.OutputTrust)
		}
		if !slices.Equal(decl.ResourceKinds, []content.ResourceKind{content.ResourcePath}) {
			t.Errorf("%s resource kinds = %v, want [path]", name, decl.ResourceKinds)
		}
		if decl.ResolveResources == nil {
			t.Errorf("%s resolves no resources: its path argument is what the grant is checked against", name)
		}
		if decl.Executes != InGo {
			t.Errorf("%s executes %q, want InGo", name, decl.Executes)
		}
		if !decl.ResultBound.Valid() || decl.ResultBound.MaxBytes != 64<<10 || decl.ResultBound.Truncation != TruncationDropTail {
			t.Errorf("%s result bound = %+v, want the declared 64 KiB drop-tail window every file tool has", name, decl.ResultBound)
		}
		if decl.Deadline <= 0 || decl.Deadline > 60*time.Second {
			t.Errorf("%s deadline = %v, want a declared per-call deadline", name, decl.Deadline)
		}
		if len(tool.ParamsSchema) == 0 || len(tool.ResultSchema) == 0 {
			t.Errorf("%s assembled no params or result contract", name)
		}
		// The stage's own rule: both are local-disk tools until .11.4 removes
		// the stub, and the guard that phrases the refusal reads this list.
		if !ReachesLocalDisk(name) {
			t.Errorf("%s is not in ReachesLocalDisk before .11.4, so the guard and the constructor disagree", name)
		}
	}
}

// TestFilesQueryToolsNarrowToTheScopedReader keeps the capability type the
// executor asserts: these two tools share files.read's narrowing, because
// they share its authority — read within the grant's paths.
func TestFilesQueryToolsNarrowToTheScopedReader(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := expiryFixturePolicy(root).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	for _, name := range []string{"files.list", "files.search"} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the assembled registry", name)
		}
		capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: root}}, RunContext{
			Environment: thisMachine(),
			Files:       staticFileSource{},
		})
		if err != nil {
			t.Fatalf("Narrow %s: %v", name, err)
		}
		if _, ok := capability.(*filesystem.ScopedReader); !ok {
			t.Fatalf("%s capability is %T, want *filesystem.ScopedReader", name, capability)
		}
		// The same constructor refuses a pane that is not this machine: the
		// stub is removed by one leaf (.11.4), not by one tool at a time.
		if _, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: root}}, RunContext{
			Environment: anotherMachine(),
			Files:       staticFileSource{},
		}); !errors.Is(err, ErrRemoteFileTools) {
			t.Fatalf("%s on a remote pane: err = %v, want ErrRemoteFileTools", name, err)
		}
		// And it fails closed with no binding at all.
		if _, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: root}}, RunContext{
			Environment: thisMachine(),
		}); err == nil {
			t.Fatalf("%s built a capability with no file binding", name)
		}
	}
}

// staticFileSource is a HandleSource that is never acquired by these tests:
// they assert the capability's TYPE, and a constructor that acquired anything
// would be building authority at assembly time.
type staticFileSource struct{}

func (staticFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return nil, nil, errors.New("staticFileSource: not acquired in these tests")
}

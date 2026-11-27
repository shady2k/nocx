package helperfs_test

// The edit path's LAST fence, at the seam the assistant writes through: the
// editor reads the file whole, patches those bytes in memory, and commits with
// the version it read. A change that lands between that read and the rename is
// refused by the HOST — nothing is installed, and the outsider's bytes are what
// remains. That is stage 10's second criterion (.11.4, C27) and the reason the
// conditional write is the host's rather than the coordinator's: a compare here
// would leave exactly the window it exists to close.
//
// The interposition is the host service's own call boundary, so the write
// arrives from "another process" at the one moment that matters, and both ends
// are the shipped ones: helperfs over a real helper client, hostsvc over the
// real write path, and filesystem.ScopedEditor deciding what to send.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestTheEditorsCommitRefusesAChangeThatLandsBeforeTheRename is C27's fence
// over helperfs + in-process hostsvc.
func TestTheEditorsCommitRefusesAChangeThatLandsBeforeTheRename(t *testing.T) {
	root := tempTree(t)
	path := filepath.Join(root, "deploy.conf")
	writeFile(t, path, []byte("before\n"))
	const outsider = "third-party wrote this\n"

	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root)))}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	bindings := filesystem.New()
	id, err := bindings.Register(p, "s1", "v1:abc", filesystem.Capabilities{
		Axis:      p.HelperAxis,
		Versioned: p.Versioned(),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = bindings.Close(id) })
	source := bindingSource{reg: bindings, id: id}

	ctx := testContext(t)
	snapshot, err := filesystem.NewScopedReaderWithExactFiles(source, []string{root}, []string{path}).
		ReadSnapshot(ctx, path, filesystem.MaxReadBytes)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}

	// The outsider writes the same path between the editor's read and its
	// commit — the interval a person's approval can be open across.
	svc.intercept = func(ctx context.Context, op string, params json.RawMessage, next func() (any, error)) (any, error) {
		if op == proto.OpWriteCommit {
			writeFile(t, path, []byte(outsider))
		}
		return next()
	}

	_, err = filesystem.NewScopedEditorWithExactFiles(source, []string{root}, []string{path}).
		Edit(ctx, path, snapshot.Revision, "PUT 1.=1:\n+after")
	var changed *filesystem.ErrChangedSinceRead
	if !errors.As(err, &changed) {
		t.Fatalf("Edit = %v, want the host's *filesystem.ErrChangedSinceRead", err)
	}
	if got := readFile(t, path); string(got) != outsider {
		t.Fatalf("%s holds %q, want the outsider's bytes untouched — a refused commit installs nothing", path, got)
	}
	assertNoTemporaries(t, root)
}

// And the paired half: the same call on an untouched file installs, so
// "refuses when it changed" cannot be satisfied by a commit that always
// refuses.
func TestTheEditorsCommitInstallsWhenNothingChanged(t *testing.T) {
	root := tempTree(t)
	path := filepath.Join(root, "deploy.conf")
	writeFile(t, path, []byte("before\n"))

	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root)))}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	bindings := filesystem.New()
	id, err := bindings.Register(p, "s2", "v1:abc", filesystem.Capabilities{
		Axis:      p.HelperAxis,
		Versioned: p.Versioned(),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = bindings.Close(id) })
	source := bindingSource{reg: bindings, id: id}

	ctx := testContext(t)
	snapshot, err := filesystem.NewScopedReaderWithExactFiles(source, []string{root}, []string{path}).
		ReadSnapshot(ctx, path, filesystem.MaxReadBytes)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	result, err := filesystem.NewScopedEditorWithExactFiles(source, []string{root}, []string{path}).
		Edit(ctx, path, snapshot.Revision, "PUT 1.=1:\n+after")
	if err != nil {
		t.Fatalf("Edit over an unchanged file: %v", err)
	}
	if result.Revision == "" || result.Revision == snapshot.Revision {
		t.Fatalf("revision = %q, want the version the new bytes landed with", result.Revision)
	}
	if got := readFile(t, path); string(got) != "after\n" {
		t.Fatalf("%s holds %q, want the patch applied", path, got)
	}
	assertNoTemporaries(t, root)
}

// bindingSource is the run's short-lived Handle over one registered binding —
// the shape the assistant's transport source has, without the transport.
type bindingSource struct {
	reg *filesystem.Registry
	id  string
}

func (s bindingSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return s.reg.Acquire(s.id, owningEverything{})
}

package app

// The run's route to the pane's files, for the app tests.
//
// These tests drive the real MCP bridge and the real tool chain, and the two
// that call files.read need what the product's transport gives a local run: a
// filesystem.HandleSource over a real binding in a real registry, backed by the
// local provider. A test that bound the tools without one is exactly the shape
// that shipped a files.read with no route at all (nocx-a62cl.11.1) — the
// capability constructor refuses with errNoFileBinding, and the refusal, not
// the read, is what the test then measures.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/session"
)

// localFilesForTest returns a HandleSource over one binding the run's session
// owns: the same registry, provider and versioned capability the transport
// binds for a local pane.
func localFilesForTest(t *testing.T, owner session.ID) filesystem.HandleSource {
	t.Helper()
	provider := local.New()
	reg := filesystem.New()
	bindingID, err := reg.Register(provider, owner, "", filesystem.Capabilities{Versioned: provider.Versioned()})
	if err != nil {
		t.Fatalf("register the local files binding: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(bindingID) })
	return localFilesBinding{reg: reg, bindingID: bindingID, owner: owner}
}

// localFilesBinding acquires the one binding for the one session the run was
// asked in, which is the whole of filesystem.Caller's contract.
type localFilesBinding struct {
	reg       *filesystem.Registry
	bindingID string
	owner     session.ID
}

func (b localFilesBinding) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return b.reg.Acquire(b.bindingID, localFilesOwner{owner: b.owner})
}

type localFilesOwner struct {
	owner session.ID
}

func (o localFilesOwner) Owns(id session.ID) bool { return id == o.owner }

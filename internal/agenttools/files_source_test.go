package agenttools

// The run's route to the pane's files, for the tests: one real binding in the
// package's own Registry over the local provider — the same two objects the
// transport wires for the assistant and for the Files panel. The file tools no
// longer hold a provider, so a test that wants them to act needs exactly what
// the product gives them.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/sessionid"
)

const testFilesSession = sessionid.ID("agenttools-test")

type testFileSource struct {
	reg *filesystem.Registry
	id  string
}

func newTestFileSource(t *testing.T) filesystem.HandleSource {
	t.Helper()
	provider := local.New()
	reg := filesystem.New()
	id, err := reg.Register(provider, testFilesSession, "", filesystem.Capabilities{Versioned: provider.Versioned()})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(id) })
	return &testFileSource{reg: reg, id: id}
}

func (s *testFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return s.reg.Acquire(s.id, testOwns{})
}

type testOwns struct{}

func (testOwns) Owns(id sessionid.ID) bool { return id == testFilesSession }

// readRevision is the revision a files.read of path shows under grant, minted
// through the same capability the tool uses rather than by reading the bytes
// here. It is what an edit test needs: the token an edit is applied under.
func readRevision(t *testing.T, grant content.Grant, path string) string {
	t.Helper()
	capability, err := narrowFilesRead(grant, []ResourceRef{{Kind: content.ResourcePath, ID: path}}, localPane(t))
	if err != nil {
		t.Fatalf("narrowFilesRead: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", capability)
	}
	snapshot, err := reader.ReadSnapshot(context.Background(), path, 64<<10)
	if err != nil {
		t.Fatalf("ReadSnapshot %s: %v", path, err)
	}
	return snapshot.Revision
}

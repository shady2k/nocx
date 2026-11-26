package assistant

// The run's route to the pane's files, for the tests: a real binding in a
// filesystem.Registry over the local provider, which is what the transport
// binds for a local pane. The capability tests are built over this rather than
// over a provider, because a capability no longer holds one.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/hashline"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/sessionid"
)

const testFilesSession = sessionid.ID("assistant-test")

type localFileSource struct {
	reg *filesystem.Registry
	id  string
}

func newLocalFileSource(t *testing.T) filesystem.HandleSource {
	t.Helper()
	provider := local.New()
	reg := filesystem.New()
	id, err := reg.Register(provider, testFilesSession, "", filesystem.Capabilities{Versioned: provider.Versioned()})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(id) })
	return &localFileSource{reg: reg, id: id}
}

func (s *localFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return s.reg.Acquire(s.id, testOwns{})
}

type testOwns struct{}

func (testOwns) Owns(id sessionid.ID) bool { return id == testFilesSession }

// spyFileSource counts acquisitions around a real source, so "nothing was
// acquired" is an assertion rather than an inference: a refusal on a pane that
// is not this machine must not even open the binding.
type spyFileSource struct {
	inner filesystem.HandleSource

	mu    sync.Mutex
	calls int
}

func (s *spyFileSource) Acquire(ctx context.Context) (filesystem.Handle, func(), error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.inner.Acquire(ctx)
}

func (s *spyFileSource) acquisitions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// revisionOf mints the revision a read of path shows, from the provider's
// canonical identity and the file's exact bytes. It is a FIXTURE and not a
// second implementation of the edit path: these tests need a token to apply a
// patch under, and taking it through the capability under test would make the
// test depend on the thing it is measuring.
func revisionOf(t *testing.T, path string, maxBytes int64) string {
	t.Helper()
	// A temporary directory is reached through a symlink on macOS, and the
	// provider answers the RESOLVED spelling; taking the canonical identity
	// here is what keeps this fixture right on both platforms.
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	data, err := os.ReadFile(canonical) // #nosec G304 -- a path this test created
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", canonical, err)
	}
	return hashline.Read(canonical, data, maxBytes).Revision
}

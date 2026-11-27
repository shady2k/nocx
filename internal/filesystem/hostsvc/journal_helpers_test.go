package hostsvc

// The journal facts every test in this package needs, in one place.
//
// A Service requires a journal (journal.go), which is the point of the change:
// a write session that left a temporary behind has to be recoverable, and a
// service that could be built without one would be a service whose writes are
// silently unrecoverable. So a test constructs one too — under its own
// temporary directory, never the machine's home — and every service gets its
// own instance id, because two services that shared one would contend for the
// same lock, which is a property only the tests that assert it should arrange.

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem/local"
)

// journalInstanceSeq is the counter behind journalFor's instance ids.
var journalInstanceSeq atomic.Uint64

// journalFor is one test's journal configuration: a fresh root inside its own
// temporary directory, and an instance id unique to this construction.
func journalFor(t *testing.T) JournalConfig {
	t.Helper()
	return JournalConfig{
		Root:       t.TempDir(),
		Hostname:   "test-host",
		InstanceID: fmt.Sprintf("%032x", journalInstanceSeq.Add(1)),
	}
}

// newTestService builds the shipped service over provider with a journal of its
// own, and closes it at the end of the test: Close is what removes the journal
// directory, and a test that left it open would leave the lock held until the
// process exits.
func newTestService(t *testing.T, provider *local.Provider) *Service {
	t.Helper()
	svc, err := New(provider, journalFor(t))
	if err != nil {
		t.Fatalf("new fs service: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the fs service: %v", err)
		}
	})
	return svc
}

package app

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/storage"
)

type failedModeDocument struct{ storage.DocumentStore }

func (failedModeDocument) Write(string, any) error { return errors.New("disk full") }

// The connect-time answer grants first, then retains a failed mode write for
// this run. Reopening both stores loses only the mode, not machine consent.
func TestResolver_ReopenedAutoReusesGrantAfterFailedHostModeWrite(t *testing.T) {
	_, logger := logtest.New(t)
	dir := t.TempDir()
	docs := storage.NewDocumentStore(dir)
	grants := consent.NewStore(logger, docs, "consent.json")
	modes := hostmode.NewStore(logger, failedModeDocument{docs}, "host-modes.json")
	const fp = "SHA256:partial"
	if err := grants.Grant(fp); err != nil {
		t.Fatal(err)
	}
	if err := modes.Set(fp, hostmode.Helper); !errors.Is(err, hostmode.ErrNotSaved) {
		t.Fatalf("mode write = %v", err)
	}
	reopenedModes := hostmode.NewStore(logger, storage.NewDocumentStore(dir), "host-modes.json")
	mode, ok := reopenedModes.Lookup(fp)
	if ok {
		t.Fatalf("mode unexpectedly persisted: %q", mode)
	}
	reopenedGrants := consent.NewStore(logger, storage.NewDocumentStore(dir), "consent.json")
	resolver := newResolver(withStore(reopenedGrants), withHelperArtifactAvailable(true))
	if got := resolver.Resolve(Machine{Fingerprint: fp, Mode: mode}); got != DesiredHelper {
		t.Fatalf("reopened auto = %q, want stored grant reused without ask", got)
	}
	if got := resolver.Resolve(Machine{Fingerprint: "SHA256:other", Mode: hostmode.Auto}); got != ConsentRequired {
		t.Fatalf("unanswered machine = %q, want consent ask", got)
	}
}

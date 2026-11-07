package transport

// nocx-3an1s.2.17, finding 1. fs.complete read the local filesystem with no
// failure-domain bulkhead. A renderer that supersedes a keystroke withdraws
// the request and its credit returns, so on a hung mount every further
// keystroke started another worker that never returned: the abandoned set
// had no bound, and no call was ever answered.
//
// The check is the one ADR-8eej3e66 §6 names: the stuck workers of one
// domain are bounded, and a call refused because that domain is suspect
// settles at once with an answer naming the domain. The block is injected
// through the server's directory-read seam, the way the filesystem tests
// hold a mount open with a provider whose List never returns.

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/operation/domains"
	"github.com/shady2k/nocx/internal/waittest"
)

type stubDomainNamer struct{ domain filesystem.Domain }

func (n stubDomainNamer) DomainKey(string) filesystem.Domain { return n.domain }

func TestFsCompleteStuckWorkersOnOneDomainAreBoundedAndTheRefusedOneAnswers(t *testing.T) {
	release := make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(unblock)

	var calls atomic.Int32
	heldReadDir := func(string) ([]os.DirEntry, error) {
		calls.Add(1)
		<-release
		return nil, os.ErrNotExist
	}

	_, logger := logtest.New(t)
	srv := NewWSServer(logger, newRegWithStub(logger),
		func(s *WSServer) { s.fsCompleteReadDir = heldReadDir },
		WithLocalFilesystemDomainNamer(stubDomainNamer{
			domain: filesystem.Domain{Kind: "mount", ID: "hung"},
		}),
	)
	// Two permits and one abandoned worker enough to make the domain
	// suspect: the production bounds are eight and four, which would take
	// four synthetic hung workers to reach, and this test's claim is the
	// bound's shape, not its measured size.
	srv.filesDomains = domains.New(domains.Options{
		PerDomain: 2, PersonReserve: 0,
		AbandonedPerDomain: 1, AbandonedGlobal: 8, HealthyReserve: 2,
	})
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	conn := connectWS(t, srv)
	t.Cleanup(func() { _ = conn.Close() })

	// Two completions occupy both permits of the one domain and never return.
	writeRequest(t, conn, "fs.complete", map[string]any{"text": "a", "cwd": "/tmp"}, 1)
	writeRequest(t, conn, "fs.complete", map[string]any{"text": "a", "cwd": "/tmp"}, 2)
	waittest.WaitFor(t, "both completions inside the domain", func() bool {
		return calls.Load() == 2
	})

	// Withdraw both: the renderer frees the credit immediately, which is the
	// behaviour that let keystrokes spawn unbounded workers.
	if raw := jsonrpcCallWithID(t, conn, "rpc.cancel", map[string]any{"id": 1}, 10); len(raw) == 0 {
		t.Fatal("rpc.cancel answered nothing")
	}
	if raw := jsonrpcCallWithID(t, conn, "rpc.cancel", map[string]any{"id": 2}, 11); len(raw) == 0 {
		t.Fatal("rpc.cancel answered nothing")
	}
	if err := waitForFilesDomains(t, srv, func(s domains.Stats) bool {
		return s.Running == 2 && s.Abandoned == 2
	}); err != nil {
		t.Fatal(err)
	}

	// The next keystroke must be answered by the suspect domain instead of
	// starting a third worker that can never return.
	writeRequest(t, conn, "fs.complete", map[string]any{"text": "a", "cwd": "/tmp"}, 3)
	frame, err := awaitFrame(conn, time.Now().Add(2*time.Second), isResponseTo(3))
	if err != nil {
		t.Fatalf("a completion on a suspect domain answered nothing: %v", err)
	}
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatalf("a completion on a suspect domain answered success: %s", frame)
	}
	if envelope.Error.Code != OperationExpiredCode {
		t.Fatalf("refusal code = %d, want %d", envelope.Error.Code, OperationExpiredCode)
	}
	data, _ := envelope.Error.Data.(map[string]any)
	if data["reason"] != "not-started" {
		t.Fatalf("refusal reason = %v, want not-started", data["reason"])
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("the refused completion started worker %d; the domain's permits bound it at 2", got)
	}

	// Release the hung workers: the domain's accounting unwinds.
	unblock()
	if err := waitForFilesDomains(t, srv, func(s domains.Stats) bool {
		return s.Running == 0 && s.Abandoned == 0
	}); err != nil {
		t.Fatal(err)
	}
}

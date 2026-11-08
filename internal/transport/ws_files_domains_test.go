package transport

// Stage 4 of ADR-8eej3e66 on the filesystem domain: a hung mount costs its own
// failure domain and nothing else, the declared turn is enforced, and the
// resources a repeated-timeout episode holds are bounded.

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/domains"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// mountProvider wraps a real local provider with a stated failure domain and,
// for the hung mount, a listing that never returns. The domain is stated
// rather than read from the machine's mount table because the test needs two
// mounts that fail independently on one disk, and the production namer
// (local.Provider.DomainKey) has its own test.
type mountProvider struct {
	filesystem.Provider
	domain string
	list   func(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error)
}

func (p *mountProvider) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	if p.list != nil {
		return p.list(ctx, path, page)
	}
	return p.Provider.List(ctx, path, page)
}

// DomainKey is the failure-domain seam (filesystem.DomainNamer).
func (p *mountProvider) DomainKey(string) filesystem.Domain {
	return filesystem.Domain{Kind: "mount", ID: p.domain}
}

// mountProviderFactory builds the test's two mounts: hungRoot lists only once
// release is closed, every other root lists normally. Both are real local
// providers underneath, so files.open's root acquisition is real.
func mountProviderFactory(hungRoot string, release <-chan struct{}) FilesystemProviderFactory {
	hung := make(chan struct{})
	var once sync.Once
	wait := func() {
		once.Do(func() { <-release })
		_ = hung
	}
	return func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		if sess.Kind() != session.KindLocal {
			return nil, fmt.Errorf("remote filesystems are not available in this test")
		}
		opts := []local.Option{}
		if rootPath != "" {
			opts = append(opts, local.WithRoot(rootPath))
		}
		base := local.New(opts...)
		if rootPath == hungRoot {
			return &mountProvider{
				Provider: base,
				domain:   "hung-mount",
				list: func(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
					wait()
					return base.List(ctx, path, page)
				},
			}, nil
		}
		return &mountProvider{Provider: base, domain: "mount:" + rootPath}, nil
	}
}

// writeRequest sends one request frame without waiting for its answer. The
// connection's inbound frames have one owner (ws_inbox_test.go), so a call
// whose answer this test does not want must not start a second reader.
func writeRequest(t *testing.T, conn *websocket.Conn, method string, params any, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}

// filesListState answers the discriminating state of a files.list result.
func filesListState(t *testing.T, raw json.RawMessage) (string, map[string]any) {
	t.Helper()
	var envelope struct {
		Result map[string]any   `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode files.list: %v\nraw: %s", err, raw)
	}
	if envelope.Error != nil {
		t.Fatalf("files.list: %+v", envelope.Error)
	}
	state, _ := envelope.Result["state"].(string)
	return state, envelope.Result
}

// A shared SFTP lease can reach its watchdog before this invocation's own
// deadline. The cause must cross the wire as the Files timeout state, with
// no partial rows, while another binding still lists successfully.
func TestFilesListLeaseWatchdogAnswersTimedOut(t *testing.T) {
	blockedRoot := t.TempDir()
	healthyRoot := t.TempDir()
	factory := func(_ session.Session, root string) (filesystem.Provider, error) {
		provider := &mountProvider{
			Provider: local.New(local.WithRoot(root)),
			domain:   root,
		}
		if root == blockedRoot {
			provider.list = func(context.Context, string, filesystem.Page) (filesystem.Listing, error) {
				return filesystem.Listing{}, ssh.ErrFSDead
			}
		}
		return provider, nil
	}
	env := newFilesTestEnv(t, WithFilesystemProviderFactory(factory))
	sid := env.openSession(t, 1)
	blocked := env.openBinding(t, sid, blockedRoot, 2)
	healthy := env.openBinding(t, sid, healthyRoot, 3)

	state, result := filesListState(t, jsonrpcCallWithID(t, env.conn, "files.list", map[string]any{
		"bindingId": blocked, "path": blockedRoot, "offset": 0, "limit": 1,
	}, 4))
	if state != "timedOut" {
		t.Fatalf("watchdog expiry answered %q, want timedOut", state)
	}
	if result["timeout"] != float64(catalog.OpFilesList.Declaration().Max.Millis) {
		t.Fatalf("timeout = %v, want the declared file-list bound", result["timeout"])
	}
	if _, partial := result["entries"]; partial {
		t.Fatal("expired listing returned partial rows")
	}
	state, _ = filesListState(t, jsonrpcCallWithID(t, env.conn, "files.list", map[string]any{
		"bindingId": healthy, "path": healthyRoot, "offset": 0, "limit": 1,
	}, 5))
	if state != "ok" {
		t.Fatalf("healthy binding answered %q beside the expired lease", state)
	}
}

// TestFilesOneBlockedMountDoesNotHoldUnrelatedWork is the check the constraint
// table names for this stage (ADR-8eej3e66, "Unrelated work completes beside a
// blocked domain"): one mount blocked for ever, another mount healthy, and
// listings and session work complete within their declared deadlines.
//
// It fails on the shape the record replaces: with one whole-domain filesystem
// gate, the first hung listing held the only permit and the healthy mount's
// listing waited one second and was refused.
func TestFilesOneBlockedMountDoesNotHoldUnrelatedWork(t *testing.T) {
	hungRoot := t.TempDir()
	healthyRoot := t.TempDir()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	env := newFilesTestEnv(t, WithFilesystemProviderFactory(mountProviderFactory(hungRoot, release)))
	// The declared capacity is measured in internal/operation/domains; two
	// here so that two blocked calls saturate the domain and the third must
	// wait, which is the only way to see the turn bound without a thirty
	// second test.
	env.ws.filesDomains = domains.New(domains.Options{
		PerDomain: 2, PersonReserve: 1,
		AbandonedPerDomain: 4, AbandonedGlobal: 8, HealthyReserve: 2,
	})

	sid := env.openSession(t, 1)
	hungBid := env.openBinding(t, sid, hungRoot, 2)
	healthyBid := env.openBinding(t, sid, healthyRoot, 3)
	baselineGoroutines := runtime.NumGoroutine()

	// Two calls that occupy both of the hung mount's permits and never answer
	// until the test releases them. Their responses are never read: this
	// connection's inbound frames have one owner.
	writeRequest(t, env.conn, "files.list", map[string]any{
		"bindingId": hungBid, "path": hungRoot, "offset": 0, "limit": 1,
	}, 100)
	writeRequest(t, env.conn, "files.list", map[string]any{
		"bindingId": hungBid, "path": hungRoot, "offset": 0, "limit": 1,
	}, 101)
	if err := waitForFilesDomains(t, env.ws, func(s domains.Stats) bool { return s.Running == 2 }); err != nil {
		t.Fatal(err)
	}

	// The healthy mount answers while the other is wedged.
	healthyDone := make(chan struct{})
	go func() {
		defer close(healthyDone)
		state, _ := filesListState(t, jsonrpcCallWithID(t, env.conn, "files.list", map[string]any{
			"bindingId": healthyBid, "path": healthyRoot, "offset": 0, "limit": 1,
		}, 102))
		if state != "ok" {
			t.Errorf("healthy mount listing state = %q, want ok", state)
		}
	}()
	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("a listing on a healthy mount did not complete while another mount was blocked")
	}

	// Unrelated work in another domain completes too.
	if raw := jsonrpcCallWithID(t, env.conn, "sessions.live", map[string]any{}, 103); len(raw) == 0 {
		t.Fatal("sessions.live answered nothing")
	}

	// A THIRD CALL ON THE WEDGED MOUNT WAITS FOR ITS TURN, and when that wait
	// ends it answers the declared bound — timedOut, naming the turn it waited
	// under — rather than hanging or being refused at once.
	//
	// THE WAIT IS ENDED HERE BY THE CALLER'S OWN WITHDRAWAL, at one second, and
	// the reason is the repeat budget: a person's turn is thirty seconds
	// (ADR-8eej3e66 §8 and the catalogue's personTurnMillis), so twenty repeats
	// of waiting it out put 600 s of pure sleep into one package binary whose
	// bound is go test's ten minutes — which is how ci-backend's repeat step
	// failed with no test hung. The domain's own expiry is proven where the wait
	// lives (internal/operation/domains,
	// TestATurnDeadlineThatExpiresNeverStartsTheWork, on a one-second bound);
	// what this asserts is the transport's half: the queued call does not
	// answer while it waits, and when the wait ends the answer carries the
	// operation's DECLARED turn rather than a number of its own.
	turn := catalog.OpFilesList.Declaration().Turn.Duration()
	writeRequest(t, env.conn, "files.list", map[string]any{
		"bindingId": hungBid, "path": hungRoot, "offset": 0, "limit": 1,
	}, 104)
	time.Sleep(time.Second)
	// rpc.cancel ends the wait on the domain's permit the same way the turn's
	// expiry does: acquire selects on the turn context, and the transport's
	// answer for that ending is the same timedOut result.
	jsonrpcCallWithID(t, env.conn, "rpc.cancel", map[string]any{"id": 104}, 105)
	raw, ferr := awaitFrame(env.conn, time.Now().Add(wantWithin), isResponseTo(104))
	if ferr != nil {
		t.Fatalf("read the answer to a call whose wait ended: %v", ferr)
	}
	state, result := filesListState(t, raw)
	if state != "timedOut" {
		t.Fatalf("a call on a saturated domain answered %q, want timedOut (%v)", state, result)
	}
	if got, _ := result["timeout"].(float64); int64(got) != turn.Milliseconds() {
		t.Fatalf("the answer names %v ms, want the declared turn %d ms", result["timeout"], turn.Milliseconds())
	}

	// Release the mount: every WORKER the episode held unwinds, and nothing it
	// grew is left behind.
	//
	// A QUEUED CALLER IS NOT A LEFTOVER. A call that arrived while the domain
	// was saturated waits its declared turn — thirty seconds for a person —
	// and this test's own poll traffic and expiry leave one in the queue
	// while the mount unwinds, which is the design (ADR-8eej3e66 §8: it
	// waits, visibly). What this asserts is the accounting the worker owns;
	// the waiter count has its own test where the queue lives
	// (internal/operation/domains, TestAWaitThatIsGrantedLeavesTheWaitingCount).
	unblock()
	if err := waitForFilesDomains(t, env.ws, func(s domains.Stats) bool {
		return s.Running == 0 && s.Abandoned == 0
	}); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeout(t, "goroutines to fall back", 5*time.Second, func() bool {
		return runtime.NumGoroutine() <= baselineGoroutines+8
	})
}

// TestFilesCloseIsBoundedWhenTheMountIsHung is the repaired close path: a
// listing that never returns holds the binding's use-guard, and files.close
// must still answer inside the documented drain bound, naming what it left
// behind. Before stage 4 this waited for ever, which is how a hung disk took
// shutdown with it (ADR-8eej3e66 §15).
func TestFilesCloseIsBoundedWhenTheMountIsHung(t *testing.T) {
	hungRoot := t.TempDir()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	env := newFilesTestEnv(t, WithFilesystemProviderFactory(mountProviderFactory(hungRoot, release)))
	sid := env.openSession(t, 1)
	hungBid := env.openBinding(t, sid, hungRoot, 2)

	writeRequest(t, env.conn, "files.list", map[string]any{
		"bindingId": hungBid, "path": hungRoot, "offset": 0, "limit": 1,
	}, 100)
	if err := waitForFilesDomains(t, env.ws, func(s domains.Stats) bool { return s.Running == 1 }); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp := jsonrpcCallWithID(t, env.conn, "files.close", map[string]any{"bindingId": hungBid}, 101)
	elapsed := time.Since(start)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode files.close: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatal("files.close answered success while a call was still inside the provider")
	}
	if elapsed > 8*time.Second {
		t.Fatalf("files.close took %v; the binding drain is bounded at 5 s", elapsed)
	}
	t.Logf("files.close answered %d after %v: %s", envelope.Error.Code, elapsed.Round(time.Millisecond), envelope.Error.Message)
	unblock()
}

// TestFilesCloseIsCleanOnAHealthyMount is the paired ordinary case: the bound
// is not a new failure on a working binding.
func TestFilesCloseIsCleanOnAHealthyMount(t *testing.T) {
	root := t.TempDir()
	env := newFilesTestEnv(t, WithFilesystemProviderFactory(mountProviderFactory(t.TempDir(), closedChannel())))
	sid := env.openSession(t, 1)
	bid := env.openBinding(t, sid, root, 2)
	resp := jsonrpcCallWithID(t, env.conn, "files.close", map[string]any{"bindingId": bid}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode files.close: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.close: %+v", envelope.Error)
	}
}

func closedChannel() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// waitForFilesDomains waits for the server's failure-domain registry to reach
// a state, and reports the last state it saw if it does not.
func waitForFilesDomains(t *testing.T, ws *WSServer, ok func(domains.Stats) bool) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last domains.Stats
	for time.Now().Before(deadline) {
		last = ws.filesDomains.Stats()
		if ok(last) {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("filesystem domains never reached the wanted state; last = %+v", last)
}

// TestFilesWorkBudgetStartsWhenThePermitIsTaken is finding 2 of
// nocx-3an1s.2.14. ADR-8eej3e66 §3: turn bounds the WAIT and starts at the
// invocation; max bounds the WORK and starts when the work starts, never
// renewed. filesDomainDo derived the work context before the bulkhead took a
// permit, from a deadline measured at the invocation, and never called
// StartWork — so a read granted after 29 s of waiting had about 1 s of its 30 s
// budget left, and expired inside a domain that had just started answering.
//
// The wait here is 1.2 s rather than 29 s for the reason the neighbouring test
// gives: a person's turn is thirty seconds and repeating it puts minutes of
// sleep into one package binary. What is asserted is not a duration but the
// relation — the budget the work receives is the declaration's max measured
// from the moment the permit was granted, and the second call waited.
func TestFilesWorkBudgetStartsWhenThePermitIsTaken(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &WSServer{filesDomains: domains.New(domains.Options{
		PerDomain: 1, PersonReserve: 0,
		AbandonedPerDomain: 4, AbandonedGlobal: 8, HealthyReserve: 2,
	})}

	// Two invocations, both made now: a real second call began before the
	// first one's permit was free.
	heldScope := catalog.Begin(base, catalog.OpFilesList, catalog.Invocation{Origin: catalog.OriginPerson})
	heldCtx := catalog.WithScope(base, heldScope)
	queuedScope := catalog.Begin(base, catalog.OpFilesList, catalog.Invocation{Origin: catalog.OriginPerson})
	queuedCtx := catalog.WithScope(base, queuedScope)
	t.Cleanup(func() {
		heldScope.Settle(catalog.OutcomeCompleted)
		queuedScope.Settle(catalog.OutcomeCompleted)
	})

	release := make(chan struct{})
	holding := make(chan struct{})
	go func() {
		_ = s.filesDomainDo(heldCtx, nil, "/held", func(context.Context) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	started := make(chan time.Duration, 1)
	done := make(chan error, 1)
	waiting := make(chan struct{})
	go func() {
		done <- s.filesDomainDo(queuedCtx, nil, "/queued", func(callCtx context.Context) error {
			close(waiting)
			deadline, ok := callCtx.Deadline()
			if !ok {
				started <- 0
				return nil
			}
			started <- time.Until(deadline)
			return nil
		})
	}()

	// The queued call is provably waiting, not merely slow: the only permit is
	// held by the call above until release closes.
	select {
	case <-waiting:
		t.Fatal("the second call took a permit while the only one was held")
	case <-time.After(1200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the granted call returned %v", err)
	}
	budget := <-started

	max := catalog.OpFilesList.Declaration().Max.Duration()
	if budget < max-500*time.Millisecond {
		t.Fatalf("work that began after waiting 1.2 s received %v of its %v budget: max is measured from the invocation, not from the grant",
			budget.Round(time.Millisecond), max)
	}
	// The scope's own record moved with it, so the timer, the record and the
	// context the provider sees agree: this is the half StartWork owns.
	if deadline := queuedScope.Deadline(); time.Until(deadline) < max-500*time.Millisecond {
		t.Fatalf("the scope's max deadline is %v away after the grant, want %v", time.Until(deadline).Round(time.Millisecond), max)
	}
}

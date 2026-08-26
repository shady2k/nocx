package content_test

// The block's DURABLE anchor (nocx-rtg0.28), design §6.1:
//
//	block.pane_id      the anchor. Durable, and what makes restore possible
//	block.session_id   provenance. Which pipe it ran in; null after it is gone
//
// Both edges, and neither does the other's work. Every assertion here spans a
// RESTART — the store is closed and reopened — because that is the interval
// the two edges disagree over, and reading a column back in the process that
// wrote it cannot see it: a session dies with the backend (D5) and a pane does
// not.

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// aPaneUnder records the layout chain a block hangs on: a workspace, its first
// tab, and the pane the commands run in. The chain is what the FK checks, so
// the fixture builds the real one rather than inserting a bare pane row.
func aPaneUnder(t *testing.T, db content.ContentDB, wsID, tabID, paneID string) {
	t.Helper()
	if _, err := db.Layout().CreateWorkspace(context.Background(),
		content.Workspace{ID: wsID, Name: "work"},
		content.Tab{ID: tabID, WorkspaceID: wsID, Position: 0, Layout: content.LayoutRow},
		content.Pane{ID: paneID, TabID: tabID, Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
}

// entriesForPane is the read the restore path needs: this pane's blocks,
// newest first. It goes through QueryEntries — the ONE ordering implementation
// — rather than a second statement of its own.
func entriesForPane(t *testing.T, led content.LedgerRepository, paneID string) content.LedgerPage {
	t.Helper()
	page, err := led.QueryEntries(context.Background(), content.LedgerQuery{
		Scope: content.ScopeEverywhere, PaneID: paneID, Limit: 50,
	})
	if err != nil {
		t.Fatalf("QueryEntries by pane %q: %v", paneID, err)
	}
	return page
}

// A block written in a pane is still found BY THAT PANE after a full backend
// restart. This is the whole bug: before pane_id existed, the only anchor was
// the session, and the session is gone by the time this second Open returns.
func TestBlockIsFoundByItsPaneAfterARestart(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	const id = "00000000-0000-7000-8000-00000000a001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-1"),
		Cwd:    "/repo", Kind: content.EntryShell, Intent: "make ci",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	page := entriesForPane(t, again.Ledger(), "pane-1")
	if len(page.Entries) != 1 || page.Entries[0].ID != id {
		t.Fatalf("blocks for pane-1 after restart = %+v, want the one entry %s", page.Entries, id)
	}
	// And the pane filter is a filter, not a coincidence of an empty store.
	if other := entriesForPane(t, again.Ledger(), "pane-nobody"); len(other.Entries) != 0 {
		t.Fatalf("blocks for a pane that recorded nothing = %+v, want none", other.Entries)
	}
}

// Both edges at once, which is the assertion that proves they are not
// redundant: after a restart the block STILL NAMES ITS PANE and NO LONGER
// NAMES A SESSION. The nulling is not automatic — the session row has to go,
// and Open is what removes it, because at store-open every session row was
// written by a previous incarnation.
func TestAfterARestartABlockNamesItsPaneAndNoSession(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	const sessionID = "session-of-the-dead-backend"
	if err := led.CreateSession(ctx, content.Session{
		ID: sessionID, WorkspaceID: "ws-1",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	const id = "00000000-0000-7000-8000-00000000b001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-1"), SessionID: strPtr(sessionID),
		Cwd: "/repo", Kind: content.EntryShell, Intent: "go test ./...",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Before the restart the row names both — otherwise the assertion below
	// would pass over a column that was never written.
	before, err := led.Entry(ctx, id)
	if err != nil || before == nil {
		t.Fatalf("Entry before restart: %+v, %v", before, err)
	}
	if before.PaneID == nil || *before.PaneID != "pane-1" {
		t.Fatalf("paneId before restart = %v, want pane-1", before.PaneID)
	}
	if before.SessionID == nil || *before.SessionID != sessionID {
		t.Fatalf("sessionId before restart = %v, want %s", before.SessionID, sessionID)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	after, err := again.Ledger().Entry(ctx, id)
	if err != nil || after == nil {
		t.Fatalf("Entry after restart: %+v, %v", after, err)
	}
	if after.PaneID == nil || *after.PaneID != "pane-1" {
		t.Fatalf("paneId after restart = %v, want pane-1 — the anchor is durable", after.PaneID)
	}
	if after.SessionID != nil {
		t.Fatalf("sessionId after restart = %v, want nil — the pipe died with the backend", *after.SessionID)
	}
}

// §8's inline-ssh case, and the one that proves the two edges answer different
// questions. A command run on the far side of an `ssh` typed into a LOCAL pane
// is anchored on that local pane — it is where the user will look for it — and
// still says it ran on the remote host. Keeping only one edge loses one of
// those two facts.
func TestABlockFromAnInlineSshKeepsItsPaneAndItsHostAcrossARestart(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")

	const remote = "ssh:deploy@build-01:22"
	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: remote, Kind: content.EnvSSH, Endpoint: strPtr("deploy@build-01:22"),
	}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	const id = "00000000-0000-7000-8000-00000000c001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: remote,
		PaneID: strPtr("pane-1"),
		Cwd:    "/srv/app", Kind: content.EntryShell, Intent: "systemctl restart app",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	page := entriesForPane(t, again.Ledger(), "pane-1")
	if len(page.Entries) != 1 {
		t.Fatalf("blocks for the local pane after restart = %+v, want the remote command", page.Entries)
	}
	got := page.Entries[0]
	if got.Environment == nil || got.Environment.Kind != content.EnvSSH {
		t.Fatalf("environment of the restored block = %+v, want the ssh host it ran on", got.Environment)
	}
	// Host() returns the endpoint facet verbatim today — taking the host out
	// of it is deferred to whoever refines the facet, and this test asserts
	// what the product says rather than a shape it does not have yet.
	if got.Environment.Host() != "deploy@build-01:22" {
		t.Fatalf("host of the restored block = %q, want the endpoint it ran on", got.Environment.Host())
	}
}

// The pane id is frontend-minted and therefore UNTRUSTED (design §7): an entry
// naming a pane that does not exist is REFUSED, never stored dangling. A
// dangling anchor is worse than none — it reads as a durable edge and resolves
// to nothing.
func TestSubmitRefusesAnEntryNamingAPaneThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	db, led, _ := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	const id = "00000000-0000-7000-8000-00000000d001"
	_, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-that-was-never-created"),
		Cwd:    "/repo", Kind: content.EntryShell, Intent: "echo hi",
	})
	if !errors.Is(err, content.ErrNoSuchPane) {
		t.Fatalf("Submit with an unknown pane: err = %v, want ErrNoSuchPane", err)
	}
	// And nothing was stored: a refused submit leaves no row behind.
	if got, getErr := led.Entry(ctx, id); getErr != nil || got != nil {
		t.Fatalf("Entry after a refused submit = %+v (err %v), want nil", got, getErr)
	}
}

// A pane that is CLOSED KEEPS the blocks anchored to it, because the pane
// keeps its row (nocx-l21ib.4): it leaves the window, and leaving the window
// is not ceasing to exist. This assertion used to say the opposite — the
// close deleted the row and entries.pane_id went null behind it — and the
// two edges only look alike from here. A SESSION is provenance and dies with
// the backend (D5), so its null is a fact; a PANE is the durable identity
// (§5), so nulling its edge was a loss dressed up as tidiness.
func TestClosingAPaneKeepsTheAnchorAndTheBlock(t *testing.T) {
	ctx := context.Background()
	db, led, _ := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	// A second tab, so closing the first pane is an ordinary close and not
	// the last-tab-in-the-application case DeletePane mints a replacement for.
	if _, err := db.Layout().CreateTab(ctx,
		content.Tab{ID: "tab-2", WorkspaceID: "ws-1", Position: 1, Layout: content.LayoutRow},
		content.Pane{ID: "pane-2", TabID: "tab-2", Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	envReady(t, led, "local")

	const id = "00000000-0000-7000-8000-00000000e001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-1"),
		Cwd:    "/repo", Kind: content.EntryShell, Intent: "ls",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Layout().DeletePane(ctx, "pane-1", content.Replacement{}); err != nil {
		t.Fatalf("DeletePane: %v", err)
	}
	got, err := led.Entry(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Entry after the pane closed = %+v, %v — the block outlives its pane", got, err)
	}
	if got.PaneID == nil || *got.PaneID != "pane-1" {
		t.Fatalf("paneId after the pane closed = %v, want pane-1 — a close must not unhook the block", got.PaneID)
	}
	// And the pane is out of the window, which is what "closed" means here:
	// its tab held nothing else, so the tab left with it.
	if panes, panesErr := db.Layout().Panes(ctx, "tab-1"); panesErr != nil || len(panes) != 0 {
		t.Fatalf("panes of the closed tab = %+v (err %v), want none in the window", panes, panesErr)
	}
}

// ── the startup sweep's own failure path (nocx-rtg0.28) ──────────────────

// dropDeadSessions runs inside Open, so its failure is Open's failure — and
// that is the whole point of testing it: the sweep is what makes
// entries.session_id's "null once that pipe is gone" true, and a store that
// came up with the sweep silently skipped would hand out rows naming sessions
// of a backend that is no longer running. Refusing to open is the only honest
// answer; a half-swept store is the soft degrade AGENTS.md names.
//
// The interval has both ends: the file is UNCHANGED from before the failed
// Open until a later Open succeeds, which is asserted by opening again with
// the trigger gone and finding the block's provenance exactly as it was.
func TestOpenFailsWhenTheDeadSessionSweepIsRefused(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	const sessionID = "session-of-the-dead-backend"
	if err := led.CreateSession(ctx, content.Session{ID: sessionID, WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	const id = "00000000-0000-7000-8000-00000000f001"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: "local",
		PaneID: strPtr("pane-1"), SessionID: strPtr(sessionID),
		Cwd: "/repo", Kind: content.EntryShell, Intent: "make ci",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Refuse the sweep's DELETE the way the retention failure tests refuse
	// theirs: a trigger in the encrypted file, installed while nothing holds
	// it, so the statement fails for a real reason rather than a stubbed one.
	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER sweep_boom BEFORE DELETE ON sessions
		 BEGIN SELECT RAISE(ABORT, 'sweep refused'); END`,
	); err != nil {
		t.Fatalf("install sweep trigger: %v", err)
	}

	again, err := reopenStore(t, path)
	if err == nil {
		_ = again.Close()
		t.Fatal("Open succeeded while the dead-session sweep was refused")
	}
	if again != nil {
		t.Fatal("Open returned a store alongside its error — a refused open must hand out nothing")
	}

	// The other end: with the refusal removed the store opens, the sweep runs,
	// and the row is exactly where it was — its pane kept, its dead session
	// dropped. Nothing was half-done by the failed attempt.
	if dropErr := rawLedger(t, path, hex.EncodeToString(testKey()), `DROP TRIGGER sweep_boom`); dropErr != nil {
		t.Fatalf("remove sweep trigger: %v", dropErr)
	}
	healthy, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen after the refusal was lifted: %v", err)
	}
	defer func() { _ = healthy.Close() }()

	got, err := healthy.Ledger().Entry(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Entry after recovery = %+v, %v", got, err)
	}
	if got.PaneID == nil || *got.PaneID != "pane-1" {
		t.Fatalf("paneId after recovery = %v, want pane-1", got.PaneID)
	}
	if got.SessionID != nil {
		t.Fatalf("sessionId after recovery = %v, want nil — the sweep ran this time", *got.SessionID)
	}
}

// A TURN has the same anchor as a command, and for the same reason
// (nocx-4em1z). The owner watched a restored tab lose every question and
// answer it held; this is the first end of that, measured at the store.
//
// A turn IS a block — the question is its intent and the answer is its body,
// exactly as a command line and its output are — so the restore read, which
// is `pane_id = ?`, must find it. It could not: SubmitAgentAsk wrote
// session_id and no pane_id, anchoring the turn to the one thing D5
// guarantees is gone by the time a restore runs.
//
// The restart is not decoration. Reading the column back in the process that
// wrote it cannot see this: the session row is still there, and only after
// Open sweeps it does the row's remaining anchor become the whole answer.
func TestATurnIsFoundByItsPaneAfterARestart(t *testing.T) {
	ctx := context.Background()
	db, led, path := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	const sessionID = "session-the-question-was-asked-in"
	if err := led.CreateSession(ctx, content.Session{ID: sessionID, WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ask := askIn(t, led, sessionID, "pane-1")

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	page := entriesForPane(t, again.Ledger(), "pane-1")
	if len(page.Entries) != 1 || page.Entries[0].ID != ask.EntryID {
		t.Fatalf("blocks for pane-1 after restart = %+v, want the one turn %s",
			page.Entries, ask.EntryID)
	}
	// And the pane filter is a filter, not a coincidence of a small store.
	if other := entriesForPane(t, again.Ledger(), "pane-nobody"); len(other.Entries) != 0 {
		t.Fatalf("blocks for a pane that asked nothing = %+v, want none", other.Entries)
	}
}

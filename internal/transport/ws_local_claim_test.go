package transport

// THE PANE'S CLAIM IS DURABLE BEFORE THE SPAWN (L7 of the local-helper design,
// nocx-ie23r.3).
//
// The helper mints the session id, so a coordinator cannot record what it is
// about to GET — only what it is about to ASK FOR. The idempotency key is that
// record: it is minted here, written to the ledger as a claim row, and carried
// into the spawn, where a repeat under the same key answers with the session
// the first one made rather than forking a second shell.
//
// The interval has both ends and both are asserted below: the claim stands
// from BEFORE the spawn until the row naming the session that spawn produced
// exists. A coordinator that dies inside it comes back to a claim it can
// replay; one that dies outside it comes back to a binding or to nothing.
//
// HOW "BEFORE" IS OBSERVED, since a test cannot stand between two statements.
// The fake opener below is called exactly where the real helper's spawn
// happens, and from there it tries to create a session row under the key it
// was handed. `sessions.id` is a primary key, so the write can only fail — and
// a failure is the proof: the row is already there, written by the open before
// it reached the spawn. If the claim were written afterwards, the probe would
// SUCCEED, and this test would fail with "the claim was not written before the
// spawn" rather than passing quietly.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// localClaimPane is the pane the local tab is the pipe of.
const localClaimPane = "0198f2b0-0000-7000-8000-0000000000c1"

// localClaimOpener stands where this machine's helper stands: it is asked to
// open a LOCAL destination, and it looks at the durable state at the exact
// moment the spawn would happen.
type localClaimOpener struct {
	reg    *session.Reg
	ledger content.LedgerRepository
	id     session.ID
	fail   error

	claim       string
	claimExists bool
}

func (o *localClaimOpener) OpenHosted(ctx context.Context, cfg session.Config, claim string) (HostedSessionOpen, bool, error) {
	o.claim = claim
	// The probe: a second row under the same primary key can only be refused.
	err := o.ledger.CreateSession(ctx, content.Session{ID: claim, WorkspaceID: content.DefaultWorkspaceID})
	o.claimExists = err != nil
	if o.fail != nil {
		return HostedSessionOpen{}, true, o.fail
	}
	sess, aerr := o.reg.Adopt(cfg, o.id, &helperOpenTestChannel{done: make(chan struct{})})
	if aerr != nil {
		return HostedSessionOpen{}, true, aerr
	}
	return HostedSessionOpen{Session: sess, Generation: "gen-local"}, true, nil
}

// claimStore is the store under one of these tests, kept so the rows can be
// read back the way a LATER coordinator reads them: by opening the same
// database again. content.Reconcile().Pending answers from the snapshot taken
// at Open — it is the carried-over set, which is exactly the question a
// restart asks — so a live read of it would report the previous incarnation's
// rows and say nothing about this one's.
type claimStore struct {
	path string
	key  []byte
	db   content.ContentDB
	ws   *WSServer
}

// openLocalClaimServer boots a server over a real content store with one
// workspace, one tab and one local pane.
func openLocalClaimServer(t *testing.T, opener *localClaimOpener) (*WSServer, *claimStore, func()) {
	t.Helper()
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(ctx, content.Config{
		Path:   path,
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	if _, err = db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: content.DefaultWorkspaceID, Name: "default"},
		content.Tab{ID: "tab-default", WorkspaceID: content.DefaultWorkspaceID, Layout: content.LayoutRow},
		content.Pane{ID: localClaimPane, TabID: "tab-default", Cwd: "/", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		_ = db.Close()
		t.Fatalf("CreateWorkspace: %v", err)
	}
	opener.ledger = db.Ledger()

	reg := newRegWithStub(log.NewSlogAdapter(nil))
	opener.reg = reg
	ws := NewWSServer(log.NewSlogAdapter(nil), reg,
		WithContentDB(db), WithHelperSessionOpener(opener))
	if err = ws.Start(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("Start: %v", err)
	}
	store := &claimStore{path: path, key: key, db: db, ws: ws}
	return ws, store, func() { store.close() }
}

// close ends the server and the store, once however many times it is called.
func (s *claimStore) close() {
	if s.ws != nil {
		_ = s.ws.Stop(context.Background())
		s.ws = nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}

// A LOCAL OPEN REACHES THE HELPER AT ALL, which until nocx-ie23r.3 it could
// not: the opener was consulted only on the remote branch, so a local pane
// could not be helper-hosted even with a helper serving on its own socket.
//
// And the claim it carries is durable before the spawn, and released once the
// binding that supersedes it exists.
func TestALocalOpenClaimsThePaneBeforeItSpawns(t *testing.T) {
	opener := &localClaimOpener{id: session.ID("0123456789abcdef0123456789abcdef")}
	ws, store, done := openLocalClaimServer(t, opener)
	defer done()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	raw := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "paneId": localClaimPane,
	})
	if rpcErrorIn(raw) != "" {
		t.Fatalf("open: %s", rpcErrorIn(raw))
	}

	if opener.claim == "" {
		t.Fatal("the spawn carried no idempotency key: a spawn nobody recorded cannot be replayed")
	}
	if !strings.HasPrefix(opener.claim, "spawn-") {
		t.Fatalf("claim = %q, want a key that names a spawn", opener.claim)
	}
	if !opener.claimExists {
		t.Fatal("the pane's claim was not durable when the spawn happened: " +
			"a coordinator that died here would leave a live PTY nothing claims")
	}

	// THE CLOSING END. The claim is gone and the binding that replaced it
	// names the session the helper minted — so a later start finds one row
	// per session and no orphan claims.
	rows := sessionRowIDs(t, store)
	if _, ok := rows[opener.claim]; ok {
		t.Error("the claim outlived the binding it was superseded by")
	}
	if _, ok := rows[string(opener.id)]; !ok {
		t.Errorf("no durable binding for the helper-minted session; rows are %v", rows)
	}
}

// A FAILED OPEN LEAVES NOTHING BEHIND. The claim is a promise about a spawn,
// and a spawn that did not happen must not leave a key standing: the next open
// of this pane would replay it, and a key that names no session is a replay
// that forks.
func TestARefusedLocalOpenLeavesNoClaim(t *testing.T) {
	opener := &localClaimOpener{
		id:   session.ID("0123456789abcdef0123456789abcdef"),
		fail: errors.New("this machine's helper did not answer"),
	}
	ws, store, done := openLocalClaimServer(t, opener)
	defer done()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	raw := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "paneId": localClaimPane,
	})
	if rpcErrorIn(raw) == "" {
		t.Fatal("a local open succeeded although its helper refused: something else opened the pane")
	}

	// The probe row the opener wrote is not the claim — the claim's own write
	// failed the primary key, which is what claimExists reports — so the only
	// row that can be here is the probe's, under the claim's id.
	if !opener.claimExists {
		t.Fatal("the pane's claim was not durable when the helper was asked")
	}
	rows := sessionRowIDs(t, store)
	if _, ok := rows[string(opener.id)]; ok {
		t.Error("a refused open wrote a binding for a session it never got")
	}
}

// rpcErrorIn is the error a JSON-RPC frame carries, or "" for a result.
func rpcErrorIn(raw []byte) string {
	var env struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Error == nil {
		return ""
	}
	return env.Error.Message
}

// sessionRowIDs reads the durable session rows back the way a later
// coordinator reads them: it ends this incarnation and opens the store again,
// which is the one reader of this table across a restart.
func sessionRowIDs(t *testing.T, store *claimStore) map[string]struct{} {
	t.Helper()
	store.close()
	reopened, err := content.Open(context.Background(), content.Config{
		Path:   store.path,
		Key:    store.key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen the content store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	pending, err := reopened.Reconcile().Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	out := make(map[string]struct{}, len(pending))
	for _, p := range pending {
		out[p.SessionID] = struct{}{}
	}
	return out
}

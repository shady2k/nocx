package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// ── fakeHistoryDB ─────────────────────────────────────────────────────────
//
// Implements the whole content.ContentDB seam so the socket tests can serve
// a real history.query with a canned store. Records what the handler asked
// for, because the rung must be passed through verbatim — never widened.
type fakeHistoryDB struct {
	mu        sync.Mutex
	page      content.HistoryPage
	err       error
	gotScope  content.Scope
	gotCwd    string
	gotHost   string
	gotLimit  int
	gotBefore *int64
	gotText   string
	calls     int
}

func (f *fakeHistoryDB) CommandHistory() content.CommandHistoryRepository { return f }
func (f *fakeHistoryDB) Conversations() content.ConversationRepository    { return nil }
func (f *fakeHistoryDB) Backup(_ context.Context, _ string) error         { return content.ErrNotImplemented }
func (f *fakeHistoryDB) Close() error                                     { return nil }
func (f *fakeHistoryDB) RestorePrivate(_ context.Context, _ []content.Conversation, _ []content.CommandRecord) error {
	return content.ErrNotImplemented
}
func (f *fakeHistoryDB) Ledger() content.LedgerRepository { return nil }

func (f *fakeHistoryDB) Add(_ context.Context, _ content.CommandRecord) (int64, error) {
	return 0, content.ErrNotImplemented
}

func (f *fakeHistoryDB) RewriteRedaction(_ context.Context, _ int64, _ content.Redaction, _ string) error {
	return nil
}

func (f *fakeHistoryDB) List(_ context.Context, _ int) ([]content.CommandRecord, error) {
	return nil, content.ErrNotImplemented
}

func (f *fakeHistoryDB) GetByID(_ context.Context, _ int64) (*content.CommandRecord, error) {
	return nil, content.ErrNotImplemented
}

func (f *fakeHistoryDB) FindByPrefix(_ context.Context, _ string, _ int) ([]content.CommandRecord, error) {
	return nil, content.ErrNotImplemented
}

func (f *fakeHistoryDB) Query(_ context.Context, scope content.Scope, cwd, host string, limit int, before *int64, text string) (content.HistoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotScope, f.gotCwd, f.gotHost, f.gotLimit, f.gotBefore, f.gotText = scope, cwd, host, limit, before, text
	return f.page, f.err
}

func (f *fakeHistoryDB) recorded() (content.Scope, string, string, int, *int64, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotScope, f.gotCwd, f.gotHost, f.gotLimit, f.gotBefore, f.gotText, f.calls
}

// newHistoryWSServer builds a server with the given store wired. A nil db
// leaves the store absent — the source=session state.
func newHistoryWSServer(t *testing.T, db content.ContentDB) (*WSServer, func()) {
	t.Helper()
	opts := []WSServerOption{}
	if db != nil {
		opts = append(opts, WithContentDB(db))
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// historyQueryResult is the decoded history.query result for assertions.
type historyQueryResult struct {
	Entries []struct {
		ID        string `json:"id"`
		Command   string `json:"command"`
		Cwd       string `json:"cwd"`
		Host      string `json:"host"`
		Status    string `json:"status"`
		ExitCode  *int   `json:"exitCode"`
		StartedAt *int64 `json:"startedAt"`
		EndedAt   *int64 `json:"endedAt"`
	} `json:"entries"`
	Scope     string `json:"scope"`
	Exhausted bool   `json:"exhausted"`
	Source    string `json:"source"`
	Coverage  *int64 `json:"coverage"`
}

func decodeHistoryResult(t *testing.T, resp *vaultRPCResult) historyQueryResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected a result")
	}
	var out historyQueryResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v\nraw: %s", err, resp.Result)
	}
	return out
}

// ── source semantics ──────────────────────────────────────────────────────

// With no store wired the method must answer session, never store: an empty
// answer and an unanswerable question must not look alike.
func TestHistoryQuery_NoStoreAnswersSession(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api",
	}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Source != "session" {
		t.Fatalf("source = %q, want session", got.Source)
	}
	if got.Scope != "directory" {
		t.Fatalf("scope = %q, want directory echoed", got.Scope)
	}
	if !got.Exhausted {
		t.Fatal("exhausted = false, want true (nothing further exists)")
	}
	if got.Entries == nil {
		t.Fatal("entries = null, want []")
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %d rows, want 0", len(got.Entries))
	}
}

// A store that has never recorded anything is "empty": session, so the
// overlay says "this session only" rather than presenting nothing as history.
func TestHistoryQuery_EmptyStoreAnswersSession(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{}} // HasRows defaults false
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Source != "session" {
		t.Fatalf("source = %q, want session for an empty store", got.Source)
	}
}

// The store answered and the rung had no matches: source=store with entries
// [] — the empty answer, distinct from the unanswerable one above.
func TestHistoryQuery_EmptyRungAnswersStore(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Entries: nil, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "directory", "cwd": "/tmp"}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Source != "store" {
		t.Fatalf("source = %q, want store (the store answered)", got.Source)
	}
	if got.Entries == nil {
		t.Fatal("entries = null, want []")
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %d rows, want 0", len(got.Entries))
	}
}

func TestHistoryQuery_AnswersFromAskedRung(t *testing.T) {
	ended := int64(1_750_000_000_000)
	exit := 2
	fake := &fakeHistoryDB{page: content.HistoryPage{
		Entries: []content.CommandRecord{
			{ID: 42, Command: "ls -la", Cwd: "/srv/api", Host: "", Status: content.StatusSuccess, ExitCode: &exit, EndedAt: &ended},
			{ID: 41, Command: "cd /srv/api", Cwd: "/srv/api", Host: "", Status: content.StatusRunning},
		},
		HasRows:   true,
		Exhausted: false,
	}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "host": "", "limit": 10,
	}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	if got.Scope != "directory" {
		t.Fatalf("scope = %q, want the asked rung echoed", got.Scope)
	}
	if got.Exhausted {
		t.Fatal("exhausted = true, want false (the fake said more exists)")
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	first := got.Entries[0]
	if first.ID != "42" || first.Command != "ls -la" || first.Cwd != "/srv/api" || first.Host != "" {
		t.Fatalf("first entry = %+v, want the opaque row handle and verbatim fields", first)
	}
	if first.Status != "success" {
		t.Fatalf("status = %q, want success", first.Status)
	}
	if first.ExitCode == nil || *first.ExitCode != 2 {
		t.Fatalf("exitCode = %v, want 2", first.ExitCode)
	}
	if first.EndedAt == nil || *first.EndedAt != ended {
		t.Fatalf("endedAt = %v, want %d", first.EndedAt, ended)
	}
	if got.Entries[1].ExitCode != nil || got.Entries[1].EndedAt != nil {
		t.Fatalf("running entry must have null exitCode/endedAt, got %+v", got.Entries[1])
	}

	// The directory rung is the exact (cwd, host) pair — the overlay's own
	// semantics (frontend/src/recall.ts). The handler must forward BOTH to
	// the store, or a remote command in the same cwd leaks into the local
	// directory rung.
	scope, cwd, host, limit, _, _, calls := fake.recorded()
	if calls != 1 || scope != content.ScopeDirectory || cwd != "/srv/api" || host != "" || limit != 10 {
		t.Fatalf("store asked with scope=%q cwd=%q host=%q limit=%d calls=%d, want directory /srv/api \"\" 10 1",
			scope, cwd, host, limit, calls)
	}
}

// The directory rung carries a remote host when the caller is on one: the
// host is forwarded verbatim, never dropped.
func TestHistoryQuery_DirectoryRungForwardsHost(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "host": "prod.example.com",
	}, 1)

	if got := decodeHistoryResult(t, resp); got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	_, cwd, host, _, _, _, _ := fake.recorded()
	if cwd != "/srv/api" || host != "prod.example.com" {
		t.Fatalf("directory rung forwarded cwd=%q host=%q, want /srv/api prod.example.com", cwd, host)
	}
}

// host rung: the host is passed verbatim; "" is the local machine and is a
// legitimate host rung, so the field's presence — not its value — is what
// makes the request valid.
func TestHistoryQuery_HostRungPassesHostVerbatim(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "host", "host": ""}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Scope != "host" || got.Source != "store" {
		t.Fatalf("scope=%q source=%q, want host store", got.Scope, got.Source)
	}
	_, _, host, _, _, _, _ := fake.recorded()
	if host != "" {
		t.Fatalf("host = %q, want the local-machine rung '' passed through", host)
	}
}

// everywhere: no rung fields are forwarded.
func TestHistoryQuery_EverywhereSendsNoRung(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)

	if got := decodeHistoryResult(t, resp); got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	scope, cwd, host, _, _, _, _ := fake.recorded()
	if scope != content.ScopeEverywhere || cwd != "" || host != "" {
		t.Fatalf("store asked with scope=%q cwd=%q host=%q, want everywhere '' ''", scope, cwd, host)
	}
}

// ── paging ────────────────────────────────────────────────────────────────

func TestHistoryQuery_PagesWithBeforeCursor(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "everywhere", "before": "77",
	}, 1)

	if got := decodeHistoryResult(t, resp); got.Exhausted != true {
		t.Fatal("exhausted = false, want the fake's answer")
	}
	_, _, _, _, before, _, _ := fake.recorded()
	if before == nil || *before != 77 {
		t.Fatalf("before = %v, want 77 passed through", before)
	}
}

// ── text filter (nocx-ms7v) ──────────────────────────────────────────────

// The filter crosses the wire and reaches the store verbatim — case is the
// store's problem, not the handler's. Absent and empty are the same state:
// no filter.
func TestHistoryQuery_TextFilterForwardedVerbatim(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "text": "DePlOy",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if _, _, _, _, _, text, _ := fake.recorded(); text != "DePlOy" {
		t.Fatalf("text = %q, want the filter passed through verbatim", text)
	}
}

// No text on the wire is no filter: the store receives "" — the same state
// the client uses when it has nothing to filter by.
func TestHistoryQuery_AbsentTextForwardsEmpty(t *testing.T) {
	fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "everywhere",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if _, _, _, _, _, text, _ := fake.recorded(); text != "" {
		t.Fatalf("text = %q, want the empty no-filter state", text)
	}
}

// ── coverage over the wire ───────────────────────────────────────────────

// The store's horizon is part of the real result off the real socket — the
// fake's HistoryPage.Coverage must reach the renderer, and an empty store
// must report null rather than omitting the field (the schema requires it).
func TestHistoryQuery_CoverageOverTheWire(t *testing.T) {
	t.Run("store horizon reaches the result", func(t *testing.T) {
		horizon := int64(1_750_000_000_000)
		fake := &fakeHistoryDB{page: content.HistoryPage{
			HasRows:   true,
			Exhausted: true,
			Coverage:  &horizon,
		}}
		ws, stop := newHistoryWSServer(t, fake)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)
		got := decodeHistoryResult(t, resp)
		if got.Coverage == nil || *got.Coverage != horizon {
			t.Fatalf("coverage = %v, want %d off the real socket", got.Coverage, horizon)
		}
	})

	t.Run("empty store reports null, not absence", func(t *testing.T) {
		ws, stop := newHistoryWSServer(t, nil)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)
		got := decodeHistoryResult(t, resp)
		if got.Source != "session" || got.Coverage != nil {
			t.Fatalf("session answer coverage = %v, want null (no horizon to state)", got.Coverage)
		}
	})
}

func TestHistoryQuery_RejectsMalformedBefore(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "everywhere", "before": "not-a-row-handle",
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602 for a malformed cursor", resp.Error)
	}
}

// limit clamps: <1 becomes the default, >200 becomes 200, in-range passes.
func TestHistoryQuery_LimitClamp(t *testing.T) {
	cases := []struct {
		sent int
		want int
	}{
		{0, 50}, {-5, 50}, {7, 7}, {200, 200}, {1000, 200},
	}
	for _, c := range cases {
		fake := &fakeHistoryDB{page: content.HistoryPage{HasRows: true, Exhausted: true}}
		ws, stop := newHistoryWSServer(t, fake)
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{
			"scope": "everywhere", "limit": c.sent,
		}, 1)
		if resp.Error != nil {
			t.Fatalf("limit=%d: unexpected error %+v", c.sent, resp.Error)
		}
		_, _, _, limit, _, _, _ := fake.recorded()
		if limit != c.want {
			t.Fatalf("limit=%d: store asked with %d, want %d", c.sent, limit, c.want)
		}
		stop()
	}
}

// ── invalid params are rejected, not silently interpreted ─────────────────

func TestHistoryQuery_RejectsInvalidParams(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"unknown scope", map[string]any{"scope": "repository"}},
		{"missing scope", map[string]any{}},
		{"directory without cwd", map[string]any{"scope": "directory"}},
		{"host without host", map[string]any{"scope": "host"}},
		{"non-object params", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws, stop := newHistoryWSServer(t, nil)
			defer stop()
			conn := connectWS(t, ws)
			var resp *vaultRPCResult
			if c.params == nil {
				resp = vaultCall(t, conn, "history.query", nil, 1)
			} else {
				resp = vaultCall(t, conn, "history.query", c.params, 1)
			}
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("error = %+v, want -32602", resp.Error)
			}
		})
	}
}

// ── the store failing is an error the caller can act on, never a session
// fallback: unavailable and broken must not collapse into each other ────────

func TestHistoryQuery_StoreErrorIsRPCError(t *testing.T) {
	fake := &fakeHistoryDB{err: context.DeadlineExceeded}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)

	if resp.Error == nil {
		t.Fatal("expected a JSON-RPC error, got a result (session fallback would hide a broken store)")
	}
	if resp.Error.Code != -32603 {
		t.Fatalf("error code = %d, want -32603", resp.Error.Code)
	}
	if resp.Result != nil {
		t.Fatal("an errored store must not also answer a result")
	}
}

// ── unknown method stays unknown ──────────────────────────────────────────

func TestHistoryQuery_UnwiredServerRejectsOtherMethods(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.queryTypo", map[string]any{"scope": "everywhere"}, 1)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("error = %+v, want -32601", resp.Error)
	}
}

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
// a real history.query with a canned store. Since nocx-rtg0.19 that seam is
// the LEDGER: history.query keeps its wire shape and answers from
// LedgerRepository.QueryEntries.
//
// Only QueryEntries and RecordCompleted are genuinely exercised through this
// fake — the embedded nil content.LedgerRepository turns any other call into
// a loud panic rather than a quiet zero value.
//
// It records the query it was asked for, because the rung must be passed
// through verbatim — never widened.
type fakeHistoryDB struct {
	content.LedgerRepository
	mu       sync.Mutex
	page     content.LedgerPage
	err      error
	gotQuery content.LedgerQuery
	calls    int
}

func (f *fakeHistoryDB) Conversations() content.ConversationRepository { return nil }
func (f *fakeHistoryDB) Backup(_ context.Context, _ string) error      { return content.ErrNotImplemented }
func (f *fakeHistoryDB) Close() error                                  { return nil }
func (f *fakeHistoryDB) Ledger() content.LedgerRepository              { return f }
func (f *fakeHistoryDB) Layout() content.LayoutRepository              { return nil }
func (f *fakeHistoryDB) APIRuns() content.APIRunRepository             { return nil }

// RecordCompleted keeps no row: this fake is the STORE-FAILURE arm of the
// write path (TestHistoryRecord_StoreErrorIsRPCError). The fake that actually
// stores what it is handed is fakeRecordHistoryDB.
func (f *fakeHistoryDB) RecordCompleted(_ context.Context, _ content.CompletedCommand) (string, error) {
	return "", content.ErrNotImplemented
}

func (f *fakeHistoryDB) QueryEntries(_ context.Context, q content.LedgerQuery) (content.LedgerPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotQuery = q
	return f.page, f.err
}

func (f *fakeHistoryDB) recorded() (content.LedgerQuery, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotQuery, f.calls
}

// ledgerRow builds one recall row the way the store returns it: the entry's
// own columns plus the RESOLVED environment, which is how a row says which
// host it ran on. A row carrying none is not answerable on this wire at all
// (the host rule in ws_history_ledger.go), so the fake always carries one.
func ledgerRow(id, command, cwd, host string, status content.EntryStatus) content.LedgerEntrySummary {
	env := environmentForHost(host)
	return content.LedgerEntrySummary{
		ID:            id,
		EnvironmentID: env.ID,
		Environment:   &env,
		Cwd:           cwd,
		Kind:          content.EntryShell,
		Intent:        command,
		Phase:         content.PhaseClosed,
		Status:        status,
	}
}

// newHistoryWSServer builds a server with the given store wired. A nil db
// leaves the store absent — the source=unavailable state. Extra options let
// a test wire the durable-history status (ws_history_status_test.go).
func newHistoryWSServer(t *testing.T, db content.ContentDB, extra ...WSServerOption) (*WSServer, func()) {
	t.Helper()
	opts := []WSServerOption{}
	if db != nil {
		opts = append(opts, WithContentDB(db))
	}
	opts = append(opts, extra...)
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

// With no store wired the method must answer unavailable — neither store
// (nothing looked) nor session (the store looked and holds nothing): an
// empty answer and an unanswerable question must not look alike.
func TestHistoryQuery_NoStoreAnswersUnavailable(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api",
	}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Source != "unavailable" {
		t.Fatalf("source = %q, want unavailable", got.Source)
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
	fake := &fakeHistoryDB{page: content.LedgerPage{}} // HasRows defaults false
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
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Entries: nil, Exhausted: true}}
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
	const doneID = "0192f0aa-0000-7000-8000-0000000000ls"
	done := ledgerRow(doneID, "ls -la", "/srv/api", "", content.EntrySuccess)
	done.EndedAt = &ended
	done.Payload = content.ShellPayloadJSON(&exit)
	running := ledgerRow("0192f0aa-0000-7000-8000-0000000000cd", "cd /srv/api", "/srv/api", "", content.EntryRunning)
	fake := &fakeHistoryDB{page: content.LedgerPage{
		Entries:   []content.LedgerEntrySummary{done, running},
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
	// The handle is the store's own id, echoed verbatim and never reshaped:
	// it is opaque on this wire, so the assertion is equality with what the
	// store said, not a check that it parses as anything.
	if first.ID != doneID || first.Command != "ls -la" || first.Cwd != "/srv/api" || first.Host != "" {
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
	// directory rung. The host crosses as its ENVIRONMENT id, which is the
	// ledger's name for the same coordinate.
	q, calls := fake.recorded()
	wantEnv := environmentForHost("").ID
	if calls != 1 || q.Scope != content.ScopeDirectory || q.Cwd != "/srv/api" || q.EnvironmentID != wantEnv || q.Limit != 10 {
		t.Fatalf("store asked with %+v calls=%d, want directory /srv/api env=%s limit=10 calls=1",
			q, calls, wantEnv)
	}
}

// The directory rung carries a remote host when the caller is on one: the
// host is forwarded verbatim, never dropped.
func TestHistoryQuery_DirectoryRungForwardsHost(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "host": "prod.example.com",
	}, 1)

	if got := decodeHistoryResult(t, resp); got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	q, _ := fake.recorded()
	wantEnv := environmentForHost("prod.example.com").ID
	if q.Cwd != "/srv/api" || q.EnvironmentID != wantEnv {
		t.Fatalf("directory rung forwarded cwd=%q env=%q, want /srv/api %s (the remote host's environment)",
			q.Cwd, q.EnvironmentID, wantEnv)
	}
	if q.EnvironmentID == environmentForHost("").ID {
		t.Fatal("the remote host collapsed to the local environment — the rung would leak")
	}
}

// host rung: the host is passed verbatim; "" is the local machine and is a
// legitimate host rung, so the field's presence — not its value — is what
// makes the request valid.
func TestHistoryQuery_HostRungPassesHostVerbatim(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "host", "host": ""}, 1)

	got := decodeHistoryResult(t, resp)
	if got.Scope != "host" || got.Source != "store" {
		t.Fatalf("scope=%q source=%q, want host store", got.Scope, got.Source)
	}
	q, _ := fake.recorded()
	if q.EnvironmentID != environmentForHost("").ID {
		t.Fatalf("environmentId = %q, want the local machine's — '' is a rung, not an absent filter", q.EnvironmentID)
	}
}

// everywhere: no rung fields are forwarded.
func TestHistoryQuery_EverywhereSendsNoRung(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)

	if got := decodeHistoryResult(t, resp); got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	q, _ := fake.recorded()
	if q.Scope != content.ScopeEverywhere || q.Cwd != "" || q.EnvironmentID != "" {
		t.Fatalf("store asked with scope=%q cwd=%q env=%q, want everywhere '' '' — a rung's coordinate on a rung that has none is a filter the user cannot see",
			q.Scope, q.Cwd, q.EnvironmentID)
	}
}

// ── paging ────────────────────────────────────────────────────────────────

func TestHistoryQuery_PagesWithBeforeCursor(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "everywhere", "before": "0192f0aa-0000-7000-8000-00000000cafe",
	}, 1)

	if got := decodeHistoryResult(t, resp); got.Exhausted != true {
		t.Fatal("exhausted = false, want the fake's answer")
	}
	q, _ := fake.recorded()
	if q.BeforeID != "0192f0aa-0000-7000-8000-00000000cafe" {
		t.Fatalf("beforeId = %q, want the previous page's last row id passed through", q.BeforeID)
	}
}

// ── text filter (nocx-ms7v) ──────────────────────────────────────────────

// The filter crosses the wire and reaches the store verbatim — case is the
// store's problem, not the handler's. Absent and empty are the same state:
// no filter.
func TestHistoryQuery_TextFilterForwardedVerbatim(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "text": "DePlOy",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if q, _ := fake.recorded(); q.Text != "DePlOy" {
		t.Fatalf("text = %q, want the filter passed through verbatim", q.Text)
	}
}

// No text on the wire is no filter: the store receives "" — the same state
// the client uses when it has nothing to filter by.
func TestHistoryQuery_AbsentTextForwardsEmpty(t *testing.T) {
	fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
	ws, stop := newHistoryWSServer(t, fake)
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "history.query", map[string]any{
		"scope": "everywhere",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if q, _ := fake.recorded(); q.Text != "" {
		t.Fatalf("text = %q, want the empty no-filter state", q.Text)
	}
}

// ── coverage over the wire ───────────────────────────────────────────────

// The store's horizon is part of the real result off the real socket — the
// fake's HistoryPage.Coverage must reach the renderer, and an empty store
// must report null rather than omitting the field (the schema requires it).
func TestHistoryQuery_CoverageOverTheWire(t *testing.T) {
	t.Run("store horizon reaches the result", func(t *testing.T) {
		horizon := int64(1_750_000_000_000)
		fake := &fakeHistoryDB{page: content.LedgerPage{
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

	t.Run("no store reports null, not absence", func(t *testing.T) {
		ws, stop := newHistoryWSServer(t, nil)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)
		got := decodeHistoryResult(t, resp)
		if got.Source != "unavailable" || got.Coverage != nil {
			t.Fatalf("unavailable answer coverage = %v, want null (no horizon to state)", got.Coverage)
		}
	})

	t.Run("empty store reports null, not absence", func(t *testing.T) {
		fake := &fakeHistoryDB{page: content.LedgerPage{Exhausted: true}} // HasRows false
		ws, stop := newHistoryWSServer(t, fake)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)
		got := decodeHistoryResult(t, resp)
		if got.Source != "session" || got.Coverage != nil {
			t.Fatalf("session answer coverage = %v, want null (no horizon to state)", got.Coverage)
		}
	})
}

// The cursor is OPAQUE — and since nocx-rtg0.19 it is opaque in fact and not
// only in the contract's word: it used to be parsed as the interim table's
// decimal rowid, and it is now the entry's own id, which the store resolves
// to a position. So the handler inspects nothing about its shape and hands it
// over verbatim; the only cursor it can refuse is the empty one, which names
// no row at all.
func TestHistoryQuery_CursorIsOpaque(t *testing.T) {
	t.Run("a handle of any shape reaches the store verbatim", func(t *testing.T) {
		fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
		ws, stop := newHistoryWSServer(t, fake)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{
			"scope": "everywhere", "before": "not-a-row-handle",
		}, 1)
		if resp.Error != nil {
			t.Fatalf("error = %+v, want the handle accepted — the transport does not read it", resp.Error)
		}
		q, _ := fake.recorded()
		if q.BeforeID != "not-a-row-handle" {
			t.Fatalf("beforeId = %q, want the handle passed through unchanged", q.BeforeID)
		}
	})

	t.Run("the empty cursor is refused", func(t *testing.T) {
		fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
		ws, stop := newHistoryWSServer(t, fake)
		defer stop()
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{
			"scope": "everywhere", "before": "",
		}, 1)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("error = %+v, want -32602 for a cursor naming no row", resp.Error)
		}
		if _, calls := fake.recorded(); calls != 0 {
			t.Fatalf("store consulted %d times for a refused cursor, want 0", calls)
		}
	})
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
		fake := &fakeHistoryDB{page: content.LedgerPage{HasRows: true, Exhausted: true}}
		ws, stop := newHistoryWSServer(t, fake)
		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{
			"scope": "everywhere", "limit": c.sent,
		}, 1)
		if resp.Error != nil {
			t.Fatalf("limit=%d: unexpected error %+v", c.sent, resp.Error)
		}
		q, _ := fake.recorded()
		if q.Limit != c.want {
			t.Fatalf("limit=%d: store asked with %d, want %d", c.sent, q.Limit, c.want)
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

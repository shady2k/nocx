package transport

// ledger.query / ledger.get over the REAL socket into the REAL store
// (nocx-rtg0.20) — the read path, and the only ordering implementation
// (design §6.2).
//
// Written from the bead's acceptance criteria: every filter is proved by
// what it EXCLUDES, an empty answer is [] rather than null, an unknown id is
// an error rather than an empty success, and the detail read carries
// artifact METADATA without the bytes.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// ── decoding helpers ──────────────────────────────────────────────────────

// queriedEntry names the fields these tests assert on. It is deliberately
// NOT the handler's DTO: the exact key set is the contract test's job
// (additionalProperties:false plus required), and a test that reuses the
// implementation's struct cannot notice a field the handler never sends.
type queriedEntry struct {
	ID          string   `json:"id"`
	Seq         int64    `json:"seq"`
	EnvID       string   `json:"environmentId"`
	Host        *string  `json:"host"`
	Cwd         string   `json:"cwd"`
	Kind        string   `json:"kind"`
	Intent      string   `json:"intent"`
	Phase       string   `json:"phase"`
	Status      string   `json:"status"`
	SubmittedAt int64    `json:"submittedAt"`
	StartedAt   *int64   `json:"startedAt"`
	EndedAt     *int64   `json:"endedAt"`
	DurationMs  *int64   `json:"durationMs"`
	ExitCode    *int     `json:"exitCode"`
	MaskedCount int      `json:"maskedCount"`
	MaskedKinds []string `json:"maskedKinds"`
	Redactions  []struct {
		Kind   string `json:"kind"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
		Prefix string `json:"prefix"`
		Suffix string `json:"suffix"`
	} `json:"redactions"`
}

type queriedPage struct {
	Entries   []queriedEntry `json:"entries"`
	Scope     string         `json:"scope"`
	Exhausted bool           `json:"exhausted"`
	HasRows   bool           `json:"hasRows"`
	Coverage  *int64         `json:"coverage"`
}

func queryCall(t *testing.T, conn *websocket.Conn, params map[string]any, id int) queriedPage {
	t.Helper()
	resp := vaultCall(t, conn, "ledger.query", params, id)
	if resp.Error != nil {
		t.Fatalf("ledger.query %+v: %+v", params, resp.Error)
	}
	var page queriedPage
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("decode ledger.query result: %v\nraw: %s", err, resp.Result)
	}
	return page
}

func queriedIDs(page queriedPage) []string {
	out := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		out = append(out, e.ID)
	}
	return out
}

// wantQueried asserts the page holds exactly these ids in this order —
// naming the whole set is what makes a silently ignored filter fail.
func wantQueried(t *testing.T, page queriedPage, ids ...string) {
	t.Helper()
	got := queriedIDs(page)
	if strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Fatalf("page = %v, want exactly %v", got, ids)
	}
}

// openEntry drives one ledger.open over the socket.
func openEntry(t *testing.T, conn *websocket.Conn, sid, id, intent string, rpcID int) {
	t.Helper()
	_, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, id, intent, 1)}, rpcID)
	if errObj != nil {
		t.Fatalf("ledger.open %s: %+v", id, errObj)
	}
}

// openEntryIn drives a ledger.open whose envelope names a directory of its
// own — the coordinate the directory rung filters on.
func openEntryIn(t *testing.T, conn *websocket.Conn, sid, id, cwd, kind, intent string, rpcID int) {
	t.Helper()
	env := ledgerEnv(sid, id, intent, 1)
	env["cwd"] = cwd
	env["kind"] = kind
	_, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{"envelope": env}, rpcID)
	if errObj != nil {
		t.Fatalf("ledger.open %s: %+v", id, errObj)
	}
}

// closeEntryOverWire ends an entry with a status and an exit code.
func closeEntryOverWire(t *testing.T, conn *websocket.Conn, sid, id, status string, exit int, rpcID int) {
	t.Helper()
	_, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope": ledgerEnv(sid, id, "make test", 2),
		"status":   status,
		"facts":    map[string]any{"terminationReason": "completed", "exitCode": exit},
	}, rpcID)
	if errObj != nil {
		t.Fatalf("ledger.close %s: %+v", id, errObj)
	}
}

// ── the empty answer ─────────────────────────────────────────────────────

// An empty result is {"entries": []} and never null — off the real socket,
// read out of the raw JSON rather than through a Go decode, which turns null
// into an empty slice and hides exactly this. The renderer's first .map is
// what a null throws in (nocx-25k9.14).
func TestLedgerQuery_EmptyAnswerIsAnArrayNotNull(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "ledger.query", map[string]any{"scope": "everywhere"}, 2)
	if resp.Error != nil {
		t.Fatalf("ledger.query on an empty store: %+v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), `"entries":[]`) {
		t.Fatalf("empty result does not carry an empty array: %s", resp.Result)
	}
	var page queriedPage
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.HasRows {
		t.Fatal("hasRows on a ledger that holds nothing")
	}
	if !page.Exhausted {
		t.Fatal("an empty page is not exhausted")
	}
	if page.Coverage != nil {
		t.Fatalf("coverage = %d with nothing recorded", *page.Coverage)
	}
	if page.Scope != "everywhere" {
		t.Fatalf("scope = %q, want the rung that was asked for", page.Scope)
	}
}

// The subtle one, off the socket: a rung that matches nothing in a ledger
// that holds rows answers hasRows=true with an empty page. history.query
// turns that into source=store; collapsing it into "no rows" ships a UI
// saying "no history" when it means "history is off".
func TestLedgerQuery_HasRowsSeparatesAnEmptyAnswerFromAnEmptyLedger(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "make test", 2)

	page := queryCall(t, conn, map[string]any{
		"scope": "directory", "environmentId": localEnvironmentID(), "cwd": "/nowhere",
	}, 3)
	if len(page.Entries) != 0 {
		t.Fatalf("the rung /nowhere answered %v", queriedIDs(page))
	}
	if !page.HasRows {
		t.Fatal("hasRows is false while the ledger holds a row the rung did not match")
	}
}

// localEnvironmentID is the id a local session's environment hashes to —
// the same derivation environmentForSession runs (EnvironmentIDFor), which
// is how a caller holding a host turns it into a rung coordinate.
func localEnvironmentID() string {
	return content.EnvironmentIDFor(content.EnvLocal, "")
}

// ── ordering ─────────────────────────────────────────────────────────────

// seq DESC, off the socket. Three opens in a row land inside the same
// millisecond often enough that this is the case wall-clock ordering gets
// wrong; the assertion is on the seq the acks carry, so it holds either way.
func TestLedgerQuery_OrdersBySeqDescOverTheWire(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "one", 2)
	openEntry(t, conn, sid, "entry-2", "two", 3)
	openEntry(t, conn, sid, "entry-3", "three", 4)

	page := queryCall(t, conn, map[string]any{"scope": "everywhere"}, 5)
	wantQueried(t, page, "entry-3", "entry-2", "entry-1")
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i-1].Seq <= page.Entries[i].Seq {
			t.Fatalf("seq is not descending: %d then %d", page.Entries[i-1].Seq, page.Entries[i].Seq)
		}
	}
}

// ── the filters, each proved by what it excludes ─────────────────────────

func TestLedgerQuery_FiltersExcludeOverTheWire(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	// The two literal rows go in FIRST so the newest entry — which the limit
	// and paging subtests below name — stays the one it was.
	openEntryIn(t, conn, sid, "literal-shell", "/repo", "shell", "grep '100%_done'", 20)
	// A LIKE pattern of "100%_done" matches this row and instr() does not.
	openEntryIn(t, conn, sid, "decoy-shell", "/repo", "shell", "1000-and-done", 21)
	openEntryIn(t, conn, sid, "here-shell", "/repo", "shell", "make test", 2)
	openEntryIn(t, conn, sid, "there-shell", "/other", "shell", "make lint", 3)
	openEntryIn(t, conn, sid, "here-ask", "/repo", "ask", "why did it fail", 4)
	closeEntryOverWire(t, conn, sid, "here-shell", "failure", 2, 5)

	t.Run("directory excludes another directory", func(t *testing.T) {
		page := queryCall(t, conn, map[string]any{
			"scope": "directory", "environmentId": localEnvironmentID(), "cwd": "/other",
		}, 10)
		wantQueried(t, page, "there-shell")
	})
	t.Run("kind excludes another kind", func(t *testing.T) {
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "kind": "ask"}, 11)
		wantQueried(t, page, "here-ask")
	})
	t.Run("status excludes another status", func(t *testing.T) {
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "status": "failure"}, 12)
		wantQueried(t, page, "here-shell")
	})
	t.Run("text excludes every intent that does not contain it", func(t *testing.T) {
		// Case-insensitive, and it reaches the store: a filter dropped on the
		// way would answer with the whole ledger and look like it worked.
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "text": "LINT"}, 18)
		wantQueried(t, page, "there-shell")
	})
	t.Run("text is a substring, not a LIKE pattern", func(t *testing.T) {
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "text": "100%_done"}, 19)
		wantQueried(t, page, "literal-shell")
	})
	t.Run("limit bounds the page and says it is not exhausted", func(t *testing.T) {
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "limit": 1}, 13)
		wantQueried(t, page, "here-ask")
		if page.Exhausted {
			t.Fatal("a page with two further entries behind it says it is exhausted")
		}
	})
	t.Run("before pages on seq and excludes what was already seen", func(t *testing.T) {
		all := queryCall(t, conn, map[string]any{"scope": "everywhere"}, 14)
		oldest := all.Entries[len(all.Entries)-1]
		page := queryCall(t, conn, map[string]any{"scope": "everywhere", "before": oldest.Seq}, 15)
		wantQueried(t, page)
		if !page.HasRows {
			t.Fatal("hasRows is false while the ledger holds every row the cursor skipped")
		}
	})
	t.Run("since excludes what came before it", func(t *testing.T) {
		all := queryCall(t, conn, map[string]any{"scope": "everywhere"}, 16)
		newest := all.Entries[0]
		page := queryCall(t, conn, map[string]any{
			"scope": "everywhere", "since": newest.SubmittedAt + 1,
		}, 17)
		wantQueried(t, page)
	})
}

// The environment rung, with a real remote session: the host rung answers
// from the environment it was asked for and excludes the other machine.
// The id is hashed forward with EnvironmentIDFor, which is how a host
// becomes a rung coordinate.
func TestLedgerQuery_HostRungExcludesTheOtherMachine(t *testing.T) {
	const sshHost = "build.example.com"
	logger := log.NewSlogAdapter(nil)
	db := newLedgerStore(t)
	reg := newRegWithStub(logger)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return ssh.NewStubChannel(logger), nil
		},
	})
	ws := NewWSServer(logger, reg,
		WithContentDB(db),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return sshHost, &ssh.ConnectConfig{User: "alice", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)

	localSID := openLocalSession(t, conn)
	sshSID := openSSHSession(t, conn, 2)
	openEntry(t, conn, localSID, "local-entry", "make test", 3)
	openEntry(t, conn, sshSID, "ssh-entry", "make deploy", 4)

	remoteID := content.EnvironmentIDFor(content.EnvSSH, sshHost)
	page := queryCall(t, conn, map[string]any{"scope": "host", "environmentId": remoteID}, 5)
	wantQueried(t, page, "ssh-entry")
	if page.Entries[0].Host == nil || *page.Entries[0].Host != sshHost {
		t.Fatalf("the row does not say which host it ran on: %+v", page.Entries[0].Host)
	}

	local := queryCall(t, conn, map[string]any{"scope": "host", "environmentId": localEnvironmentID()}, 6)
	wantQueried(t, local, "local-entry")
	if local.Entries[0].Host == nil || *local.Entries[0].Host != "" {
		t.Fatalf("the local row's host = %v, want the empty string", local.Entries[0].Host)
	}
}

// ── the request is refused rather than answered wrongly ──────────────────

// A value the closed enums do not name is a rejected request, never an
// empty result set: an empty page for a misspelled status reads as "nothing
// ever failed here", which is the answer most likely to be believed.
func TestLedgerQuery_RefusesWhatItCannotAnswer(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "make test", 2)

	bad := map[string]map[string]any{
		"no scope":                 {},
		"unknown scope":            {"scope": "recent"},
		"unknown kind":             {"scope": "everywhere", "kind": "script"},
		"unknown status":           {"scope": "everywhere", "status": "ok"},
		"directory with no rung":   {"scope": "directory", "cwd": "/repo"},
		"directory with no cwd":    {"scope": "directory", "environmentId": localEnvironmentID()},
		"host with no environment": {"scope": "host"},
		"negative before":          {"scope": "everywhere", "before": -1},
		"negative since":           {"scope": "everywhere", "since": -1},
	}
	id := 10
	for name, params := range bad {
		t.Run(name, func(t *testing.T) {
			resp := vaultCall(t, conn, "ledger.query", params, id)
			id++
			if resp.Error == nil {
				t.Fatalf("ledger.query %+v answered %s rather than refusing", params, resp.Result)
			}
			if resp.Error.Code != -32602 {
				t.Fatalf("ledger.query %+v error code = %d, want -32602", params, resp.Error.Code)
			}
		})
	}
}

// A limit above the ceiling is CLAMPED rather than refused — the same
// product contract history.query's page limit carries, so one concept keeps
// one behaviour. What it must never be is unbounded.
func TestLedgerQuery_LimitAboveTheCeilingIsClamped(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "make test", 2)

	page := queryCall(t, conn, map[string]any{
		"scope": "everywhere", "limit": content.MaxLedgerPageLimit + 5000,
	}, 3)
	wantQueried(t, page, "entry-1")
}

// ── the redaction receipt rides the row ──────────────────────────────────

// The receipt is READ back out of the entry's payload (EntryMaskingOf,
// nocx-rtg0.24) rather than recomputed by re-running the detector over the
// stored text — which would be a second owner of one fact and would mask
// text that is already masked. The proof is that the recorded intent is the
// MASKED one and the counts still describe what was taken out of it.
func TestLedgerQuery_CarriesTheRedactionReceiptItWasWrittenWith(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	const secret = "export OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCD" //nolint:gosec // a synthetic detector fixture
	openEntry(t, conn, sid, "entry-1", secret, 2)

	page := queryCall(t, conn, map[string]any{"scope": "everywhere"}, 3)
	wantQueried(t, page, "entry-1")
	row := page.Entries[0]
	if row.Intent == secret {
		t.Fatal("the recorded intent is the raw text — the durable command is always the masked one")
	}
	if row.MaskedCount != 1 {
		t.Fatalf("maskedCount = %d, want 1: %+v", row.MaskedCount, row)
	}
	if len(row.MaskedKinds) != 1 || row.MaskedKinds[0] != "openai" {
		t.Fatalf("maskedKinds = %v, want [openai]", row.MaskedKinds)
	}
	if len(row.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want exactly the one segment the mask left", row.Redactions)
	}
	seg := row.Redactions[0]
	if seg.Start < 0 || seg.End > len([]rune(row.Intent))+len(row.Intent) || seg.Start >= seg.End {
		t.Fatalf("redaction span [%d:%d] does not address the stored intent %q", seg.Start, seg.End, row.Intent)
	}
	if row.Intent[seg.Start:seg.End] == "" {
		t.Fatalf("redaction span [%d:%d] selects nothing of %q", seg.Start, seg.End, row.Intent)
	}
}

// A clean command carries an empty receipt, never a null one: no mask is
// [] on both lists (contracts/history.query.schema.json).
func TestLedgerQuery_CleanCommandCarriesEmptyListsNotNull(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "make test", 2)

	resp := vaultCall(t, conn, "ledger.query", map[string]any{"scope": "everywhere"}, 3)
	if resp.Error != nil {
		t.Fatalf("ledger.query: %+v", resp.Error)
	}
	raw := string(resp.Result)
	if !strings.Contains(raw, `"maskedKinds":[]`) || !strings.Contains(raw, `"redactions":[]`) {
		t.Fatalf("a clean command's receipt is not empty arrays: %s", raw)
	}
}

// ── ledger.get ───────────────────────────────────────────────────────────

type gotEntry struct {
	Entry queriedEntry `json:"entry"`
	Edges []struct {
		From    string          `json:"from"`
		To      string          `json:"to"`
		Rel     string          `json:"rel"`
		Payload json.RawMessage `json:"payload"`
	} `json:"edges"`
	Artifacts []struct {
		ID            string  `json:"id"`
		ExecutionID   int64   `json:"executionId"`
		MediaType     string  `json:"mediaType"`
		State         string  `json:"state"`
		ByteLen       int64   `json:"byteLen"`
		ChunkCount    int     `json:"chunkCount"`
		CaptureMethod string  `json:"captureMethod"`
		Encoding      string  `json:"encoding"`
		Stream        *string `json:"stream"`
	} `json:"artifacts"`
}

// The detail read: the entry, its edges and its artifact METADATA. The
// bodies are absent because "the recall read must not haul bytes" — the raw
// result is searched for the chunk text, since a Go decode into a struct
// that names no such field could not see it.
func TestLedgerGet_ReturnsEdgesAndArtifactMetadataWithoutTheBytes(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	ctx := context.Background()

	openEntry(t, conn, sid, "entry-1", "make test", 2)
	openEntry(t, conn, sid, "entry-2", "make test again", 3)
	closeEntryOverWire(t, conn, sid, "entry-2", "success", 0, 4)

	led := db.Ledger()
	if err := led.AddEdge(ctx, content.Edge{
		From: "entry-2", To: "entry-1", Rel: content.RelRerunOf, Payload: `{}`,
	}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	row, err := led.Entry(ctx, "entry-2")
	if err != nil || row == nil || len(row.Executions) == 0 {
		t.Fatalf("Entry(entry-2) = %+v, %v — want a row with an execution", row, err)
	}
	const body = "the-output-bytes-nobody-asked-for"
	if _, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: row.ID, ExecutionID: &row.Executions[0].ID,
		ID: "artifact-1", MediaType: content.MediaText,
		CaptureMethod: content.CaptureRawOutput, CaptureVersion: 1, Encoding: "utf-8",
	}); err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	if err := led.AppendChunk(ctx, "artifact-1", 1, []byte(body)); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	resp := vaultCall(t, conn, "ledger.get", map[string]any{"id": "entry-2"}, 5)
	if resp.Error != nil {
		t.Fatalf("ledger.get: %+v", resp.Error)
	}
	if strings.Contains(string(resp.Result), body) {
		t.Fatalf("ledger.get hauled the chunk bodies:\n%s", resp.Result)
	}
	var got gotEntry
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode ledger.get: %v\nraw: %s", err, resp.Result)
	}
	if got.Entry.ID != "entry-2" {
		t.Fatalf("entry = %q, want entry-2", got.Entry.ID)
	}
	if got.Entry.ExitCode == nil || *got.Entry.ExitCode != 0 {
		t.Fatalf("exitCode = %v, want 0 — null is not zero", got.Entry.ExitCode)
	}
	if len(got.Edges) != 1 || got.Edges[0].Rel != "rerun-of" ||
		got.Edges[0].From != "entry-2" || got.Edges[0].To != "entry-1" {
		t.Fatalf("edges = %+v, want the one rerun-of edge", got.Edges)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want exactly one", got.Artifacts)
	}
	art := got.Artifacts[0]
	if art.ID != "artifact-1" || art.ExecutionID != row.Executions[0].ID {
		t.Fatalf("artifact = %+v, want the one appended to execution %d", art, row.Executions[0].ID)
	}
	if art.ByteLen != int64(len(body)) || art.ChunkCount != 1 {
		t.Fatalf("artifact metadata = %+v, want byteLen %d and one chunk", art, len(body))
	}
}

// An unknown id is an ERROR, never an empty success: an empty entry reads as
// "that command left no trace", which is a different fact from "no such id".
func TestLedgerGet_UnknownIDIsAnErrorNotAnEmptySuccess(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "ledger.get", map[string]any{"id": "no-such-entry"}, 2)
	if resp.Error == nil {
		t.Fatalf("ledger.get on an unknown id answered %s", resp.Result)
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602 (an id the caller sent that no row carries)", resp.Error.Code)
	}
	if missing := vaultCall(t, conn, "ledger.get", map[string]any{}, 3); missing.Error == nil {
		t.Fatalf("ledger.get with no id answered %s", missing.Result)
	}
}

// The evicted-prose fact reaches the renderer off the REAL socket
// (nocx-dc2fr.4): a turn whose `text` children retention stripped answers
// ledger.get with proseEvicted true, and a turn whose prose is whole answers
// false. Both are validated against the schema — a fixture-built payload
// could not show a field the handler never sends (AGENTS.md rule 5).
func TestLedgerGet_ProseEvictedOverTheWire(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	openLocalSession(t, conn)
	ctx := context.Background()
	led := db.Ledger()
	envID := localEnvironmentID()
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID, Criticality: content.CriticalityRoutine, Payload: `{}`,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	// One WHOLE turn: the question, a `text` child under it with a body,
	// the run closed. The SECOND turn's body is PINNED, so the sweep takes
	// the first turn's prose and leaves the second — the paired positive.
	makeTurn := func(id, artifactID string, pinned bool) {
		if _, err := led.Submit(ctx, content.SubmitEntry{
			ID: id, Client: "test-client", EnvironmentID: envID, Cwd: "/repo",
			Kind: content.EntryAsk, Intent: "how big is it?", Payload: "{}",
		}); err != nil {
			t.Fatalf("submit the turn: %v", err)
		}
		execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: id})
		if err != nil {
			t.Fatalf("start the turn's run: %v", err)
		}
		zero := 0
		payload := content.ShellPayloadJSON(&zero)
		if err = led.FinishExecution(ctx, execID, content.FinishExecution{
			EndedAt: 2_000_000_000_000, TerminationReason: content.TermCompleted,
			Status: content.EntrySuccess, Payload: &payload,
		}); err != nil {
			t.Fatalf("close the turn: %v", err)
		}
		pos := 0
		child := id + "-txt"
		if _, err = led.Submit(ctx, content.SubmitEntry{
			ID: child, Client: "test-client", EnvironmentID: envID, Cwd: "/repo",
			ParentID: &id, Pos: &pos, Kind: content.EntryText, Intent: "", Payload: "{}",
		}); err != nil {
			t.Fatalf("seat the prose block: %v", err)
		}
		aid, err := led.AppendArtifact(ctx, content.AppendArtifact{
			ID: artifactID, EntryID: child, MediaType: content.MediaText, Pinned: pinned,
		})
		if err != nil {
			t.Fatalf("append the prose artifact: %v", err)
		}
		if err := led.AppendChunk(ctx, aid, 1, []byte("the answer the model wrote")); err != nil {
			t.Fatalf("append the prose body: %v", err)
		}
		if _, err := led.EvictBodies(ctx, content.BodyEvictionRequest{KeepBytes: 0, Max: 10}); err != nil {
			t.Fatalf("evict bodies: %v", err)
		}
	}

	schema := loadSchema(t, "ledger.get.schema.json")
	makeTurn("turn-evicted", "art-aaa", false)
	got := vaultCall(t, conn, "ledger.get", map[string]any{"id": "turn-evicted"}, 20)
	if got.Error != nil {
		t.Fatalf("ledger.get (evicted): %+v", got.Error)
	}
	validateJSON(t, schema, got.Result, "ledger.get result (prose evicted)")
	var evicted struct {
		ProseEvicted bool `json:"proseEvicted"`
	}
	if err := json.Unmarshal(got.Result, &evicted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !evicted.ProseEvicted {
		t.Fatalf("proseEvicted off the socket = false, want true after the sweep took the body")
	}

	makeTurn("turn-kept", "art-bbb", true)
	kept := vaultCall(t, conn, "ledger.get", map[string]any{"id": "turn-kept"}, 21)
	if kept.Error != nil {
		t.Fatalf("ledger.get (kept): %+v", kept.Error)
	}
	validateJSON(t, schema, kept.Result, "ledger.get result (prose kept)")
	var intact struct {
		ProseEvicted bool `json:"proseEvicted"`
	}
	if err := json.Unmarshal(kept.Result, &intact); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if intact.ProseEvicted {
		t.Fatalf("proseEvicted off the socket = true for a turn whose prose is whole")
	}
}

// A PROSE BLOCK'S BODY REACHES THE RENDERER (nocx-dc2fr.7, and it did not).
//
// An artifact belongs to its BLOCK and names an execution only when an
// attempt produced it (ADR-0040 decision 3). A run of assistant prose was
// printed, not attempted, so its body hangs on the entry alone — and
// handleGet flattened `row.Executions` only. Every prose body was therefore
// stored, read back by the ledger, and dropped at the wire: the live turn was
// right and the restored one drew every sentence of it BLANK.
//
// Both unit suites were green while that was true, which is the part worth
// remembering. The store's test asked the store, and the renderer's test
// supplied the facts itself, so the defect sat in the one seam neither
// crossed — the socket. That is why this test is over the real wire and why
// AGENTS.md rule 5 insists on the difference: it took the epic's end-to-end
// check to find it at all.
//
// Both halves are asserted together, because the pair is the point: an
// entry's OWN body and an execution's body must both come back, and a fix
// that swapped one loop for the other would pass half of this.
func TestLedgerGet_AProseBodyAndACommandBodyBothReachTheWire(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	openLocalSession(t, conn)
	ctx := context.Background()
	led := db.Ledger()
	envID := localEnvironmentID()
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID, Criticality: content.CriticalityRoutine,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	// The turn, one run of prose seated under it, and the prose's own body:
	// no execution, because nothing was attempted.
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: "wire-turn", Client: "test-client", EnvironmentID: envID, Cwd: "/repo",
		Kind: content.EntryAsk, Intent: "what went wrong?", Payload: "{}",
	}); err != nil {
		t.Fatalf("submit the turn: %v", err)
	}
	pos := 0
	turnID := "wire-turn"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: "wire-prose", Client: "test-client", EnvironmentID: envID, Cwd: "/repo",
		ParentID: &turnID, Pos: &pos, Kind: content.EntryText, Intent: "", Payload: "{}",
	}); err != nil {
		t.Fatalf("seat the prose block: %v", err)
	}
	proseArtifact, err := led.AppendArtifact(ctx, content.AppendArtifact{
		ID: "art-prose", EntryID: "wire-prose", MediaType: content.MediaText,
	})
	if err != nil {
		t.Fatalf("append the prose artifact: %v", err)
	}
	if err = led.AppendChunk(ctx, proseArtifact, 1, []byte("line 3 is wrong")); err != nil {
		t.Fatalf("append the prose body: %v", err)
	}

	got := vaultCall(t, conn, "ledger.get", map[string]any{"id": "wire-prose"}, 30)
	if got.Error != nil {
		t.Fatalf("ledger.get on the prose block: %+v", got.Error)
	}
	validateJSON(t, loadSchema(t, "ledger.get.schema.json"), got.Result, "ledger.get result (prose block)")
	var body struct {
		Artifacts []struct {
			ID          string `json:"id"`
			MediaType   string `json:"mediaType"`
			ExecutionID *int64 `json:"executionId"`
		} `json:"artifacts"`
	}
	if err = json.Unmarshal(got.Result, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Artifacts) != 1 || body.Artifacts[0].ID != "art-prose" {
		t.Fatalf("the prose block's artifacts off the socket = %+v, want the one body it owns", body.Artifacts)
	}
	if body.Artifacts[0].MediaType != string(content.MediaText) {
		t.Fatalf("the prose body's media type = %q, want %q",
			body.Artifacts[0].MediaType, content.MediaText)
	}

	// The paired half: a command's body hangs on the ATTEMPT that produced
	// it, and still comes back. Without this the fix could have moved the
	// loop rather than added one.
	if _, err = led.Submit(ctx, content.SubmitEntry{
		ID: "wire-cmd", Client: "test-client", EnvironmentID: envID, Cwd: "/repo",
		Kind: content.EntryShell, Intent: "cat -n a.txt", Payload: "{}",
	}); err != nil {
		t.Fatalf("submit the command: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: "wire-cmd"})
	if err != nil {
		t.Fatalf("start the command's execution: %v", err)
	}
	if _, err = led.AppendArtifact(ctx, content.AppendArtifact{
		ID: "art-cmd", EntryID: "wire-cmd", ExecutionID: &execID,
		MediaType: content.MediaVT,
	}); err != nil {
		t.Fatalf("append the command artifact: %v", err)
	}
	cmd := vaultCall(t, conn, "ledger.get", map[string]any{"id": "wire-cmd"}, 31)
	if cmd.Error != nil {
		t.Fatalf("ledger.get on the command: %+v", cmd.Error)
	}
	var cmdBody struct {
		Artifacts []struct {
			ID string `json:"id"`
		} `json:"artifacts"`
	}
	if err = json.Unmarshal(cmd.Result, &cmdBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cmdBody.Artifacts) != 1 || cmdBody.Artifacts[0].ID != "art-cmd" {
		t.Fatalf("the command's artifacts off the socket = %+v, want the body its attempt produced",
			cmdBody.Artifacts)
	}
}

// ── the store's failures reach the caller ────────────────────────────────

// Every external call this path makes has a test where it fails. The store
// is closed underneath the handler: both reads report the failure rather
// than answering with an empty page, which cannot be told from "no history".
func TestLedgerReadMethods_ReportAStoreFailure(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	openEntry(t, conn, sid, "entry-1", "make test", 2)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i, method := range []string{"ledger.query", "ledger.get"} {
		params := map[string]any{"scope": "everywhere"}
		if method == "ledger.get" {
			params = map[string]any{"id": "entry-1"}
		}
		resp := vaultCall(t, conn, method, params, 10+i)
		if resp.Error == nil {
			t.Fatalf("%s over a closed store answered %s", method, resp.Result)
		}
	}
}

// With no content store wired at all, the read methods say the method is not
// available rather than answering an empty page that reads as "no history".
func TestLedgerReadMethods_WithoutAContentStore(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "ledger.query", map[string]any{"scope": "everywhere"}, 2)
	if resp.Error == nil {
		t.Fatalf("ledger.query with no store answered %s", resp.Result)
	}
	got := vaultCall(t, conn, "ledger.get", map[string]any{"id": "entry-1"}, 3)
	if got.Error == nil {
		t.Fatalf("ledger.get with no store answered %s", got.Result)
	}
}

// The read RESTORE is made of (nocx-ycla4, design §5): a pane's own blocks,
// asked for by the anchor that survives a restart. The store has honoured
// PaneID since nocx-rtg0.28; until now the wire could not say it, so the
// renderer could ask for "everything" and filter — which would have been a
// second ordering implementation, in the place the ladder's whole point is
// that there is one.
func TestLedgerQuery_NarrowsToOnePane(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	ctx := context.Background()

	const paneA = "0198f2b0-0000-7000-8000-0000000000a1"
	const paneB = "0198f2b0-0000-7000-8000-0000000000b1"
	seedPaneChain(t, db, paneA, paneB)

	recordInPane(t, db, paneA, "make test")
	recordInPane(t, db, paneA, "make lint")
	recordInPane(t, db, paneB, "git status")

	intents := func(paneID string, id int) []string {
		resp := vaultCall(t, conn, "ledger.query", map[string]any{
			"scope": "everywhere", "paneId": paneID,
		}, id)
		if resp.Error != nil {
			t.Fatalf("ledger.query paneId=%s: %+v", paneID, resp.Error)
		}
		var page struct {
			Entries []struct {
				Intent string `json:"intent"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]string, 0, len(page.Entries))
		for _, e := range page.Entries {
			out = append(out, e.Intent)
		}
		return out
	}

	// Newest first, and only this pane's — the other pane's command is not a
	// near miss, it is a different tab's work.
	if got := intents(paneA, 1); len(got) != 2 || got[0] != "make lint" || got[1] != "make test" {
		t.Fatalf("pane A = %v, want [make lint, make test]", got)
	}
	if got := intents(paneB, 2); len(got) != 1 || got[0] != "git status" {
		t.Fatalf("pane B = %v, want [git status]", got)
	}

	// Absent paneId is unchanged: the ladder without a pane filter still sees
	// everything, because recall is scoped by environment and directory and
	// never by pane (ADR-0019 §5).
	resp := vaultCall(t, conn, "ledger.query", map[string]any{"scope": "everywhere"}, 3)
	if resp.Error != nil {
		t.Fatalf("ledger.query without paneId: %+v", resp.Error)
	}
	var all struct {
		Entries []json.RawMessage `json:"entries"`
	}
	_ = json.Unmarshal(resp.Result, &all)
	if len(all.Entries) != 3 {
		t.Fatalf("unfiltered page = %d entries, want 3", len(all.Entries))
	}

	// An id that is not a UUIDv7 is refused rather than silently matching
	// nothing: an empty page is the answer most likely to be believed.
	if bad := vaultCall(t, conn, "ledger.query", map[string]any{
		"scope": "everywhere", "paneId": "pane-1",
	}, 4); bad.Error == nil || bad.Error.Code != -32602 {
		t.Fatalf("paneId=pane-1 = %+v, want -32602", bad.Error)
	}
	_ = ctx
}

// seedPaneChain records the layout chain two panes hang on: a workspace, its
// first tab and pane, then a second tab with the other pane. The FK checks
// it, so the fixture builds the real chain rather than inserting bare rows.
func seedPaneChain(t *testing.T, db content.ContentDB, paneA, paneB string) {
	t.Helper()
	ctx := context.Background()
	const wsID = "0198f2b0-0000-7000-8000-00000000f001"
	const tabA = "0198f2b0-0000-7000-8000-00000000f002"
	const tabB = "0198f2b0-0000-7000-8000-00000000f003"
	if _, err := db.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: wsID, Name: "work"},
		content.Tab{ID: tabA, WorkspaceID: wsID, Layout: content.LayoutRow},
		content.Pane{ID: paneA, TabID: tabA, Cwd: "/repo", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, err := db.Layout().CreateTab(ctx,
		content.Tab{ID: tabB, WorkspaceID: wsID, Position: 1, Layout: content.LayoutRow},
		content.Pane{ID: paneB, TabID: tabB, Cwd: "/srv", Kind: content.PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
}

func recordInPane(t *testing.T, db content.ContentDB, paneID, intent string) {
	t.Helper()
	pane := paneID
	if _, err := db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		PaneID: &pane,
		Cwd:    "/repo",
		Intent: intent,
		Status: content.EntrySuccess,
		Source: content.SourceUser,
	}); err != nil {
		t.Fatalf("RecordCompleted(%q): %v", intent, err)
	}
}

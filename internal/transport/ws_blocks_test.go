package transport

// The block source's tests (nocx-5u3oz.6): what the model may list and read,
// asserted against a REAL ledger holding blocks from another tab and from an
// earlier session of the same tab, and a REAL session registry — the two
// facts the scope is derived from (which pane the session is the pipe of,
// and when it was opened) come from the session itself, not from a fake that
// could be made to agree with the code.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

const (
	blockPaneThis  = "0198f2b0-0000-7000-8000-0000000000a1"
	blockPaneOther = "0198f2b0-0000-7000-8000-0000000000a2"
)

// waitPastMilli returns once the backend wall clock has left millisecond ms.
// It is not a sleep and not a timing assumption: the session floor is a
// wall-clock millisecond, so a fixture writing "before the session" and
// "after the session" inside ONE millisecond would be writing an ambiguity
// rather than exercising the boundary. It waits for an observable change —
// the clock's own tick — and nothing longer.
func waitPastMilli(ms int64) {
	for time.Now().UnixMilli() <= ms {
		runtime.Gosched()
	}
}

// captureBody attaches a plain body to a recorded block, the way the
// renderer's capture does (the derived text/plain artifact is the one search,
// copy and the block tools read).
func captureBody(t *testing.T, db content.ContentDB, entryID, artifactID, body string) {
	t.Helper()
	cols, rows := 80, 24
	kept, err := db.Ledger().CaptureOutput(context.Background(), content.CaptureOutput{
		EntryID:        entryID,
		ArtifactID:     artifactID,
		MediaType:      content.MediaText,
		CaptureMethod:  content.CaptureTerminalCells,
		CaptureVersion: 1,
		TerminalCols:   &cols,
		TerminalRows:   &rows,
		Seq:            1,
		Body:           []byte(body),
	})
	if err != nil {
		t.Fatalf("CaptureOutput(%s): %v", entryID, err)
	}
	if !kept {
		t.Fatalf("CaptureOutput(%s) did not keep the body", entryID)
	}
}

// recordBlockWithBody records one finished command in a pane and gives it an
// output body; it answers the entry id, which is the id the tools address.
func recordBlockWithBody(t *testing.T, db content.ContentDB, paneID, intent, artifactID, body string) string {
	t.Helper()
	pane := paneID
	id, err := db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		PaneID: &pane,
		Cwd:    "/repo",
		Intent: intent,
		Status: content.EntrySuccess,
		Source: content.SourceUser,
	})
	if err != nil {
		t.Fatalf("RecordCompleted(%q): %v", intent, err)
	}
	if id == "" {
		t.Fatalf("RecordCompleted(%q) recorded nothing", intent)
	}
	captureBody(t, db, id, artifactID, body)
	// Leave the millisecond this row was stamped with before returning. The
	// session floor is a wall-clock millisecond and the comparison includes
	// it, so a fixture that records "before the session" and then opens the
	// session inside ONE tick writes a row the boundary cannot classify —
	// which is an ambiguity in the fixture, not a case worth asserting.
	waitPastMilli(entrySubmittedAt(t, db, id))
	return id
}

// entrySubmittedAt reads back the wall clock the store stamped on a row: the
// value the session floor is compared against.
func entrySubmittedAt(t *testing.T, db content.ContentDB, id string) int64 {
	t.Helper()
	e, err := db.Ledger().Entry(context.Background(), id)
	if err != nil || e == nil {
		t.Fatalf("Entry(%q): %v", id, err)
	}
	return e.SubmittedAt
}

// blockHarness is a server over a real store and a real session registry,
// with the layout chain two panes hang on and a live session in the first.
type blockHarness struct {
	ws   *WSServer
	db   content.ContentDB
	stop func()
}

func newBlockHarness(t *testing.T) *blockHarness {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	db := newLedgerStore(t)
	seedPaneChain(t, db, blockPaneThis, blockPaneOther)
	reg := newRegWithStub(logger)
	ws := NewWSServer(logger, reg, WithContentDB(db))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := &blockHarness{ws: ws, db: db, stop: func() { _ = ws.Stop(ctx) }}
	t.Cleanup(h.stop)
	return h
}

// openIn opens a real session as the pipe of paneID and returns its id. The
// session's own OpenedAt is the floor the scope uses, so the fixture waits
// past that millisecond before recording anything that must count as "this
// session's".
func (h *blockHarness) openIn(t *testing.T, paneID string) string {
	t.Helper()
	sess, err := h.ws.registry.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cwd: "/repo", Cols: 80, Rows: 24, PaneID: paneID,
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = h.ws.registry.Close(sess.ID()) })
	waitPastMilli(sess.OpenedAt().UnixMilli())
	return string(sess.ID())
}

// TestListBlocks_ListsOnlyTheGrantedSessionsBlocks is the bead's first
// criterion, against a ledger that holds exactly the two things that must
// not be listed: a block of ANOTHER TAB (another pane), and a block of an
// EARLIER SESSION of this same tab (recorded before this session's pipe
// existed). Neither is filtered out of the answer — neither is ever in it.
func TestListBlocks_ListsOnlyTheGrantedSessionsBlocks(t *testing.T) {
	h := newBlockHarness(t)

	// Before the session exists: the same pane's earlier life.
	earlier := recordBlockWithBody(t, h.db, blockPaneThis, "cat /etc/passwd",
		"0198f2b0-0000-7000-8000-0000000000b1", "root:x:0:0")

	sid := h.openIn(t, blockPaneThis)

	// This session's own block, and another tab's, recorded after it.
	mine := recordBlockWithBody(t, h.db, blockPaneThis, "df -h",
		"0198f2b0-0000-7000-8000-0000000000b2", "Filesystem  Size\n/dev/sda1   1T")
	other := recordBlockWithBody(t, h.db, blockPaneOther, "kubectl get pods",
		"0198f2b0-0000-7000-8000-0000000000b3", "NAME READY")

	list, err := h.ws.ListSessionItems(context.Background(), sid, 10)
	if err != nil {
		t.Fatalf("ListSessionItems: %v", err)
	}
	ids := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		ids = append(ids, item.ID)
	}
	if len(ids) != 1 || ids[0] != mine {
		t.Fatalf("listed %v (%d items), want exactly the granted session's %q", ids, len(ids), mine)
	}
	for _, unwanted := range []struct{ id, what string }{
		{other, "another tab's item"},
		{earlier, "an earlier session's item"},
	} {
		for _, got := range ids {
			if got == unwanted.id {
				t.Errorf("%s (%s) was listed", unwanted.what, unwanted.id)
			}
		}
	}
	got := list.Items[0]
	if got.Command != "df -h" {
		t.Errorf("command = %q, want df -h", got.Command)
	}
	if got.State != "exited" {
		t.Errorf("state = %q, want exited", got.State)
	}
	if got.Lines != 2 {
		t.Errorf("lines=%d, want 2", got.Lines)
	}
}

// TestReadBlock_RefusesAnIDTheGrantDoesNotName is the bead's third
// criterion, asserted by GUESSING: the ids of the other tab's block and of
// the earlier session's block are handed to the read, and both answer
// exactly as an id that names nothing — one sentence, no command, no output.
func TestReadBlock_RefusesAnIDTheGrantDoesNotName(t *testing.T) {
	h := newBlockHarness(t)
	earlier := recordBlockWithBody(t, h.db, blockPaneThis, "cat /etc/shadow",
		"0198f2b0-0000-7000-8000-0000000000c1", "root:!!:19000")
	sid := h.openIn(t, blockPaneThis)
	other := recordBlockWithBody(t, h.db, blockPaneOther, "aws sts get-caller-identity",
		"0198f2b0-0000-7000-8000-0000000000c2", "arn:aws:iam::1234")
	mine := recordBlockWithBody(t, h.db, blockPaneThis, "echo hello",
		"0198f2b0-0000-7000-8000-0000000000c3", "hello")

	for _, guess := range []struct{ id, what string }{
		{other, "another tab's block"},
		{earlier, "an earlier session's block"},
		{"0198f2b0-0000-7000-8000-0000000000ff", "an id that names nothing"},
	} {
		item, err := h.ws.ReadSessionItem(context.Background(), sid, guess.id, 0, 100)
		if err == nil {
			t.Fatalf("reading %s succeeded: %+v", guess.what, item)
		}
		if err != assistant.ErrSessionItemNotFound {
			t.Errorf("reading %s answered %v, want the same answer as an unknown id", guess.what, err)
		}
		if item.Text != "" || item.Command != "" {
			t.Errorf("reading %s leaked %+v", guess.what, item)
		}
	}

	// The paired end: the granted session's own item reads.
	item, err := h.ws.ReadSessionItem(context.Background(), sid, mine, 0, 100)
	if err != nil {
		t.Fatalf("ReadSessionItem on the granted session's own item: %v", err)
	}
	if item.Text != "hello" || item.Command != "echo hello" {
		t.Fatalf("read %+v, want the item's own command and text", item)
	}
}

// TestReadSessionItem_WindowIsHonest is the second criterion at the source:
// the returned window is clamped to the stored output.
func TestReadSessionItem_WindowIsHonest(t *testing.T) {
	h := newBlockHarness(t)
	sid := h.openIn(t, blockPaneThis)
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}
	id := recordBlockWithBody(t, h.db, blockPaneThis, "make -j",
		"0198f2b0-0000-7000-8000-0000000000d1", strings.Join(lines, "\n"))

	item, err := h.ws.ReadSessionItem(context.Background(), sid, id, 290, 50)
	if err != nil {
		t.Fatalf("ReadSessionItem: %v", err)
	}
	if item.Total != 300 {
		t.Fatalf("total = %d, want 300", item.Total)
	}
	if item.Start != 290 || item.End != 300 {
		t.Fatalf("returned window = [%d,%d), want [290,300)", item.Start, item.End)
	}
	if !strings.HasSuffix(item.Text, "line-299") || !strings.HasPrefix(item.Text, "line-290") {
		t.Fatalf("text = %q, want lines 290..299", item.Text)
	}

	past, err := h.ws.ReadSessionItem(context.Background(), sid, id, 5000, 10)
	if err != nil {
		t.Fatalf("a window past the end must be answered, not refused: %v", err)
	}
	if past.Start != 300 || past.End != 300 || past.Text != "" {
		t.Fatalf("past the end = [%d,%d) %q, want the empty span at 300", past.Start, past.End, past.Text)
	}
	if past.Total != 300 {
		t.Fatalf("past the end total = %d, want 300", past.Total)
	}
}

// A block whose body the store never kept is listed as a block with no body
// and reads as a stated absence: history off, output retention off and a
// sensitive command all land here, and none of them may look like a command
// that printed nothing.
func TestBlocks_ABlockWithNoBodyIsStatedNotEmpty(t *testing.T) {
	h := newBlockHarness(t)
	sid := h.openIn(t, blockPaneThis)
	pane := blockPaneThis
	id, err := h.db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		PaneID: &pane,
		Cwd:    "/repo",
		Intent: "ssh prod",
		Status: content.EntryUnknown,
		Source: content.SourceUser,
	})
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}

	list, listErr := h.ws.ListSessionItems(context.Background(), sid, 10)
	if listErr != nil {
		t.Fatalf("ListSessionItems: %v", listErr)
	}
	if len(list.Items) != 1 || list.Items[0].Lines != 0 || list.Items[0].State != "exited" {
		t.Fatalf("listed %+v, want one exited item with no body lines", list.Items)
	}
	item, readErr := h.ws.ReadSessionItem(context.Background(), sid, id, 0, 10)
	if readErr != nil {
		t.Fatalf("ReadSessionItem: %v", readErr)
	}
	if item.Total != 0 || item.Text != "" || item.Note == "" {
		t.Fatalf("read %+v, want a stated absence", item)
	}
	if item.Command != "ssh prod" {
		t.Fatalf("command = %q, want the item's own command", item.Command)
	}
}

// The model is offered blocks that ENDED, and never one still open.
//
// It matters more since a turn became a block of its own (nocx-4em1z): the
// open entry in a pane is usually THE QUESTION BEING ANSWERED RIGHT NOW, so
// without this the model is handed its own unanswered question as context.
// The same rule covers a command still running — an open entry has no
// outcome and no complete body, and a summary of one would be a fact that
// changes under the reader.
func TestListBlocks_AnOpenEntryIsNotOffered(t *testing.T) {
	h := newBlockHarness(t)
	sid := h.openIn(t, blockPaneThis)
	pane := blockPaneThis
	ctx := context.Background()

	// One finished block, and one entry still open in the same pane.
	if _, err := h.db.Ledger().RecordCompleted(ctx, content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		PaneID: &pane,
		Cwd:    "/repo",
		Intent: "make ci",
		Status: content.EntrySuccess,
		Source: content.SourceUser,
	}); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	if _, err := h.db.Ledger().Submit(ctx, content.SubmitEntry{
		ID:            "0198f2b0-0000-7000-8000-0000000000f1",
		Client:        "test-client",
		EnvironmentID: "local",
		PaneID:        &pane,
		Cwd:           "/repo",
		Kind:          content.EntryShell,
		Intent:        "why did the build fail?",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	list, err := h.ws.ListSessionItems(ctx, sid, 10)
	if err != nil {
		t.Fatalf("ListSessionItems: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("listed %+v, want finished and running items", list.Items)
	}
	states := map[string]string{}
	for _, item := range list.Items {
		states[item.Command] = item.State
	}
	if states["make ci"] != "exited" || states["why did the build fail?"] != "running" {
		t.Fatalf("item states = %v, want make ci=exited and question=running", states)
	}
}

// A session this backend does not hold is an ERROR and never an empty list:
// "there is no such session" and "this session has run nothing" must not
// look alike, or the model reads the second and tells the person so.
func TestListBlocks_UnknownSessionIsNotAnEmptyList(t *testing.T) {
	h := newBlockHarness(t)
	if _, err := h.ws.ListSessionItems(context.Background(), "0000000000000000000000000000dead", 10); err == nil {
		t.Fatal("listing an unknown session succeeded; want an error")
	}
	// And a session attached to no recorded pane says exactly that: it has
	// no anchor in the record, so nothing it ran was ever anchored either.
	sess, err := h.ws.registry.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cwd: "/repo", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = h.ws.registry.Close(sess.ID()) })
	_, err = h.ws.ListSessionItems(context.Background(), string(sess.ID()), 10)
	if err == nil {
		t.Fatal("listing a session with no pane succeeded; want an error")
	}
	if !strings.Contains(err.Error(), "no recorded pane") {
		t.Errorf("error = %v, want it to name the missing anchor", err)
	}
}

// ── end to end, off the real socket ──────────────────────────────────────

// TestBlocks_EndToEndOverTheRealSocket is the wiring proof the deadcode
// ratchet cannot give (AGENTS.md testing rule 2: on an interface-first
// codebase the only check that reports a feature as WIRED is the one that
// watches a user do it). A person asks a question in a pane that has run a
// long command; the REAL engine, with the embedded schemas, is offered the
// block tools by the run's grant; it lists, learns the total, reads the END
// of the output, and the answer that streams back off the socket names the
// line that lives there and nowhere else.
func TestBlocks_EndToEndOverTheRealSocket(t *testing.T) {
	const marker = "fatal: no space left on device"
	lines := make([]string, 400)
	for i := range lines {
		lines[i] = fmt.Sprintf("compiling module-%03d", i)
	}
	lines[398] = marker

	// The provider: list, then read the window the total points at, then
	// answer with what the tool result actually carried.
	prov := &blockToolProvider{session: "", marker: marker}
	srv := httptest.NewServer(http.HandlerFunc(prov.serve))
	defer srv.Close()

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
	h.createEndpointAt(srv.URL)

	seedPaneChain(t, h.db, blockPaneThis, blockPaneOther)
	sid, openErr := openSessionInPane(t, h.conn, blockPaneThis, 1)
	if openErr != nil {
		t.Fatalf("open in pane: %+v", openErr)
	}
	prov.session = sid
	// The session is open: everything recorded from here is its own.
	waitPastMilli(sessionOpenedAt(t, h.ws, sid))
	recordBlockWithBody(t, h.db, blockPaneThis, "make -j8",
		"0198f2b0-0000-7000-8000-0000000000e1", strings.Join(lines, "\n"))

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-0000000000e9",
		"sessionId": sid,
		"question":  "why did the build fail?",
		"cwd":       "/repo",
	}, 2); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	// The answer, off the socket, in deltas.
	var answer strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(answer.String(), marker) {
		raw := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		if raw == nil {
			break
		}
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, raw)
		}
		answer.WriteString(d.Text)
	}
	if !strings.Contains(answer.String(), marker) {
		t.Fatalf("answer = %q, want the line at the END of 400 lines of output.\nprovider saw %d requests, last tool call: %s",
			answer.String(), prov.requests, prov.lastCall)
	}
	if prov.readStart < 300 {
		t.Errorf("the model read from line %d; the marker is at 398 — the list's total did not reach it", prov.readStart)
	}
}

// blockToolProvider is the fake model of the end-to-end: it proposes
// blocks.list, then aims blocks.read at the END using the total the list
// result carried, then answers with the marker IF the tool result it was
// handed actually contained it. The last step is what makes the assertion
// about the product rather than about the test.
type blockToolProvider struct {
	session   string
	marker    string
	requests  int
	lastCall  string
	readStart int
}

func (p *blockToolProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.requests++
	switch p.requests {
	case 1:
		p.lastCall = "session.list"
		streamToolCallChunk(w, "session.list", fmt.Sprintf(`{"sessionId":%q}`, p.session))
	case 2:
		itemID, total := listedItemFrom(string(body))
		p.readStart = total - 20
		if p.readStart < 0 {
			p.readStart = 0
		}
		p.lastCall = "session.read"
		streamToolCallChunk(w, "session.read", fmt.Sprintf(
			`{"sessionId":%q,"id":%q,"start":%d,"count":20}`, p.session, itemID, p.readStart))
	default:
		if strings.Contains(string(body), p.marker) {
			streamAnswerChunk(w, p.marker)
			return
		}
		streamAnswerChunk(w, "the output ends with something else")
	}
}

// listedItemFrom reads the item id and line count out of the session.list
// result the engine put into the conversation — the model's own path to both.
func listedItemFrom(body string) (string, int) {
	id := jsonStringAfter(body, `\"id\":\"`)
	total := 0
	if i := strings.Index(body, `\"lines\":`); i >= 0 {
		rest := body[i+len(`\"lines\":`):]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		total, _ = strconv.Atoi(rest[:end])
	}
	return id, total
}

func jsonStringAfter(body, key string) string {
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	end := strings.Index(rest, `\"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// streamAnswerChunk writes one streamed answer the way a completion arrives.
func streamAnswerChunk(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	chunk := func(content, finish string) {
		d := map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 0,
			"model": "probe-model",
			"choices": []map[string]any{{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": content},
				"finish_reason": finish,
			}},
		}
		b, _ := json.Marshal(d)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	chunk(text, "")
	chunk("", "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func sessionOpenedAt(t *testing.T, ws *WSServer, sid string) int64 {
	t.Helper()
	sess, err := ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry.Get(%q): %v", sid, err)
	}
	return sess.OpenedAt().UnixMilli()
}

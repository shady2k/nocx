package transport

// notify.feed.read / notify.feed.markRead / notify.feed.changed — the
// renderer's window onto the notification centre (nocx-p0xhg.5).
//
// The tests that matter here are the ones driving the REAL method through the
// REAL socket: a test validating a payload it built itself proves the struct
// is well-formed, not that the server sends it. That is the defect
// contracts/ exists for — vault.status had never sent defaultProvider while
// both suites were green.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/transport/outbound"
)

// newNotifyFeedWS builds a started WSServer with the feed wired (nil leaves
// WithNotifyFeed unwired — the -32601 shape) and one connected client. Same
// shape as newNotifyRaiserWS, which is the package's existing vocabulary for
// this: one helper per seam, not one harness per test.
func newNotifyFeedWS(t *testing.T, f NotifyFeed) (*WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger), WithNotifyFeed(f))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return ws, conn
}

// newTestFeed is the feed the wire tests read: small budgets, a real clock,
// and constructed through NewFeed so a limits change that breaks validation
// breaks here too.
func newTestFeed(t *testing.T) *notify.Feed {
	t.Helper()
	return newTestFeedWithTail(t, 20)
}

// newTestFeedWithTail is the same feed with the run tail as the one knob, so
// a test that wants runDropped > 0 says so in one number instead of adding
// twenty occurrences to provoke it.
func newTestFeedWithTail(t *testing.T, retained int) *notify.Feed {
	t.Helper()
	feed, err := notify.NewFeed(notify.FeedLimits{
		MaxOccurrences:   10,
		MaxRetainedBytes: 1 << 20,
		MaxRunRetained:   retained,
		CollapseWindow:   30 * time.Second,
	}, notify.RealClock{})
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	return feed
}

func TestNotifyFeedRead_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.feed.read.schema.json")
	feed := newTestFeed(t)
	feed.Add(notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelWarning,
		At:          time.Now(),
		Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
	})

	_, conn := newNotifyFeedWS(t, feed)
	resp := jsonrpcCall(t, conn, "notify.feed.read", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("notify.feed.read: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "notify.feed.read result")
}

// The mark is authoritative: the result carries the revision the renderer
// applies directly, and the feed the NEXT read returns has no unread rows
// left. A schema-shaped answer that marked nothing would satisfy the
// contract perfectly — which is how vault.status stayed green while never
// sending the field the page read — so the second read is the assertion
// that matters.
func TestNotifyFeedMarkRead_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.feed.markRead.schema.json")
	feed := newTestFeed(t)
	feed.Add(notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelWarning,
		At:          time.Now(),
		Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
	})

	_, conn := newNotifyFeedWS(t, feed)
	before := feedRead(t, conn, 1)
	if before.UnreadCount != 1 {
		t.Fatalf("unreadCount before the mark = %d, want 1", before.UnreadCount)
	}

	raw := callResult(t, conn, "notify.feed.markRead", 2)
	validateJSON(t, schema, raw, "notify.feed.markRead result")

	var marked feedMarkReadResult
	if err := json.Unmarshal(raw, &marked); err != nil {
		t.Fatalf("unmarshal markRead result: %v", err)
	}
	if marked.Revision <= before.Revision {
		t.Errorf("revision = %d, want greater than the pre-mark %d", marked.Revision, before.Revision)
	}

	after := feedRead(t, conn, 3)
	if after.UnreadCount != 0 {
		t.Errorf("unreadCount after the mark = %d, want 0", after.UnreadCount)
	}
	if after.Revision != marked.Revision {
		t.Errorf("read revision = %d, want the mark's %d", after.Revision, marked.Revision)
	}
	if len(after.Occurrences) != 1 || !after.Occurrences[0].Read {
		t.Errorf("occurrences after the mark = %+v, want the one row flagged read", after.Occurrences)
	}
}

// The rows the renderer draws carry what the feed holds, not merely a
// schema-shaped skeleton: the id ingress minted, the collapse count, the
// attribution the source stamped. `trust` is absent from the DTO by
// construction (there is no field to send), so what this pins is that
// everything else IS sent.
func TestNotifyFeedRead_CarriesTheFeedsOwnValues(t *testing.T) {
	feed := newTestFeed(t)
	at := time.Now()
	ev := notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelWarning,
		At:          at,
		Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
	}
	id := feed.Add(ev)
	feed.Add(ev) // the same collapse key inside the window: one row, count 2

	_, conn := newNotifyFeedWS(t, feed)
	got := feedRead(t, conn, 1)
	if len(got.Occurrences) != 1 {
		t.Fatalf("occurrences = %d, want the two adds collapsed into 1", len(got.Occurrences))
	}
	o := got.Occurrences[0]
	if len(o.Run) != 2 {
		t.Fatalf("run = %+v, want the two adds as two members", o.Run)
	}
	want := feedOccurrenceDTO{
		ID: string(id), At: o.At, Title: "deploy failed", Body: "exit status 1",
		Kind: "session.ended", Level: "warning", Count: 2, Read: false,
		BackendID: "local", SessionID: "s1", Host: "prod-1",
		Run: []feedRunMemberDTO{
			{ID: o.Run[0].ID, At: o.Run[0].At, Title: "deploy failed", Read: false},
			{ID: o.Run[1].ID, At: o.Run[1].At, Title: "deploy failed", Read: false},
		},
		RunDropped: 0,
	}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("occurrence = %+v, want %+v", o, want)
	}
	if o.Run[0].ID == string(id) && o.Run[1].ID == string(id) {
		t.Errorf("both members carry the row's id %q; a join mints its own", id)
	}
	if o.At == "" {
		t.Error("at is empty; ingress stamps it and the wire must carry it")
	}
	// The one field that must never appear. Read the raw bytes rather than
	// the struct: the struct cannot carry it, which is the point, but the
	// handler could still have marshalled something else.
	var loose map[string]any
	if err := json.Unmarshal(rawFeedRead(t, conn, 2), &loose); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	occs, _ := loose["occurrences"].([]any)
	if len(occs) != 1 {
		t.Fatalf("occurrences = %v", loose["occurrences"])
	}
	row, _ := occs[0].(map[string]any)
	if _, present := row["trust"]; present {
		t.Error("the wire carries trust; it is a routing capability bound (ADR-0029 §3), not something a surface renders")
	}
}

// An empty feed sends occurrences as [] and never null: the schema's
// `type: array` rejects null, and that is exactly the defect the contracts'
// first run caught on vault.status.
func TestNotifyFeedRead_EmptyFeedSendsAnArray(t *testing.T) {
	schema := loadSchema(t, "notify.feed.read.schema.json")
	_, conn := newNotifyFeedWS(t, newTestFeed(t))
	raw := rawFeedRead(t, conn, 1)
	validateJSON(t, schema, raw, "notify.feed.read result (empty feed)")
	if !strings.Contains(string(raw), `"occurrences":[]`) {
		t.Errorf("empty feed sent %s, want occurrences as []", raw)
	}
}

// The run, off the REAL socket, with a tail that has overflowed. This is the
// case the whole of D2 exists for: the row says it collapsed five, holds the
// newest two, and admits the three it no longer has — so an expansion can say
// "2 of 5" rather than presenting a truncation as the whole.
//
// It is driven through the real method rather than through snapshotToResult
// because a test that builds its own payload proves the struct is
// well-formed, not that the server sends it.
func TestNotifyFeedRead_OverTheWireCarriesTheRunTail(t *testing.T) {
	schema := loadSchema(t, "notify.feed.read.schema.json")
	feed := newTestFeedWithTail(t, 2)

	base := time.Now()
	titles := []string{"step 1", "step 2", "step 3", "step 4", "step 5"}
	for i, title := range titles {
		feed.Add(notify.Event{
			SessionID: "s1", Title: title,
			Kind: notify.KindBlockFinished, Trust: notify.TrustAttested, Level: notify.LevelSuccess,
			At:          base.Add(time.Duration(i) * time.Second),
			Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
		})
	}

	_, conn := newNotifyFeedWS(t, feed)
	raw := rawFeedRead(t, conn, 1)
	validateJSON(t, schema, raw, "notify.feed.read result (an overflowed tail)")

	var got feedReadResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Occurrences) != 1 {
		t.Fatalf("occurrences = %d, want the five adds collapsed into 1", len(got.Occurrences))
	}
	o := got.Occurrences[0]
	if o.Count != 5 {
		t.Errorf("count = %d, want 5", o.Count)
	}
	if o.RunDropped != 3 {
		t.Errorf("runDropped = %d, want 3", o.RunDropped)
	}
	if o.Count != len(o.Run)+o.RunDropped {
		t.Errorf("count %d != run %d + runDropped %d — the invariant an expansion counts on", o.Count, len(o.Run), o.RunDropped)
	}
	// Newest first, the same direction as occurrences: the schema says so
	// and the renderer draws the expansion in the order it arrives.
	wantTitles := []string{"step 5", "step 4"}
	for i, m := range o.Run {
		if m.Title != wantTitles[i] {
			t.Errorf("member %d title = %q, want %q — the tail is the NEWEST members, newest first", i, m.Title, wantTitles[i])
		}
		if m.At == "" {
			t.Errorf("member %d carries no instant; an expansion whose rows share one timestamp is not worth opening", i)
		}
	}
	if len(o.Run) == 2 && o.Run[0].At == o.Run[1].At {
		t.Errorf("both members carry the instant %q; each keeps its own arrival", o.Run[0].At)
	}
}

// A member carries exactly four keys. No trust, no level and no body: the ROW
// owns severity and detail, and a member that could disagree with its row
// would be a second answer to one question. Read off the raw bytes, because
// `additionalProperties: false` in the schema and the absence of a struct
// field are two claims and this asserts the one that reaches the renderer.
func TestNotifyFeedRead_AMemberCarriesNoSeverityAndNoDetail(t *testing.T) {
	feed := newTestFeed(t)
	feed.Add(notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Kind: notify.KindSessionEnded, Trust: notify.TrustAttested, Level: notify.LevelDanger,
		At:          time.Now(),
		Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
	})

	_, conn := newNotifyFeedWS(t, feed)
	var loose map[string]any
	if err := json.Unmarshal(rawFeedRead(t, conn, 1), &loose); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	occs, _ := loose["occurrences"].([]any)
	if len(occs) != 1 {
		t.Fatalf("occurrences = %v", loose["occurrences"])
	}
	row, _ := occs[0].(map[string]any)
	members, _ := row["run"].([]any)
	if len(members) != 1 {
		t.Fatalf("run = %v, want the fresh row holding itself", row["run"])
	}
	member, _ := members[0].(map[string]any)
	want := map[string]bool{"id": true, "at": true, "title": true, "read": true}
	for key := range member {
		if !want[key] {
			t.Errorf("a member carries %q; the row owns severity and detail, and a member that could disagree with its row would be a second answer to one question", key)
		}
	}
	for key := range want {
		if _, present := member[key]; !present {
			t.Errorf("a member is missing %q, which the schema requires", key)
		}
	}
}

// Marking the feed read marks the row AND every member it holds; a later join
// clears only the row's. That asymmetry is the whole reason an expansion
// shows individual marks, and this is it arriving at the renderer.
func TestNotifyFeedRead_MemberReadMarksSurviveAJoin(t *testing.T) {
	feed := newTestFeed(t)
	base := time.Now()
	add := func(title string, i int) {
		feed.Add(notify.Event{
			SessionID: "s1", Title: title,
			Kind: notify.KindBlockFinished, Trust: notify.TrustAttested, Level: notify.LevelSuccess,
			At:          base.Add(time.Duration(i) * time.Second),
			Attribution: notify.Attribution{Backend: "local", Host: "prod-1", Session: "s1"},
		})
	}
	add("seen 1", 0)
	add("seen 2", 1)

	_, conn := newNotifyFeedWS(t, feed)
	_ = callResult(t, conn, "notify.feed.markRead", 1)

	marked := feedRead(t, conn, 2)
	for i, m := range marked.Occurrences[0].Run {
		if !m.Read {
			t.Fatalf("member %d (%q) is unread after markRead", i, m.Title)
		}
	}

	add("unseen", 2)

	after := feedRead(t, conn, 3)
	o := after.Occurrences[0]
	if o.Read {
		t.Error("the row stayed read after a join; the count rose, so there is something new to see")
	}
	if len(o.Run) != 3 {
		t.Fatalf("run = %+v, want three members", o.Run)
	}
	// Newest first: the new arrival leads, the two seen ones follow with
	// their marks intact.
	if o.Run[0].Title != "unseen" || o.Run[0].Read {
		t.Errorf("newest member = %+v, want the unread arrival", o.Run[0])
	}
	for _, m := range o.Run[1:] {
		if !m.Read {
			t.Errorf("the join cleared %q's mark; it was seen and the new arrival was not", m.Title)
		}
	}
}

// Without WithNotifyFeed both methods answer -32601, exactly as
// notify.raise does without a raiser: the capability is absent, and the
// caller's next move is to stop calling rather than to fix its arguments.
func TestNotifyFeed_NotWired_MethodsUnavailable(t *testing.T) {
	_, conn := newNotifyFeedWS(t, nil)
	for id, method := range map[int]string{1: "notify.feed.read", 2: "notify.feed.markRead"} {
		resp := jsonrpcCallWithID(t, conn, method, map[string]any{}, id)
		var envelope struct {
			Result json.RawMessage  `json:"result"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(resp, &envelope); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
		}
		if envelope.Error == nil {
			t.Fatalf("%s answered %s on a server with no feed, want -32601", method, envelope.Result)
		}
		if envelope.Error.Code != -32601 {
			t.Errorf("%s code = %d (%s), want -32601", method, envelope.Error.Code, envelope.Error.Message)
		}
	}
}

// The change notification, off the real socket, at every attached client.
// It carries the revision and nothing else — that is what makes it
// droppable without loss (nocx-sb3f).
func TestNotifyFeedChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.feed.changed.schema.json")
	feed := newTestFeed(t)
	ws, first := newNotifyFeedWS(t, feed)
	second := connectWS(t, ws)
	t.Cleanup(func() { _ = second.Close() })

	// A dial that has returned is not yet a client: the server registers the
	// connection on its own goroutine, and a broadcast that overtakes the
	// registration legitimately misses it. Wait on the registration itself —
	// the observable — rather than on a duration.
	waitForConns(t, ws, 2)

	ws.BroadcastFeedChanged(7)

	for name, conn := range map[string]*websocket.Conn{"first client": first, "second client": second} {
		params := readNotification(t, conn, "notify.feed.changed", wantWithin)
		validateJSON(t, schema, params, "notify.feed.changed params ("+name+")")
		var got feedChangedParams
		if err := json.Unmarshal(params, &got); err != nil {
			t.Fatalf("%s: unmarshal params: %v", name, err)
		}
		if got.Revision != 7 {
			t.Errorf("%s: revision = %d, want 7", name, got.Revision)
		}
	}
}

// A client that refuses the frame must not cost another client the
// notification. The refusing connection is real and registered; its
// outbound is closed, so TryEnqueue answers ErrConnClosed deterministically
// rather than after a queue fills — a test may not depend on timing.
func TestBroadcastFeedChangedSurvivesARefusingClient(t *testing.T) {
	ws, live := newNotifyFeedWS(t, newTestFeed(t))

	dead := &wsConn{
		out: outbound.New(refusingSocket{}, outbound.Config{}),
		log: log.NewSlogAdapter(nil),
		id:  9999,
	}
	dead.out.Close() // every later TryEnqueue answers ErrConnClosed
	ws.registerConn(dead)
	t.Cleanup(func() { ws.unregisterConn(dead) })

	ws.BroadcastFeedChanged(3)

	params := readNotification(t, live, "notify.feed.changed", wantWithin)
	var got feedChangedParams
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.Revision != 3 {
		t.Errorf("revision = %d, want 3", got.Revision)
	}
}

// refusingSocket is a Socket that never writes. It exists so a registered
// connection can refuse a frame deterministically; the pump is closed
// before any frame reaches it.
type refusingSocket struct{}

func (refusingSocket) ReadMessage() (int, []byte, error) { return 0, nil, errClosedTestSocket }
func (refusingSocket) SetWriteDeadline(time.Time) error  { return nil }
func (refusingSocket) WriteMessage(int, []byte) error    { return errClosedTestSocket }
func (refusingSocket) Close() error                      { return nil }

var errClosedTestSocket = errors.New("test socket refuses everything")

// --- small readers, so each test above says what it asserts and not how ---

func callResult(t *testing.T, conn *websocket.Conn, method string, id int) json.RawMessage {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, method, map[string]any{}, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("%s: unmarshal: %v\nraw: %s", method, err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("%s: %+v", method, envelope.Error)
	}
	return envelope.Result
}

func rawFeedRead(t *testing.T, conn *websocket.Conn, id int) json.RawMessage {
	t.Helper()
	return callResult(t, conn, "notify.feed.read", id)
}

func feedRead(t *testing.T, conn *websocket.Conn, id int) feedReadResult {
	t.Helper()
	var got feedReadResult
	if err := json.Unmarshal(rawFeedRead(t, conn, id), &got); err != nil {
		t.Fatalf("unmarshal notify.feed.read result: %v", err)
	}
	return got
}

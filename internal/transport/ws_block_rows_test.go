package transport

// The block rows stream's contract (nocx-2v80t.3.7): rows that leave the
// screen become the authenticated command's block, in order, and stop at the
// interval's authenticated end. What is NOT tested here is the helper's own
// half — until both halves merge there is no real source of rows, so these
// tests drive the WSServer's stream API the way the app wiring will, and the
// one criterion that says "over the real helper" waits for the merge, with
// the over-the-wire read-back test below proving the stored form end to end
// in the meantime.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

func aStreamRow(text string) emulator.Row {
	row := emulator.Row{Cells: make([]emulator.Cell, 0, len(text))}
	for _, r := range text {
		row.Cells = append(row.Cells, emulator.Cell{
			Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true,
		})
	}
	return row
}

type closeFailureBlockStore struct {
	ledger     content.LedgerRepository
	fail       bool
	appendFail bool
}

func (s *closeFailureBlockStore) OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error) {
	return s.ledger.OpenBlockOutput(ctx, in)
}

func (s *closeFailureBlockStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	if s.appendFail {
		return fmt.Errorf("injected block append failure")
	}
	return s.ledger.AppendBlockRows(ctx, in)
}

func (s *closeFailureBlockStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	if s.fail {
		return content.BlockRowsSummary{}, fmt.Errorf("injected block close failure")
	}
	return s.ledger.CloseBlockRows(ctx, in)
}

func (s *closeFailureBlockStore) RecordClearBoundary(ctx context.Context, in content.RecordClearBoundary) (content.ClearBoundaryRecorded, error) {
	return s.ledger.RecordClearBoundary(ctx, in)
}

func blockRowsBody(t *testing.T, db content.ContentDB, entryID string) string {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	if row == nil {
		t.Fatalf("no entry %s", entryID)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType != content.MediaBlockRows {
				continue
			}
			art, err := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
			if err != nil {
				t.Fatalf("Artifact: %v", err)
			}
			if art == nil {
				t.Fatal("metadata named an artifact the store does not hold")
			}
			var body strings.Builder
			for _, c := range art.Chunks {
				body.Write(c)
			}
			return body.String()
		}
	}
	return ""
}

// startsACommand submits the command the way a typed command really flows —
// the submit opens the entry and the shell's authenticated start attaches to
// it — and answers the entry the attempt rides (the attempt id IS the entry
// id; the store writes them as one row).
func startsACommand(t *testing.T, e *lifecycleTestEnv, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle, seq uint64, command string) string {
	t.Helper()
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 41))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, seq, lifecycleStartEvt(nil, command)))
	return got.ID
}

// streamRows reads a stored block the way a client does: the artifact body
// parsed line by line, each row's text rejoined from its cells. A cell-per-
// grapheme row never carries a contiguous string, so a substring assert on
// the body is an assertion about nothing.
func streamRows(t *testing.T, db content.ContentDB, entryID string) []struct {
	From uint64
	Text string
} {
	t.Helper()
	body := blockRowsBody(t, db, entryID)
	var out []struct {
		From uint64
		Text string
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		// A stored row carries its line as `text` (nocx-zg3k3.2.12).
		var loose struct {
			From uint64 `json:"from"`
			Row  struct {
				Text string `json:"text"`
			} `json:"row"`
		}
		if err := json.Unmarshal([]byte(line), &loose); err != nil {
			t.Fatalf("parse stored line: %v\nline: %s", err, line)
		}
		out = append(out, struct {
			From uint64
			Text string
		}{loose.From, loose.Row.Text})
	}
	return out
}

func assertBlockSealed(t *testing.T, db content.ContentDB, entryID string) {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	for _, ex := range row.Executions {
		for _, artifact := range ex.Artifacts {
			if artifact.MediaType != content.MediaBlockRows {
				continue
			}
			stored, err := db.Ledger().Artifact(context.Background(), artifact.ID)
			if err != nil {
				t.Fatalf("Artifact(%s): %v", artifact.ID, err)
			}
			if stored.State != content.ArtifactSealed {
				t.Fatalf("block %s state = %q, want sealed", entryID, stored.State)
			}
			return
		}
	}
	t.Fatalf("entry %s has no block rows artifact", entryID)
}

// The opposite event order is also load-bearing: ledger submit/start and the
// authenticated OPEN complete before the helper offers rows, so delivery
// appends immediately to the already-selected block.
func TestBlockRowsArrived_AppendsToTheAuthenticatedCommand(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'one\\ntwo\\n'")

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("one"), aStreamRow("two"),
	})
	if !confirm || written != 2 {
		t.Fatalf("ack = (%d, %v), want exclusive end row 2", written, confirm)
	}
	kept := streamRows(t, db, attempt)
	if len(kept) != 2 {
		t.Fatalf("stored block holds %d rows, want 2", len(kept))
	}
	wantTexts := []string{"one", "two"}
	for i, want := range wantTexts {
		if kept[i].From != uint64(i) { //nolint:gosec // a row index, not a byte count
			t.Fatalf("row %d carries from=%d", i, kept[i].From)
		}
		if kept[i].Text != want {
			t.Fatalf("row %d reads %q, want %q", i, kept[i].Text, want)
		}
	}
}

// A shell can submit the next command before the helper has delivered the
// previous interval's queued rows. Those rows must remain with the interval
// that emitted them until its end marker opens the next block.
func TestBlockRowsArrived_WaitsForPriorIntervalBeforeNextOpen(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x61)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")

	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first-departed")}); !confirm {
		t.Fatal("the first interval's delayed row was not confirmed")
	}
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 1, []emulator.Row{aStreamRow("first-final")}, false)

	// A replay from below the closed interval's own boundary is confirmed at
	// that boundary — so the helper's mark may advance — and enters no block.
	// Its closing screen never streams again: the runtime holds it back.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first-tail-replayed")}); !confirm || written != 1 {
		t.Fatalf("replayed row ack = (%d, %v), want the closed boundary 1", written, confirm)
	}
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("second-departed")}); !confirm {
		t.Fatal("the second interval's row was not confirmed")
	}

	firstRows := streamRows(t, db, first)
	if len(firstRows) != 2 || firstRows[0].Text != "first-departed" || firstRows[1].Text != "first-final" {
		t.Fatalf("first block rows = %+v, want its two rows", firstRows)
	}
	secondRows := streamRows(t, db, second)
	if len(secondRows) != 1 || secondRows[0].Text != "second-departed" {
		t.Fatalf("second block rows = %+v, want its one row", secondRows)
	}
}

// A long command's output crosses the screen. At its end marker the rows still
// ON the screen are appended to it as its closing screen, and those rows leave
// the screen later, one at a time, as the next command's output pushes them
// off — so the next interval's stream re-delivers them at the very indices the
// closed block stored them at, and they belong to the block that already holds
// them. How many leave is the SCREEN's business, not the count's: the row the
// boundary's cursor sits above is overwritten in place and never leaves, so a
// rule that drops len(closing) rows eats the next command's first output
// (measured in the e2e: 33 closing rows, 32 departures, the successor's first
// row lost and its block never containing `-001`). Identity against the screen
// is what separates a row leaving again from the successor's own row.
func TestBlockRowsArrived_RowsBehindTheBoundaryEnterNoBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x91)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	secondFence := lifecycleFence(0x92)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))

	// The first command's departed rows, then its end marker at row 2 carrying
	// the two rows the boundary sat on — a screenful that has not left the
	// screen yet.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("first-1"), aStreamRow("first-2"),
	}); !confirm || written != 2 {
		t.Fatalf("first rows ack = (%d, %v), want rows 0..1 written", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 2, []emulator.Row{
		aStreamRow("first-3"), aStreamRow("first-4"),
	}, false)

	// A replay of the closed interval's own departed rows is confirmed and
	// stored nowhere. Its closing screen never arrives again at all: the
	// runtime holds it back (rowstream.go), so there is no delivery of it to
	// refuse — the ownership of those rows is the runtime's.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("first-1-again"), aStreamRow("first-2-again"),
	}); !confirm || written != 2 {
		t.Fatalf("replay ack = (%d, %v), want the closed boundary 2", written, confirm)
	}
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 4, 0, []emulator.Row{aStreamRow("second-1")}); !confirm || written != 5 {
		t.Fatalf("second rows ack = (%d, %v), want the successor's first row written at 4", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 5, []emulator.Row{aStreamRow("second-2")}, false)

	firstRows := streamRows(t, db, first)
	wantFirst := []struct {
		From uint64
		Text string
	}{{0, "first-1"}, {1, "first-2"}, {2, "first-3"}, {3, "first-4"}}
	if len(firstRows) != len(wantFirst) {
		t.Fatalf("first block holds %d rows, want its own rows and its closing screen: %+v", len(firstRows), firstRows)
	}
	for i, want := range wantFirst {
		if firstRows[i].From != want.From || firstRows[i].Text != want.Text {
			t.Fatalf("first block row %d = (%d, %q), want (%d, %q)", i, firstRows[i].From, firstRows[i].Text, want.From, want.Text)
		}
	}
	secondRows := streamRows(t, db, second)
	wantSecond := []struct {
		From uint64
		Text string
	}{{4, "second-1"}, {5, "second-2"}}
	if len(secondRows) != len(wantSecond) {
		t.Fatalf("second block holds %d rows, want exactly its own and no row of the previous command: %+v", len(secondRows), secondRows)
	}
	for i, want := range wantSecond {
		if secondRows[i].From != want.From || secondRows[i].Text != want.Text {
			t.Fatalf("second block row %d = (%d, %q), want (%d, %q)", i, secondRows[i].From, secondRows[i].Text, want.From, want.Text)
		}
	}
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
}

// The end marker and the authenticated completion that names its fence travel
// on different carriers, so the end marker routinely arrives first and parks.
// Its EndRow is the boundary from that moment: the rows that stream after it
// belong to the interval that follows, and the ended interval's own block must
// not swallow them. Without that, a long command's block absorbs the head of
// the next command's output, the next block opens short, and the closing
// append lands behind a cursor that already moved.
func TestBlockRowsArrived_RowsAfterAParkedEndMarkerWaitForTheNextInterval(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0xa1)

	// The first interval's own rows, then its end marker at row 2 — parked,
	// because its completion has not been published yet.
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("first-1"), aStreamRow("first-2"),
	}); !confirm || written != 2 {
		t.Fatalf("first rows ack = (%d, %v), want rows 0..1 written", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 2, []emulator.Row{aStreamRow("first-screen")}, false)

	// The shell has moved on and its next command's output is already
	// leaving the screen, while the completion is still on its way. None of
	// it may enter the first interval's block, and the mark must not claim
	// it is written either.
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{
		aStreamRow("second-1"), aStreamRow("second-2"),
	}); confirm {
		t.Fatal("rows past a parked boundary were acknowledged as written")
	}

	// The completion arrives, the fence resolves, and the first interval
	// seals with its own rows and its closing screen — and nothing else.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 5, lifecyclePromptEvt()))

	firstRows := streamRows(t, db, first)
	wantFirst := []struct {
		From uint64
		Text string
	}{{0, "first-1"}, {1, "first-2"}, {2, "first-screen"}}
	if len(firstRows) != len(wantFirst) {
		t.Fatalf("first block holds %d rows, want its own and no row of the next command: %+v", len(firstRows), firstRows)
	}
	for i, want := range wantFirst {
		if firstRows[i].From != want.From || firstRows[i].Text != want.Text {
			t.Fatalf("first block row %d = (%d, %q), want (%d, %q)", i, firstRows[i].From, firstRows[i].Text, want.From, want.Text)
		}
	}
	assertBlockSealed(t, db, first)

	// The next interval's block opens on the rows held for it — once, at
	// their own absolute indices — and its own output follows.
	second := startsACommand(t, e, pub, lane, h, 6, "printf second")
	secondFence := lifecycleFence(0xa2)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 7, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))
	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 4, 0, []emulator.Row{aStreamRow("second-3")}); !confirm || written != 5 {
		t.Fatalf("second rows ack = (%d, %v), want row 4 written", written, confirm)
	}
	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 5, []emulator.Row{aStreamRow("second-screen")}, false)

	secondRows := streamRows(t, db, second)
	wantSecond := []struct {
		From uint64
		Text string
	}{{2, "second-1"}, {3, "second-2"}, {4, "second-3"}, {5, "second-screen"}}
	if len(secondRows) != len(wantSecond) {
		t.Fatalf("second block holds %d rows, want its own four: %+v", len(secondRows), secondRows)
	}
	for i, want := range wantSecond {
		if secondRows[i].From != want.From || secondRows[i].Text != want.Text {
			t.Fatalf("second block row %d = (%d, %q), want (%d, %q)", i, secondRows[i].From, secondRows[i].Text, want.From, want.Text)
		}
	}
	assertBlockSealed(t, db, second)
}

// A queued block's authenticated completion can beat the prior interval's
// ordered end marker. Its close must wait with the queued block, otherwise the
// prior interval loses the current destination before its own end arrives.
func TestBlockRowsQueuedEndWaitsForPriorInterval(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x71)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	secondFence := lifecycleFence(0x72)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))

	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 2, nil, false)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[session.ID(sid)]
	queuedEndCount := len(e.ws.blockStream.queuedEnds[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if current == nil {
		t.Fatal("queued end removed the current block")
	}
	if current.attempt != first {
		t.Fatalf("queued end replaced current block with %q, want %q", current.attempt, first)
	}
	if queuedEndCount != 1 {
		t.Fatalf("queued end count = %d, want 1", queuedEndCount)
	}

	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 1, nil, false)
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
}

// TestBlockRowsUnknownAttemptOnNestedDomainClose_DoesNotStickTheQueue pins
// nocx-2v80t.3.24: a nested domain that closes with an attempt still open —
// the shell died before an authenticated fence could ever name it, exactly
// what `exit` does to its own remote shell (nocx-mlyu, case 1 of the race it
// measured: the start frame DOES reach the kernel sometimes) — used to leave
// that attempt's block "current" in this stream forever. attemptFact had no
// case for AttemptUnknown, and the transition reaches this stream through no
// OTHER path (lifecyclepub's transitionsBelow: PublishAttemptClosed is the
// only notice, because by the time the lane's own fact next derives, the
// domain is gone and the lane has moved past this attempt). So every later
// command in the session queued up behind a current that could never
// close. Against a real sshd (nocxify-journey.spec.ts) the remote `exit`'s
// start reached the kernel in every run measured for this bead.
func TestBlockRowsUnknownAttemptOnNestedDomainClose_DoesNotStickTheQueue(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	if err := pub.BindTransport("T2", noopPort{}); err != nil {
		t.Fatalf("BindTransport T2: %v", err)
	}
	h2, err := pub.RequestDomain(lane, &h.Domain, "T2")
	if err != nil {
		t.Fatalf("RequestDomain nested: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycle.Event{
		Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{},
	}))
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, h2, e.conn)

	// The far shell's own `exit`: its start frame reaches the kernel and
	// opens a block, but the shell that would report its completion is the
	// one `exit` destroys.
	exitAttempt := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h2.Domain), "exit"), 41))
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 2, lifecycleStartEvt(nil, "exit")))

	// The shell's own exit trap sends domain_closed before its process's own
	// exit ends the channel (nocx-2v80t.3.22's measured ordering): the
	// child's attempt goes Unknown through the kernel's ordinary close path,
	// not through a transport loss.
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 3, lifecycle.Event{
		Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{},
	}))

	// The parent reclaims the lane exactly as nocx.bash's own
	// __nocx_prompt_command does once the nested launch returns.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycle.Event{
		Kind: lifecycle.KindDomainActivated, DomainActivated: &lifecycle.DomainActivatedEvent{},
	}))

	// Without the fix, exit's block is still "current" here, and this NEXT
	// command's own end queues up behind it forever. With the fix, exit's
	// block sealed the moment it went Unknown, so this one becomes current
	// immediately and closes on its own fence like any ordinary command.
	next := startsACommand(t, e, pub, lane, h, 4, "printf next")
	nextFence := lifecycleFence(0x93)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 5, lifecycleCompleteEvt(lifecycle.AttemptID(next), 0, nextFence)))
	e.ws.BlockIntervalEnded(session.ID(sid), nextFence, 1, nil, false)

	assertBlockSealed(t, db, exitAttempt.ID)
	assertBlockSealed(t, db, next)
}

// blockRowsArtifactCount is how many block-rows artifacts an entry carries —
// one per block the stream opened for it.
func blockRowsArtifactCount(t *testing.T, db content.ContentDB, entryID string) int {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	n := 0
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				n++
			}
		}
	}
	return n
}

// submitsInChild is startsACommand for a command typed inside a child
// domain, whose frames ride the child's own transport.
func submitsInChild(t *testing.T, e *lifecycleTestEnv, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle, tID lifecycle.TransportID, seq uint64, command string) string {
	t.Helper()
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 41))
	mustLifecycleIngest(t, pub, tID, lifecycleEnv(lane, h, seq, lifecycleStartEvt(nil, command)))
	return got.ID
}

// TestBlockRowsSshJourney_EveryBlockKeepsItsOwnRows pins nocx-2v80t.3.24's
// row attribution across a whole ssh session, in the order the journey
// produced it against a real sshd: the local `ssh` block is sealed at the
// child's hello (the fenceless environment entry, nocx-2v80t.3.21), a remote
// command runs and completes, the remote `exit` destroys the shell that would
// have completed it (so it goes Unknown), and the parent reclaims the lane —
// at which point the lane's fact names the local `ssh` attempt OPEN again,
// because in the lifecycle it is: it completes for real a moment later, with
// the status the ssh client exited with.
//
// That re-report used to open the entered block a SECOND time — a fresh
// artifact for the same entry, taking the stream's single queued slot from
// whatever held it — and the ssh's real end then sealed "exit / Connection
// closed" into it. An entered block is over at the entry: its later
// completion settles the attempt, not a new block.
func TestBlockRowsSshJourney_EveryBlockKeepsItsOwnRows(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	// The local `ssh`, handing the lane to a child that says hello.
	sshAttempt := startsACommand(t, e, pub, lane, h, 2, "ssh host")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycle.Event{
		Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{},
	}))
	if err := pub.BindTransport("T2", noopPort{}); err != nil {
		t.Fatalf("BindTransport T2: %v", err)
	}
	h2, err := pub.RequestDomain(lane, &h.Domain, "T2")
	if err != nil {
		t.Fatalf("RequestDomain nested: %v", err)
	}
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, h2, e.conn)
	var noFence [32]byte
	e.ws.BlockIntervalEnded(session.ID(sid), noFence, 0, []emulator.Row{aStreamRow("password:")}, false)

	// A remote command, completed on the child's transport.
	remote := submitsInChild(t, e, pub, lane, h2, "T2", 2, "echo journey-1-ok")
	remoteFence := lifecycleFence(0xa1)
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 3, lifecycleCompleteEvt(lifecycle.AttemptID(remote), 0, remoteFence)))
	e.ws.BlockIntervalEnded(session.ID(sid), remoteFence, 0, []emulator.Row{aStreamRow("journey-1-ok")}, false)

	// The remote `exit`: its start reaches the kernel, its shell dies.
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 4, lifecyclePromptEvt()))
	exitAttempt := submitsInChild(t, e, pub, lane, h2, "T2", 5, "exit")
	mustLifecycleIngest(t, pub, "T2", lifecycleEnv(lane, h2, 6, lifecycle.Event{
		Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{},
	}))

	// The parent reclaims the lane; its `ssh` is still running there, and
	// completes for real with the client's own status.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecycle.Event{
		Kind: lifecycle.KindDomainActivated, DomainActivated: &lifecycle.DomainActivatedEvent{},
	}))
	sshFence := lifecycleFence(0xa2)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 5, lifecycleCompleteEvt(lifecycle.AttemptID(sshAttempt), 0, sshFence)))
	e.ws.BlockIntervalEnded(session.ID(sid), sshFence, 0,
		[]emulator.Row{aStreamRow("exit"), aStreamRow("Connection to host closed.")}, false)

	// The next local command gets its own block, with its own rows.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecyclePromptEvt()))
	next := startsACommand(t, e, pub, lane, h, 7, "echo local-after-exit")
	nextFence := lifecycleFence(0xa3)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 8, lifecycleCompleteEvt(lifecycle.AttemptID(next), 0, nextFence)))
	e.ws.BlockIntervalEnded(session.ID(sid), nextFence, 0, []emulator.Row{aStreamRow("local-after-exit")}, false)

	if n := blockRowsArtifactCount(t, db, sshAttempt); n != 1 {
		t.Fatalf("the entered ssh block has %d rows artifacts, want exactly the one sealed at the entry", n)
	}
	for _, want := range []struct {
		entry, what string
		rows        []string
	}{
		{sshAttempt, "the local ssh", []string{"password:"}},
		{remote, "the remote command", []string{"journey-1-ok"}},
		{next, "the local command after the session", []string{"local-after-exit"}},
	} {
		assertBlockSealed(t, db, want.entry)
		got := streamRows(t, db, want.entry)
		var texts []string
		for _, r := range got {
			texts = append(texts, r.Text)
		}
		if strings.Join(texts, "\n") != strings.Join(want.rows, "\n") {
			t.Fatalf("%s block holds %q, want %q", want.what, texts, want.rows)
		}
	}
	assertBlockSealed(t, db, exitAttempt)
}

func TestBlockRowsPendingRowsSplitAtPriorEnd(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x81)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	secondFence := lifecycleFence(0x82)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.flushing[session.ID(sid)] = true
	e.ws.blockStream.mu.Unlock()
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first-tail")}); confirm {
		t.Fatal("rows queued behind a flush were acknowledged early")
	}
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("second-row")}); confirm {
		t.Fatal("next-interval rows queued behind a flush were acknowledged early")
	}
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.flushing[session.ID(sid)] = false
	e.ws.blockStream.mu.Unlock()

	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 2, nil, false)
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 1, []emulator.Row{aStreamRow("first-final")}, false)

	firstRows := streamRows(t, db, first)
	if len(firstRows) != 2 || firstRows[0].Text != "first-tail" || firstRows[1].Text != "first-final" {
		t.Fatalf("first block rows = %+v, want exactly its two rows", firstRows)
	}
	secondRows := streamRows(t, db, second)
	if len(secondRows) != 1 || secondRows[0].Text != "second-row" {
		t.Fatalf("second block rows = %+v, want exactly its one row", secondRows)
	}
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
}

func TestBlockRowsCloseFailureRetainsCurrentForRetry(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	failing := &closeFailureBlockStore{ledger: db.Ledger(), fail: true}
	e.ws.blockRowsStore = failing
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x91)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	secondFence := lifecycleFence(0x92)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))

	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 0, nil, false)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[session.ID(sid)]
	queued := e.ws.blockStream.queued[session.ID(sid)]
	pending := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	_, fenceRetained := e.ws.blockStream.fences[session.ID(sid)][fmt.Sprintf("%x", firstFence)]
	e.ws.blockStream.mu.Unlock()
	if current == nil || current.attempt != first {
		t.Fatalf("current after failed close = %+v, want %q", current, first)
	}
	if queued != second {
		t.Fatalf("queued after failed close = %q, want %q", queued, second)
	}
	if pending != 1 || !fenceRetained {
		t.Fatalf("failed close recovery state = pending %d, fence retained %v; want 1, true", pending, fenceRetained)
	}

	failing.fail = false
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 0, nil, false)
	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 0, nil, false)
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
	e.ws.blockStream.mu.Lock()
	finalCurrent := e.ws.blockStream.current[session.ID(sid)]
	finalPending := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if finalCurrent != nil || finalPending != 0 {
		t.Fatalf("close retry left state: current=%+v pending=%d", finalCurrent, finalPending)
	}
}

func TestBlockRowsClosingAppendFailureRetainsCurrentForRetry(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	failing := &closeFailureBlockStore{ledger: db.Ledger(), appendFail: true}
	e.ws.blockRowsStore = failing
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x93)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")
	secondFence := lifecycleFence(0x94)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, secondFence)))

	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 0, []emulator.Row{aStreamRow("first-final")}, false)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[session.ID(sid)]
	pending := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if current == nil || current.attempt != first || pending != 1 {
		t.Fatalf("append failure state = current=%+v pending=%d; want first and one retry", current, pending)
	}

	failing.appendFail = false
	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 0, []emulator.Row{aStreamRow("first-final")}, false)
	e.ws.BlockIntervalEnded(session.ID(sid), secondFence, 0, nil, false)
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
}

// If the authenticated open fact beats ledger persistence, rows must stay
// unacknowledged until the bind retry opens the durable block. The helper has
// no separate copy after confirmation, so acknowledging this interval while
// current is nil would lose it permanently.
func TestBlockRowsArrived_BeforeLedgerBindIsHeldUntilRetry(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(session.ID(sid), func(upTo uint64) {
		confirmed = append(confirmed, upTo)
	})

	const command = "printf before-bind"
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 42))
	e.ws.blockStream.openAttemptFor(e.ws, session.ID(sid), got.ID)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[session.ID(sid)]
	waiting := e.ws.blockStream.waiting[session.ID(sid)]
	e.ws.blockStream.mu.Unlock()
	if current != nil || waiting != got.ID {
		t.Fatalf("pre-bind open state = current:%v waiting:%q, want no block and waiting for %q", current != nil, waiting, got.ID)
	}

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("before-bind"),
	})
	if confirm {
		t.Fatalf("pre-bind rows were acknowledged through %d; the helper cannot replay them", written)
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleStartEvt(nil, command)))

	kept := streamRows(t, db, got.ID)
	if len(kept) != 1 || kept[0].Text != "before-bind" {
		t.Fatalf("bind retry stored rows = %+v, want the held pre-bind row", kept)
	}
	// The held row sits at index 0, so the mark the helper may advance to is
	// the exclusive end of what the store holds: 1.
	if len(confirmed) != 1 || confirmed[0] != 1 {
		t.Fatalf("deferred confirmation = %v, want [1]", confirmed)
	}
}

// An authenticated end can resolve while the bind retry is flushing rows.
// The close must park behind that flush, or the artifact can seal before the
// deferred rows reach the store.
func TestBlockRowsCloseWaitsForDeferredRows(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf deferred")

	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("seed")}); !confirm {
		t.Fatal("seed row was not confirmed")
	}
	existing := streamRows(t, db, attempt)
	from := existing[len(existing)-1].From + 1
	e.ws.blockStream.mu.Lock()
	block := e.ws.blockStream.current[session.ID(sid)]
	pending := []pendingRows{{from: from, rows: []emulator.Row{aStreamRow("deferred")}}}
	e.ws.blockStream.pending[session.ID(sid)] = pending
	e.ws.blockStream.flushing[session.ID(sid)] = true
	e.ws.blockStream.mu.Unlock()

	e.ws.closeBlockRows(session.ID(sid), attempt, from+1, []emulator.Row{aStreamRow("closing")}, "deferred-close", false)
	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	stillOpen := e.ws.blockStream.open[session.ID(sid)][attempt] != nil
	e.ws.blockStream.mu.Unlock()
	if parked != 1 || !stillOpen {
		t.Fatalf("close gate state = parked:%d open:%v, want parked:1 open:true", parked, stillOpen)
	}
	e.ws.blockStream.mu.Lock()
	delete(e.ws.blockStream.pending, session.ID(sid))
	e.ws.blockStream.mu.Unlock()

	e.ws.blockStream.flushPendingRows(e.ws, session.ID(sid), block, pending, nil)
	kept := streamRows(t, db, attempt)
	if len(kept) != 3 || kept[0].Text != "seed" || kept[1].Text != "deferred" || kept[2].Text != "closing" {
		t.Fatalf("flush-before-close rows = %+v, want seed, deferred, closing", kept)
	}
	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	for _, ex := range row.Executions {
		for _, artifact := range ex.Artifacts {
			if artifact.MediaType != content.MediaBlockRows {
				continue
			}
			stored, err := db.Ledger().Artifact(context.Background(), artifact.ID)
			if err != nil {
				t.Fatalf("Artifact: %v", err)
			}
			if stored.State != content.ArtifactSealed {
				t.Fatalf("deferred block state = %q, want sealed", stored.State)
			}
		}
	}
}

// A newer command can end while an older block is flushing deferred rows. The
// close belongs to the newer attempt, not to the block whose append happened to
// drain the gate.
func TestBlockRowsCloseKeepsTheAttemptThatEndedDuringAFlush(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first")}); !confirm {
		t.Fatal("first row was not confirmed")
	}
	firstFence := lifecycleFence(0x41)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "printf second")

	e.ws.blockStream.mu.Lock()
	firstBlock := e.ws.blockStream.open[session.ID(sid)][first]
	pending := []pendingRows{{from: 1, rows: []emulator.Row{aStreamRow("deferred")}}}
	e.ws.blockStream.pending[session.ID(sid)] = pending
	e.ws.blockStream.flushing[session.ID(sid)] = true
	e.ws.blockStream.mu.Unlock()
	if firstBlock == nil {
		t.Fatal("first block was not open")
	}

	e.ws.closeBlockRows(session.ID(sid), second, 2, []emulator.Row{aStreamRow("second-final")}, "second-close", false)
	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if parked != 1 {
		t.Fatalf("newer close parked = %d, want 1", parked)
	}

	e.ws.blockStream.mu.Lock()
	delete(e.ws.blockStream.pending, session.ID(sid))
	e.ws.blockStream.mu.Unlock()
	e.ws.blockStream.flushPendingRows(e.ws, session.ID(sid), firstBlock, pending, nil)

	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 2, []emulator.Row{aStreamRow("first-final")}, false)
	assertBlockSealed(t, db, first)
	assertBlockSealed(t, db, second)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, lifecycleFence(0x42))))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 7, lifecyclePromptEvt()))

	nextSeq := uint64(8)
	for i := 3; i <= 30; i++ {
		attempt := startsACommand(t, e, pub, lane, h, nextSeq, fmt.Sprintf("printf command-%02d", i))
		if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow(fmt.Sprintf("command-%02d", i))}); !confirm {
			t.Fatalf("command %d row was not confirmed", i)
		}
		fence := lifecycleFence(byte(i))
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, nextSeq+1, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
		e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final")}, false)
		assertBlockSealed(t, db, attempt)
		if i < 30 {
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, nextSeq+2, lifecyclePromptEvt()))
		}
		nextSeq += 3
	}
}

// THE REFUSAL. Output retention off: the command keeps its row, the rows
// that follow are confirmed (the helper's mark moves past rows nobody will
// ever want) and NOTHING is stored — not even a first chunk.
func TestBlockRowsArrived_RefusedCommandConfirmsButStoresNothing(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := newLedgerStoreWithPolicy(t, policy)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make secret")

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("classified")})
	if !confirm || written != 1 {
		t.Fatalf("ack = (%d, %v), want exclusive end row 1", written, confirm)
	}
	if body := blockRowsBody(t, db, attempt); body != "" {
		t.Fatalf("a refused command stored %q — not even a first chunk may be written", body)
	}
}

// THE END, completion first. The fence the kernel accepted resolves the end
// marker: the closing rows go in, the block seals, and the attached client
// hears both that it grew and that it closed.
func TestBlockIntervalEnded_SealsWhenTheCompletionArrivedFirst(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	fence := lifecycleFence(0x44)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	// The end marker arrives AFTER its completion and must still be
	// authenticated by it.
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")}, false)

	kept := streamRows(t, db, attempt)
	if len(kept) != 2 || kept[1].From != 1 || kept[1].Text != "final screen" {
		t.Fatalf("the sealed block's rows = %+v, want the streamed row and the closing screen", kept)
	}
	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				art, _ := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
				if art.State != content.ArtifactSealed {
					t.Fatalf("the block is %q, want sealed", art.State)
				}
			}
		}
	}
	// The attached client heard about both.
	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.grew")); err != nil {
		t.Fatalf("no block.grew reached the subscriber: %v", err)
	}
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}
}

// THE END, no fence at all: an authenticated environment entry
// (nocx-2v80t.3.21) has none to carry — the shell that would have printed
// one is no longer the one holding the terminal — so it resolves to the
// session's CURRENT block directly rather than waiting for a fence that can
// never arrive.
func TestBlockIntervalEnded_NoFenceResolvesToTheCurrentBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "ssh host")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("Welcome")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	var noFence [32]byte
	e.ws.BlockIntervalEnded(session.ID(sid), noFence, 1, []emulator.Row{aStreamRow("password:")}, false)

	kept := streamRows(t, db, attempt)
	if len(kept) != 2 || kept[1].From != 1 || kept[1].Text != "password:" {
		t.Fatalf("the sealed block's rows = %+v, want the streamed row and the entry's closing screen", kept)
	}
	assertBlockSealed(t, db, attempt)

	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}
}

// With no current block to seal, the zero-nonce end is dropped rather than
// parked: parking it would wait forever for a fence a zero nonce can never
// carry, which is exactly the shape nocx-2v80t.3.9's own bound exists to
// end for an ordinary fence — extended here to the one case that bound
// cannot reach, because there is no fence to ever resolve at all.
func TestBlockIntervalEnded_NoFenceWithNoCurrentBlockIsDropped(t *testing.T) {
	e, _, _, _, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	var noFence [32]byte
	e.ws.BlockIntervalEnded(session.ID(sid), noFence, 0, nil, false)

	bs := e.ws.blockStream
	bs.mu.Lock()
	parked := len(bs.ends[session.ID(sid)])
	bs.mu.Unlock()
	if parked != 0 {
		t.Fatalf("a fenceless end with nothing to close was parked (%d ends), want dropped", parked)
	}
}

// TestBlockGrewAndClosed_OverTheWireConformsToContract is the stage review's
// finding 4 (nocx-2v80t.3.15): block.grew and block.closed were only ever
// awaited by their method name (isNotification), never validated against
// their own schemas — a field the handler stopped sending, or renamed, would
// have gone unnoticed by every existing test. This drives the same
// authenticated close as the test above and validates the params the two
// notifications ACTUALLY carried, off the real socket (AGENTS.md rule 5's
// third check), rather than a payload built by the test.
func TestBlockGrewAndClosed_OverTheWireConformsToContract(t *testing.T) {
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	fence := lifecycleFence(0x47)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")}, false)

	deadline := time.Now().Add(wantWithin)
	grewMsg, err := awaitFrame(e.conn, deadline, isNotification("block.grew"))
	if err != nil {
		t.Fatalf("no block.grew reached the subscriber: %v", err)
	}
	grewFrame, ok := decodeFrame(grewMsg)
	if !ok {
		t.Fatalf("block.grew frame did not decode: %s", grewMsg)
	}
	validateJSON(t, loadSchema(t, "block.grew.schema.json"), grewFrame.Params, "block.grew params (real socket)")

	closedMsg, err := awaitFrame(e.conn, deadline, isNotification("block.closed"))
	if err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}
	closedFrame, ok := decodeFrame(closedMsg)
	if !ok {
		t.Fatalf("block.closed frame did not decode: %s", closedMsg)
	}
	validateJSON(t, loadSchema(t, "block.closed.schema.json"), closedFrame.Params, "block.closed params (real socket)")
}

// createSessionRow writes the durable `sessions` row a real hosted open
// would already have written before any block ever runs on it
// (session_open.go's recordHostedBinding / claimSpawn) — this lightweight
// lifecycle test harness never hosts a real session, so a test that reaches
// content.RecordClearBoundary's own FOREIGN KEY on sessions(id) has to write
// it by hand, exactly as production's own open path would have.
func createSessionRow(t *testing.T, db content.ContentDB, sid string) {
	t.Helper()
	if err := db.Ledger().CreateSession(context.Background(), content.Session{
		ID: sid, WorkspaceID: "ws-lifecycle",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

// TestBlockClearBoundary_KeepsTheRunningCommandAndNotifies (nocx-2v80t.3.17):
// a real erase inside a command that is still running — almost always
// `clear` itself — must never hide the very block reporting it. The running
// command's own entry is named as keepEntryId, and the store's own
// exclusion (content.RecordClearBoundary) is scoped so the same entry can
// never be excluded either — this asserts the LIVE half, the notification
// an attached client uses to remove every OTHER block right away.
func TestBlockClearBoundary_KeepsTheRunningCommandAndNotifies(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))

	// An earlier command, sealed — this is what the boundary must hide.
	before := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	fence := lifecycleFence(0x51)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(before), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")}, false)
	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("the earlier command never closed: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

	// `clear` itself: still running when the erase is sighted inside it.
	attempt := startsACommand(t, e, pub, lane, h, 5, "clear")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.BlockClearBoundary(session.ID(sid))

	deadline = time.Now().Add(wantWithin)
	msg, err := awaitFrame(e.conn, deadline, isNotification("block.cleared"))
	if err != nil {
		t.Fatalf("no block.cleared reached the subscriber: %v", err)
	}
	frame, ok := decodeFrame(msg)
	if !ok {
		t.Fatalf("block.cleared frame did not decode: %s", msg)
	}
	var params struct {
		KeepEntryID *string `json:"keepEntryId"`
	}
	if err = json.Unmarshal(frame.Params, &params); err != nil {
		t.Fatalf("decode block.cleared params: %v", err)
	}
	if params.KeepEntryID == nil || *params.KeepEntryID != attempt {
		t.Fatalf("keepEntryId = %v, want the running command's own entry %q", params.KeepEntryID, attempt)
	}

	// The durable half: the earlier, sealed command is hidden from an
	// ordinary pane read, but its record still exists — never deleted
	// (nocx-zg3k3.10.3's owner decision) — and `clear`'s own still-running
	// block is not hidden by its own report of the erase.
	page, err := db.Ledger().QueryEntries(context.Background(), content.LedgerQuery{
		Scope: content.ScopePane, PaneID: "01930000-0000-7000-8000-0000000000a1", Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryEntries: %v", err)
	}
	var ids []string
	for _, e := range page.Entries {
		ids = append(ids, e.ID)
	}
	for _, id := range ids {
		if id == before {
			t.Fatalf("the sealed command %q is still shown after the clear boundary: %v", before, ids)
		}
	}
	found := false
	for _, id := range ids {
		if id == attempt {
			found = true
		}
	}
	if !found {
		t.Fatalf("the still-running command %q was hidden by its own report of the clear: %v", attempt, ids)
	}
	if _, err := db.Ledger().Entry(context.Background(), before); err != nil {
		t.Fatalf("the hidden entry is no longer readable by id — it must never be deleted: %v", err)
	}
}

// TestBlockClearBoundary_NoOpenBlockKeepsNothing: an erase sighted with no
// interval open (a plain scrollback, or between commands) names no entry to
// keep — every block the client currently shows is removed.
func TestBlockClearBoundary_NoOpenBlockKeepsNothing(t *testing.T) {
	e, _, _, _, sid, db := newLifecycleLedgerEnv(t, true)
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))

	e.ws.BlockClearBoundary(session.ID(sid))

	deadline := time.Now().Add(wantWithin)
	msg, err := awaitFrame(e.conn, deadline, isNotification("block.cleared"))
	if err != nil {
		t.Fatalf("no block.cleared reached the subscriber: %v", err)
	}
	frame, ok := decodeFrame(msg)
	if !ok {
		t.Fatalf("block.cleared frame did not decode: %s", msg)
	}
	var params struct {
		KeepEntryID *string `json:"keepEntryId"`
	}
	if err = json.Unmarshal(frame.Params, &params); err != nil {
		t.Fatalf("decode block.cleared params: %v", err)
	}
	if params.KeepEntryID != nil {
		t.Fatalf("keepEntryId = %q, want null: nothing was open", *params.KeepEntryID)
	}
}

// TestBlockCleared_OverTheWireConformsToContract validates the REAL result
// off the real socket (AGENTS.md rule 5's third check), rather than a
// payload the test built.
func TestBlockCleared_OverTheWireConformsToContract(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	createSessionRow(t, db, sid)
	e.ws.AttachBlockRows(session.ID(sid))
	startsACommand(t, e, pub, lane, h, 2, "clear")

	e.ws.BlockClearBoundary(session.ID(sid))

	deadline := time.Now().Add(wantWithin)
	msg, err := awaitFrame(e.conn, deadline, isNotification("block.cleared"))
	if err != nil {
		t.Fatalf("no block.cleared reached the subscriber: %v", err)
	}
	frame, ok := decodeFrame(msg)
	if !ok {
		t.Fatalf("block.cleared frame did not decode: %s", msg)
	}
	validateJSON(t, loadSchema(t, "block.cleared.schema.json"), frame.Params, "block.cleared params (real socket)")
}

// THE END, end marker first — the order ADR-0024 decision 7 says is real.
// The fence has not been seen yet, so the end waits (bounded) for its
// completion, and the completion resolves it: one meeting, either order.
func TestBlockIntervalEnded_WaitsForTheCompletionThatNamesIt(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	fence := lifecycleFence(0x45)
	// The end arrives while the kernel holds no fence for anyone.
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")}, false)
	if body := blockRowsBody(t, db, attempt); strings.Contains(body, "final screen") {
		t.Fatal("an unauthenticated end marker appended the closing rows")
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	kept := streamRows(t, db, attempt)
	found := false
	for _, r := range kept {
		if r.Text == "final screen" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the parked end never resolved; rows = %+v", kept)
	}
}

// A TEARDOWN with no end marker seals what arrived. The interval's own end
// never will come; the rows already stored are the block's truth.
func TestDetachBlockRows_SealsAnUnendedBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("partial")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.DetachBlockRows(session.ID(sid))
	kept := streamRows(t, db, attempt)
	if len(kept) != 1 || kept[0].Text != "partial" {
		t.Fatalf("the sealed block lost what arrived: %+v", kept)
	}
	// And a detached session's rows are nobody's: the stream is inert.
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 5, 0, []emulator.Row{aStreamRow("late")}); confirm {
		t.Fatal("a detached session's stream still answered")
	}
}

// THE INERT TODAY. Without a rows source the attempt-fact hook opens
// nothing: a block that opens and can never close is the defect, so until
// the halves merge and the wiring lands, no start may open one.
func TestBlockRowsStream_IsInertWithoutASource(t *testing.T) {
	e, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if body := blockRowsBody(t, db, attempt); body != "" {
		t.Fatalf("a block opened with no rows source to feed it:\n%s", body)
	}
	if _, confirm := e.ws.BlockRowsArrived(lifecycleSessionOf(t, e), 0, 0, []emulator.Row{aStreamRow("x")}); confirm {
		t.Fatal("rows were confirmed with no source attached")
	}
}

// Rows outside an authenticated attempt are prompt scroll, not the next
// command's output. They keep the original drop-and-confirm outcome.
func TestBlockRowsArrived_NoAttemptRowsAreConfirmedAndDropped(t *testing.T) {
	e, _, _, _, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 7, 0, []emulator.Row{aStreamRow("prompt")})
	if !confirm || written != 8 {
		t.Fatalf("no-attempt rows ack = (%d, %v), want (8, true)", written, confirm)
	}
	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if len(e.ws.blockStream.pending[session.ID(sid)]) != 0 {
		t.Fatal("no-attempt rows were retained for a future command")
	}
}

// lifecycleSessionOf is the session id the env's lane is registered to.
func lifecycleSessionOf(t *testing.T, e *lifecycleTestEnv) session.ID {
	t.Helper()
	e.ws.lifecycleMu.Lock()
	defer e.ws.lifecycleMu.Unlock()
	for _, id := range e.ws.lifecycleLanes {
		return id
	}
	t.Fatal("the env registered no lane")
	return ""
}

// THE READ-BACK, OVER THE WIRE (AGENTS.md rule 5's third check): rows the
// store holds come back through the EXISTING read path — ledger.get's
// metadata and ledger.artifact's body — off a real socket, and the body
// parses line by line into the vocabulary the contract declares. The
// command here is driven through the real submit-and-authenticated-start
// path, so what the wire answers is what the coordinator stored, not a
// payload the test assembled.
func TestBlockRowsReadBack_OverTheWireConformsToContract(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'a\\nb\\n'")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("a"), aStreamRow("b"),
	}); !confirm {
		t.Fatal("the streamed rows were not confirmed")
	}
	fence := lifecycleFence(0x46)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 2, []emulator.Row{aStreamRow("done")}, false)

	// The artifact id, off ledger.get's metadata (what a restored block's
	// client reads first).
	getResp := jsonrpcCallWithID(t, e.conn, "ledger.get", map[string]any{"id": attempt}, 51)
	getSchema := loadSchema(t, "ledger.get.schema.json")
	var getEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(getResp, &getEnvelope); err != nil {
		t.Fatalf("ledger.get response: %v", err)
	}
	if getEnvelope.Error != nil {
		t.Fatalf("ledger.get refused the block's entry: %+v", getEnvelope.Error)
	}
	var getResult map[string]any
	if err := json.Unmarshal(getEnvelope.Result, &getResult); err != nil {
		t.Fatalf("ledger.get result: %v", err)
	}
	if err := getSchema.Validate(getResult); err != nil {
		t.Fatalf("ledger.get does not conform to its contract: %v", err)
	}

	// The body, off ledger.artifact.
	artifactID := rowsArtifactID(t, db, attempt)
	artResp := jsonrpcCallWithID(t, e.conn, "ledger.artifact", map[string]any{"id": artifactID}, 52)
	artSchema := loadSchema(t, "ledger.artifact.schema.json")
	rowsSchema := loadSchema(t, "ledger.blockRows.schema.json")
	var artEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(artResp, &artEnvelope); err != nil {
		t.Fatalf("ledger.artifact response: %v", err)
	}
	if artEnvelope.Error != nil {
		t.Fatalf("ledger.artifact refused the block's body: %+v", artEnvelope.Error)
	}
	var artResult map[string]any
	if err := json.Unmarshal(artEnvelope.Result, &artResult); err != nil {
		t.Fatalf("ledger.artifact result: %v", err)
	}
	if err := artSchema.Validate(artResult); err != nil {
		t.Fatalf("ledger.artifact does not conform to its contract: %v", err)
	}
	body, _ := artResult["body"].(string)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the body holds %d lines, want the two streamed rows and the closing screen", len(lines))
	}
	for i, line := range lines {
		var doc any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if err := rowsSchema.Validate(doc); err != nil {
			t.Fatalf("stored line %d does not conform to ledger.blockRows.schema.json: %v", i, err)
		}
	}
}

func rowsArtifactID(t *testing.T, db content.ContentDB, entryID string) string {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if row == nil {
		t.Fatalf("no entry %s", entryID)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				return ex.Artifacts[i].ID
			}
		}
	}
	t.Fatalf("entry %s carries no block rows artifact", entryID)
	return ""
}

// blockRowsSummaryOf reads a sealed block's summary the way a reader does:
// the artifact's payload, which the close rewrote to it.
func blockRowsSummaryOf(t *testing.T, db content.ContentDB, entryID string) (lost, dropped uint64) {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	for _, ex := range row.Executions {
		for _, artifact := range ex.Artifacts {
			if artifact.MediaType != content.MediaBlockRows {
				continue
			}
			stored, err := db.Ledger().Artifact(context.Background(), artifact.ID)
			if err != nil {
				t.Fatalf("Artifact(%s): %v", artifact.ID, err)
			}
			var summary struct {
				LostRows    uint64 `json:"lostRows"`
				DroppedRows uint64 `json:"droppedRows"`
			}
			if err := json.Unmarshal([]byte(stored.Payload), &summary); err != nil {
				t.Fatalf("decode the block's summary %q: %v", stored.Payload, err)
			}
			return summary.LostRows, summary.DroppedRows
		}
	}
	t.Fatalf("entry %s has no block rows artifact", entryID)
	return 0, 0
}

// A loss-only delivery (nocx-2v80t.3.26, finding 4) — no rows, a loss at the
// gap it names — is how the helper states a hole at the very end of an
// interval. It used to be discarded here before it reached the store, so the
// block's summary said nothing was lost. It reaches the summary, the ack
// moves to the end of the gap, and the closing screen lands after the hole.
// Paired with the ordinary tail, which records no loss.
func TestBlockRowsArrived_ALossOnlyDeliveryReachesTheSummary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hole     bool
		wantLost uint64
	}{
		{name: "a hole at the tail", hole: true, wantLost: 1},
		{name: "the ordinary tail", hole: false, wantLost: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
			e.ws.AttachBlockRows(session.ID(sid))
			attempt := startsACommand(t, e, pub, lane, h, 2, "cat big")

			if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
				aStreamRow("one"), aStreamRow("two"),
			}); !confirm {
				t.Fatal("the ordinary rows were not confirmed")
			}
			endRow := uint64(2)
			if tc.hole {
				written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 3, 1, nil)
				if !confirm || written != 3 {
					t.Fatalf("the loss-only delivery was answered (%d, %v), want confirmed through 3", written, confirm)
				}
				endRow = 3
			}
			fence := lifecycleFence(0x61)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
			e.ws.BlockIntervalEnded(session.ID(sid), fence, endRow, []emulator.Row{aStreamRow("$ ")}, false)

			assertBlockSealed(t, db, attempt)
			lost, dropped := blockRowsSummaryOf(t, db, attempt)
			if lost != tc.wantLost || dropped != 0 {
				t.Fatalf("summary lost=%d dropped=%d, want lost=%d dropped=0", lost, dropped, tc.wantLost)
			}
			rows := streamRows(t, db, attempt)
			if len(rows) != 3 || rows[2].Text != "$ " || rows[2].From != endRow {
				t.Fatalf("stored rows = %+v, want one, two and the closing screen at %d", rows, endRow)
			}
		})
	}
}

// The same loss-only tail, arriving while an earlier append is still being
// flushed, is held with the rows and split to the interval it ends — a hole
// at the end row belongs to the interval that END closes, never to the next.
func TestBlockRowsPendingLossOnlyTailStaysWithItsInterval(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	first := startsACommand(t, e, pub, lane, h, 2, "printf first")
	firstFence := lifecycleFence(0x71)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, firstFence)))
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.flushing[session.ID(sid)] = true
	e.ws.blockStream.mu.Unlock()
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("first-tail")}); confirm {
		t.Fatal("rows queued behind a flush were acknowledged early")
	}
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 1, nil); confirm {
		t.Fatal("a loss queued behind a flush was acknowledged early")
	}
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.flushing[session.ID(sid)] = false
	e.ws.blockStream.mu.Unlock()

	e.ws.BlockIntervalEnded(session.ID(sid), firstFence, 2, []emulator.Row{aStreamRow("first-final")}, false)

	assertBlockSealed(t, db, first)
	if lost, _ := blockRowsSummaryOf(t, db, first); lost != 1 {
		t.Fatalf("the first block's summary names %d lost, want the tail's 1", lost)
	}
	rows := streamRows(t, db, first)
	if len(rows) != 2 || rows[1].Text != "first-final" || rows[1].From != 2 {
		t.Fatalf("first block rows = %+v, want first-tail and the closing screen at 2", rows)
	}
}

// A clear whose durable half failed is not announced as done (nocx-2v80t.3.26,
// finding 7): the store never recorded the cursor, so a reconnect would show
// every block the live removal hid, and the live half and the read-time half
// would disagree about what is visible — the one thing block.cleared's
// contract says they never do. Paired with TestBlockClearBoundary_Keeps…,
// where the write succeeds and the notification follows.
func TestBlockClearBoundary_AClearThatDidNotPersistIsNotAnnounced(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	createSessionRow(t, db, sid)
	e.ws.blockRowsStore = &clearFailureBlockStore{closeFailureBlockStore{ledger: db.Ledger()}}
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "clear")

	e.ws.BlockClearBoundary(session.ID(sid))

	// An observable event AFTER the clear on the same ordered socket: once
	// block.grew has arrived, anything the clear sent is already in the inbox.
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("after")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isNotification("block.grew")); err != nil {
		t.Fatalf("no block.grew reached the subscriber: %v", err)
	}
	if msg, ok := inboxOf(e.conn).take(isNotification("block.cleared")); ok {
		t.Fatalf("a clear the store never recorded was announced as done: %s", msg)
	}
	if rows := streamRows(t, db, attempt); len(rows) != 1 {
		t.Fatalf("the running block holds %d rows, want 1", len(rows))
	}
}

type clearFailureBlockStore struct{ closeFailureBlockStore }

func (s *clearFailureBlockStore) RecordClearBoundary(context.Context, content.RecordClearBoundary) (content.ClearBoundaryRecorded, error) {
	return content.ClearBoundaryRecorded{}, fmt.Errorf("injected clear boundary failure")
}

// awaitBlockClosed answers the next block.closed the subscriber hears, its
// params validated against the contract off the real socket.
func awaitBlockClosed(t *testing.T, e *lifecycleTestEnv) blockClosedParams {
	t.Helper()
	msg, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isNotification("block.closed"))
	if err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}
	frame, ok := decodeFrame(msg)
	if !ok {
		t.Fatalf("block.closed frame did not decode: %s", msg)
	}
	validateJSON(t, loadSchema(t, "block.closed.schema.json"), frame.Params, "block.closed params (real socket)")
	var got blockClosedParams
	if err := json.Unmarshal(frame.Params, &got); err != nil {
		t.Fatalf("block.closed params: %v", err)
	}
	return got
}

// EVERY RESOLVED END IS SAID (nocx-2v80t.3.27). The renderer finishes a
// block on block.closed and on nothing else, so an end the coordinator
// resolved for a block the store did not keep has to be said too — the
// renderer cannot know that no notification is coming. The kept case is the
// pair: the same end, the same notification, and `kept` says which it was.
func TestBlockIntervalEnded_EveryResolvedEndSaysBlockClosed(t *testing.T) {
	t.Run("kept: the store holds the block's rows", func(t *testing.T) {
		e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, true)
		e.ws.AttachBlockRows(session.ID(sid))
		attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
		fence := lifecycleFence(0x51)
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
		e.ws.BlockIntervalEnded(session.ID(sid), fence, 0, []emulator.Row{aStreamRow("done")}, false)

		got := awaitBlockClosed(t, e)
		if got.EntryID != attempt || !got.Kept {
			t.Fatalf("block.closed = %+v, want entry %q kept", got, attempt)
		}
	})

	t.Run("not kept: output retention refused the block", func(t *testing.T) {
		policy := content.NewPolicy()
		policy.SetOutputEnabled(false)
		db := newLedgerStoreWithPolicy(t, policy)
		e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
		e.ws.AttachBlockRows(session.ID(sid))
		attempt := startsACommand(t, e, pub, lane, h, 2, "make secret")
		fence := lifecycleFence(0x52)
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
		e.ws.BlockIntervalEnded(session.ID(sid), fence, 0, []emulator.Row{aStreamRow("classified")}, false)

		got := awaitBlockClosed(t, e)
		if got.EntryID != attempt || got.Kept {
			t.Fatalf("block.closed = %+v, want entry %q not kept", got, attempt)
		}
	})

	t.Run("no block at all: a shell-originated attempt with no store", func(t *testing.T) {
		e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, false)
		e.ws.AttachBlockRows(session.ID(sid))
		shellID := lifecycle.AttemptID("att-shell-closed")
		fence := lifecycleFence(0x53)
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&shellID, "ls")))
		// The end marker first this time: it parks until the completion
		// names its fence, and the meeting is what closes it.
		e.ws.BlockIntervalEnded(session.ID(sid), fence, 0, []emulator.Row{aStreamRow("a b c")}, false)
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(shellID, 0, fence)))

		got := awaitBlockClosed(t, e)
		if got.EntryID != string(shellID) || got.Kept {
			t.Fatalf("block.closed = %+v, want entry %q not kept", got, shellID)
		}
	})
}

// THE SESSION'S END settles what is still open (ADR-0074 decision 3): a
// completion whose end marker never came, and a block whose command never
// ended, are each said closed at the detach — the renderer waits for
// block.closed and nothing after this can send it (nocx-2v80t.3.27).
func TestDetachBlockRows_SaysEveryUnendedBlockClosed(t *testing.T) {
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	fence := lifecycleFence(0x54)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	// No end marker: the helper went away first.
	e.ws.DetachBlockRows(session.ID(sid))

	got := awaitBlockClosed(t, e)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want entry %q kept", got, attempt)
	}
}

// The struct half of the contract: both answers of `kept` marshal to what
// the schema accepts, and `kept` is sent even when false — a false that the
// encoder dropped would read as a missing required field.
func TestBlockClosed_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "block.closed.schema.json")
	for _, p := range []blockClosedParams{{EntryID: "e1", Kept: true}, {EntryID: "e2", Kept: false}} {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal %+v: %v", p, err)
		}
		validateJSON(t, schema, raw, "block.closed params (DTO)")
	}
}

// THE PATHS THAT END A BLOCK WITHOUT A COMPLETION each say block.closed too
// (nocx-2v80t.3.30): the renderer closes a block on that notification alone,
// so an end the stream settles in silence is a block running forever on
// screen.
func TestBlockClosed_EndsWithoutACompletionAreSaid(t *testing.T) {
	t.Run("an attempt gone unknown whose block never opened", func(t *testing.T) {
		e, _, _, _, sid, _ := newLifecycleLedgerEnv(t, true)
		e.ws.AttachBlockRows(session.ID(sid))
		e.ws.blockStream.abandonAttempt(e.ws, session.ID(sid), "att-never-opened")

		got := awaitBlockClosed(t, e)
		if got.EntryID != "att-never-opened" || got.Kept {
			t.Fatalf("block.closed = %+v, want entry att-never-opened not kept", got)
		}
	})

	t.Run("an environment entry with no store: the block is tracked, unkept", func(t *testing.T) {
		e, pub, lane, h, sid, _ := newLifecycleLedgerEnv(t, false)
		e.ws.AttachBlockRows(session.ID(sid))
		sshID := lifecycle.AttemptID("att-ssh-nostore")
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&sshID, "ssh host")))

		var noFence [32]byte
		e.ws.BlockIntervalEnded(session.ID(sid), noFence, 0, nil, false)

		got := awaitBlockClosed(t, e)
		if got.EntryID != string(sshID) || got.Kept {
			t.Fatalf("block.closed = %+v, want entry %q not kept", got, sshID)
		}
	})
}

package transport

// A replacement reader replays only its LATEST output mark, and once the next
// command has started that is the NEXT command's mark (internal/helper/session
// rows.go's resend). It arrives before the recovered interval's retained rows
// and its end marker, so a coordinator that takes it as the recovered block's
// start hides that block's whole output behind a mark that belongs to another
// interval (nocx-4y82c.1.11 review 6, P1).
//
// And a mark that reaches its block after the block was sealed and said closed
// is written to a payload the renderer already read: nothing makes it read
// again, so the card keeps the echo it froze (P2). Recording the start has to
// say the close again.

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func TestAReplayedSuccessorMarkIsNotTheRecoveredIntervalsStart(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf a")
	if up, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("$ printf a"), aStreamRow("a"), aStreamRow("a"),
	}, ""); !confirm || up != 3 {
		t.Fatalf("rows ack = (%d, %v), want 3", up, confirm)
	}

	// The reconnect: the block is adopted from durable storage, and the
	// helper's replacement reader replays only its latest mark — the next
	// command's, past this interval's end.
	bs := e.ws.blockStream
	bs.mu.Lock()
	bs.current[session.ID(sid)].recoveredOutputInterval = true
	bs.mu.Unlock()
	e.ws.BlockOutputStartRow(session.ID(sid), 5)

	// This interval's own end: row 3. The mark at 5 is not inside it.
	fence := lifecycleFence(0x91)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 3, nil, false)

	if got := recordedOutputStart(t, db, attempt); got != nil {
		t.Fatalf("recorded output start = %d, want none: the mark belongs to the interval that follows", *got)
	}
	kept := streamRows(t, db, attempt)
	if len(kept) != 3 {
		t.Fatalf("the sealed block holds %d rows, want its own 3: %+v", len(kept), kept)
	}
	art := blockArtifact(t, db, attempt)
	body, err := content.BlockRowsTextAfter(art.Chunks, content.BlockRowsOutputStart(art.Payload))
	if err != nil {
		t.Fatalf("BlockRowsTextAfter: %v", err)
	}
	if body != "$ printf a\na\na" {
		t.Fatalf("body read = %q, want the recovered output whole", body)
	}

	// The mark is not lost: it is the next block's, and the block that opens
	// next takes it as its own start.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	next := startsACommand(t, e, pub, lane, h, 5, "printf b")
	if got := recordedOutputStart(t, db, next); got == nil || *got != 5 {
		t.Fatalf("the next block's recorded start = %v, want the replayed mark, 5", got)
	}
}

func TestALateOutputStartOnASealedBlockSaysClosedAgain(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf a")
	if up, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("$ printf a"), aStreamRow("a"),
	}, ""); !confirm || up != 2 {
		t.Fatalf("rows ack = (%d, %v), want 2", up, confirm)
	}
	fence := lifecycleFence(0x93)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 2, nil, false)
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}

	// The block's own mark arrives after its close — a mark queued behind the
	// end marker, or the helper's replay after the seal. It is inside the
	// interval, so it is this block's start.
	e.ws.BlockOutputStartRow(session.ID(sid), 1)
	if got := recordedOutputStart(t, db, attempt); got == nil || *got != 1 {
		t.Fatalf("recorded output start on the sealed block = %v, want 1", got)
	}
	// And the renderer is told to read again: it froze the unfiltered rows on
	// the first close and nothing else would ever make it re-read.
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isNotification("block.closed")); err != nil {
		t.Fatalf("the late start did not say closed again: %v", err)
	}
	art := blockArtifact(t, db, attempt)
	body, err := content.BlockRowsTextAfter(art.Chunks, content.BlockRowsOutputStart(art.Payload))
	if err != nil {
		t.Fatalf("BlockRowsTextAfter: %v", err)
	}
	if body != "a" {
		t.Fatalf("body read = %q, want the output only", body)
	}
}

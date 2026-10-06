package transport

// The three ceilings one command's output meets AT ONCE, measured together
// at the three shipped geometries with scrollback (nocx-zg3k3.5.7). The
// retired stage nocx-2v80t.2 was tested only on a 20x3 screen with one
// four-cell line, and real terminals broke it — so this test drives a real
// session: a real /bin/sh on a real PTY inside the real helper session
// service, its real emulator, the real row stream over the real wire, and
// the real block-rows store behind the real WSServer.
//
// The chain, and what each ceiling owns:
//
//   - the helper's frame bound: the row bridge splits every batch at
//     rowsPerFrame=32 rows (internal/helper/session/rows.go) before one
//     rows frame is encoded, so NO rows frame a coordinator receives may
//     carry more. Counted at the attachment's own observer — one
//     OutputRows delivery is exactly one decoded frame.
//   - the content artifact ceiling: content.MaxArtifactBytes (1 MiB), the
//     input-validation ceiling no setting may walk into. Measured from the
//     sealed artifact's own ByteLen.
//   - the per-command output cap: history.outputCapKB (default 256 KiB,
//     content.Policy), the user preference that decides how much of one
//     command's output is worth keeping. It is the bound that must STOP
//     storage — evicting whole chunks and counting the rows it took in
//     DroppedRows — never a frame refusal and never an artifact error.
//
// The paired case runs at every size too: output under the cap stores
// every row with nothing lost, and DroppedRows stays zero.
//
// What is deliberately NOT claimed here: the helper-to-publisher lifecycle
// leg. The command prints its own render fence (the product's OSC 1337
// spelling), and the authenticated half of the rendezvous arrives the way
// the coordinator's kernel delivers it — OpLifecycleComplete to the helper
// and the same completion through the publisher's real ingest — which is
// the route internal/helper/client's own over-the-wire acceptance
// (TestARowsStreamReachesTheCoordinatorInOrderThenTheEnd) already proves
// end to end. The rows, the fence and both seals are the real session's.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// rowsPerFrameBound is the helper's own split bound, spelled here because
// internal/helper/session declares it unexported (rowsPerFrame=32): a frame
// carrying more is the defect the retired stage shipped.
const rowsPerFrameBound = 32

// floodRows prints past the 256 KiB cap at every shipped geometry: even at
// 80 columns a styled full-width row costs well over 100 stored bytes, so
// 4000 rows clear the cap with room to spare while staying far under the
// artifact ceiling once the cap has taken its share.
const floodRows = 4000

// underCapRows prints comfortably under the cap at every shipped geometry,
// so the paired block stores every row it streamed.
const underCapRows = 50

// burstRows prints short rows — eleven bytes of row each — so any read the
// pty hands the runtime that is a few hundred bytes already carries more
// than the helper's 32-row split bound. Full-width rows alone never did:
// observed maxima were 29 and 19 rows per frame at 120x40 and 200x50, so
// the frame-bound assertion had nothing over the bound to bite on at two
// of the three sizes (nocx-zg3k3.5.9). The burst forces a batch past the
// bound at every size, which is what makes the bound load-bearing.
const burstRows = 1500

// boundsHelperConn is the pty-less exec lane client.Dial asks for, with the
// REAL helper host and the REAL session service — real local spawner, real
// shell, real PTY — serving it on the other end of the pipes.
type boundsHelperConn struct {
	stdin  io.WriteCloser
	stdout io.Reader

	exited chan struct{}
	code   int
	done   chan struct{}
	once   sync.Once
}

// serveBoundsHelper runs the shipped helper stack over one lane.
func serveBoundsHelper(t *testing.T, in io.Reader, out io.Writer) {
	t.Helper()
	logger := logtest.Slog(t)
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID("testhash"),
		Spawner:    helpersession.NewLocalSpawner(logger, helpersession.Shell{Path: "/bin/sh"}, ""),
		Inspector:  helpersession.NewInspector(),
		Log:        logger,
		Limits:     helpersession.DefaultLimits(),
	})
	t.Cleanup(svc.Close)
	h := host.New(in, out, "testhash", "instance-1", logger)
	h.Register(svc)
	release := svc.Bind(h)
	defer release()
	if err := h.Serve(context.Background()); err != nil {
		t.Logf("helper host served: %v", err)
	}
}

func newBoundsHelperConn(t *testing.T) *boundsHelperConn {
	t.Helper()
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	c := &boundsHelperConn{
		stdin:  toPeerW,
		stdout: fromPeerR,
		exited: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go func() {
		serveBoundsHelper(t, toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		c.code = 0
		close(c.exited)
	}()
	t.Cleanup(func() { _ = c.stdin.Close() })
	return c
}

func (c *boundsHelperConn) Stdin() io.WriteCloser { return c.stdin }
func (c *boundsHelperConn) Stdout() io.Reader     { return c.stdout }
func (c *boundsHelperConn) Stderr() io.Reader     { return strings.NewReader("") }
func (c *boundsHelperConn) Start(string) error    { return nil }
func (c *boundsHelperConn) Wait() (int, error) {
	<-c.exited
	return c.code, nil
}
func (c *boundsHelperConn) Done() <-chan struct{} { return c.done }
func (c *boundsHelperConn) LostErr() error {
	select {
	case <-c.done:
		return io.ErrClosedPipe
	default:
		return nil
	}
}

func (c *boundsHelperConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.stdin.Close()
}

// boundsRecorder is the coordinator-side observer spelled at the seam the
// app's own binding uses: it counts every decoded rows frame (the frame
// bound's measurement), reproduces the frame's own wire bytes (the
// bytes-per-row measurement), records intervals in order, and hands every
// delivery to the REAL transport sink, which appends to the REAL store. The
// observers run on the connection's read loop, so they only record, forward
// and return.
type boundsRecorder struct {
	mu sync.Mutex

	// Per-frame, per-size measurements.
	frames          int
	maxRowsInFrame  int
	totalRows       uint64
	totalPayloadBts int64

	// Events in arrival order; the intervals are sliced from these.
	batches []client.OutputRows
	ends    []client.IntervalEnd

	// The two statements that must stay silent for the cap to be the only
	// thing that stopped storage.
	sawIncomplete bool
	sawLoss       uint64

	sid session.ID
	ws  *WSServer

	confirmations chan struct{}
	confirmed     uint64
	confirmClosed bool
	confirmDone   chan struct{}
	confirmErrors chan error
	stopConfirm   sync.Once
}

func (r *boundsRecorder) onRows(o client.OutputRows) {
	r.mu.Lock()
	r.frames++
	if len(o.Rows) > r.maxRowsInFrame {
		r.maxRowsInFrame = len(o.Rows)
	}
	r.totalRows += uint64(len(o.Rows)) //nolint:gosec // len is never negative
	// The frame's own bytes, reproduced with the encoders the helper's pump
	// itself runs (EncodeRows + the rows document), so the per-row figure is
	// what the wire really carried for this frame's rows.
	if encoded, err := sessionruntime.EncodeRows(o.Rows); err == nil {
		if payload, err := json.Marshal(proto.OutputRowsDoc{
			FromRow: o.FromRow, LostRows: o.LostRows, Incomplete: o.Incomplete, Rows: encoded,
		}); err == nil {
			r.totalPayloadBts += int64(len(payload)) //nolint:gosec // len is never negative
		}
	}
	if o.Incomplete {
		r.sawIncomplete = true
	}
	if o.LostRows > 0 {
		r.sawLoss += o.LostRows
	}
	r.batches = append(r.batches, o)
	r.mu.Unlock()
	// Forward to the real sink just as the app binding does. Incomplete
	// markers terminate the block without acknowledging rows; ordinary
	// batches are confirmed only after the store accepts them.
	if o.Incomplete {
		r.ws.BlockOutputIncomplete(r.sid, o.FromRow)
		return
	}
	// Mirror the app's asynchronous confirmation path too: the ack releases
	// the helper's retained resend copy only after the store accepts these
	// rows, and cannot run synchronously on the client's read loop.
	if upTo, confirm := r.ws.BlockRowsArrived(r.sid, o.FromRow, o.LostRows, o.Rows, ""); confirm {
		r.offerConfirmation(upTo)
	}
}

func (r *boundsRecorder) offerConfirmation(upTo uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmClosed {
		return
	}
	r.confirmed = max(r.confirmed, upTo)
	select {
	case r.confirmations <- struct{}{}:
	default:
	}
}

func (r *boundsRecorder) startConfirmations(attached *client.AttachedSession) {
	// Mirror the production latest-wins watermark, including deferred store
	// flushes. Intermediate marks add no durability and can amplify resends.
	r.confirmations = make(chan struct{}, 1)
	r.confirmDone = make(chan struct{})
	r.confirmErrors = make(chan error, 1)
	go func() {
		defer close(r.confirmDone)
		for range r.confirmations {
			r.mu.Lock()
			upTo := r.confirmed
			r.mu.Unlock()
			if err := attached.ConfirmWritten(context.Background(), upTo); err != nil {
				select {
				case r.confirmErrors <- err:
				default:
				}
			}
		}
	}()
}

func (r *boundsRecorder) stopConfirmations() {
	r.stopConfirm.Do(func() {
		r.mu.Lock()
		r.confirmClosed = true
		close(r.confirmations)
		r.mu.Unlock()
	})
	<-r.confirmDone
}

func (r *boundsRecorder) onEnd(e client.IntervalEnd) {
	r.mu.Lock()
	r.ends = append(r.ends, e)
	r.mu.Unlock()
	r.ws.BlockIntervalEnded(r.sid, [32]byte(e.Nonce), e.EndRow, e.Closing, e.NoFence)
}

// intervalAt slices the events up to the (zero-based) end marker: every
// streamed row at an index inside THAT interval — at or after the previous
// boundary, below this one — and the closing screen the boundary carried.
func (r *boundsRecorder) intervalAt(k int) (rows uint64, closing int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.ends[k]
	var floor uint64
	if k > 0 {
		floor = r.ends[k-1].EndRow
	}
	for _, b := range r.batches {
		for i := range b.Rows {
			if idx := b.FromRow + uint64(i); floor <= idx && idx < end.EndRow { //nolint:gosec // a slice index, never negative
				rows++
			}
		}
	}
	return rows, len(end.Closing)
}

// storedLines counts the rows the artifact actually holds, the same
// derivation the store's own summary runs over its chunks.
func storedLines(a *content.Artifact) uint64 {
	var n uint64
	for _, chunk := range a.Chunks {
		n += uint64(countByte(chunk, '\n')) //nolint:gosec // a count, not a byte value
	}
	return n
}

func countByte(b []byte, c byte) int {
	n := 0
	for _, x := range b {
		if x == c {
			n++
		}
	}
	return n
}

// rowsArtifactFor reads the rows artifact of one attempt, chunks included.
func rowsArtifactFor(t *testing.T, db content.ContentDB, entryID string) *content.Artifact {
	t.Helper()
	art, err := db.Ledger().Artifact(context.Background(), rowsArtifactID(t, db, entryID))
	if err != nil {
		t.Fatalf("Artifact(%s): %v", entryID, err)
	}
	if art == nil {
		t.Fatalf("no artifact row for entry %s", entryID)
	}
	return art
}

// waitForEnds waits for the n-th (one-based) end marker — the observable
// the rendezvous produces; the deadline only turns a silent stream into a
// failure.
func waitForEnds(t *testing.T, r *boundsRecorder, n int) client.IntervalEnd {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.mu.Lock()
		got := len(r.ends)
		var end client.IntervalEnd
		if got >= n {
			end = r.ends[n-1]
		}
		r.mu.Unlock()
		if got >= n {
			return end
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the stream never delivered end marker %d (has %d, %d frames)", n, got, r.frames)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForSealed waits until the block's artifact is sealed — the store's
// own statement that the close committed.
func waitForSealed(t *testing.T, db content.ContentDB, entryID string) *content.Artifact {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		art := rowsArtifactFor(t, db, entryID)
		if art.State == content.ArtifactSealed {
			return art
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the block for %s never sealed (state %q)", entryID, art.State)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fenceNonce mints the n-th distinct fence nonce.
func fenceNonce(n int) (lifecycle.FenceNonce, string) {
	var f lifecycle.FenceNonce
	for i := range f {
		f[i] = byte(0x40 + n) //nolint:gosec // a marker byte, not a byte count
	}
	return f, hex.EncodeToString(f[:])
}

// burstCommand prints n short rows and closes with the interval's own
// render fence. The rows are deliberately narrow: they never wrap, and
// their small size is what packs more than 32 departures into any ordinary
// read the pty delivers.
func burstCommand(n int, nonceHex string) string {
	var b strings.Builder
	b.WriteString("stty -echo 2>/dev/null; i=0; while [ $i -lt ")
	fmt.Fprintf(&b, "%d", n)
	b.WriteString(" ]; do printf '%09d\\n' \"$i\"; i=$((i+1)); done; printf '\\033]1337;NOCX_FENCE;")
	b.WriteString(nonceHex)
	b.WriteString("\\007'")
	return b.String()
}

// styledFloodCommand builds a shell loop that prints n styled, full-width
// rows (the case the retired stage never ran: real geometry, real styling,
// scrollback departures) and closes with the interval's own render fence.
func styledFloodCommand(n, cols int, nonceHex string) string {
	width := cols - 1 // one short of wrap: the row spans the width and never folds
	var b strings.Builder
	b.WriteString("stty -echo 2>/dev/null; i=0; while [ $i -lt ")
	fmt.Fprintf(&b, "%d", n)
	b.WriteString(" ]; do printf '\\033[38;2;90;64;200m%-")
	fmt.Fprintf(&b, "%d", width)
	b.WriteString("s\\033[0m\\n' \"r$i\"; i=$((i+1)); done; printf '\\033]1337;NOCX_FENCE;")
	b.WriteString(nonceHex)
	b.WriteString("\\007'")
	return b.String()
}

// typeCommand types one line through the one-shot write path the
// coordinator really uses: read the screen, mint a target for its first
// row, spend it on the text. A shell that redraws its prompt between the
// read and the commit answers stale_target, and a fresh read is the way
// through — the same answer the coordinator's own caller gives.
func typeCommand(t *testing.T, c *client.Client, id proto.HostSessionID, text string) {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		snap, err := c.Snapshot(context.Background(), client.HostSessionID{
			Generation: string(id.Generation), Session: id.Session,
		})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		target, err := c.Target(context.Background(), proto.TargetParams{
			Session: id, SnapshotID: snap.SnapshotID, Kind: "region", First: 0, Last: 0,
		})
		if err != nil {
			t.Fatalf("target: %v", err)
		}
		result, err := c.Intent(context.Background(), proto.IntentParams{
			Session: id, Token: target.Token, AccessEpoch: snap.AccessEpoch,
			CommitBy: int64(monoclock.Now()) + int64(10*time.Second),
			Kind:     "text", Payload: []byte(text),
		})
		if err != nil {
			t.Fatalf("intent: %v", err)
		}
		if result.State == "executed" {
			return
		}
	}
	t.Fatal("the command never reached the shell: the write path refused every attempt")
}

func TestTheThreeOutputBoundsHoldTogetherAtRealGeometry(t *testing.T) {
	for _, g := range []struct {
		cols, rows int
	}{
		{80, 24}, {120, 40}, {200, 50},
	} {
		t.Run(fmt.Sprintf("%dx%d", g.cols, g.rows), func(t *testing.T) {
			e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)

			conn := newBoundsHelperConn(t)
			c, dialErr := client.Dial(context.Background(), client.Config{
				Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
			})
			if dialErr != nil {
				t.Fatalf("dial the helper: %v", dialErr)
			}
			var rec *boundsRecorder
			t.Cleanup(func() {
				_ = c.Close()
				if rec != nil && rec.confirmDone != nil {
					rec.stopConfirmations()
				}
			})

			// The real pane: a real shell on a real PTY at this geometry,
			// with the helper's default retained window and row budget. The
			// recorder confirms each stored batch asynchronously, like the app,
			// so the resend copy does not accumulate while the flood runs.
			spawnIn := proto.SpawnParams{
				Cwd: "/", Cols: uint16(g.cols), Rows: uint16(g.rows), //nolint:gosec // the shipped geometries
				WindowBytes:    8 << 20,
				IdempotencyKey: fmt.Sprintf("bounds-%dx%d", g.cols, g.rows),
			}
			var spawnRaw json.RawMessage
			if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn, spawnIn, &spawnRaw); err != nil {
				t.Fatalf("spawn: %v", err)
			}
			var spawned proto.SpawnResult
			if err := json.Unmarshal(spawnRaw, &spawned); err != nil {
				t.Fatalf("decode spawn: %v", err)
			}

			rec = &boundsRecorder{sid: session.ID(sid), ws: e.ws}
			attached, err := c.Attach(context.Background(), proto.AttachParams{
				Session: spawned.Entry.Session, Subscriber: "0123456789abcdef0123456789abcdef",
			})
			if err != nil {
				t.Fatalf("attach: %v", err)
			}
			rec.startConfirmations(attached)
			attached.OnOutputRows(rec.onRows)
			attached.OnIntervalEnd(rec.onEnd)
			// The real helper's byte carrier has a 64 KiB credit window.
			// Read it concurrently, like the app: Read acknowledges consumed
			// bytes, so the producer can reach the interval's physical fence.
			// The rows plane still reaches the real block store unchanged.
			byteDrainDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, attached)
				close(byteDrainDone)
			}()
			t.Cleanup(func() {
				_ = c.Close()
				<-byteDrainDone
			})
			// The block stream is attached for the session, exactly as the
			// app's binding does; without a source the stream is inert and
			// no block would ever open.
			e.ws.AttachBlockRowsWithConfirmation(session.ID(sid), rec.offerConfirmation)

			// runCommand runs one command through one block: the real
			// submit over the ws conn, a command that prints its own
			// fence, the authenticated half through both real routes (the
			// publisher's ingest on the lane, and the helper's own
			// completion op), the end marker — the observable the
			// rendezvous produces — and the shell's prompt_ready, which
			// is what admits the NEXT submit (the kernel's own interval
			// rhythm: complete, prompt_ready, submit).
			runCommand := func(seqSubmit uint64, endIdx, nonceIdx int, command string) (attempt string, end client.IntervalEnd) {
				t.Helper()
				nonce, nonceHex := fenceNonce(nonceIdx)
				attempt = startsACommand(t, e, pub, lane, h, seqSubmit, command)
				typeCommand(t, c, spawned.Entry.Session, command+"\r")
				if err := pub.Ingest(context.Background(), "T", lifecycleEnv(lane, h, seqSubmit+1,
					lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, nonce))); err != nil {
					t.Fatalf("ingest the completion: %v", err)
				}
				if err := c.Call(context.Background(), proto.ServiceSession, proto.OpLifecycleComplete, proto.LifecycleCompleteParams{
					Session:     spawned.Entry.Session,
					Incarnation: proto.Incarnation{Session: spawned.Entry.Session.Session, Generation: 1},
					Nonce:       nonceHex,
				}, nil); err != nil {
					t.Fatalf("lifecycle-complete: %v", err)
				}
				end = waitForEnds(t, rec, endIdx)
				if err := pub.Ingest(context.Background(), "T", lifecycleEnv(lane, h, seqSubmit+2,
					lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})); err != nil {
					t.Fatalf("ingest the prompt ready: %v", err)
				}
				return attempt, end
			}

			// ── THE FLOOD: past the cap, under the ceiling ────────────────
			_, floodHex := fenceNonce(1)
			flood, _ := runCommand(2, 1, 1, styledFloodCommand(floodRows, g.cols, floodHex))
			flooded, closing := rec.intervalAt(0)
			art := waitForSealed(t, db, flood)

			if rec.sawIncomplete {
				t.Fatal("the helper's row buffer overflowed (an incomplete marker arrived): the flood measured the wrong bound")
			}
			if rec.sawLoss != 0 {
				t.Fatalf("the stream stated a loss of %d rows: the cap is not the bound that stopped storage", rec.sawLoss)
			}
			if rec.frames == 0 {
				t.Fatal("no rows frame was ever published")
			}
			// THE FRAME BOUND: no frame may carry more than the helper's
			// split allows, at any geometry.
			if rec.maxRowsInFrame > rowsPerFrameBound {
				t.Fatalf("a frame carried %d rows, over the helper's %d-row bound", rec.maxRowsInFrame, rowsPerFrameBound)
			}
			// THE CAP'S BOUND: the cap keeps head and tail together (the
			// head reservation, and the newest chunks totalling at least
			// half the cap), evicts whole chunks at a 16 KiB granularity,
			// and the protected ends alone may exceed it — so what storage
			// holds is the cap plus at most one eviction chunk of tail
			// overshoot. Far past it is the defect.
			const boundsChunkBytes = 16 << 10
			if art.ByteLen > content.DefaultOutputCapBytes+boundsChunkBytes {
				t.Fatalf("the artifact holds %d bytes, over the %d-byte cap plus one %d-byte eviction chunk", art.ByteLen, content.DefaultOutputCapBytes, boundsChunkBytes)
			}
			if art.ByteLen > content.MaxArtifactBytes {
				t.Fatalf("the artifact holds %d bytes, over the store's own %d-byte ceiling", art.ByteLen, content.MaxArtifactBytes)
			}
			lost, dropped := blockRowsSummaryOf(t, db, flood)
			if lost != 0 {
				t.Fatalf("the sealed summary states %d lost rows: a frame or bridge loss, not the cap, took them", lost)
			}
			if dropped == 0 {
				t.Fatalf("the flood stored %d bytes under a %d-byte cap with NOTHING counted dropped: the cap never fired, so this measured no bound at all", art.ByteLen, content.DefaultOutputCapBytes)
			}
			if art.Truncated == nil || *art.Truncated != content.TruncCap {
				t.Fatalf("the sealed block says truncated=%v, want the cap's own mark", art.Truncated)
			}
			// THE EXACT ACCOUNTING, from independent sides: the store held
			// or dropped every row the interval produced — nothing silently
			// lost to a frame refusal or an artifact error.
			held := storedLines(art)
			if held+dropped != flooded+uint64(closing) { //nolint:gosec // a row count
				t.Fatalf("the block holds %d rows and dropped %d, but the interval produced %d streamed + %d closing: the numbers do not close", held, dropped, flooded, closing)
			}

			// ── THE BURST: one batch past the frame bound, at this size ────
			_, burstHex := fenceNonce(2)
			burst, _ := runCommand(5, 2, 2, burstCommand(burstRows, burstHex))
			burstStreamed, _ := rec.intervalAt(1)
			burstArt := waitForSealed(t, db, burst)
			burstLost, burstDropped := blockRowsSummaryOf(t, db, burst)
			if rec.sawIncomplete {
				t.Fatal("the helper's row buffer overflowed during the burst: the frame bound measured an incomplete stream")
			}

			// THE FRAME BOUND'S PRECONDITION (nocx-zg3k3.5.9): a batch
			// larger than the split bound reached the pump at this size.
			// Frames are the pump's output, so the evidence is the split's
			// own signature: a frame carrying exactly the bound whose
			// successor continues the same batch — fromRow moves on without
			// a loss of its own. Without this, the bound assertion below
			// has proved nothing here: at 120x40 and 200x50 the observed
			// maxima were 29 and 19 rows, so removing the production split
			// stayed green.
			fullSplits := 0
			for i := 0; i+1 < len(rec.batches); i++ {
				cur, next := rec.batches[i], rec.batches[i+1]
				if len(cur.Rows) == rowsPerFrameBound &&
					next.FromRow == cur.FromRow+uint64(len(cur.Rows)) && //nolint:gosec // a row count
					next.LostRows == 0 && len(next.Rows) > 0 {
					fullSplits++
				}
			}
			if fullSplits == 0 {
				t.Fatalf("no frame carried the full %d-row split with a continuing successor, and the burst streamed %d rows: no batch over the bound reached the pump, so the frame bound proved nothing at this size", rowsPerFrameBound, burstStreamed)
			}
			if burstStreamed < burstRows-500 {
				t.Fatalf("the burst streamed %d of %d rows: the forced batch was not delivered whole", burstStreamed, burstRows)
			}
			if rec.maxRowsInFrame != rowsPerFrameBound {
				t.Fatalf("the largest frame carried %d rows, want exactly the %d-row bound: the split was never exercised at this size", rec.maxRowsInFrame, rowsPerFrameBound)
			}
			if burstLost != 0 || burstDropped != 0 {
				t.Fatalf("the burst states lost=%d dropped=%d, want zero and zero: the burst measured a loss, not a bound", burstLost, burstDropped)
			}
			if burstArt.Truncated != nil {
				t.Fatalf("the burst says truncated=%v, want none", burstArt.Truncated)
			}

			// ── THE PAIRED CASE: under the cap, nothing lost ──────────────
			_, smallHex := fenceNonce(3)
			small, _ := runCommand(8, 3, 3, styledFloodCommand(underCapRows, g.cols, smallHex))
			smallRows, smallClosing := rec.intervalAt(2)
			smallArt := waitForSealed(t, db, small)
			smallLost, smallDropped := blockRowsSummaryOf(t, db, small)
			if smallLost != 0 || smallDropped != 0 {
				t.Fatalf("under-cap output states lost=%d dropped=%d, want zero and zero: every row must store", smallLost, smallDropped)
			}
			if smallArt.Truncated != nil {
				t.Fatalf("under-cap output says truncated=%v, want none", smallArt.Truncated)
			}
			if smallArt.ByteLen > content.DefaultOutputCapBytes {
				t.Fatalf("under-cap output stored %d bytes, over the cap", smallArt.ByteLen)
			}
			if got := storedLines(smallArt); got != smallRows+uint64(smallClosing) { //nolint:gosec // a row count
				t.Fatalf("under-cap output stored %d rows, produced %d streamed + %d closing: a row went missing", got, smallRows, smallClosing)
			}

			// All frames through the last end have reached the recorder. Drain
			// their asynchronous acks before cleanup, just like the app's binding.
			rec.stopConfirmations()
			select {
			case err := <-rec.confirmErrors:
				t.Fatalf("confirm rows written to the block store: %v", err)
			default:
			}

			// THE MEASUREMENTS THE BEAD ASKS THE REPORT TO CARRY.
			wirePerRow := float64(rec.totalPayloadBts) / float64(rec.totalRows)
			t.Logf("%dx%d: frames=%d maxRowsPerFrame=%d wireBytesPerRow=%.1f storedBytes=%d storedRows=%d dropped=%d storedBytesPerRow=%.1f",
				g.cols, g.rows, rec.frames, rec.maxRowsInFrame, wirePerRow,
				art.ByteLen, held, dropped, float64(art.ByteLen)/float64(held))
		})
	}
}

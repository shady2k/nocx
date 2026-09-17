package client_test

// The one-shot write path's five ops (nocx-6q1uh.6, spec §6, §7.2) off the
// real socket: contracts/README.md's three checks, applied to snapshot,
// target, intent, intent-status and access-bump. TestTheOneShotWritePathDTOs
// ConformToTheirContracts is the first two (the Go struct marshals to
// something every schema accepts); TestTheOneShotWritePathConformsToItsCon
// tractOverTheWire is the third and the only one that can catch a field the
// helper never actually sends — it drives one real session end to end
// through all five ops, validating the RAW bytes that crossed the socket at
// every step against that op's own schema.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// TestTheOneShotWritePathDTOsConformToTheirContracts builds the ten shapes
// directly — no server, no socket — and checks each against its own schema.
// It is the cheap half of AGENTS.md rule 5: it proves the Go struct is
// well-formed, not that the wire actually sends it, which is what the
// over-the-wire test below is for.
func TestTheOneShotWritePathDTOsConformToTheirContracts(t *testing.T) {
	session := proto.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"}

	cases := []struct {
		schema string
		value  any
	}{
		{"session.snapshot.params.schema.json", proto.SnapshotParams{Session: session}},
		{"session.snapshot.schema.json", proto.SnapshotResult{
			SnapshotID: 1, Frame: proto.ScreenFrame{Lines: [][]proto.ScreenCell{}},
			Revision: 1, InputFence: 0, Completeness: proto.CompletenessComplete,
			AccessEpoch: 1, ReadBarrier: true,
		}},
		{"session.target.params.schema.json", proto.TargetParams{
			Session: session, SnapshotID: 1, Kind: "region", First: 0, Last: 0,
		}},
		{"session.target.schema.json", proto.TargetResult{
			Token: "opaque", TokenID: "0123456789abcdef0123456789abcdef", ExpiresAtMs: 1,
		}},
		{"session.intent.params.schema.json", proto.IntentParams{
			Session: session, Token: "opaque", AccessEpoch: 1, CommitBy: 1,
			Kind: "text", Payload: []byte("hi"),
		}},
		{"session.intent.schema.json", proto.IntentResult{
			State: "executed", BytesWritten: 2, FenceAfter: 1,
		}},
		{"session.intent.schema.json", proto.IntentResult{
			State: "refused", BytesWritten: 0, FenceAfter: 0,
			Refusal: &proto.IntentRefusal{Cause: "stale_target", RegionNow: "menu text", RegionTruncated: false},
		}},
		{"session.intent-status.params.schema.json", proto.IntentStatusParams{
			Session: session, TokenID: "0123456789abcdef0123456789abcdef",
		}},
		{"session.intent-status.schema.json", proto.IntentStatusResult{State: "unknown"}},
		{"session.intent-status.schema.json", proto.IntentStatusResult{
			State:  "executed",
			Result: &proto.IntentResult{State: "executed", BytesWritten: 2, FenceAfter: 1},
		}},
		{"session.access-bump.params.schema.json", proto.AccessBumpParams{Session: session, Above: 1}},
		{"session.access-bump.schema.json", proto.AccessBumpResult{Epoch: 2}},
	}
	for _, c := range cases {
		if err := validateHelperJSON(loadHelperSchema(t, c.schema), mustMarshal(t, c.value)); err != nil {
			t.Errorf("%T does not satisfy %s: %v", c.value, c.schema, err)
		}
	}
}

// recordingConn wraps a client.HelperConn and records the Result of the
// most recent session.Response frame the client's own read pump has
// decoded off Stdout() — the exact bytes ONE client call (c.Snapshot,
// c.Target, ...) actually received, not a second, independent round trip.
//
// Snapshot and target are minted fresh every call (hs.snapNext++,
// tokenBook.Mint's own random id), so calling an op once through a raw
// c.Call to capture the wire and AGAIN through its typed client method —
// TestTheOneShotWritePathConformsToItsContractOverTheWire used to do
// exactly that — necessarily gets two DIFFERENT snapshots or tokens: the
// mismatch it then reported ("client.Snapshot decoded snapshotId 2, the
// wire carried 1") was two live resources, never a decode defect. Recording
// the SAME call's own bytes instead of a second call is what proves
// client.Snapshot/client.Target decode what the wire actually sent.
type recordingConn struct {
	client.HelperConn
	stdout io.Reader

	mu     sync.Mutex
	result json.RawMessage
	have   bool
}

func newRecordingConn(base client.HelperConn) *recordingConn {
	r := &recordingConn{HelperConn: base}
	pr, pw := io.Pipe()
	dec := proto.NewDecoder(func(t proto.FrameType, _, _ uint32, payload []byte) {
		if t != proto.TypeResponse {
			return
		}
		var resp proto.Response
		if err := json.Unmarshal(payload, &resp); err != nil || resp.Result == nil {
			return
		}
		r.mu.Lock()
		r.result = append(json.RawMessage(nil), resp.Result...)
		r.have = true
		r.mu.Unlock()
	}, nil)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := base.Stdout().Read(buf)
			if n > 0 {
				// Fed to the decoder BEFORE the copy reaches pw: pw.Write
				// blocks until the client's own read pump reads from pr, so
				// by the time that pump can possibly act on these bytes,
				// dec.Feed has already recorded whatever response they
				// completed.
				_ = dec.Feed(buf[:n]) // never returns an error (frame.go's own doc)
				if _, werr := pw.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	r.stdout = pr
	return r
}

func (r *recordingConn) Stdout() io.Reader { return r.stdout }

// lastResult answers the most recently decoded response's raw result bytes.
// It must be called right after the client call whose wire bytes it is
// meant to capture — calls in this test are strictly sequential, so there
// is never more than one response in flight to be ambiguous about.
func (r *recordingConn) lastResult(t *testing.T) json.RawMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.have {
		t.Fatal("no response recorded yet")
	}
	return r.result
}

// hostedSessionsRecording is hostedSessions (session_service_contract_test.go)
// with its HelperConn wrapped in a recordingConn, so a test can validate the
// schema of the exact bytes a typed client method (c.Snapshot, c.Target, ...)
// itself decoded, rather than a second call's.
func hostedSessionsRecording(t *testing.T) (*client.Client, *recordingConn) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: "testhash",
		Spawner:    session.NewLocalSpawner(log, session.Shell{Path: "/bin/sh"}, ""),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)

	base := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	rec := newRecordingConn(base)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: rec, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}

// oneShotWriteSession spawns a real shell through the real session service —
// the same seam spawnOverTheWire (screen_contract_test.go) uses — and hands
// back the client, its recorder and the spawned entry.
func oneShotWriteSession(t *testing.T) (*client.Client, *recordingConn, client.SessionEntry) {
	t.Helper()
	c, rec := hostedSessionsRecording(t)
	entry := spawnOverTheWire(t, c)
	return c, rec, entry
}

// waitStableSnapshot reads snapshots until two consecutive reads agree on
// the whole frame (content AND cursor — the same two facts checkToken's
// digest covers), and returns the second of that agreeing pair.
//
// It is a WAIT ON OBSERVABLE STATE, not a duration (AGENTS.md: "a test may
// not depend on timing... wait on an observable state change"): the shell
// this test spawns is real and keeps producing output on its own schedule
// for a while after the pty exists, and "nothing is still arriving" is
// exactly what two identical reads in a row means. The deadline below is a
// FAILSAFE against a shell that never settles, not the success condition —
// the same shape every bounded wait elsewhere in this package already uses
// (a select on an event with a t.Fatal on the side that only fires when
// something is actually wrong).
func waitStableSnapshot(t *testing.T, c *client.Client, id client.HostSessionID) proto.SnapshotResult {
	t.Helper()
	ctx := context.Background()
	prev, err := c.Snapshot(ctx, id)
	if err != nil {
		t.Fatalf("client.Snapshot: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(5 * time.Millisecond)
		cur, err := c.Snapshot(ctx, id)
		if err != nil {
			t.Fatalf("client.Snapshot: %v", err)
		}
		if reflect.DeepEqual(cur.Frame, prev.Frame) {
			return cur
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shell's screen never settled within %s; last two frames still disagreed", 5*time.Second)
		}
		prev = cur
	}
}

// TestTheOneShotWritePathConformsToItsContractOverTheWire drives one real
// session through all five ops in the order a coordinator actually uses
// them: read a snapshot, mint a target from it, spend the target, poll its
// status, then bump the session's access epoch — validating the raw bytes
// off the socket against each op's own schema at every step.
func TestTheOneShotWritePathConformsToItsContractOverTheWire(t *testing.T) {
	c, rec, entry := oneShotWriteSession(t)
	ctx := context.Background()
	var err error
	session := proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	}

	// --- snapshot ---
	snapParams := proto.SnapshotParams{Session: session}
	if err = validateHelperJSON(loadHelperSchema(t, "session.snapshot.params.schema.json"), mustMarshal(t, snapParams)); err != nil {
		t.Fatalf("snapshot params do not satisfy the contract: %v", err)
	}
	// A real login shell keeps printing after the pty exists — a prompt
	// redraw, a startup file's own output — asynchronously with everything
	// this test does. Minting a target against a screen that is still
	// settling races that output: the token's digest is taken from THIS
	// read, checkToken (tokens.go) recomputes it from whatever the screen
	// holds at commit, and content that arrived in between is an honest
	// stale_target — the mechanism working as designed against an unquiet
	// terminal, not a defect in it (measured: a handful of failures in
	// several dozen runs, gone once the read waits for the shell to go
	// quiet first). waitStableSnapshot is that wait: it reads until two
	// consecutive reads agree, which is what "nothing is still arriving"
	// actually means, rather than a duration nobody could size correctly
	// for a shell's own startup files.
	//
	// Each call is still through the typed client method itself — snapshot
	// ids are minted fresh per call (hs.snapNext++, session.go), so a raw
	// capture of one and a decode of a different one would always disagree;
	// rec captures the SAME response the STABLE call itself decoded.
	clientSnap := waitStableSnapshot(t, c, entry.HostSessionID)
	snapRaw := rec.lastResult(t)
	if err = validateHelperJSON(loadHelperSchema(t, "session.snapshot.schema.json"), snapRaw); err != nil {
		t.Fatalf("the snapshot result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, snapRaw)
	}
	var snap proto.SnapshotResult
	if err = json.Unmarshal(snapRaw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.AccessEpoch != 1 {
		t.Errorf("a fresh incarnation's access epoch = %d, want 1", snap.AccessEpoch)
	}
	if !snap.ReadBarrier {
		t.Error("a local session's snapshot reports readBarrier = false, want true")
	}
	if clientSnap.SnapshotID != snap.SnapshotID {
		t.Errorf("client.Snapshot decoded snapshotId %d, the wire carried %d", clientSnap.SnapshotID, snap.SnapshotID)
	}

	// --- target ---
	targetParams := proto.TargetParams{
		Session: session, SnapshotID: snap.SnapshotID, Kind: "region",
		First: 0, Last: snap.Frame.Rows - 1,
	}
	if err = validateHelperJSON(loadHelperSchema(t, "session.target.params.schema.json"), mustMarshal(t, targetParams)); err != nil {
		t.Fatalf("target params do not satisfy the contract: %v", err)
	}
	// ONE call again, for the same reason as snapshot above: a token id is
	// minted fresh per Mint (tokens.go, drawn at random), so a second,
	// independent c.Target call can never carry the same tokenId the first
	// one did — rec captures the bytes THIS call actually received.
	clientTarget, err := c.Target(ctx, targetParams)
	if err != nil {
		t.Fatalf("client.Target: %v", err)
	}
	targetRaw := rec.lastResult(t)
	if err = validateHelperJSON(loadHelperSchema(t, "session.target.schema.json"), targetRaw); err != nil {
		t.Fatalf("the target result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, targetRaw)
	}
	var target proto.TargetResult
	if err = json.Unmarshal(targetRaw, &target); err != nil {
		t.Fatalf("decode target: %v", err)
	}
	if clientTarget.TokenID != target.TokenID {
		t.Errorf("client.Target decoded tokenId %q, the wire carried %q", clientTarget.TokenID, target.TokenID)
	}

	// --- intent: spend the token with a text write ---
	const payload = "hi"
	intentParams := proto.IntentParams{
		Session: session, Token: target.Token, AccessEpoch: snap.AccessEpoch,
		CommitBy: farFutureCommitBy(), Kind: "text", Payload: []byte(payload),
	}
	if err = validateHelperJSON(loadHelperSchema(t, "session.intent.params.schema.json"), mustMarshal(t, intentParams)); err != nil {
		t.Fatalf("intent params do not satisfy the contract: %v", err)
	}
	var intentRaw json.RawMessage
	if err = c.Call(ctx, proto.ServiceSession, proto.OpIntent, intentParams, &intentRaw); err != nil {
		t.Fatalf("intent: %v", err)
	}
	if err = validateHelperJSON(loadHelperSchema(t, "session.intent.schema.json"), intentRaw); err != nil {
		t.Fatalf("the intent result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, intentRaw)
	}
	var intentResult proto.IntentResult
	if err = json.Unmarshal(intentRaw, &intentResult); err != nil {
		t.Fatalf("decode intent: %v", err)
	}
	if intentResult.State != "executed" {
		t.Fatalf("intent state = %q, want executed (refusal: %+v)", intentResult.State, intentResult.Refusal)
	}
	if intentResult.BytesWritten != len(payload) {
		t.Errorf("bytesWritten = %d, want %d", intentResult.BytesWritten, len(payload))
	}
	clientIntent, err := c.Intent(ctx, intentParams)
	if err != nil {
		t.Fatalf("client.Intent (replay of the same token+intent): %v", err)
	}
	if clientIntent.State != "executed" || clientIntent.BytesWritten != len(payload) {
		t.Errorf("replaying the same token+intent = %+v, want the same executed answer", clientIntent)
	}

	// --- intent-status: the replay above proved the record; poll it too ---
	statusParams := proto.IntentStatusParams{Session: session, TokenID: target.TokenID}
	if err = validateHelperJSON(loadHelperSchema(t, "session.intent-status.params.schema.json"), mustMarshal(t, statusParams)); err != nil {
		t.Fatalf("intent-status params do not satisfy the contract: %v", err)
	}
	var statusRaw json.RawMessage
	if err = c.Call(ctx, proto.ServiceSession, proto.OpIntentStatus, statusParams, &statusRaw); err != nil {
		t.Fatalf("intent-status: %v", err)
	}
	if err = validateHelperJSON(loadHelperSchema(t, "session.intent-status.schema.json"), statusRaw); err != nil {
		t.Fatalf("the intent-status result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, statusRaw)
	}
	var status proto.IntentStatusResult
	if err = json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("decode intent-status: %v", err)
	}
	if status.State != "executed" || status.Result == nil || status.Result.BytesWritten != len(payload) {
		t.Fatalf("intent-status = %+v, want the recorded executed result", status)
	}
	clientStatus, err := c.IntentStatus(ctx, entry.HostSessionID, target.TokenID)
	if err != nil {
		t.Fatalf("client.IntentStatus: %v", err)
	}
	if clientStatus.State != "executed" {
		t.Errorf("client.IntentStatus state = %q, want executed", clientStatus.State)
	}

	// --- access-bump ---
	bumpParams := proto.AccessBumpParams{Session: session, Above: snap.AccessEpoch}
	if err = validateHelperJSON(loadHelperSchema(t, "session.access-bump.params.schema.json"), mustMarshal(t, bumpParams)); err != nil {
		t.Fatalf("access-bump params do not satisfy the contract: %v", err)
	}
	var bumpRaw json.RawMessage
	if err = c.Call(ctx, proto.ServiceSession, proto.OpAccessBump, bumpParams, &bumpRaw); err != nil {
		t.Fatalf("access-bump: %v", err)
	}
	if err = validateHelperJSON(loadHelperSchema(t, "session.access-bump.schema.json"), bumpRaw); err != nil {
		t.Fatalf("the access-bump result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, bumpRaw)
	}
	var bump proto.AccessBumpResult
	if err = json.Unmarshal(bumpRaw, &bump); err != nil {
		t.Fatalf("decode access-bump: %v", err)
	}
	if bump.Epoch != snap.AccessEpoch+1 {
		t.Fatalf("access-bump(above=%d) = %d, want %d", snap.AccessEpoch, bump.Epoch, snap.AccessEpoch+1)
	}
	clientBump, err := c.AccessBump(ctx, entry.HostSessionID, snap.AccessEpoch)
	if err != nil {
		t.Fatalf("client.AccessBump: %v", err)
	}
	if clientBump.Epoch != bump.Epoch {
		t.Errorf("client.AccessBump repeat (idempotent on above) = %d, want %d", clientBump.Epoch, bump.Epoch)
	}

	// A snapshot taken AFTER the bump reports the raised epoch.
	afterBump, err := c.Snapshot(ctx, entry.HostSessionID)
	if err != nil {
		t.Fatalf("client.Snapshot after bump: %v", err)
	}
	if afterBump.AccessEpoch != bump.Epoch {
		t.Errorf("snapshot after the bump reports epoch %d, want %d", afterBump.AccessEpoch, bump.Epoch)
	}

	// A fresh session.intent presenting the OLD (now stale) access epoch is
	// refused access_revoked at the commit point, never executed.
	target2Params := proto.TargetParams{Session: session, SnapshotID: afterBump.SnapshotID, Kind: "region", First: 0, Last: afterBump.Frame.Rows - 1}
	target2, err := c.Target(ctx, target2Params)
	if err != nil {
		t.Fatalf("client.Target (second): %v", err)
	}
	staleIntent, err := c.Intent(ctx, proto.IntentParams{
		Session: session, Token: target2.Token, AccessEpoch: snap.AccessEpoch, // the OLD epoch
		CommitBy: farFutureCommitBy(), Kind: "text", Payload: []byte("x"),
	})
	if err != nil {
		t.Fatalf("client.Intent (stale epoch): %v", err)
	}
	if staleIntent.State != "refused" || staleIntent.Refusal == nil || staleIntent.Refusal.Cause != "access_revoked" {
		t.Fatalf("intent presenting a superseded access epoch = %+v, want refused/access_revoked", staleIntent)
	}
}

// farFutureCommitBy is a commitBy comfortably in the future on the shared
// monotonic clock this test's own process and the in-process helper share
// (they are the same binary in this fake-conn fixture): int64 nanoseconds is
// what internal/monoclock reads, and a fixed large-but-finite offset from an
// ordinary reading is enough that no real test run could ever cross it.
func farFutureCommitBy() int64 {
	return int64(1)<<62 - 1
}

// TestAnOlderGenerationAnswersUnknownOpForTheOneShotWritePath is
// ErrIntentUnsupported's own test, the pair ErrScreenUnsupported and
// ErrLifecycleAdoptUnsupported already have (session_adopt_lifecycle_test.go's
// olderGeneration and helperServing, reused unchanged): a generation from
// before this epic answers unknown_op to all five ops, and the client must
// read that as "this generation cannot do this" rather than as a broken
// connection.
func TestAnOlderGenerationAnswersUnknownOpForTheOneShotWritePath(t *testing.T) {
	c := helperServing(t, olderGeneration{})
	session := client.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"}

	checks := []struct {
		name string
		call func() error
	}{
		{"Snapshot", func() error { _, err := c.Snapshot(context.Background(), session); return err }},
		{"Target", func() error {
			_, err := c.Target(context.Background(), proto.TargetParams{
				Session: proto.HostSessionID{Generation: "testhash", Session: session.Session},
			})
			return err
		}},
		{"Intent", func() error {
			_, err := c.Intent(context.Background(), proto.IntentParams{
				Session: proto.HostSessionID{Generation: "testhash", Session: session.Session},
			})
			return err
		}},
		{"IntentStatus", func() error {
			_, err := c.IntentStatus(context.Background(), session, "0123456789abcdef0123456789abcdef")
			return err
		}},
		{"AccessBump", func() error { _, err := c.AccessBump(context.Background(), session, 1); return err }},
	}
	for _, chk := range checks {
		err := chk.call()
		if !errors.Is(err, client.ErrIntentUnsupported) {
			t.Errorf("%s against a generation without the one-shot write path = %v, want ErrIntentUnsupported", chk.name, err)
		}
		if errors.Is(err, client.ErrLost) {
			t.Errorf("%s: an op an older generation does not know is a refusal, never a lost connection", chk.name)
		}
	}
}

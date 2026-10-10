package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helperSession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

const (
	helperWindowBytes = 256 << 10
	overflowBytes     = 2 << 20
)

// heldAttachedReader gates the actual helper attachment's next Read only after
// the deliberate prefix marker has passed through that same reader. The helper
// client and service remain connected and running while the coordinator reader
// is held, so the real helper window—not a proxy or test ring—moves past it.
type heldAttachedReader struct {
	*helperclient.AttachedSession
	prefixMarker []byte
	prefixTail   []byte
	gated        chan struct{}
	release      <-chan struct{}
	gateOnce     sync.Once
	armed        bool
}

func (r *heldAttachedReader) Read(p []byte) (int, error) {
	if r.armed {
		r.armed = false
		r.gateOnce.Do(func() { close(r.gated) })
		<-r.release
	}
	n, err := r.AttachedSession.Read(p)
	if n > 0 {
		scan := append(r.prefixTail, p[:n]...)
		if bytes.Contains(scan, r.prefixMarker) {
			r.armed = true
		} else if len(r.prefixMarker) > 1 {
			keep := len(r.prefixMarker) - 1
			if len(scan) > keep {
				scan = scan[len(scan)-keep:]
			}
			r.prefixTail = append(r.prefixTail[:0], scan...)
		}
	}
	return n, err
}

func inProcessOutputHelper(t *testing.T) *helperclient.Client {
	t.Helper()
	logger := logtest.Slog(t)
	svc := helperSession.New(helperSession.Options{
		Generation: "output-gap-test",
		Spawner:    helperSession.NewLocalSpawner(logger, helperSession.Shell{Path: "/bin/sh"}, ""),
		Inspector:  helperSession.NewInspector(),
		Log:        logger,
		Limits:     helperSession.DefaultLimits(),
	})
	t.Cleanup(svc.Close)

	coordinator, helper := net.Pipe()
	peer := host.New(helper, helper, "output-gap-test", "output-gap-helper", logger)
	peer.Register(svc)
	release := svc.Bind(peer)
	peerDone := make(chan error, 1)
	go func() {
		defer release()
		peerDone <- peer.Serve(context.Background())
	}()

	c, err := helperclient.Dial(context.Background(), helperclient.Config{
		Exec: helperclient.NewSocketConn(coordinator), ExpectHash: "output-gap-test",
		SentinelTTL: 5 * time.Second, Log: logger,
	})
	if err != nil {
		t.Fatalf("dial the in-process helper over its wire: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		<-peerDone
	})
	return c
}

func shellQuoteForOutputGapTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// shellPrintfOctalForOutputGapTest encodes the marker so an interactive shell's
// command echo cannot satisfy the test's output-marker observation.
func shellPrintfOctalForOutputGapTest(value string) string {
	var format strings.Builder
	for _, b := range []byte(value) {
		fmt.Fprintf(&format, "\\%03o", b)
	}
	return format.String()
}

// TestARealHelperWindowResetReachesTheLiveWebSocketInOrder covers a live
// coordinator, an actual helper window and its actual attachment reader. The
// test gates AttachedSession.Read, lets the shell overflow the helper's
// bounded window, then releases that same reader and observes the gap on the
// coordinator WebSocket between the two byte ranges.
func TestARealHelperWindowResetReachesTheLiveWebSocketInOrder(t *testing.T) {
	ctx, logger := logtest.New(t)
	helper := inProcessOutputHelper(t)
	entry, err := helper.Spawn(ctx, proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, WindowBytes: helperWindowBytes,
	})
	if err != nil {
		t.Fatalf("spawn the helper-hosted shell: %v", err)
	}
	sid := session.ID(entry.HostSessionID.Session)
	readerOffset := entry.Window.Base

	reg := session.New(logger, nil)
	ws := NewWSServer(logger, reg)
	if startErr := ws.Start(ctx); startErr != nil {
		t.Fatalf("start coordinator: %v", startErr)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	releaseReader := make(chan struct{})
	var releaseOnce sync.Once
	openReader := func() { releaseOnce.Do(func() { close(releaseReader) }) }
	prefixMarker := []byte("NOCX_LIVE_GAP_PREFIX_0123456789")
	gated := make(chan struct{})
	t.Cleanup(openReader)
	var attached *helperclient.AttachedSession
	var adopted session.Session
	type helperHole struct {
		lost   uint64
		reason string
	}
	helperHoles := make(chan helperHole, 1)
	var firstHole sync.Once
	err = ws.ReadoptHostedSession(ctx, sid, func(ctx context.Context, from uint64) (HostedSessionOpen, error) {
		a, attachErr := helper.Attach(ctx, proto.AttachParams{
			Subscriber: "0123456789abcdef0123456789abcdef",
			Session: proto.HostSessionID{
				Generation: proto.GenerationID(entry.HostSessionID.Generation),
				Session:    entry.HostSessionID.Session,
			},
			Offset: proto.StreamOffset(from), Fresh: false, RequestWrite: true,
		})
		if attachErr != nil {
			return HostedSessionOpen{}, attachErr
		}
		attached = a
		channel := &heldAttachedReader{
			AttachedSession: a, prefixMarker: prefixMarker, gated: gated, release: releaseReader,
		}
		var adoptErr error
		adopted, adoptErr = reg.Adopt(ctx, session.Config{
			Kind: session.KindLocal, Cwd: "/", Cols: 80, Rows: 24,
		}, sid, channel)
		if adoptErr != nil {
			_ = a.Close()
			return HostedSessionOpen{}, adoptErr
		}
		return HostedSessionOpen{Session: adopted, ObserveOutputHoles: func(report func(uint64, string)) {
			a.OnOutputHole(func(lost uint64, reason string) {
				firstHole.Do(func() { helperHoles <- helperHole{lost: lost, reason: reason} })
				report(lost, reason)
			})
		}}, nil
	})
	if err != nil {
		t.Fatalf("readopt the real helper session: %v", err)
	}
	if attached == nil || adopted == nil {
		t.Fatal("readopt did not install the helper attachment and session")
	}
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	identity := adopted.Identity()
	attachRaw := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": string(sid), "instanceId": string(identity.InstanceID),
		"sessionEpoch": identity.Epoch, "offset": readerOffset,
	}, 1)
	var attachEnvelope struct {
		Result attachResult     `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(attachRaw, &attachEnvelope); err != nil {
		t.Fatalf("decode browser attach: %v (%s)", err, attachRaw)
	}
	if attachEnvelope.Error != nil {
		t.Fatalf("browser attach: %+v", attachEnvelope.Error)
	}
	if attachEnvelope.Result.From != readerOffset || !attachEnvelope.Result.Resumed || attachEnvelope.Result.Reset {
		t.Fatalf("browser attach = %+v, want no gap before the helper reader is released at %d", attachEnvelope.Result, readerOffset)
	}

	read := func(what string) (int, []byte) {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		kind, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read %s: %v", what, readErr)
		}
		return kind, raw
	}
	ackOffset := func(offset uint64) {
		t.Helper()
		raw, marshalErr := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "ack",
			"params": map[string]any{"sessionId": string(sid), "offset": offset},
		})
		if marshalErr != nil {
			t.Fatalf("marshal acknowledgement at %d: %v", offset, marshalErr)
		}
		if writeErr := conn.WriteMessage(websocket.TextMessage, raw); writeErr != nil {
			t.Fatalf("acknowledge output at %d: %v", offset, writeErr)
		}
	}

	// Let a deliberate shell prefix pass through the real helper reader and
	// reach the WebSocket. The command's printf uses octal escapes so its
	// interactive echo cannot impersonate the output marker.
	marker := filepath.Join(t.TempDir(), "output-finished")
	command := fmt.Sprintf(
		"printf '%s'; read -r _; head -c %d /dev/zero; printf done > %s; read -r _\n",
		shellPrintfOctalForOutputGapTest(string(prefixMarker)), overflowBytes, shellQuoteForOutputGapTest(marker),
	)
	if n, writeErr := attached.Write([]byte(command)); writeErr != nil || n != len(command) {
		t.Fatalf("write staged command to the actual helper session: wrote %d/%d, err %v", n, len(command), writeErr)
	}

	ackedPrefixEnd := readerOffset
	var prefixTail []byte
	for !bytes.Contains(prefixTail, prefixMarker) {
		kind, raw := read("the deliberate prefix on the real WebSocket")
		if kind != websocket.BinaryMessage {
			var notification struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &notification); err == nil && notification.Method == "session.outputGap" {
				t.Fatalf("gap arrived before the staged prefix: %s", raw)
			}
			continue
		}
		frame, decodeErr := DecodeFrame(raw)
		if decodeErr != nil {
			t.Fatalf("decode staged prefix output: %v", decodeErr)
		}
		if session.IDFromBytes(frame.SessionID) != sid {
			t.Fatalf("staged prefix frame belongs to %s, want %s", session.IDFromBytes(frame.SessionID), sid)
		}
		ackedPrefixEnd += uint64(len(frame.Payload))
		ackOffset(ackedPrefixEnd)
		scan := append(prefixTail, frame.Payload...)
		if len(prefixMarker) > 1 && len(scan) >= len(prefixMarker)-1 {
			prefixTail = append(prefixTail[:0], scan[len(scan)-(len(prefixMarker)-1):]...)
		} else {
			prefixTail = append(prefixTail[:0], scan...)
		}
		if bytes.Contains(scan, prefixMarker) {
			break
		}
	}
	waittest.WaitForTimeout(t, "the actual helper reader to gate after the acknowledged prefix", wantWithin, func() bool {
		select {
		case <-gated:
			return true
		default:
			return false
		}
	})

	// The shell is now parked in read; resume it only after the helper's real
	// reader is held, then wait for its own output window and completion marker.
	if n, writeErr := attached.Write([]byte("\n")); writeErr != nil || n != 1 {
		t.Fatalf("release the staged shell command: wrote %d/1, err %v", n, writeErr)
	}
	stagedPrefixEnd := ackedPrefixEnd
	var overflow helperclient.SessionEntry
	var inventoryErr error
	waittest.WaitForTimeout(t, "the helper window to pass the acknowledged prefix while its reader is gated", wantWithin, func() bool {
		entries, listErr := helper.Sessions(ctx)
		if listErr != nil {
			inventoryErr = listErr
			return false
		}
		for _, candidate := range entries {
			if candidate.HostSessionID.Session != string(sid) || candidate.Window.Base <= stagedPrefixEnd || candidate.Window.Written <= candidate.Window.Base {
				continue
			}
			if data, readErr := os.ReadFile(marker); readErr == nil && string(data) == "done" { //nolint:gosec // G304: this test-created marker is under t.TempDir().
				overflow = candidate
				return true
			}
		}
		return false
	})
	if inventoryErr != nil {
		t.Fatalf("read helper inventory while waiting for its window to overflow: %v", inventoryErr)
	}
	if overflow.Window.Base <= stagedPrefixEnd {
		t.Fatalf("helper window base = %d, want it past staged prefix end %d", overflow.Window.Base, stagedPrefixEnd)
	}
	openReader()

	// Any already-sent in-flight bytes before the helper's reset are also
	// observed and acknowledged here; this final acked prefix ends at the gap.
	var gap sessionOutputGapNotification
	for {
		kind, raw := read("output prefix or session.outputGap")
		if kind == websocket.BinaryMessage {
			frame, decodeErr := DecodeFrame(raw)
			if decodeErr != nil {
				t.Fatalf("decode pre-gap output: %v", decodeErr)
			}
			if session.IDFromBytes(frame.SessionID) != sid {
				t.Fatalf("pre-gap frame belongs to %s, want %s", session.IDFromBytes(frame.SessionID), sid)
			}
			ackedPrefixEnd += uint64(len(frame.Payload))
			ackOffset(ackedPrefixEnd)
			continue
		}
		var notification struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &notification); err != nil {
			t.Fatalf("decode control frame before gap: %v (%s)", err, raw)
		}
		if notification.Method != "session.outputGap" {
			continue
		}
		if notification.JSONRPC != "2.0" {
			t.Fatalf("gap JSON-RPC version = %q, want 2.0", notification.JSONRPC)
		}
		if err := json.Unmarshal(notification.Params, &gap); err != nil {
			t.Fatalf("decode same-coordinator gap payload: %v (%s)", err, notification.Params)
		}
		validateJSON(t, loadSchema(t, "session.outputGap.schema.json"), notification.Params, "same-coordinator session.outputGap params off the real socket")
		break
	}
	if ackedPrefixEnd < stagedPrefixEnd {
		t.Fatalf("pre-gap acked prefix end = %d, before staged prefix end %d", ackedPrefixEnd, stagedPrefixEnd)
	}
	var helperHoleAtReset helperHole
	select {
	case helperHoleAtReset = <-helperHoles:
	default:
		t.Fatal("the actual helper attachment reader reported no reset before the WebSocket gap")
	}
	wantEnd := ackedPrefixEnd + helperHoleAtReset.lost
	if helperHoleAtReset.reason != proto.GapReasonWindow {
		t.Fatalf("helper reset reason = %q, want %q", helperHoleAtReset.reason, proto.GapReasonWindow)
	}
	if gap.SessionID != string(sid) || gap.Start != ackedPrefixEnd || gap.End != wantEnd || gap.Reason != "hostWindow" {
		t.Fatalf("same-coordinator gap = %+v, want session %s range [%d,%d) reason hostWindow from helper reset %+v", gap, sid, ackedPrefixEnd, wantEnd, helperHoleAtReset)
	}
	ack, marshalErr := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "ack",
		"params": map[string]any{"sessionId": string(sid), "offset": gap.End},
	})
	if marshalErr != nil {
		t.Fatalf("marshal acknowledgement at gap end %d: %v", gap.End, marshalErr)
	}
	if writeErr := conn.WriteMessage(websocket.TextMessage, ack); writeErr != nil {
		t.Fatalf("acknowledge live gap through %d: %v", gap.End, writeErr)
	}

	for {
		kind, raw := read("post-gap helper output")
		if kind != websocket.BinaryMessage {
			var notification struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &notification)
			if notification.Method == "session.outputGap" {
				t.Fatalf("duplicate output-gap notification after the attach reader's reset: %s", raw)
			}
			continue
		}
		frame, decodeErr := DecodeFrame(raw)
		if decodeErr != nil {
			t.Fatalf("decode post-gap output: %v", decodeErr)
		}
		if session.IDFromBytes(frame.SessionID) != sid || len(frame.Payload) == 0 {
			t.Fatalf("post-gap frame = %+v, want non-empty output for %s", frame, sid)
		}
		return
	}
}

// TestAReadoptResetIsReportedToTheFirstWebSocketAfterTheRingHasPassedIt covers
// the startup order in which a helper reattachment has already crossed its
// lost range before a browser can attach. The recorder consumes that hole
// while the socket is absent; the first browser still needs the event even
// when it starts at the ring's current end.
func TestAReadoptResetIsReportedToTheFirstWebSocketAfterTheRingHasPassedIt(t *testing.T) {
	ctx, logger := logtest.New(t)
	helper := inProcessOutputHelper(t)
	entry, err := helper.Spawn(ctx, proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, WindowBytes: helperWindowBytes,
	})
	if err != nil {
		t.Fatalf("spawn helper session: %v", err)
	}
	sid := session.ID(entry.HostSessionID.Session)
	id := proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	}

	// Produce more than the host window with no reader attached. This is the
	// same condition a replacement coordinator meets after its predecessor
	// has gone: the helper has kept the shell, but the requested cursor is now
	// behind the window's base.
	writer, err := helper.Attach(ctx, proto.AttachParams{
		Subscriber:   "11111111111111111111111111111111",
		Session:      id,
		Offset:       proto.StreamOffset(entry.Window.Base),
		Fresh:        true,
		RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach to start the helper command: %v", err)
	}
	commandDone := filepath.Join(t.TempDir(), "overflow-done")
	const drainMarker = "NOCX_OUTPUT_GAP_DRAINED"
	// Keep the interactive shell alive after the large output finishes. The
	// test releases this read only after the late WebSocket has attached, so
	// the marker below is new output from the same live session, not an attempt
	// to write into a shell that exited while the coordinator was absent.
	command := fmt.Sprintf(
		"head -c %d /dev/zero; printf '%s'; printf done > %s; read -r _\n",
		overflowBytes, drainMarker, shellQuoteForOutputGapTest(commandDone),
	)
	if n, writeErr := writer.Write([]byte(command)); writeErr != nil || n != len(command) {
		t.Fatalf("start detached helper output: wrote %d/%d, err %v", n, len(command), writeErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("detach the command writer: %v", closeErr)
	}
	waittest.WaitForTimeout(t, "the helper command to finish while detached", 20*time.Second, func() bool {
		_, statErr := os.Stat(commandDone)
		return statErr == nil
	})

	var beforeReadopt helperclient.SessionEntry
	wantWritten := entry.Window.Written + uint64(overflowBytes+len(drainMarker))
	waittest.WaitFor(t, "the helper window to receive the complete overflow through its sentinel", func() bool {
		entries, listErr := helper.Sessions(ctx)
		if listErr != nil {
			return false
		}
		for _, candidate := range entries {
			if candidate.HostSessionID.Session == entry.HostSessionID.Session {
				beforeReadopt = candidate
				return candidate.Window.Base > entry.Window.Base && candidate.Window.Written >= wantWritten
			}
		}
		return false
	})

	rec := newFakeRecorder()
	reg := session.New(logger, nil)
	ws := NewWSServer(logger, reg, WithSessionOutputRecorder(rec))
	if startErr := ws.Start(ctx); startErr != nil {
		t.Fatalf("start coordinator: %v", startErr)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	type observedHole struct {
		lost   uint64
		reason string
	}
	holeCh := make(chan observedHole, 16)
	var attached *helperclient.AttachedSession
	var adopted session.Session
	err = ws.ReadoptHostedSession(ctx, sid, func(ctx context.Context, from uint64) (HostedSessionOpen, error) {
		if from != entry.Window.Base {
			return HostedSessionOpen{}, fmt.Errorf("readopt offset %d, want prior recorded cursor %d", from, entry.Window.Base)
		}
		a, attachErr := helper.Attach(ctx, proto.AttachParams{
			Subscriber:   "22222222222222222222222222222222",
			Session:      id,
			Offset:       proto.StreamOffset(from),
			Fresh:        false,
			RequestWrite: true,
		})
		if attachErr != nil {
			return HostedSessionOpen{}, attachErr
		}
		attached = a
		adopted, attachErr = reg.Adopt(ctx, session.Config{
			Kind: session.KindLocal, Cwd: "/", Cols: 80, Rows: 24,
		}, sid, a)
		if attachErr != nil {
			_ = a.Close()
			return HostedSessionOpen{}, attachErr
		}
		return HostedSessionOpen{Session: adopted, ObserveOutputHoles: func(report func(uint64, string)) {
			a.OnOutputHole(func(lost uint64, reason string) {
				report(lost, reason)
				holeCh <- observedHole{lost: lost, reason: reason}
			})
		}}, nil
	})
	if err != nil {
		t.Fatalf("readopt helper session: %v", err)
	}
	if attached == nil || adopted == nil {
		t.Fatal("readopt did not retain the real helper attachment and session")
	}

	var gap observedHole
	waittest.WaitFor(t, "the readopt observer to receive the helper attach-time reset", func() bool {
		return len(holeCh) > 0
	})
	gap = <-holeCh
	if gap.lost == 0 || gap.reason != proto.GapReasonWindow {
		t.Fatalf("helper reset = %+v, want a non-empty host-window gap", gap)
	}
	if gap.lost != beforeReadopt.Window.Base-entry.Window.Base {
		t.Fatalf("helper reset lost %d bytes, want the host base delta %d", gap.lost, beforeReadopt.Window.Base-entry.Window.Base)
	}

	// Wait until the recorder and ring have consumed the reset and replay. The
	// browser joins only after the hole is behind the stream cursor, as in the
	// restart/reclaim acceptance path.
	var hostAfter helperclient.SessionEntry
	waittest.WaitForTimeout(t, "the readopt ring to catch up with the helper", 20*time.Second, func() bool {
		entries, listErr := helper.Sessions(ctx)
		if listErr != nil {
			return false
		}
		for _, candidate := range entries {
			if candidate.HostSessionID.Session == entry.HostSessionID.Session {
				hostAfter = candidate
				return ws.getRx(sid).ring.writtenLocked() == candidate.Window.Written
			}
		}
		return false
	})
	if hostAfter.Exit != nil {
		t.Fatalf("helper session exited before the late client attached: %+v", hostAfter.Exit)
	}
	if hostAfter.Window.Written <= gap.lost {
		t.Fatalf("host output ended at %d, not after gap width %d", hostAfter.Window.Written, gap.lost)
	}
	holes := []observedHole{gap}
	waittest.WaitFor(t, "the recorder to consume every readopt output hole", func() bool {
		return len(rec.skipCalls()) == len(holes)+len(holeCh)
	})
	for len(holeCh) > 0 {
		holes = append(holes, <-holeCh)
	}
	skips := rec.skipCalls()
	if len(skips) != len(holes) {
		t.Fatalf("recorded %d host holes, observed %d helper resets: skips=%+v holes=%+v", len(skips), len(holes), skips, holes)
	}
	type expectedGap struct {
		start  uint64
		end    uint64
		reason string
	}
	expectedGaps := make([]expectedGap, 0, len(skips))
	cursor := entry.Window.Base
	for i, skip := range skips {
		if skip.reason != "hostWindow" {
			t.Fatalf("recorder skip %d reason = %q, want hostWindow", i, skip.reason)
		}
		if i == 0 && skip.resumeAt != beforeReadopt.Window.Base {
			t.Fatalf("first recorded reset ends at %d, want pre-readopt host base %d", skip.resumeAt, beforeReadopt.Window.Base)
		}
		expectedGaps = append(expectedGaps, expectedGap{start: cursor, end: skip.resumeAt, reason: skip.reason})
		cursor = skip.resumeAt
	}
	for i, hole := range holes {
		if hole.lost == 0 || hole.reason != proto.GapReasonWindow || expectedGaps[i].end-expectedGaps[i].start != hole.lost {
			t.Fatalf("helper reset %d = %+v, independently recorded range = %+v; want matching hostWindow bounds", i, hole, expectedGaps[i])
		}
	}
	rx := ws.getRx(sid)
	rx.deliveryMu.Lock()
	outputGaps := append([]sessionOutputGapRecord(nil), rx.outputGaps...)
	rx.deliveryMu.Unlock()
	if len(outputGaps) != len(holes) {
		t.Fatalf("retained output gaps = %+v, want one for each helper reset %+v", outputGaps, holes)
	}
	for i, recorded := range outputGaps {
		if recorded.end-recorded.start != holes[i].lost || recorded.reason != "hostWindow" {
			t.Fatalf("retained gap %d = %+v, want width %d hostWindow", i, recorded, holes[i].lost)
		}
	}

	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	offset := ws.getRx(sid).ring.writtenLocked()
	identity := adopted.Identity()
	attachRaw := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": string(sid), "instanceId": string(identity.InstanceID),
		"sessionEpoch": identity.Epoch, "offset": offset,
	}, 1)
	var attachEnvelope struct {
		Result attachResult     `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(attachRaw, &attachEnvelope); err != nil {
		t.Fatalf("decode late browser attach: %v (%s)", err, attachRaw)
	}
	if attachEnvelope.Error != nil {
		t.Fatalf("late browser attach: %+v", attachEnvelope.Error)
	}
	if !attachEnvelope.Result.Resumed || attachEnvelope.Result.Reset || attachEnvelope.Result.From != offset {
		t.Fatalf("late browser attach = %+v, want resume at current end %d", attachEnvelope.Result, offset)
	}

	if err := conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
		t.Fatalf("set deadline for the deferred gap events: %v", err)
	}
	for i, expected := range expectedGaps {
		kind, gapFrame, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("late attach did not receive deferred output gap %d/%d: %v", i+1, len(expectedGaps), readErr)
		}
		if kind != websocket.TextMessage {
			t.Fatalf("frame %d after late attach is type %d, want the informational gap before any binary output", i+1, kind)
		}
		var gapEnvelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(gapFrame, &gapEnvelope); err != nil {
			t.Fatalf("decode late-attach frame %d: %v (%s)", i+1, err, gapFrame)
		}
		if gapEnvelope.Method != "session.outputGap" {
			t.Fatalf("frame %d after late attach is %q, want session.outputGap before later output", i+1, gapEnvelope.Method)
		}
		var notice sessionOutputGapNotification
		if err := json.Unmarshal(gapEnvelope.Params, &notice); err != nil {
			t.Fatalf("decode deferred output-gap event %d: %v (%s)", i+1, err, gapEnvelope.Params)
		}
		if notice.SessionID != string(sid) || notice.Start != expected.start || notice.End != expected.end || notice.Reason != expected.reason {
			t.Fatalf("late output-gap event %d = %+v, want session %s [%d,%d) %s", i+1, notice, sid, expected.start, expected.end, expected.reason)
		}
	}
	rx.deliveryMu.Lock()
	retainedGaps := len(rx.outputGaps)
	rx.deliveryMu.Unlock()
	if retainedGaps != len(expectedGaps) {
		t.Fatalf("late attach retained %d output gaps, want all %d ranges for replay after disconnect", retainedGaps, len(expectedGaps))
	}

	// The retained gaps are delivered before any later stream bytes. Cause
	// the same helper session to emit a fresh marker after the first attach.
	const afterGapMarker = "NOCX_AFTER_READOPT_GAP_0123456789"
	if n, writeErr := attached.Write([]byte("\n")); writeErr != nil || n != 1 {
		t.Fatalf("release the live shell after the deferred gap: wrote %d/1, err %v", n, writeErr)
	}
	cmd := fmt.Sprintf("printf '%s\\n'\n", shellPrintfOctalForOutputGapTest(afterGapMarker))
	if n, writeErr := attached.Write([]byte(cmd)); writeErr != nil || n != len(cmd) {
		t.Fatalf("write post-gap marker to the helper: wrote %d/%d, err %v", n, len(cmd), writeErr)
	}
	if err := conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
		t.Fatalf("set deadline for post-gap binary output: %v", err)
	}
	var afterGapOutput []byte
	for !bytes.Contains(afterGapOutput, []byte(afterGapMarker)) {
		kind, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read post-gap binary output: %v", readErr)
		}
		if kind != websocket.BinaryMessage {
			var notification struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &notification)
			if notification.Method == "session.outputGap" {
				t.Fatalf("duplicate deferred output-gap event before post-gap bytes: %s", raw)
			}
			continue
		}
		frame, decodeErr := DecodeFrame(raw)
		if decodeErr != nil {
			t.Fatalf("decode post-gap output frame: %v", decodeErr)
		}
		if session.IDFromBytes(frame.SessionID) != sid {
			t.Fatalf("post-gap frame belongs to %s, want %s", session.IDFromBytes(frame.SessionID), sid)
		}
		afterGapOutput = append(afterGapOutput, frame.Payload...)
	}
}

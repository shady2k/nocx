package app

// The original nocx-5q0ej journey, through the shipped App, a freshly built
// helper, real shells/PTYs, and renderer session.intent requests on one socket.
// B must execute AND publish its output before the test releases A's producer.
// This is event ordering, not a latency benchmark: no elapsed-time threshold
// distinguishes success from failure. Timeouts only stop a broken test.
//
// A real producer cannot guarantee the kernel never presents EAGAIN. Therefore
// real_flood alone is not claimed to fail every scheduling run on the old
// implementation. held_helper_boundary additionally parks A's focus-out at the
// existing helper-routing seam. Everything before and after that hold is real;
// it deterministically catches the old server-global completion FIFO without
// pretending a userspace producer controls kernel scheduling. Neither case
// exercises browser focus handlers or painting; those remain UI acceptance.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

const busyWireMarker = "BUSY-WIRE-B-EXECUTED"

// TestBusyTerminalWireProcess is selected only when the real pane shell
// launches this test binary as its foreground program. The environment is
// scoped to that command, never to the parent test or the helper daemon.
func TestBusyTerminalWireProcess(t *testing.T) {
	mode := os.Getenv("NOCX_BUSY_WIRE_MODE")
	if mode == "" {
		return
	}
	conn, dialErr := net.Dial("unix", os.Getenv("NOCX_BUSY_WIRE_SOCKET"))
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	defer func() { _ = conn.Close() }()
	if mode == "reporter" {
		if _, err := fmt.Fprintln(os.Stdout, busyWireMarker); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, "reporter\n"); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "producer" {
		t.Fatalf("unknown subprocess mode %q", mode)
	}

	// Repaint without scrolling: this isolates session control from history
	// storage cost. The writer is unpaced and stops only on explicit release
	// (or a broken PTY/control connection during failure cleanup).
	chunk := bytes.Repeat([]byte("\x1b[Hbusy terminal output"), 1024)
	if _, err := os.Stdout.Write(append([]byte("\x1b[?1049h"), chunk...)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "producer\n"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		var command [1]byte
		for {
			if _, err := io.ReadFull(conn, command[:]); err != nil {
				return
			}
			if command[0] == 'q' {
				if _, err := io.WriteString(conn, "alive\n"); err != nil {
					return
				}
				continue
			}
			return // explicit release
		}
	}()
	for {
		select {
		case <-stop:
			_, _ = io.WriteString(os.Stdout, "\x1b[?1049l")
			return
		default:
		}
		if _, err := os.Stdout.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
}

type busyWireHeldSource struct {
	*paneScreen
	mu      sync.Mutex
	target  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *busyWireHeldSource) Intent(ctx context.Context, sid string, p proto.IntentParams) (proto.IntentResult, error) {
	s.mu.Lock()
	hold := sid == s.target && p.Kind == "focus" && string(p.Payload) == "out"
	s.mu.Unlock()
	if hold {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return proto.IntentResult{}, ctx.Err()
		}
	}
	return s.paneScreen.Intent(ctx, sid, p)
}

type busyWireResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type busyWireRenderer struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	responses chan busyWireResponse
	marker    chan struct{}
	errors    chan error
	done      chan struct{}
	mu        sync.Mutex
	markerSID string
}

func newBusyWireRenderer(t *testing.T, a *App) *busyWireRenderer {
	t.Helper()
	r := &busyWireRenderer{
		conn: dialRenderer(t, a), responses: make(chan busyWireResponse, 32),
		marker: make(chan struct{}, 1), errors: make(chan error, 1), done: make(chan struct{}),
	}
	if err := r.conn.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	go r.read()
	t.Cleanup(func() {
		_ = r.conn.Close()
		<-r.done
	})
	return r
}

// One socket reader drains A continuously; an unconsumed frame queue must not
// become a second source of backpressure. Only RPC answers and the B marker
// are retained. No testing.T method is called from this goroutine.
func (r *busyWireRenderer) read() {
	defer close(r.done)
	offsets := make(map[string]uint64)
	seenMarker := false
	for {
		kind, raw, readErr := r.conn.ReadMessage()
		if readErr != nil {
			r.errors <- readErr
			return
		}
		if kind == websocket.TextMessage {
			var response busyWireResponse
			if err := json.Unmarshal(raw, &response); err == nil && response.ID != 0 {
				r.responses <- response
			}
			continue
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		frame, decodeErr := transport.DecodeFrame(raw)
		if decodeErr != nil {
			r.errors <- decodeErr
			return
		}
		sid := string(session.IDFromBytes(frame.SessionID))
		if frame.MsgType == transport.MsgTypeData {
			offsets[sid] += uint64(len(frame.Payload))
			if err := r.send(0, "ack", map[string]any{"sessionId": sid, "offset": offsets[sid]}); err != nil {
				r.errors <- err
				return
			}
		}
		r.mu.Lock()
		wanted := sid == r.markerSID
		r.mu.Unlock()
		if !wanted || seenMarker || frame.MsgType != transport.MsgTypeMetadata {
			continue
		}
		// Check the renderer's actual screen rows, not raw PTY transport
		// output or a value synthesized by the test. This is the current
		// session.frame.schema.json rows[].text representation.
		var screen struct {
			Rows []struct {
				Text string `json:"text"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(frame.Payload, &screen); err != nil {
			r.errors <- fmt.Errorf("decode B screen: %w", err)
			return
		}
		for _, row := range screen.Rows {
			if strings.Contains(row.Text, busyWireMarker) {
				r.marker <- struct{}{}
				seenMarker = true
				break
			}
		}
	}
}

func (r *busyWireRenderer) send(id int, method string, params any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != 0 {
		req["id"] = id
	}
	return r.conn.WriteJSON(req)
}

func (r *busyWireRenderer) response(t *testing.T, id int) json.RawMessage {
	t.Helper()
	for {
		select {
		case response := <-r.responses:
			if len(response.Error) != 0 && string(response.Error) != "null" {
				t.Fatalf("RPC %d failed: %s", response.ID, response.Error)
			}
			if response.ID == id {
				return response.Result
			}
		case err := <-r.errors:
			t.Fatalf("renderer failed waiting for RPC %d: %v", id, err)
		case <-time.After(time.Minute):
			t.Fatalf("RPC %d did not complete", id)
		}
	}
}

func busyWireCommand(binary, socket, mode string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	return "env NOCX_BUSY_WIRE_MODE=" + mode + " NOCX_BUSY_WIRE_SOCKET=" + quote(socket) + " " +
		quote(binary) + " -test.run='^TestBusyTerminalWireProcess$'\n"
}

func TestBusyTerminalDoesNotHoldOtherPaneExecutionOverTheRealAppWire(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "real_flood"
		if held {
			name = "held_helper_boundary"
		}
		t.Run(name, func(t *testing.T) {
			a := newLocalPaneApp(t)
			var source *busyWireHeldSource
			if held {
				source = &busyWireHeldSource{
					paneScreen: newPaneScreen(logtest.Slog(t), a.Session, a.localHelper, a.helperRegistry),
					entered:    make(chan struct{}), release: make(chan struct{}),
				}
				transport.WithPaneIntentSource(source)(a.Transport)
				defer close(source.release)
			}
			r := newBusyWireRenderer(t, a)
			type paneIdentity struct {
				SessionID   string `json:"sessionId"`
				AccessEpoch uint64 `json:"accessEpoch"`
			}
			open := func(id int) paneIdentity {
				t.Helper()
				if err := r.send(id, "open", map[string]any{"cols": 100, "rows": 30}); err != nil {
					t.Fatal(err)
				}
				var pane paneIdentity
				if err := json.Unmarshal(r.response(t, id), &pane); err != nil || pane.SessionID == "" || pane.AccessEpoch == 0 {
					t.Fatalf("open returned no controller identity: %+v (%v)", pane, err)
				}
				return pane
			}
			paneA, paneB := open(1), open(2)
			for _, pane := range []paneIdentity{paneA, paneB} {
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				_, err := a.Transport.AwaitIntegration(ctx, session.ID(pane.SessionID))
				cancel()
				if err != nil {
					t.Fatalf("waiting for shell startup: %v", err)
				}
			}
			r.mu.Lock()
			r.markerSID = paneB.SessionID
			r.mu.Unlock()
			if held {
				source.mu.Lock()
				source.target = paneA.SessionID
				source.mu.Unlock()
			}

			// Keep the Unix socket path short on macOS (sun_path is bounded).
			dir, dirErr := os.MkdirTemp("", "nocx-busy-")
			if dirErr != nil {
				t.Fatal(dirErr)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			socket := filepath.Join(dir, "events.sock")
			listener, listenErr := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if listenErr != nil {
				t.Fatal(listenErr)
			}
			t.Cleanup(func() { _ = listener.Close() })
			if err := listener.SetDeadline(time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			binary, executableErr := os.Executable()
			if executableErr != nil {
				t.Fatal(executableErr)
			}
			intent := func(id int, pane paneIdentity, kind, payload string) {
				t.Helper()
				if err := r.send(id, "session.intent", map[string]any{
					"sessionId": pane.SessionID, "accessEpoch": pane.AccessEpoch,
					"kind": kind, "payload": []byte(payload),
				}); err != nil {
					t.Fatal(err)
				}
			}
			intent(3, paneA, "text", busyWireCommand(binary, socket, "producer"))
			assertExecuted := func(id int) {
				t.Helper()
				var result struct {
					State string `json:"state"`
				}
				if err := json.Unmarshal(r.response(t, id), &result); err != nil || result.State != "executed" {
					t.Fatalf("intent %d did not execute: state=%q, err=%v", id, result.State, err)
				}
			}
			assertExecuted(3)
			producer, producerErr := listener.AcceptUnix()
			if producerErr != nil {
				t.Fatalf("A did not start its real producer: %v", producerErr)
			}
			defer func() { _ = producer.Close() }()
			if err := producer.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			producerControl := bufio.NewReader(producer)
			if ready, err := producerControl.ReadString('\n'); err != nil || ready != "producer\n" {
				t.Fatalf("producer start = %q, %v", ready, err)
			}
			// A cannot complete before this byte: its foreground program is
			// owned by this live connection, not by a duration or a guessed PID.
			released := false
			defer func() {
				if !released {
					_, _ = producer.Write([]byte{1})
				}
			}()
			intent(4, paneA, "focus", "out")
			if held {
				select {
				case <-source.entered:
				case err := <-r.errors:
					t.Fatal(err)
				case <-time.After(time.Minute):
					t.Fatal("A focus-out never reached the helper boundary")
				}
			}
			intent(5, paneB, "focus", "in")
			intent(6, paneB, "text", busyWireCommand(binary, socket, "reporter"))
			// The subprocess handshake proves real B execution, not merely an
			// intent receipt. Its marker is absent from the command's echo.
			reporter, reporterErr := listener.AcceptUnix()
			if reporterErr != nil {
				t.Fatalf("B did not execute while A was producing (held=%v): %v", held, reporterErr)
			}
			if err := reporter.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
				_ = reporter.Close()
				t.Fatal(err)
			}
			report, reportErr := bufio.NewReader(reporter).ReadString('\n')
			_ = reporter.Close()
			if reportErr != nil || report != "reporter\n" {
				t.Fatalf("B execution report = %q, %v", report, reportErr)
			}
			select {
			case <-r.marker:
			case err := <-r.errors:
				t.Fatalf("B output did not reach its renderer: %v", err)
			case <-time.After(time.Minute):
				t.Fatal("B executed but its output never reached the actual WebSocket")
			}
			assertExecuted(6)
			// A must answer after B's actual wire output and before release.
			// This handshake proves its process is still alive; a one-time
			// select on a newly started reader would prove no such thing.
			if _, err := producer.Write([]byte{'q'}); err != nil {
				t.Fatalf("asking whether A is still alive: %v", err)
			}
			if alive, err := producerControl.ReadString('\n'); err != nil || alive != "alive\n" {
				t.Fatalf("A did not survive until B's wire output: %q, %v", alive, err)
			}
			if _, err := producer.Write([]byte{1}); err != nil {
				t.Fatalf("releasing A: %v", err)
			}
			released = true
			if _, err := producerControl.ReadByte(); err != io.EOF {
				t.Fatalf("producer did not end cleanly after release: %v", err)
			}
			t.Logf("B execution and screen-frame output preceded producer release; held=%v", held)
		})
	}
}

// THE CROSS-TAB LATENCY CHECK AT THE WIRE (nocx-07ymy, GitHub #692).
//
// The owner's report — heavy activity in one tab, other tabs slow — is not
// measurable at one layer, so this file measures the one the wire owns: a real
// App, a real helper, two real shells and one real WebSocket. A runs a
// producer; B runs the operations a person performs (a keystroke, a command,
// an ordinary control call), and every number is the arrival of the thing a
// renderer would read.
//
// THE LAYERS ARE REPORTED SEPARATELY because "slow" does not say which:
//
//	ack    send -> the intent answer: the control lane, the helper and the PTY write
//	input  send -> the moment B's own foreground program READ the bytes
//	exec   send -> the moment it began answering (the program's own clock)
//	screen send -> the marker in a published screen row: what a renderer paints
//
// MEASURED 2026-11-05 on a 2-core / 4 GiB Linux VM, shipped log level (info):
// at A idle, B's command-to-screen was 8–10 ms and its keystroke-to-screen
// 6–7 ms. At A repainting a full 100x30 grid 100 times a second (340 KiB/s of
// screen frames) those became 17 ms and 13 ms; at an unpaced producer
// (700–780 KiB/s) 16–25 ms and 12–21 ms. An ordinary control call
// (`uistate.get`, which touches neither helper nor screen) did not move:
// 0.16–0.26 ms idle, 0.20–0.32 ms busy.
//
// SO THIS LAYER COUPLES, BUT BY A FACTOR OF ABOUT TWO AT A LOAD NO AGENT TAB
// PRODUCES, and part of that is the machine: A's ingest and the producer
// between them used 1.5–1.8 of the VM's 2 cores. The layer that made the
// owner's symptom a SYMPTOM was the renderer, and its check is
// e2e/cross-tab-latency.spec.ts with the deterministic unit test beside the
// pane (frontend/src/terminal-content.test.ts, nocx-07ymy).
//
// The premise that A actually produced is a floor of 10 screen frames a second,
// not the ~100 an ordinary build reaches: the producer is this test binary, so
// `go test -race` slows it too, and a floor above that would assert the runner.
//
// The ceiling is deliberately loose, and set by the regression rather than by
// today's numbers. It is not a performance target; it is the shape that must
// not come back — a shared worker that put one session's work in front of
// another's (nocx-5q0ej measured 7.5 s of it, ~750x). The 10x ceiling stays;
// every run prints the numbers so a trend is visible in the CI log.
//
// Both phases must measure the same work in B. The original idle phase began
// on a partly empty screen; by the busy phase B was scrolling and persisting
// departed rows. A Linux -race probe (nocx-nqmjq.6) measured command screens
// at 1.62 ms initially, 12.34 ms with A busy, and 5.36 ms after A stopped:
// stopping A did not restore the original baseline. Priming B through actual
// screen deliveries until it scrolls removes that confound, not the 10x bound.
// The historical VM measurements above remain historical; this probe does not
// establish the cause of every macOS failure.
package app

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

const (
	xtMarkerDone = "XT-DONE-"
	xtRows       = 30
)

// TestCrossTabChildProcess is the program the panes run, selected only by its
// own environment, which is scoped to the command, never to the test.
func TestCrossTabChildProcess(t *testing.T) {
	mode := os.Getenv("NOCX_XT_MODE")
	if mode == "" {
		return
	}
	conn, err := net.Dial("unix", os.Getenv("NOCX_XT_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	report := func(format string, args ...any) {
		if _, err := fmt.Fprintf(conn, format+"\n", args...); err != nil {
			t.Fatalf("reporting to the harness: %v", err)
		}
	}
	switch mode {
	case "worker":
		// The foreground program B runs: raw mode (its POSIX launcher execs
		// it under `stty raw -echo`), echoing every byte back as it arrives and
		// answering a completed line with one marker. It never forks, so what
		// is measured is the product's path and not a process spawn.
		report("ready %d", time.Now().UnixNano())
		stdin := bufio.NewReader(os.Stdin)
		var line []byte
		for {
			b, err := stdin.ReadByte()
			if err != nil {
				return
			}
			report("key %d", time.Now().UnixNano())
			if b == 0x15 { // Ctrl-U: the line editor's own "kill the line"
				line = line[:0]
				continue
			}
			if _, err := os.Stdout.Write([]byte{b}); err != nil {
				return
			}
			if b != '\r' && b != '\n' {
				line = append(line, b)
				continue
			}
			report("cmd %d", time.Now().UnixNano())
			if _, err := fmt.Fprintf(os.Stdout, "\r\n%s%s\r\n", xtMarkerDone, string(line)); err != nil {
				return
			}
			line = line[:0]
		}
	case "tui":
		// A realistic full-screen repaint: omp's shape and cadence (20 Hz,
		// e2e/fixtures/resize-borrow.mjs), a whole 100x30 grid per frame and
		// no scrolling.
		var b strings.Builder
		draw := func(n int) []byte {
			b.Reset()
			b.WriteString("\x1b[?2026h\x1b[H\x1b[2J")
			b.WriteString(fmt.Sprintf("OMP 18.10.4  turn %d  GLM-5.3 /work/nocx 1M\r\n", n))
			for row := 0; row < 26; row++ {
				b.WriteString(fmt.Sprintf("│ %-96s │\r\n", fmt.Sprintf("working line %d: internal/sessionruntime/runtime.go", row)))
			}
			b.WriteString("\x1b[30;1H⠸ thinking\x1b[?2026l")
			return []byte(b.String())
		}
		if _, err := io.WriteString(os.Stdout, "\x1b[?1049h"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stdout.Write(draw(0)); err != nil {
			t.Fatal(err)
		}
		report("tui")
		stop := make(chan struct{})
		go func() {
			defer close(stop)
			buf := make([]byte, 1)
			if _, rerr := conn.Read(buf); rerr == nil && buf[0] == 'q' {
				return
			}
		}()
		tick := time.NewTicker(50 * time.Millisecond)
		if hz := os.Getenv("NOCX_XT_HZ"); hz != "" {
			if n, perr := strconv.Atoi(hz); perr == nil && n > 0 {
				tick = time.NewTicker(time.Second / time.Duration(n))
			}
		}
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-stop:
				_, _ = io.WriteString(os.Stdout, "\x1b[?1049l")
				return
			case <-tick.C:
				if _, err := os.Stdout.Write(draw(n)); err != nil {
					t.Fatal(err)
				}
			}
		}
	case "producer":
		chunk := []byte("\x1b[H" + strings.Repeat("busy terminal output ", 800))
		if _, err := os.Stdout.Write(append([]byte("\x1b[?1049h"), chunk...)); err != nil {
			t.Fatal(err)
		}
		report("producer")
		stop := make(chan struct{})
		go func() {
			defer close(stop)
			buf := make([]byte, 1)
			if _, rerr := conn.Read(buf); rerr == nil && buf[0] == 'q' {
				return
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
	default:
		t.Fatalf("unknown NOCX_XT_MODE %q", mode)
	}
}

// ── the renderer ───────────────────────────────────────────────────────────

type xtEvent struct {
	at   time.Time
	kind string // "data" or "screen"
}

type xtResponse struct {
	at     time.Time
	id     int
	result json.RawMessage
	err    json.RawMessage
}

type xtPane struct {
	SessionID   string `json:"sessionId"`
	AccessEpoch uint64 `json:"accessEpoch"`
}

type xtMarker struct {
	sid  string
	ch   chan struct{}
	seen map[string]bool
}

type xtWire struct {
	t    *testing.T
	conn *websocket.Conn

	writeMu sync.Mutex
	nextID  atomic.Int64

	mu       sync.Mutex
	streams  map[string][]byte
	offsets  map[string]uint64
	markers  map[string]*xtMarker
	events   map[string][]xtEvent
	frames   map[string]int
	bytes    map[string]int
	dataN    map[string]int
	dataB    map[string]int
	baseRows map[string]uint64

	acks      chan map[string]any
	responses chan xtResponse
	errors    chan error
	done      chan struct{}
}

func newXTWire(t *testing.T, a *App) *xtWire {
	t.Helper()
	w := &xtWire{
		t: t, conn: dialRenderer(t, a),
		streams: map[string][]byte{}, offsets: map[string]uint64{},
		markers: map[string]*xtMarker{}, events: map[string][]xtEvent{},
		frames: map[string]int{}, bytes: map[string]int{},
		dataN: map[string]int{}, dataB: map[string]int{},
		baseRows: map[string]uint64{},
		acks:     make(chan map[string]any, 8192), responses: make(chan xtResponse, 8192),
		errors: make(chan error, 8), done: make(chan struct{}),
	}
	go w.read()
	go w.writeAcks()
	t.Cleanup(func() {
		_ = w.conn.Close()
		<-w.done
	})
	return w
}

func (w *xtWire) writeAcks() {
	for params := range w.acks {
		if err := w.send(0, "ack", params); err != nil {
			return
		}
	}
}

func (w *xtWire) read() {
	defer close(w.done)
	defer close(w.acks)
	for {
		kind, raw, err := w.conn.ReadMessage()
		if err != nil {
			w.errors <- err
			return
		}
		now := time.Now()
		switch kind {
		case websocket.TextMessage:
			var resp struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(raw, &resp); err == nil && resp.ID != 0 {
				w.responses <- xtResponse{at: now, id: resp.ID, result: resp.Result, err: resp.Error}
			}
		case websocket.BinaryMessage:
			frame, err := transport.DecodeFrame(raw)
			if err != nil {
				w.errors <- err
				return
			}
			sid := string(session.IDFromBytes(frame.SessionID))
			switch frame.MsgType {
			case transport.MsgTypeData:
				w.mu.Lock()
				w.streams[sid] = append(w.streams[sid], frame.Payload...)
				w.offsets[sid] += uint64(len(frame.Payload))
				off := w.offsets[sid]
				w.dataN[sid]++
				w.dataB[sid] += len(frame.Payload)
				hits := w.scanLocked(sid, w.streams[sid], "data")
				w.mu.Unlock()
				w.publish(hits, "data", now)
				w.acks <- map[string]any{"sessionId": sid, "offset": off}
			case transport.MsgTypeMetadata:
				var screen struct {
					State struct {
						BaseRow uint64 `json:"baseRow"`
					} `json:"state"`
					Rows []struct {
						Text string `json:"text"`
					} `json:"rows"`
				}
				if err := json.Unmarshal(frame.Payload, &screen); err != nil {
					w.errors <- fmt.Errorf("decode screen: %w", err)
					return
				}
				var b strings.Builder
				b.WriteByte('\n')
				for _, row := range screen.Rows {
					b.WriteString(row.Text)
					b.WriteByte('\n')
				}
				w.mu.Lock()
				w.frames[sid]++
				w.bytes[sid] += len(raw)
				w.baseRows[sid] = screen.State.BaseRow
				hits := w.scanLocked(sid, []byte(b.String()), "screen")
				w.mu.Unlock()
				w.publish(hits, "screen", now)
			}
		}
	}
}

// scanLocked reports the markers of sid whose bytes this arrival completes
// and this KIND has not recorded before. The byte buffer is cumulative, so a
// marker split across two frames is attributed to the frame that finished it.
func (w *xtWire) scanLocked(sid string, buf []byte, kind string) []string {
	var hits []string
	for marker, m := range w.markers {
		if m.sid != sid || m.seen[kind] {
			continue
		}
		needle := []byte(marker)
		if kind == "screen" && len(marker) == 1 {
			// A keystroke is echoed onto its own row. An old command's
			// "run" must not satisfy a new "r", "u" or "n" trial.
			needle = []byte("\n" + marker + "\n")
		}
		if bytes.Contains(buf, needle) {
			hits = append(hits, marker)
		}
	}
	return hits
}

func (w *xtWire) publish(hits []string, kind string, now time.Time) {
	if len(hits) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, marker := range hits {
		m := w.markers[marker]
		if m == nil || m.seen[kind] {
			continue
		}
		m.seen[kind] = true
		w.events[marker] = append(w.events[marker], xtEvent{at: now, kind: kind})
		if kind != "screen" {
			continue
		}
		select {
		case <-m.ch:
		default:
			close(m.ch)
		}
	}
}

func (w *xtWire) expect(sid, marker string) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan struct{})
	w.markers[marker] = &xtMarker{sid: sid, ch: ch, seen: map[string]bool{}}
	return ch
}

func (w *xtWire) baseRow(sid string) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.baseRows[sid]
}

func TestCrossTabMarkerWaitsForScreenDelivery(t *testing.T) {
	w := &xtWire{markers: map[string]*xtMarker{}, events: map[string][]xtEvent{}}
	const marker = "visible-command-result"
	ready := w.expect("pane", marker)
	w.publish([]string{marker}, "data", time.Now())
	select {
	case <-ready:
		t.Fatal("raw bytes completed a trial before its screen was delivered")
	default:
	}
	w.publish([]string{marker}, "screen", time.Now())
	select {
	case <-ready:
	default:
		t.Fatal("the delivered screen did not complete the trial")
	}
}

func TestCrossTabKeyMarkerRequiresItsOwnScreenRow(t *testing.T) {
	w := &xtWire{markers: map[string]*xtMarker{}}
	w.expect("pane", "r")
	if hits := w.scanLocked("pane", []byte("\nXT-DONE-run000001\n"), "screen"); len(hits) != 0 {
		t.Fatal("a letter in an old command satisfied a new keystroke")
	}
	if hits := w.scanLocked("pane", []byte("\nXT-DONE-run000001\nr\n"), "screen"); len(hits) != 1 {
		t.Fatal("the echoed keystroke on its own row was not observed")
	}
}

func (w *xtWire) forget(marker string) {
	w.mu.Lock()
	delete(w.markers, marker)
	delete(w.events, marker)
	w.mu.Unlock()
}

func (w *xtWire) send(id int, method string, params any) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != 0 {
		req["id"] = id
	}
	return w.conn.WriteJSON(req)
}

func (w *xtWire) call(method string, params any) (json.RawMessage, time.Duration) {
	w.t.Helper()
	id := int(w.nextID.Add(1))
	start := time.Now()
	if err := w.send(id, method, params); err != nil {
		w.t.Fatalf("send %s: %v", method, err)
	}
	for {
		select {
		case resp := <-w.responses:
			if resp.id != id {
				continue
			}
			if len(resp.err) != 0 && string(resp.err) != "null" {
				w.t.Fatalf("%s failed: %s", method, resp.err)
			}
			return resp.result, resp.at.Sub(start)
		case err := <-w.errors:
			w.t.Fatalf("renderer failed waiting for %s: %v", method, err)
		case <-time.After(time.Minute):
			w.t.Fatalf("%s did not answer", method)
		}
	}
}

func (w *xtWire) open() xtPane {
	w.t.Helper()
	raw, _ := w.call("open", map[string]any{"cols": 100, "rows": xtRows})
	var pane xtPane
	if err := json.Unmarshal(raw, &pane); err != nil || pane.SessionID == "" || pane.AccessEpoch == 0 {
		w.t.Fatalf("open returned no identity: %+v (%v)", pane, err)
	}
	return pane
}

// intent drops what arrival detection has been shown for the session, then
// sends one intent and waits for its answer, so each trial's marker can only
// be completed by bytes this trial caused.
func (w *xtWire) intent(pane xtPane, kind, payload string) (time.Duration, time.Time) {
	w.t.Helper()
	start := time.Now()
	w.mu.Lock()
	w.streams[pane.SessionID] = []byte{}
	w.mu.Unlock()
	_, ack := w.call("session.intent", map[string]any{
		"sessionId": pane.SessionID, "accessEpoch": pane.AccessEpoch, "kind": kind, "payload": []byte(payload),
	})
	return ack, start
}

// ── the measurement ────────────────────────────────────────────────────────

type xtSample struct {
	ack    time.Duration // send -> the intent answer (the control-plane lane + PTY write)
	input  time.Duration // send -> the foreground program read the bytes
	cmd    time.Duration // send -> the program started answering a command
	data   time.Duration // send -> the marker in the byte stream
	screen time.Duration // send -> the marker in a screen row
}

func xtCommand(binary, socket, mode, nonce string, rate int) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	cmd := fmt.Sprintf("env NOCX_XT_MODE=%s NOCX_XT_HZ=%d", mode, rate)
	if nonce != "" {
		cmd += " NOCX_XT_NONCE=" + quote(nonce)
	}
	cmd += " NOCX_XT_SOCKET=" + quote(socket) + " " + quote(binary) + " -test.run='^TestCrossTabChildProcess$'"
	// Ctrl-U first: a key trial leaves its letters on the line, and a command
	// appended to them is a command that never runs.
	return "\x15" + cmd + "\n"
}

// xtCPU totals CPU seconds of every process whose executable contains one of
// the needles, read from /proc (this measurement runs on Linux).
func xtCPU(needles ...string) map[string]float64 {
	out := map[string]float64{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	const tick = 1.0 / 100.0 // USER_HZ
	for _, e := range entries {
		if _, err := fmt.Sscanf(e.Name(), "%d", new(int)); err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil {
			continue
		}
		matched := ""
		for _, needle := range needles {
			if strings.Contains(exe, needle) {
				matched = needle
			}
		}
		if matched == "" {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
		if len(fields) < 15 {
			continue
		}
		utime, errU := strconv.ParseFloat(fields[11], 64)
		stime, errS := strconv.ParseFloat(fields[12], 64)
		if errU != nil || errS != nil {
			continue
		}
		out[matched] += (utime + stime) * tick
	}
	return out
}

// xtSelfCPU is this process's own user+system CPU seconds.
func xtSelfCPU() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6 +
		float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
}

func xtPercentile(samples []time.Duration, fraction float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted[int(float64(len(sorted)-1)*fraction)]
}

func xtP(prefix string, samples []time.Duration) string {
	return fmt.Sprintf("%s p50=%v p95=%v", prefix, xtPercentile(samples, .5), xtPercentile(samples, .95))
}

func TestCrossTabLatencyAtTheWire(t *testing.T) {
	// The load: a whole 100x30 grid repainted 100 times a second — an agent
	// streaming output, not an unpaced flood.
	workload := "tui"
	rounds, hz := 24, 100
	a := newLocalPaneApp(t)
	w := newXTWire(t, a)
	paneA := w.open()
	paneB := w.open()
	for _, pane := range []xtPane{paneA, paneB} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		_, err := a.Transport.AwaitIntegration(ctx, session.ID(pane.SessionID))
		cancel()
		if err != nil {
			t.Fatalf("waiting for shell startup: %v", err)
		}
	}

	dir, err := os.MkdirTemp("", "nocx-xt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "events.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	type reading struct {
		kind string
		ns   int64
	}
	readings := make(chan reading, 8192)
	workerReady := make(chan struct{}, 1)
	producerReady := make(chan net.Conn, 1)
	go func() {
		for {
			conn, aerr := listener.AcceptUnix()
			if aerr != nil {
				return
			}
			go func(conn *net.UnixConn) {
				// The worker and the producers keep this connection for their
				// whole life; a reading-only child would close it. All three
				// streams are line-oriented, so one reader serves each.
				r := bufio.NewReader(conn)
				first := true
				for {
					line, rerr := r.ReadString('\n')
					if rerr != nil {
						_ = conn.Close()
						return
					}
					line = strings.TrimSuffix(line, "\n")
					switch {
					case strings.HasPrefix(line, "ready "):
						if first {
							workerReady <- struct{}{}
							first = false
						}
						if ns, perr := strconv.ParseInt(strings.TrimPrefix(line, "ready "), 10, 64); perr == nil {
							readings <- reading{"ready", ns}
						}
					case strings.HasPrefix(line, "key "):
						if ns, perr := strconv.ParseInt(strings.TrimPrefix(line, "key "), 10, 64); perr == nil {
							readings <- reading{"key", ns}
						}
					case strings.HasPrefix(line, "cmd "):
						if ns, perr := strconv.ParseInt(strings.TrimPrefix(line, "cmd "), 10, 64); perr == nil {
							readings <- reading{"cmd", ns}
						}
					case line == "tui" || line == "producer":
						producerReady <- conn
					}
				}
			}(conn)
		}
	}()

	waitMarker := func(ch chan struct{}, what string) {
		select {
		case <-ch:
		case err := <-w.errors:
			t.Fatalf("%s: renderer failed: %v", what, err)
		case <-time.After(15 * time.Second):
			w.mu.Lock()
			dump := string(w.streams[paneB.SessionID])
			w.mu.Unlock()
			t.Fatalf("%s: marker never arrived; B stream=%q", what, dump)
		}
	}
	// one keystroke: one byte into the raw-mode worker, echoed back.
	keyTrial := func(n int) xtSample {
		marker := string(rune('a' + n%26))
		ch := w.expect(paneB.SessionID, marker)
		ack, start := w.intent(paneB, "text", marker)
		waitMarker(ch, "key "+marker)
		s := xtSample{ack: ack}
		select {
		case r := <-readings:
			if r.kind == "key" {
				s.input = time.Unix(0, r.ns).Sub(start)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("worker did not report the keystroke")
		}
		w.mu.Lock()
		for _, ev := range w.events[marker] {
			switch ev.kind {
			case "data":
				s.data = ev.at.Sub(start)
			case "screen":
				s.screen = ev.at.Sub(start)
			}
		}
		if os.Getenv("NOCX_XT_DUMP") != "" {
			t.Logf("DUMP key %q data=%v screen=%v stream=%q", marker, s.data, s.screen, string(w.streams[paneB.SessionID]))
		}
		w.mu.Unlock()
		w.forget(marker)
		return s
	}

	// one command: a line the worker answers with a marker, no process spawn.
	cmdTrial := func(n int) xtSample {
		token := fmt.Sprintf("run%06d", n)
		marker := xtMarkerDone + token
		ch := w.expect(paneB.SessionID, marker)
		ack, start := w.intent(paneB, "text", "\x15"+token+"\r")
		waitMarker(ch, "cmd "+token)
		s := xtSample{ack: ack}
		deadline := time.After(10 * time.Second)
		for s.cmd == 0 {
			select {
			case r := <-readings:
				switch r.kind {
				case "key":
					s.input = time.Unix(0, r.ns).Sub(start)
				case "cmd":
					s.cmd = time.Unix(0, r.ns).Sub(start)
				}
			case <-deadline:
				t.Fatalf("worker did not report the command")
			}
		}
		w.mu.Lock()
		for _, ev := range w.events[marker] {
			switch ev.kind {
			case "data":
				s.data = ev.at.Sub(start)
			case "screen":
				s.screen = ev.at.Sub(start)
			}
		}
		w.mu.Unlock()
		w.forget(marker)
		return s
	}

	control := func() time.Duration {
		_, dur := w.call("uistate.get", map[string]any{})
		return dur
	}

	type phase struct {
		name    string
		keys    []xtSample
		cmds    []xtSample
		control []time.Duration
	}
	runPhase := func(name string, base int) *phase {
		p := &phase{name: name}
		firstRow := w.baseRow(paneB.SessionID)
		for i := 0; i < rounds; i++ {
			p.keys = append(p.keys, keyTrial(i))
			before := w.baseRow(paneB.SessionID)
			p.cmds = append(p.cmds, cmdTrial(base+i))
			if after := w.baseRow(paneB.SessionID); after <= before {
				t.Fatalf("[%s] command %d did not scroll B: base row %d -> %d; idle and busy must measure the same work",
					name, i, before, after)
			}
			p.control = append(p.control, control())
		}
		t.Logf("[%s] all %d commands scrolled B: base row %d -> %d", name, rounds,
			firstRow, w.baseRow(paneB.SessionID))
		return p
	}
	drain := func() {
		// The worker reports the line terminator too; nothing it wrote after a
		// marker is left for the next trial to misread.
		for {
			select {
			case <-readings:
			default:
				return
			}
		}
	}

	report := func(p *phase) (cmdScreen, keyScreen []time.Duration) {
		var keyAck, keyInput, keyData []time.Duration
		for _, sample := range p.keys {
			keyAck = append(keyAck, sample.ack)
			keyInput = append(keyInput, sample.input)
			keyData = append(keyData, sample.data)
			keyScreen = append(keyScreen, sample.screen)
		}
		t.Logf("[%s] key   %s | %s | %s | %s", p.name,
			xtP("ack", keyAck), xtP("input", keyInput), xtP("data", keyData), xtP("screen", keyScreen))
		var cmdAck, cmdInput, cmdExec, cmdData []time.Duration
		for _, sample := range p.cmds {
			cmdAck = append(cmdAck, sample.ack)
			cmdInput = append(cmdInput, sample.input)
			cmdExec = append(cmdExec, sample.cmd)
			cmdData = append(cmdData, sample.data)
			cmdScreen = append(cmdScreen, sample.screen)
		}
		t.Logf("[%s] cmd   %s | %s | %s | %s | %s", p.name,
			xtP("ack", cmdAck), xtP("input", cmdInput), xtP("exec", cmdExec), xtP("data", cmdData), xtP("screen", cmdScreen))
		t.Logf("[%s] control %s n=%d", p.name, xtP("rpc", p.control), len(p.control))
		return cmdScreen, keyScreen
	}

	// B's POSIX launcher owns raw mode until exec. Setting it in Fish before
	// a separate exec lets the interactive shell restore cooked tty settings
	// between jobs, leaving the worker waiting for a line instead of a byte.
	worker := posixPaneCommand("stty raw -echo; exec env NOCX_XT_MODE=worker NOCX_XT_SOCKET=" +
		shellQuoteForSh(socket) + " " + shellQuoteForSh(binary) + " -test.run='^TestCrossTabChildProcess$'")
	_, _ = w.intent(paneB, "text", "\x15"+worker+"\n")
	select {
	case <-workerReady:
	case <-time.After(time.Minute):
		t.Fatalf("B's worker did not start")
	}
	time.Sleep(300 * time.Millisecond)
	drain()

	// Each command advances at least one row. Bound preparation by the
	// viewport's height, but stop only when a delivered screen proves B
	// has scrolled. No timing estimate or fixed warm-up count decides this.
	scrolled := false
	for i := 1; i <= xtRows; i++ {
		before := w.baseRow(paneB.SessionID)
		cmdTrial(-i)
		if w.baseRow(paneB.SessionID) > before {
			scrolled = true
			t.Logf("B reached scrolling after %d preparation commands", i)
			break
		}
	}
	if !scrolled {
		t.Fatal("B did not scroll after a viewport of commands")
	}
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		t.Fatalf("this stand's home: %v", homeErr)
	}
	selfIdle0 := xtSelfCPU()
	cpuIdle0 := xtCPU(filepath.Join(home, ".nocx", "helper"), binary)
	idleStart := time.Now()
	idle := runPhase("idle", 0)
	idleDur := time.Since(idleStart)
	drain()
	selfIdle1 := xtSelfCPU()
	cpuIdle1 := xtCPU(filepath.Join(home, ".nocx", "helper"), binary)

	mode := "tui"
	if workload != "tui" {
		mode = "producer"
	}
	_, _ = w.intent(paneA, "text", xtCommand(binary, socket, mode, "", hz))
	var producer net.Conn
	select {
	case producer = <-producerReady:
	case <-time.After(time.Minute):
		t.Fatalf("A's producer did not start")
	}
	time.Sleep(2 * time.Second)
	w.mu.Lock()
	f0, b0 := w.frames[paneA.SessionID], w.bytes[paneA.SessionID]
	d0, n0 := w.dataB[paneA.SessionID], w.dataN[paneA.SessionID]
	w.mu.Unlock()
	selfBusy0 := xtSelfCPU()
	cpuBusyBefore := xtCPU(filepath.Join(home, ".nocx", "helper"), binary)
	busyStart := time.Now()
	busy := runPhase("busy", 500)
	busyDur := time.Since(busyStart)
	selfBusy1 := xtSelfCPU()
	cpuBusy := xtCPU(filepath.Join(home, ".nocx", "helper"), binary)
	w.mu.Lock()
	f1, b1 := w.frames[paneA.SessionID], w.bytes[paneA.SessionID]
	d1, n1 := w.dataB[paneA.SessionID], w.dataN[paneA.SessionID]
	w.mu.Unlock()
	t.Logf("A during busy: screens %d frames/%.1f KiB (%.0f/s, %.0f KiB/s), bytes %d frames/%.1f KiB (%.0f/s, %.0f KiB/s)",
		f1-f0, float64(b1-b0)/1024, float64(f1-f0)/busyDur.Seconds(), float64(b1-b0)/1024/busyDur.Seconds(),
		n1-n0, float64(d1-d0)/1024, float64(n1-n0)/busyDur.Seconds(), float64(d1-d0)/1024/busyDur.Seconds())
	if producer != nil {
		_, _ = producer.Write([]byte{'q'})
		_ = producer.Close()
	}
	time.Sleep(500 * time.Millisecond)
	drain()

	idleCmd, idleKey := report(idle)
	busyCmd, busyKey := report(busy)

	// THE PREMISE: a fixture that produced nothing proves nothing. The floor
	// is low ON PURPOSE, and set by what a -race build can do rather than by
	// what an ordinary one does: the producer is this same test binary, so
	// under `go test -race` (which is how CI repeats a changed test) it is
	// instrumented too, and on a 2-core VM its output fell from ~100 to ~43
	// screen frames a second. An earlier floor of 50 failed on exactly that
	// and asserted the runner rather than the product. Ten frames a second
	// still proves the producer ran end to end; the rate is printed so a log
	// shows how hard the check was driven.
	busyFramesPerSecond := float64(f1-f0) / busyDur.Seconds()
	if busyFramesPerSecond < 10 {
		t.Fatalf("A produced %.0f screen frames a second; the bound below would prove nothing", busyFramesPerSecond)
	}

	// THE CEILING IS SET BY THE REGRESSION THIS EXISTS TO CATCH, not by what
	// the product does today. Every run prints what it measured; the
	// assertion is only that B did not WAIT FOR A. nocx-5q0ej, #534, was one
	// shared worker that put A's work in front of B's: a command in B waited
	// 7.5 s for what takes 10 ms, a factor of ~750. The ceiling is 10x this
	// run's own idle phase, with B demonstrably scrolling in BOTH phases.
	// The ratio must not compare drawing into unused rows with persisting
	// departed rows, nor treat raw-byte arrival as visible screen delivery.
	// Hidden-pane renderer coalescing is checked separately beside the pane
	// (frontend/src/terminal-content.test.ts, nocx-07ymy).
	const bound = 10.0
	for _, layer := range []struct {
		name          string
		idle, latency []time.Duration
	}{
		{"command to screen", idleCmd, busyCmd},
		{"keystroke to screen", idleKey, busyKey},
	} {
		was, now := xtPercentile(layer.idle, .5), xtPercentile(layer.latency, .5)
		t.Logf("%s: idle p50=%v, A busy p50=%v (%.2fx)", layer.name, was, now,
			float64(now)/float64(was))
		if now > time.Duration(float64(was)*bound) {
			t.Errorf("%s followed A's load: %v idle, %v while A repaints at %.0f frames/s (bound %.1fx)",
				layer.name, was, now, busyFramesPerSecond, bound)
		}
	}
	delta := func(a, b map[string]float64, key string) float64 { return b[key] - a[key] }
	helper, children := filepath.Join(home, ".nocx", "helper"), binary
	t.Logf("CPU idle (%.1fs): helper %.2fs (%.2f cores), B/children %.2fs (%.2f cores), coordinator+harness %.2fs (%.2f cores)",
		idleDur.Seconds(), delta(cpuIdle0, cpuIdle1, helper), delta(cpuIdle0, cpuIdle1, helper)/idleDur.Seconds(),
		delta(cpuIdle0, cpuIdle1, children), delta(cpuIdle0, cpuIdle1, children)/idleDur.Seconds(),
		selfIdle1-selfIdle0, (selfIdle1-selfIdle0)/idleDur.Seconds())
	t.Logf("CPU busy (%.1fs): helper %.2fs (%.2f cores), A+B children %.2fs (%.2f cores), coordinator+harness %.2fs (%.2f cores)",
		busyDur.Seconds(), delta(cpuBusyBefore, cpuBusy, helper), delta(cpuBusyBefore, cpuBusy, helper)/busyDur.Seconds(),
		delta(cpuBusyBefore, cpuBusy, children), delta(cpuBusyBefore, cpuBusy, children)/busyDur.Seconds(),
		selfBusy1-selfBusy0, (selfBusy1-selfBusy0)/busyDur.Seconds())
	// UNDER THE LOCK THE READER WRITES THEM WITH. The connection is still
	// open here — the reader goroutine keeps counting while this line runs —
	// so reading these four maps bare is a data race (the race detector found
	// this one first in CI).
	w.mu.Lock()
	bDataN, bDataB := w.dataN[paneB.SessionID], w.dataB[paneB.SessionID]
	bFrames, bBytes := w.frames[paneB.SessionID], w.bytes[paneB.SessionID]
	w.mu.Unlock()
	t.Logf("B received: %d data frames/%d bytes, %d screen frames/%d bytes",
		bDataN, bDataB, bFrames, bBytes)
}

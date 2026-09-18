// Command s14mock is nocx-6q1uh.14's real-helper mock agent: a program named
// "claude" that a worker pane's real shell finds on PATH, standing in for a
// real Claude Code session the way worker_orchestration_task_test.go's own
// stub already does for enrolment alone (AGENTS.md's own framing: "the agent
// is the thing being orchestrated, not the orchestration").
//
// # What this program is, and is not
//
// It is not a terminal-protocol emulator of Claude: it never interprets the
// bytes it receives as a VT100 program would. What it does is narrower and
// exactly what session_surface_realhelper_test.go needs: on cue, it repaints
// its own stdout with the VERIFIED bytes internal/agentcapture.Paint produces
// from one of three real, already-trusted corpus frames (claude-idle,
// claude-permission, claude-working — the SAME captures and marks
// session_surface_happypath_test.go's s14FakeHelper replays), so the REAL
// terminal emulator inside the REAL helper daemon this test spawns reconstructs
// that exact frame — the round trip
// TestPaintingAndReplayingAFrameDoesNotMoveTheVerdict already proves sound,
// and newHappyVerifiedClaudeCalibration already relies on for the shipped
// claude rule's own calibration. It reacts to two things a real Claude's own
// pane would show reacting to: plain text arriving on its stdin (a
// session.keys/session.message "text" intent, which sessionruntime encodes
// verbatim — nocx-6q1uh's own runtime.go — so this is never bracketed-paste
// wrapped) is echoed into the input box exactly where a real Claude's cursor
// sits; a bare Enter (a "key" intent) closes the box or, while a menu is
// showing, hands off to the working frame the way answering a permission
// question really does. Every other key this test's own flow can produce
// (an arrow, an unrecognised control sequence) is logged and otherwise
// ignored, because sendOption's own loop (session_keys.go) never needs to
// move this menu's selection when the caller asks for the option already
// under the cursor — which is what session_surface_realhelper_test.go always
// does, for the reason its own doc explains.
//
// # The two side channels
//
// Real stdin only carries what the real helper's real runtime actually
// writes to the real PTY — there is no way for a test to also tell this
// program "now show the menu" over that same channel without it being a key
// or text intent a real Claude could have received. So a phase change (the
// model deciding to show a permission menu, or finishing its turn) arrives
// over a plain file the test overwrites and this program polls: the SAME
// explicit, test-driven role session_surface_happypath_test.go's own
// s14FakeHelper.setPhase plays for its fake, moved out of process because
// there is no in-process struct to call a method on any more.
//
// Every byte actually read from stdin is appended to a second file verbatim,
// so the test can assert on what production really wrote to a real PTY
// (AGENTS.md: never on durations) rather than on a count this program kept
// of itself.
//
// # Calling tools, and the third side channel
//
// An agent's own act is a tool call, and a mock that could not make one could
// not stand in for an agent at all — a worker's report (ADR-0070 decision 1)
// is one, and so is everything a coordinator does. So a third file carries a
// CALL ON CUE — `{"tool":"workers.report","args":{"kind":"done","text":"…"}}`
// — and this program makes it the way a real agent makes it: it starts the MCP
// bridge this repository ships (`<helper> mcp --socket <socket>`, exactly the
// invocation the shell integration stages into the agent's mcp.json) as its
// own CHILD, so the endpoint admits the call as the pane's own session by the
// process tree it runs in, and the call travels over the ordinary endpoint
// with the ordinary authorizer in front of it. Nothing here writes a mailbox
// or a record: the call is a real one and its answer is written beside the cue.
//
// The two paths the bridge needs are non-secret and arrive in a fourth file
// the stand writes ({"helper": …, "socket": …}) — a local caller presents no
// bearer (ADR-0058), which is why there is no token anywhere in this program.
//
// The `exit` cue is the last one: a pane whose process ends. The stand spawns
// that worker with `claude; exit`, so returning from run() ends the agent and
// then the pane's own shell — and the record learns the exit the ordinary way,
// through the supervisor watching the session.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentcapture/replaylocal"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/paneview"
)

// phase is which of the three verified corpus moments this pane currently
// shows — s14Phase's own vocabulary in session_surface_happypath_test.go,
// reused here as plain strings because the cue channel is a text file a test
// writes by hand.
type phase string

const (
	phaseIdle    phase = "idle"
	phaseMenu    phase = "menu"
	phaseWorking phase = "working"
)

// captureMoment names one real corpus capture and the mark
// session_surface_happypath_test.go's own happyReplayCapture already
// replays it at — no new claim about the corpus is made here.
type captureMoment struct {
	name string
	atMs int64
}

var moments = map[phase]captureMoment{
	phaseIdle:    {"claude-idle", 11000},
	phaseMenu:    {"claude-permission", 49000},
	phaseWorking: {"claude-working", 17000},
}

// exitCue is the one cue that is not a frame: a worker whose process ends.
// It is a cue value rather than a fifth phase because it is not something a
// pane SHOWS — it is the pane ceasing to exist.
const exitCue = "exit"

// mcpConfig is what the stand writes for the panes it spawns: the two
// non-secret paths the shipped MCP bridge is started with. A local caller
// presents no bearer (ADR-0058 — the endpoint admits it by the process tree),
// so there is no token here and none anywhere in this program.
type mcpConfig struct {
	Helper string `json:"helper"`
	Socket string `json:"socket"`
}

// callRequest is one call cue: which tool to call, and with what arguments.
// The arguments are passed through verbatim, so a journey writes exactly what
// the endpoint will see — including the ids it names.
type callRequest struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "s14mock:", err)
		os.Exit(1)
	}
}

func run() error {
	capturesDir := os.Getenv("S14_CAPTURES_DIR")
	if capturesDir == "" {
		return fmt.Errorf("S14_CAPTURES_DIR is not set")
	}
	stateDir := os.Getenv("S14_STATE_DIR")
	if stateDir == "" {
		return fmt.Errorf("S14_STATE_DIR is not set")
	}
	sessionID := os.Getenv("NOCX_SESSION_ID")
	if sessionID == "" {
		// Defensive only: every worker pane this test spawns is enrolled by
		// the real shell integration, which always exports this (AD-7). A
		// pane that somehow has none still gets a private, stable file pair
		// rather than colliding with another pane's.
		sessionID = fmt.Sprintf("pid-%d", os.Getpid())
	}

	restore, rawErr := setRawMode(int(os.Stdin.Fd()))
	if rawErr == nil && restore != nil {
		defer func() { _ = restoreMode(int(os.Stdin.Fd()), restore) }()
	}
	// A raw-mode failure (stdin is not a tty) is not fatal: the ordinary case
	// while this binary is exercised directly by hand.

	cueDir := filepath.Join(stateDir, "cue")
	logDir := filepath.Join(stateDir, "log")
	callDir := filepath.Join(stateDir, "call")
	callLogDir := filepath.Join(stateDir, "calllog")
	for _, dir := range []string{cueDir, logDir, callDir, callLogDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	cueFile := filepath.Join(cueDir, sessionID)
	logFile := filepath.Join(logDir, sessionID)
	callFile := filepath.Join(callDir, sessionID)
	callLogFile := filepath.Join(callLogDir, sessionID)
	mcpConfigFile := filepath.Join(stateDir, "mcp.json")

	m, err := newMock(capturesDir)
	if err != nil {
		return err
	}

	logHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- this test's own state directory
	if err != nil {
		return err
	}
	defer func() { _ = logHandle.Close() }()

	callLog, err := os.OpenFile(callLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- this test's own state directory
	if err != nil {
		return err
	}
	defer func() { _ = callLog.Close() }()

	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

	m.setPhase(phaseIdle)
	if _, err := out.Write(m.paintCurrent()); err != nil {
		return err
	}
	if err := out.Flush(); err != nil {
		return err
	}

	byteCh := make(chan byte, 4096)
	go func() {
		defer close(byteCh)
		buf := make([]byte, 1)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				byteCh <- buf[0]
			}
			if rerr != nil {
				return
			}
		}
	}()

	cueTicker := time.NewTicker(15 * time.Millisecond)
	defer cueTicker.Stop()
	callTicker := time.NewTicker(15 * time.Millisecond)
	defer callTicker.Stop()
	var lastCue string
	initialCue, cueErr := os.ReadFile(cueFile) // #nosec G304 -- this test's own state directory
	if cueErr == nil {
		lastCue = strings.TrimSpace(string(initialCue))
	}
	var lastCall string

	var chunk []byte
	flushTimer := time.NewTimer(time.Hour)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	flushArmed := false

	flush := func() {
		if len(chunk) == 0 {
			return
		}
		next := m.handleChunk(chunk)
		chunk = nil
		if next != nil {
			_, _ = out.Write(next)
			_ = out.Flush()
		}
	}

	for {
		select {
		case b, ok := <-byteCh:
			if !ok {
				flush()
				return nil
			}
			if _, werr := logHandle.Write([]byte{b}); werr != nil {
				return werr
			}
			chunk = append(chunk, b)
			if !flushArmed {
				flushArmed = true
			}
			flushTimer.Reset(15 * time.Millisecond)
		case <-flushTimer.C:
			flushArmed = false
			flush()
		case <-cueTicker.C:
			b, rerr := os.ReadFile(cueFile) // #nosec G304 -- this test's own state directory
			if rerr != nil {
				continue
			}
			cue := strings.TrimSpace(string(b))
			if cue == "" || cue == lastCue {
				continue
			}
			if cue == exitCue {
				// THE PANE'S OWN PROCESS ENDING. The stand spawns this
				// worker's pane with `claude; exit`, so returning from here
				// ends the shell too: the pty goes with it and the record
				// learns the exit the ordinary way, through the supervisor
				// watching the session.
				flush()
				return nil
			}
			lastCue = cue
			m.setPhase(phase(cue))
			if _, werr := out.Write(m.paintCurrent()); werr != nil {
				return werr
			}
			if werr := out.Flush(); werr != nil {
				return werr
			}
		case <-callTicker.C:
			b, rerr := os.ReadFile(callFile) // #nosec G304 -- this test's own state directory
			if rerr != nil {
				continue
			}
			raw := strings.TrimSpace(string(b))
			if raw == "" || raw == lastCall {
				continue
			}
			lastCall = raw
			outcome := m.callTool(raw, mcpConfigFile)
			if _, werr := callLog.WriteString(outcome + "\n"); werr != nil {
				return werr
			}
			if serr := callLog.Sync(); serr != nil {
				return serr
			}
		}
	}
}

// mock holds the three verified frames and this pane's own live state: which
// one is showing, and what text (if any) is echoed into its input box right
// now. It is the same shape session_surface_happypath_test.go's own
// s14Session plays for the fake helper, moved into a real program that
// renders real bytes instead of returning a Go struct over a fake RPC.
type mock struct {
	mu    sync.Mutex
	rules *agentdriver.Registry
	base  map[phase]paneview.Frame
	cur   phase
	echo  string
}

func newMock(capturesDir string) (*mock, error) {
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		return nil, fmt.Errorf("agent driver registry: %w", err)
	}
	base := make(map[phase]paneview.Frame, len(moments))
	for p, mo := range moments {
		frame, ferr := replayCapture(capturesDir, mo.name, mo.atMs)
		if ferr != nil {
			return nil, ferr
		}
		base[p] = frame
	}
	return &mock{rules: rules, base: base, cur: phaseIdle}, nil
}

func replayCapture(capturesDir, name string, atMs int64) (paneview.Frame, error) {
	path := filepath.Join(capturesDir, name+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		return paneview.Frame{}, fmt.Errorf("read capture %s: %w", name, err)
	}
	moments, err := agentcapture.Frames(context.Background(), replaylocal.Replayer{}, header, chunks, []int64{atMs})
	if err != nil {
		return paneview.Frame{}, fmt.Errorf("replay capture %s at %dms: %w", name, atMs, err)
	}
	return moments[0].Frame, nil
}

// setPhase moves to a fresh cue: the box's own pending echo never survives a
// phase change, exactly as s14FakeHelper.setPhase's own doc explains (a real
// Claude does not keep showing your half-typed text once it has moved on).
func (m *mock) setPhase(p phase) {
	if _, ok := m.base[p]; !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = p
	m.echo = ""
}

// paintCurrent renders this pane's own current frame — its base capture,
// overlaid with any pending echo — as the bytes that reproduce it.
func (m *mock) paintCurrent() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paintLocked()
}

// paintLocked renders this pane's own current frame — its base capture, with
// any pending echo shown in a box grown to hold it — as the bytes that
// reproduce it. It is a full repaint (agentcapture.Paint erases first), so a
// box that grew between two paints does not leave rows of the smaller one
// behind.
func (m *mock) paintLocked() []byte {
	frame := m.base[m.cur]
	if m.echo != "" {
		frame = grownInputBox(m.rules, frame, m.echo)
	}
	return agentcapture.Paint(frame)
}

// callTool makes one real tools/call on cue and answers one line saying what
// came back: the tool result for the test to read, or the failure that stopped
// it. Nothing is retried and nothing is judged here — a mock that silently
// retried would hide exactly the failure a test is looking for, and this
// program is the only agent in a worker's or a coordinator's pane, so every
// call a journey needs is written here as a cue.
func (m *mock) callTool(raw, configPath string) string {
	var req callRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return fmt.Sprintf("CUE-MALFORMED %v", err)
	}
	if req.Tool == "" {
		return "CUE-WITHOUT-TOOL"
	}
	cfgRaw, err := os.ReadFile(configPath) // #nosec G304 -- this test's own state directory
	if err != nil {
		return fmt.Sprintf("NO-MCP-CONFIG %v", err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return fmt.Sprintf("MCP-CONFIG-MALFORMED %v", err)
	}
	// tools/list is the one MCP method that is not a tools/call, and it is
	// here because discovering which tools a pane's agent has IS a call an
	// agent makes: the bridge answers it from the endpoint's own catalogue.
	if req.Tool == "tools/list" {
		resp, err := callMCPMethod(cfg, "tools/list", nil)
		if err != nil {
			return fmt.Sprintf("CALL-FAILED %v", err)
		}
		return resp
	}
	args := req.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	resp, err := callMCPMethod(cfg, "tools/call", map[string]any{"name": req.Tool, "arguments": args})
	if err != nil {
		return fmt.Sprintf("CALL-FAILED %v", err)
	}
	return resp
}

// callMCPMethod starts the shipped MCP bridge as this process's own child and
// makes one request over it: `initialize` first, because the bridge negotiates
// its protocol version from that request and the version decides the shape of
// the result, then the request itself. One JSON object per line is the
// bridge's whole framing (internal/mcpstdio).
//
// THE CHILD IS NECESSARY AND NOT CONVENIENCE. The endpoint admits a local
// caller by the process tree it runs in, so a call made from THIS process —
// a descendant of the pane's own shell — is a call made as the pane's session
// and can name no other.
//
// The bridge's stderr is discarded rather than inherited: on a pty this
// program's stderr is the pane, and a diagnostic line painted into the frame
// the sweep classifies would be this mock corrupting its own screen. A
// failure the caller must act on arrives as a JSON-RPC error on stdout, which
// is the wire.
func callMCPMethod(cfg mcpConfig, method string, params any) (string, error) {
	if cfg.Helper == "" || cfg.Socket == "" {
		return "", fmt.Errorf("the mcp config names no helper or socket: %+v", cfg)
	}
	cmd := exec.Command(cfg.Helper, "mcp", "--socket", cfg.Socket) // #nosec G204 -- this test's own config file
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	recordTree(cfg, cmd.Process.Pid)
	lines := bufio.NewReader(stdout)
	if err := writeMCPLine(stdin, map[string]any{
		"jsonrpc": "2.0", "id": "m-init", "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-11-25"},
	}); err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	if _, err := readMCPLine(lines); err != nil {
		return "", fmt.Errorf("the bridge never answered initialize: %w", err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": "m-call", "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := writeMCPLine(stdin, request); err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	line, err := readMCPLine(lines)
	if err != nil {
		return "", fmt.Errorf("the bridge never answered %s: %w", method, err)
	}
	return line, nil
}

// recordTree writes the connecting process's own ancestry where a diagnosis
// can read it. It is diagnostic only: the endpoint admits a local caller by
// walking this chain from the peer's pid to a pane's recorded root, so when a
// report is refused with "not in a pane nocx has enrolled", which tree the
// caller was actually in is the first and often only question.
func recordTree(cfg mcpConfig, pid int) {
	dir := os.Getenv("S14_STATE_DIR")
	if dir == "" {
		return
	}
	var b strings.Builder
	for cur := pid; cur > 1; {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cur)) // #nosec G304 -- our own process tree
		if err != nil {
			break
		}
		fields := strings.Fields(string(raw))
		name, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", cur)) // #nosec G304 -- our own process tree
		fmt.Fprintf(&b, "%d:%s ", cur, strings.TrimSpace(string(name)))
		if len(fields) < 4 {
			break
		}
		next, err := strconv.Atoi(fields[3])
		if err != nil || next == cur {
			break
		}
		cur = next
	}
	out := filepath.Join(dir, "tree")
	if err := os.MkdirAll(out, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(out, "bridge.txt"), []byte(b.String()), 0o600)
}

func writeMCPLine(w io.Writer, msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// readMCPLine reads one newline-delimited message, and refuses a line the
// bridge did not terminate rather than guessing where it ended.
func readMCPLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\n"), nil
}

// handleChunk is one flushed run of stdin bytes — everything that arrived
// without a 15ms gap, which is generous beside the single write() each of
// production's own paste/key intents makes (session_keys.go's commitStep,
// one Intent call, one PTY write). It returns the bytes to repaint with, or
// nil when nothing on screen needs to change.
func (m *mock) handleChunk(chunk []byte) []byte {
	if len(chunk) == 0 {
		return nil
	}
	if isEnter(chunk) {
		return m.onEnter()
	}
	if containsEscape(chunk) {
		// An arrow key or another control sequence this test's own flow
		// never needs to move (sendOption never sends one when the caller
		// asks for the option already under the cursor — see the file doc).
		// Logged already; nothing to repaint.
		return nil
	}
	return m.onText(string(chunk))
}

// onEnter is design §6.4's confirm (closing a menu into the working frame,
// the same way answering a real permission question hands off to a turn) or
// design §8.2 step 3's submit (clearing whatever this pane's own paste left
// in the box). Enter with nothing pending is a legitimate no-op: the
// worker's own enrolment types a task and presses Enter into a bare idle
// screen, which this program shows exactly as a real free_text pane would.
func (m *mock) onEnter() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.cur {
	case phaseMenu:
		m.cur = phaseWorking
		m.echo = ""
	default:
		if m.echo == "" {
			return nil
		}
		m.echo = ""
	}
	return m.paintLocked()
}

// onText is design §8.2 step 2's echo: the pasted text lands in the input
// box exactly where a real Claude's cursor sits, right after its prompt
// marker (grownInputBox's own doc). A menu never receives one in this
// test's own flow (session.message only targets the input/working target
// kinds — pane_messages.go's deliveryTargetKind), so it is accepted but
// left unrendered rather than corrupting a menu frame nothing asked to
// change.
func (m *mock) onText(text string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == phaseMenu {
		return nil
	}
	m.echo = text
	return m.paintLocked()
}

// isEnter reports whether chunk is nothing but CR and/or LF bytes — the
// shapes a real key intent named "Enter" plausibly encodes to
// (internal/sessionruntime's own ghostty-backed encoder is not reproduced
// here; this program tolerates either spelling rather than assuming one).
func isEnter(chunk []byte) bool {
	for _, b := range chunk {
		if b != '\r' && b != '\n' {
			return false
		}
	}
	return true
}

// containsEscape reports whether chunk carries a raw ESC byte anywhere —
// this test's own flow never needs this program to react to one (see the
// file doc), so such a chunk is logged and otherwise left alone.
func containsEscape(chunk []byte) bool {
	for _, b := range chunk {
		if b == 0x1b {
			return true
		}
	}
	return false
}

// promptMarker is the glyph Claude's input box opens a typed line with, and
// boxIndent what its continuation rows are indented by. Both are read by the
// shipped rule's own inputText extractor (claude.rule.json's pattern), which
// is why they are spelled here rather than chosen.
const (
	promptMarker = "❯"
	boxIndent    = "  "
)

// grownInputBox returns a copy of frame whose input box shows text, wrapped
// over as many rows as it needs.
//
// A REAL CLAUDE'S BOX WRAPS A PASTE, and this program had to learn to, because
// the captured frames hold a box with ONE content row: a text longer than the
// pane is wide was truncated to it, and the delivery that types a worker's
// briefing reads the box back to confirm what it pasted (pane_messages.go's
// boxContainsEcho) — so a truncated echo is a briefing that never confirms and
// a task refused afterwards for arriving before its own rules. The rule's
// inputText extractor joins the box's content rows with a single space
// (claude.rule.json's `join`, projected by agentdriver.Observation.InputText),
// which is exactly what a box whose one line has WRAPPED reads back as — so
// the wrap below breaks AT SPACES, and a break inside a word would insert a
// character the worker never typed.
//
// The box grows UPWARD, the way Claude's does: its bottom rule stays on the
// row it was on, the content rows are added above it, and the transcript above
// scrolls by as many rows as the box grew.
func grownInputBox(reg *agentdriver.Registry, frame paneview.Frame, text string) paneview.Frame {
	obs := reg.Observe("claude", frame)
	top, bottom := obs.InputBox.First, obs.InputBox.Last
	if bottom <= top || bottom >= len(frame.Lines) {
		return frame
	}
	width := len(frame.Lines[top])
	rows := wrapBox(text, width-len([]rune(promptMarker+"\u00A0")))
	if len(rows) == 0 {
		return frame
	}
	extra := len(rows) - 1
	if extra >= top {
		// No room to grow. A screen this small is not what this mock draws,
		// and inventing a box that overwrites its own rules would be a frame
		// no classifier could read — so the base frame is left alone and the
		// echo is refused by the reader rather than faked here.
		return frame
	}
	out := frame
	out.Lines = append([][]paneview.Cell(nil), frame.Lines...)
	for i := 0; i+extra < top; i++ {
		out.Lines[i] = frame.Lines[i+extra]
	}
	first := bottom - len(rows)
	out.Lines[first-1] = frame.Lines[top] // the box's own top rule, glyphs and all
	cursorRow, cursorCol := first, 0
	for i, chunk := range rows {
		prefix := boxIndent
		if i == 0 {
			prefix = promptMarker + "\u00A0"
		}
		out.Lines[first+i] = rowCells(prefix+chunk, width)
		cursorRow = first + i
		cursorCol = len([]rune(prefix + chunk))
	}
	out.CursorY = cursorRow
	out.CursorX = cursorCol
	return out
}

// wrapBox breaks text into rows of at most width runes, at spaces: a break
// consumes the space it fell on, so the reading that joins the rows back with
// one space reconstructs the text exactly. A single word wider than the row is
// broken rather than dropped — no text in this repository has one, and losing
// it silently would be the worse answer.
func wrapBox(text string, width int) []string {
	if width < 1 || text == "" {
		return nil
	}
	rest := []rune(text)
	var rows []string
	for len(rest) > 0 {
		if len(rest) <= width {
			rows = append(rows, string(rest))
			break
		}
		cut := width
		for cut > 0 && rest[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			rows = append(rows, string(rest[:width]))
			rest = rest[width:]
			continue
		}
		rows = append(rows, string(rest[:cut]))
		rest = rest[cut+1:]
	}
	return rows
}

// rowCells renders one row of text into a frame's own cell grid, blank after
// the text, so a synthesized row is the same shape the emulator produces.
func rowCells(text string, width int) []paneview.Cell {
	row := make([]paneview.Cell, 0, width)
	for _, r := range text {
		if len(row) >= width {
			break
		}
		row = append(row, paneview.Cell{Text: string(r), Width: 1})
	}
	for len(row) < width {
		row = append(row, paneview.Cell{Text: " ", Width: 1})
	}
	return row
}

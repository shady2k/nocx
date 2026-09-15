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
// of its own.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(cueDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return err
	}
	cueFile := filepath.Join(cueDir, sessionID)
	logFile := filepath.Join(logDir, sessionID)

	m, err := newMock(capturesDir)
	if err != nil {
		return err
	}

	logHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- this test's own state directory
	if err != nil {
		return err
	}
	defer func() { _ = logHandle.Close() }()

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
	var lastCue string
	initialCue, cueErr := os.ReadFile(cueFile) // #nosec G304 -- this test's own state directory
	if cueErr == nil {
		lastCue = strings.TrimSpace(string(initialCue))
	}

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
			lastCue = cue
			m.setPhase(phase(cue))
			if _, werr := out.Write(m.paintCurrent()); werr != nil {
				return werr
			}
			if werr := out.Flush(); werr != nil {
				return werr
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

func (m *mock) paintLocked() []byte {
	frame := m.base[m.cur]
	if m.echo != "" {
		frame = overlayInputBox(m.rules, frame, m.echo)
	}
	return agentcapture.Paint(frame)
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
// marker (overlayInputBox's own doc). A menu never receives one in this
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

// overlayInputBox returns a copy of frame whose input-box content row shows
// text right after the agent's own prompt marker — session_surface_happypath_test.go's
// own s14OverlayInputBox, reproduced here because that one lives in a
// _test.go file in a different binary and cannot be imported. It touches
// nothing a classifier reads: only the one row Document.InputBox names, so
// the copy's classification is exactly frame's.
func overlayInputBox(reg *agentdriver.Registry, frame paneview.Frame, text string) paneview.Frame {
	obs := reg.Observe("claude", frame)
	if obs.InputBox.Last < obs.InputBox.First {
		return frame
	}
	out := frame
	out.Lines = append([][]paneview.Cell(nil), frame.Lines...)
	for row := obs.InputBox.First; row <= obs.InputBox.Last && row < len(out.Lines); row++ {
		src := out.Lines[row]
		marker := -1
		for i, c := range src {
			if c.Text == "❯" {
				marker = i
				break
			}
		}
		if marker < 0 {
			continue
		}
		dst := append([]paneview.Cell(nil), src...)
		col := marker + 2 // right after "❯ "
		for _, r := range text {
			if col >= len(dst) {
				break
			}
			dst[col] = paneview.Cell{Text: string(r), Width: 1}
			col++
		}
		out.Lines[row] = dst
		return out // exactly one row is the box's own content row
	}
	return out
}

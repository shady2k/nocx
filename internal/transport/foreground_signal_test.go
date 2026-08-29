package transport

import (
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

// handoffSession is one pty whose FOREGROUND GROUP CHANGES under the ladder.
// That is the state this file exists for: the execution being stopped dies on
// the first signal, and the person's next command takes the foreground a
// moment later — inside the escalation's own grace window.
type handoffSession struct {
	mu sync.Mutex
	// fg is the foreground job's group, 0 at a prompt. It answers
	// "what is in front", which is a different question from "does this group
	// still exist" — and telling the two apart is what this fake is for.
	fg int
	// live is every group that still exists. A group can be alive and NOT in
	// front: that is precisely the state the assistant's command is in once
	// the person has started something else.
	live map[int]bool
	// diesOn ends the group it is delivered to, and hands the foreground to
	// `successor` if that group was the one in front (0 = back to a prompt).
	diesOn    syscall.Signal
	successor int
	got       []signalledGroup
}

type signalledGroup struct {
	pgid int
	sig  syscall.Signal
}

// newHandoffSession starts with `fg` in front, and every named group alive.
func newHandoffSession(fg int, alive ...int) *handoffSession {
	h := &handoffSession{fg: fg, live: map[int]bool{}}
	if fg != 0 {
		h.live[fg] = true
	}
	for _, pgid := range alive {
		h.live[pgid] = true
	}
	return h
}

func (h *handoffSession) SignalForeground(sig syscall.Signal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fg == 0 {
		return pty.ErrNoForeground
	}
	return h.signalLocked(h.fg, sig)
}

func (h *handoffSession) ForegroundJob() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fg == 0 {
		return 0, pty.ErrNoForeground
	}
	return h.fg, nil
}

func (h *handoffSession) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.live[pgid] {
		return pty.ErrNoForeground
	}
	return h.signalLocked(pgid, sig)
}

func (h *handoffSession) signalLocked(pgid int, sig syscall.Signal) error {
	if !h.live[pgid] {
		return pty.ErrNoForeground
	}
	if sig == 0 {
		return nil // the existence check: this group is still there
	}
	h.got = append(h.got, signalledGroup{pgid: pgid, sig: sig})
	if h.diesOn != 0 && sig == h.diesOn {
		delete(h.live, pgid)
		if h.fg == pgid {
			h.fg = h.successor
		}
	}
	return nil
}

// takeOver models the person starting a command: a different group is alive
// and in front from now on.
func (h *handoffSession) takeOver(pgid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fg = pgid
	h.live[pgid] = true
}

func (h *handoffSession) signalsTo(pgid int) []syscall.Signal {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []syscall.Signal
	for _, s := range h.got {
		if s.pgid == pgid {
			out = append(out, s.sig)
		}
	}
	return out
}

// THE LADDER KEEPS THE ADDRESSEE IT STARTED WITH (nocx-uvac6.11).
//
// stopForeground used to ask the kernel "what is in front right now" at every
// rung, and cooperatedForeground reported success only when NOTHING was in
// front. So a command that took its SIGINT and exited looked like a command
// that had ignored it, the moment the person started their own next command
// inside the one-second grace — and TERM, then KILL, went to that command.
//
// agent.cancel made this reachable from a key a person presses while typing
// (nocx-uvac6.10), which is what turned a latent hazard into a defect.
func TestStopForeground_SignalsOnlyTheGroupItStartedAgainst(t *testing.T) {
	const assistantGroup, personGroup = 4100, 4200
	sess := newHandoffSession(assistantGroup)
	sess.diesOn, sess.successor = syscall.SIGINT, personGroup
	sess.live[personGroup] = true

	outcome := stopForeground(log.NewSlogAdapter(nil), session.ID("sid"), sess, 200*time.Millisecond)

	if outcome != foregroundDelivered {
		t.Fatalf("outcome = %q, want %q", outcome, foregroundDelivered)
	}
	if got := sess.signalsTo(assistantGroup); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("the addressed group got %v, want exactly [SIGINT]", got)
	}
	if got := sess.signalsTo(personGroup); len(got) != 0 {
		t.Fatalf("a command the person started afterwards was signalled: %v", got)
	}
}

// The ladder still escalates against a command that genuinely ignores it —
// the whole point of the rungs — and the escalation stays on the one group.
func TestStopForeground_EscalatesOnTheSameGroupWhenNothingCooperates(t *testing.T) {
	const stubborn = 4300
	sess := newHandoffSession(stubborn)

	outcome := stopForeground(log.NewSlogAdapter(nil), session.ID("sid"), sess, 20*time.Millisecond)

	if outcome != foregroundDelivered {
		t.Fatalf("outcome = %q, want %q", outcome, foregroundDelivered)
	}
	want := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	got := sess.signalsTo(stubborn)
	if len(got) != len(want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("signals = %v, want %v", got, want)
		}
	}
}

// A prompt has no job to stop, and saying "nothing running" is what keeps a
// caller from claiming a command was stopped.
func TestStopForeground_AtAPromptSignalsNothing(t *testing.T) {
	sess := newHandoffSession(0)

	outcome := stopForeground(log.NewSlogAdapter(nil), session.ID("sid"), sess, 20*time.Millisecond)

	if outcome != foregroundNothingRunning {
		t.Fatalf("outcome = %q, want %q", outcome, foregroundNothingRunning)
	}
	if got := sess.signalsTo(0); len(got) != 0 {
		t.Fatalf("a prompt was signalled: %v", got)
	}
}

// Ctrl+C keeps its own meaning: ONE SIGINT to whatever is in front right now.
// It does not capture a group, because "interrupt this" is a question about
// the present moment and the person may press it again.
func TestInterruptForeground_AddressesWhateverIsInFrontNow(t *testing.T) {
	const first, second = 4400, 4500
	sess := newHandoffSession(first, second)
	sess.diesOn, sess.successor = syscall.SIGINT, second

	if got := interruptForeground(log.NewSlogAdapter(nil), session.ID("sid"), sess); got != foregroundDelivered {
		t.Fatalf("first interrupt = %q, want %q", got, foregroundDelivered)
	}
	if got := interruptForeground(log.NewSlogAdapter(nil), session.ID("sid"), sess); got != foregroundDelivered {
		t.Fatalf("second interrupt = %q, want %q", got, foregroundDelivered)
	}
	if got := sess.signalsTo(first); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("first group got %v, want [SIGINT]", got)
	}
	if got := sess.signalsTo(second); len(got) != 1 || got[0] != syscall.SIGINT {
		t.Fatalf("second group got %v, want [SIGINT]", got)
	}
}

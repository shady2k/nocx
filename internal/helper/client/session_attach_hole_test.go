package client_test

// A RESET AT ATTACH IS A HOLE LIKE ANY OTHER (nocx-k6p18.30).
//
// The live reset — the helper reclaiming its window out from under a reader
// that is already attached — has reached the coordinator's recorder since
// nocx-k6p18.25. The reset an ATTACH comes back with never did, and it is the
// one a coordinator taking a session back after being replaced meets EVERY
// time the host out-produced its window while nobody was listening. That is
// the epic's own scenario: quit nocx mid-build, come back, and the stretch the
// host's window dropped has to be NAMED rather than silently absent.
//
// The two assertions are the two halves of naming it: the observer is told the
// width, and it is told BEFORE the reader reaches a byte from the far side of
// the hole — because where a hole sits is the whole of what a reader needs
// from it, and a hole recorded under bytes that came after it is at an offset
// that is not the hole's.

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// attachHoleObserver collects what the attachment reported and when, in the
// reader's own coordinate: how many bytes had been handed over when the hole
// was stated.
type attachHoleObserver struct {
	mu     sync.Mutex
	holes  []observedHole
	readSo int
}

type observedHole struct {
	lost   uint64
	reason string
	seenAt int
}

func (o *attachHoleObserver) note(lost uint64, reason string) {
	o.mu.Lock()
	o.holes = append(o.holes, observedHole{lost: lost, reason: reason, seenAt: o.readSo})
	o.mu.Unlock()
}

func (o *attachHoleObserver) advance(n int) {
	o.mu.Lock()
	o.readSo = n
	o.mu.Unlock()
}

func (o *attachHoleObserver) observed() []observedHole {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observedHole(nil), o.holes...)
}

// TestAnAttachThatResetsStatesTheHoleItResetOver is the case the coordinator's
// re-adoption is built on: a reader that asks to resume at an offset the host's
// window has already reclaimed.
func TestAnAttachThatResetsStatesTheHoleItResetOver(t *testing.T) {
	c, proc := hostedFakeShell(t)

	entry, err := c.Spawn(context.Background(), proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, WindowBytes: 1, // clamped up to the helper's floor
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// The shell out-produces the window with NOBODY attached, which is exactly
	// the interval this epic exists for: no coordinator, no reader, and the
	// oldest bytes discarded rather than the build stopped (AD-10's amendment).
	chunk := bytes.Repeat([]byte("a"), 32*1024)
	for range 20 {
		if printErr := proc.print(chunk); printErr != nil {
			t.Fatalf("shell output: %v", printErr)
		}
	}
	const marker = "OUTPUT-THE-NEW-COORDINATOR-SEES"
	if printErr := proc.print([]byte(marker)); printErr != nil {
		t.Fatalf("shell output: %v", printErr)
	}

	// THE WINDOW IS QUIET BEFORE THE ATTACH, and this wait is what makes the
	// test about the attach rather than about a race. While the helper's pump
	// is still draining, a subscriber's first read can meet a window that has
	// reclaimed since the attach answered — a LIVE reset, which has been
	// reported since nocx-k6p18.25 and would mask the attach-time one entirely.
	// Waiting until the window has taken every byte the shell wrote leaves the
	// attach result as the only thing that can state the loss.
	//
	// A state change, never a duration: the helper's own inventory reports how
	// much this session has produced, and the test knows how much it printed.
	//nolint:gosec // a byte count of a slice this test wrote; never negative.
	total := uint64(20*len(chunk) + len(marker))
	waitForWindow(t, c, entry.HostSessionID, total)

	// The coordinator comes back and asks to resume where its RECORDING ended,
	// which is the start of the stream: it recorded nothing before it died.
	// Fresh is false — it has a recording to be continuous with, which is the
	// whole reason the offset is its own and not the window's base.
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: 0, Fresh: false, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = attached.Close() }()

	obs := &attachHoleObserver{}
	attached.OnOutputHole(obs.note)

	read := make(chan []byte, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 8*1024)
		for {
			n, readErr := attached.Read(buf)
			if n > 0 {
				obs.advance(len(seen))
				seen = append(seen, buf[:n]...)
				if bytes.Contains(seen, []byte(marker)) {
					read <- seen
					return
				}
			}
			if readErr != nil {
				read <- seen
				return
			}
		}
	}()

	var seen []byte
	select {
	case seen = <-read:
	case <-time.After(20 * time.Second):
		t.Fatal("the reader never reached the output the window still held")
	}

	got := obs.observed()
	if len(got) == 0 {
		t.Fatalf("the attach reset over a hole and said nothing: no output hole was observed across "+
			"%d bytes. A coordinator taking a session back would splice two non-adjacent stretches "+
			"of one stream together and call it a recording", len(seen))
	}
	if got[0].lost == 0 {
		t.Fatal("the hole was stated without its width: a gap of nothing is a gap nobody can report")
	}
	if got[0].reason != proto.GapReasonWindow {
		t.Fatalf("the hole was stated with reason %q, not the helper's own %q — the reason is what "+
			"tells a person which knob dropped their bytes", got[0].reason, proto.GapReasonWindow)
	}
	// In stream order, and here that means FIRST: everything this reader will
	// ever receive is on the far side of this hole.
	if got[0].seenAt != 0 {
		t.Fatalf("the hole was stated after %d bytes had been read; on an attach-time reset it "+
			"precedes every byte this attachment will ever hand over", got[0].seenAt)
	}
	// AD-6 again: nothing of the coordinator's own is in the byte stream.
	if foreign := bytes.ReplaceAll(bytes.ReplaceAll(seen, []byte(marker), nil), []byte("a"), nil); len(foreign) != 0 {
		t.Fatalf("the client wrote %d bytes of its own into the session's output: %q", len(foreign), foreign)
	}
}

// waitForWindow blocks until the helper's window reports that it has taken
// `written` bytes from the shell — the point past which nothing can reclaim
// any further, because nothing more is coming.
func waitForWindow(t *testing.T, c *client.Client, id client.HostSessionID, written uint64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := c.Sessions(context.Background())
		if err != nil {
			t.Fatalf("ask the helper what it holds: %v", err)
		}
		for _, e := range entries {
			if e.HostSessionID == id && e.Window.Written >= written {
				return
			}
		}
	}
	t.Fatalf("the helper's window never took the %d bytes the shell printed", written)
}

// AND THE PAIRED POSITIVE, without which the assertion above is satisfiable by
// a client that reports a hole on every attach: an attachment that resumes at
// an offset the window still holds loses nothing and says so by saying nothing.
func TestAnAttachThatResumesInsideTheWindowStatesNoHole(t *testing.T) {
	c, proc := hostedFakeShell(t)

	entry, err := c.Spawn(context.Background(), proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, WindowBytes: 1,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	const marker = "OUTPUT-INSIDE-THE-WINDOW"
	if printErr := proc.print([]byte(marker)); printErr != nil {
		t.Fatalf("shell output: %v", printErr)
	}

	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "fedcba9876543210fedcba9876543210",
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: false, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = attached.Close() }()

	obs := &attachHoleObserver{}
	attached.OnOutputHole(obs.note)

	read := make(chan []byte, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 8*1024)
		for {
			n, readErr := attached.Read(buf)
			if n > 0 {
				seen = append(seen, buf[:n]...)
				if bytes.Contains(seen, []byte(marker)) {
					read <- seen
					return
				}
			}
			if readErr != nil {
				read <- seen
				return
			}
		}
	}()
	select {
	case <-read:
	case <-time.After(20 * time.Second):
		t.Fatal("the reader never received the output the window was holding for it")
	}

	if got := obs.observed(); len(got) != 0 {
		t.Fatalf("a hole was stated over a resume that lost nothing: %+v. A recording holed where "+
			"nothing is missing sends a person after bytes that were never lost", got)
	}
}

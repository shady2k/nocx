package sessionruntime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// The interval machinery's shared harness, over the REAL emulator
// (libghostty-vt behind its port) driven directly: a Session ingests program
// bytes the way the pump would, and the authenticated boundary arrives
// through [Session.Completed] and [Session.SightFence] the way the contract
// delivers them. The sealed observation record these tests once also read
// was retired by nocx-zg3k3.5.4; what a boundary seals is read off the row
// stream (rowstream_test.go owns the stream's own tests), and the helpers
// here are what the rest of the suite drives every ingest and boundary with.

// obsSession builds a runtime over a real ghostty emulator and a harness
// terminal: no PTY and no pump, so the test controls every ingest.
func obsSession(t *testing.T, g Geometry) *Session {
	t.Helper()
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the real emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	return obsSessionOver(t, g, screen)
}

// obsSessionOver is obsSession with the screen left to the caller: a schedule
// that must SEE a departure report which cannot be read hands in the real
// adapter wrapped in the harness instrument that strikes one, while obsSession
// itself stays the plain shape the rest of the suite uses.
func obsSessionOver(t *testing.T, g Geometry, screen emulator.Terminal) *Session {
	t.Helper()
	s, err := New(Config{
		Incarnation:  Incarnation{Session: "observation", Generation: 1},
		Geometry:     g,
		Terminal:     newHarnessTerminal(g),
		Emulator:     screen,
		Completeness: CompletenessComplete,
	})
	if err != nil {
		t.Fatalf("build the runtime over the real emulator: %v", err)
	}
	return s
}

// obsSeal drives one authenticated boundary through the contract: the
// completion parks, the fence sighted on the stream joins it, and the
// interval seals at that join.
func obsSeal(t *testing.T, s *Session, nonce FenceNonce) {
	t.Helper()
	s.Completed(s.Incarnation(), nonce, 0)
	s.SightFenceBoundary(t, nonce)
	if got := s.RendezvousFor(nonce).State; got != RendezvousComplete {
		t.Fatalf("the boundary reads %s, want complete", rendezvousStateName(got))
	}
}

// SightFenceBoundary is one fence sighting, the test's spelling of the join.
// The completion may park the interval (the screen is never read at the
// completion); the sighting is what carries the boundary and seals it, so
// every harness caller lands both halves.
func (s *Session) SightFenceBoundary(t *testing.T, nonce FenceNonce) {
	t.Helper()
	if err := s.SightFence(nonce, []byte("fence-source")); err != nil {
		t.Fatalf("sight the fence that joins the boundary: %v", err)
	}
}

// obsFeed is one ingest of n numbered lines starting at from — the same
// L%06d lines the real-PTY tests print, fed the way a carrier hands bytes.
func obsFeed(t *testing.T, s *Session, from, n int) {
	t.Helper()
	var sb strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&sb, "L%06d\r\n", i)
	}
	if err := s.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest %d lines: %v", n, err)
	}
}

func obsNonce(k byte) FenceNonce {
	var n FenceNonce
	for i := range n {
		n[i] = k
	}
	return n
}

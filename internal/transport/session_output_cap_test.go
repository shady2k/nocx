package transport

// AN EPISODE, NOT A PASS (nocx-3an1s.1.7).
//
// Measured on an installed build: one session under ordinary output, held at
// its recording's output cap, wrote "session recording dropped its oldest
// bytes" every 10 to 200 ms for as long as it ran, and pushed every other line
// out of nocx.log. The pass is not the event — the EPISODE is, and these are
// its two ends: at most one line per interval carrying the total since the
// last one, and a closing line carrying what is left.

import (
	"testing"
	"time"
)

func TestCapEpisode_OneLinePerIntervalWithTheTotalSinceTheLast(t *testing.T) {
	// fakeClock is the package's (ws_paneobserve_test.go): a rule about which
	// pass writes a line is asserted by moving time, never by waiting for it.
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	e := newCapEpisode(time.Minute, clock.Now)

	// The first eviction opens the episode, and says so at once: a person
	// reading the log wants to know the recording started dropping.
	since, total, reported := e.evicted(100)
	if !reported || since != 100 || total != 100 {
		t.Fatalf("first eviction = (%d, %d, %v), want (100, 100, true)", since, total, reported)
	}

	// The measured flood: the next hundred passes are 10 ms apart and none of
	// them may write a line — but none of their bytes may be lost either.
	for i := 0; i < 100; i++ {
		clock.advance(10 * time.Millisecond)
		if _, _, again := e.evicted(10); again {
			t.Fatalf("pass %d wrote a line inside the interval", i)
		}
	}

	// Past the interval, the very next pass writes ONE line carrying
	// everything since the last one — the count, not just the last pass.
	clock.advance(time.Minute)
	since, total, reported = e.evicted(7)
	if !reported {
		t.Fatal("no line after the interval elapsed")
	}
	if want := uint64(100*10 + 7); since != want {
		t.Fatalf("line reports %d bytes since the last, want %d", since, want)
	}
	if want := 100 + since; total != want {
		t.Fatalf("line reports an episode total of %d, want %d", total, want)
	}

	// Within the interval again: nothing to say yet, and the closing line
	// carries the remainder rather than dropping it.
	if _, _, again := e.evicted(5); again {
		t.Fatal("a pass inside the interval wrote a line")
	}
	since, total, reported = e.stopped()
	if !reported || since != 5 {
		t.Fatalf("closing line = (%d, %d, %v), want the 5 bytes since the last line", since, total, reported)
	}
	if want := uint64(100 + 100*10 + 7 + 5); total != want {
		t.Fatalf("closing line reports an episode total of %d, want %d", total, want)
	}

	// Once closed, it is closed: a second stop is not a second line.
	if _, _, again := e.stopped(); again {
		t.Fatal("the episode wrote a second closing line")
	}
}

// An episode that never opened writes nothing when it stops — the ordinary
// path of every session that never reached its cap.
func TestCapEpisode_NoEvictionMeansNoClosingLine(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	e := newCapEpisode(time.Minute, clock.Now)
	if _, _, again := e.stopped(); again {
		t.Fatal("an episode that never dropped anything wrote a closing line")
	}
	// And an episode whose last pass already wrote a line has nothing left to
	// say: the total is on the record.
	if _, _, again := e.evicted(9); !again {
		t.Fatal("the first eviction did not open the episode")
	}
	if _, _, again := e.stopped(); again {
		t.Fatal("a closing line repeated a total that was already written")
	}
}

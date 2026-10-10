package session

// The scrollback op is judged through the REAL helper: a session spawned by
// this service, its emulator the real libghostty behind defaultScreen, its
// output flowing through the owner pump the way a session's does. What is
// asserted is what a coordinator can see from outside: the emulator's
// retained total, through HistoryRows — the one read that answers "how far
// does this session scroll back".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// scrollbackSpawner is a Spawner whose every spawn is the same process, the
// one the test feeds.
type scrollbackSpawner struct {
	proc *rawReaderFakeProcess
}

func (s *scrollbackSpawner) Spawn(_ SpawnRequest) (Process, error) {
	return s.proc, nil
}

func numberedFeed(n int) []byte {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "L%05d\r\n", i)
	}
	return []byte(sb.String())
}

// awaitTotal waits on the emulator's retained total — an observable state
// change the owner's own ingestion produces — and fails on the test's
// deadline, never on a clock of its own.
func awaitTotal(t *testing.T, what string, term emulator.Terminal, want func(int) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		page, err := term.HistoryRows(0, 0)
		if err != nil {
			t.Fatalf("read the retained total: %v", err)
		}
		if want(page.Total) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (last total %d)", what, page.Total)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitCursorAtBottom waits until the produced output has been INGESTED —
// the cursor the feed left behind is the fact that says so — so an assertion
// that follows reads the emulator after the feed, never mid-feed.
func awaitCursorAtBottom(t *testing.T, term emulator.Terminal) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		cursor, err := term.Cursor()
		if err == nil && cursor.Y == 23 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the feed to reach the emulator")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// scrollbackLog is this file's discard logger; discardLog belongs to the
// external test package.
func scrollbackLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// budgetTotalOf reads the emulator's retained total.
func budgetTotalOf(t *testing.T, term emulator.Terminal) int {
	t.Helper()
	page, err := term.HistoryRows(0, 0)
	if err != nil {
		t.Fatalf("read the retained total: %v", err)
	}
	return page.Total
}

// The whole story in one session: the budget the spawn carried holds from
// birth, the op lowers it on the RUNNING session, and zero leaves a bounded
// capture floor while the live page remains empty — every step observed on
// the real emulator through the service's own pump.
func TestSetScrollbackAppliesToARunningSession(t *testing.T) {
	proc := newRawReaderFakeProcess()
	svc := New(Options{
		Generation: "gen-under-test",
		Spawner:    &scrollbackSpawner{proc: proc},
		Log:        scrollbackLog(),
		Limits:     Limits{},
	})
	t.Cleanup(svc.Close)

	var atSpawn uint64 = 200
	res, err := svc.spawn(context.Background(), proto.SpawnParams{
		Cols: 80, Rows: 24, ScrollbackLines: &atSpawn,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	hs, err := svc.find(res.Entry.Session)
	if err != nil {
		t.Fatalf("find the spawned session: %v", err)
	}

	// The spawn-time budget: 3000 lines against a budget of 200 (applied a
	// page wide) leaves the emulator holding no more than the budget's own
	// promise.
	proc.produce(numberedFeed(3000))
	awaitCursorAtBottom(t, hs.screen)
	awaitTotal(t, "the spawn-time budget to prune", hs.screen, func(n int) bool { return n <= 200+512 })

	// The op lowers the RUNNING session: the prune lands at once, before
	// any further output. The tolerance is the port's own page-wide bound
	// (scrollback.go) plus one row: the pinned darwin archive's page
	// arithmetic holds a single row past the boundary linux lands inside
	// (613 measured at budget 100, ci-mac), and the promise under test is
	// the budget's, not the archive's rounding.
	raw, err := json.Marshal(proto.SetScrollbackParams{Session: hs.id, MaxLines: 100})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err = svc.Call(context.Background(), proto.OpSetScrollback, raw); err != nil {
		t.Fatalf("set-scrollback: %v", err)
	}
	awaitTotal(t, "the lowered budget to prune", hs.screen, func(n int) bool { return n <= 100+512+1 })

	// Zero keeps the emulator's bounded page floor for durable capture, but
	// the helper's live surface must not expose it.
	raw, err = json.Marshal(proto.SetScrollbackParams{Session: hs.id, MaxLines: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err = svc.Call(context.Background(), proto.OpSetScrollback, raw); err != nil {
		t.Fatalf("set-scrollback 0: %v", err)
	}
	proc.produce(numberedFeed(2000))
	awaitTotal(t, "zero's bounded capture floor after output", hs.screen, func(n int) bool { return n >= 243 })
	if got := budgetTotalOf(t, hs.screen); got < 243 || got > 512 {
		t.Fatalf("after a feed at zero the session retains %d rows, want the bounded capture floor", got)
	}
	page, err := hs.historyPage(nil, 64)
	if err != nil {
		t.Fatalf("history page at zero: %v", err)
	}
	if got := len(decodePageRows(t, page.Rows)); got != 0 || page.More {
		t.Fatalf("history page at zero exposes %d rows with more=%v, want an empty page", got, page.More)
	}
}

// A spawn that names no budget carries the default: output far past it is
// held to it, and a session from a coordinator with no setting behaves like
// the default says.
func TestSpawnWithoutABudgetCarriesTheDefault(t *testing.T) {
	proc := newRawReaderFakeProcess()
	svc := New(Options{
		Generation: "gen-under-test",
		Spawner:    &scrollbackSpawner{proc: proc},
		Log:        scrollbackLog(),
		Limits:     Limits{},
	})
	t.Cleanup(svc.Close)

	res, err := svc.spawn(context.Background(), proto.SpawnParams{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	hs, err := svc.find(res.Entry.Session)
	if err != nil {
		t.Fatalf("find the spawned session: %v", err)
	}

	proc.produce(numberedFeed(3000))
	awaitCursorAtBottom(t, hs.screen)
	// 3000 lines at the default are all retained — the default is not zero
	// and not some tiny clamp — while the applied limit (default widened by
	// a page) is where they stop if the session goes on forever.
	if got := budgetTotalOf(t, hs.screen); got < 2900 || uint64(got) > DefaultScrollbackLines+512 {
		t.Fatalf("a session with no named budget retains %d rows, want the default's promise", got)
	}
}

// The op a session cannot answer by id is refused, not applied somewhere.
func TestSetScrollbackRefusesAnUnknownSession(t *testing.T) {
	proc := newRawReaderFakeProcess()
	svc := New(Options{
		Generation: "gen-under-test",
		Spawner:    &scrollbackSpawner{proc: proc},
		Log:        scrollbackLog(),
		Limits:     Limits{},
	})
	t.Cleanup(svc.Close)

	raw, err := json.Marshal(proto.SetScrollbackParams{
		Session:  proto.HostSessionID{Generation: "gen-under-test", Session: strings.Repeat("ab", 16)},
		MaxLines: 10,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err = svc.Call(context.Background(), proto.OpSetScrollback, raw); err == nil {
		t.Fatal("an op naming no session this helper holds was accepted")
	}
}

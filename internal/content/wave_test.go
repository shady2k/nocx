package content_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/wave"
)

const (
	testCoordSession = "sess-coordinator"
	testWaveID       = wave.ID("wave-1")
)

func testParticipant(id wave.ParticipantID) wave.Participant {
	return wave.Participant{
		ID:           id,
		Wave:         testWaveID,
		Role:         wave.RoleWorker,
		State:        wave.StatePrepared,
		Task:         "read AGENTS.md and report",
		RegisteredAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
}

func testWaveLiveness() wave.Liveness {
	return wave.Liveness{
		BackendInstance: "backend-A",
		SessionID:       "sess-worker",
		Epoch:           7,
		Lane:            "lane-1",
		Attempt:         1,
		OutputOffset:    4096,
	}
}

// newWaveStore opens a store and seeds the wave every test needs.
func newWaveStore(t *testing.T) (content.ContentDB, string) {
	t.Helper()
	db, dir := newTestStore(t)
	if err := db.Waves().CreateWave(context.Background(), testWaveID, testCoordSession); err != nil {
		t.Fatalf("create wave: %v", err)
	}
	return db, dir
}

// reopenWaveStore is the freshly constructed reader over the same path: every
// assertion below reads through one, never through the connection that wrote.
func reopenWaveStore(t *testing.T, dir string) content.ContentDB {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    testKey(),
		Budget: testBudget,
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The record survives the process that wrote it, which is the whole reason it
// is durable rather than a map: a coordinator that restarts has to be told
// what it holds by something that did not restart with it.
func TestAParticipantSurvivesTheConnectionThatWroteIt(t *testing.T) {
	ctx := context.Background()
	db, dir := newWaveStore(t)
	p := testParticipant("p-1")
	if err := db.Waves().CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit prepared: %v", err)
	}
	if err := db.Waves().MarkLive(ctx, p.ID, testWaveLiveness()); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := reopenWaveStore(t, dir).Waves().Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != wave.StateLive {
		t.Fatalf("state = %q, want %q", got.State, wave.StateLive)
	}
	if got.Task != p.Task {
		t.Fatalf("task = %q, want %q", got.Task, p.Task)
	}
	if got.Liveness != testWaveLiveness() {
		t.Fatalf("liveness = %+v, want %+v", got.Liveness, testWaveLiveness())
	}
}

// The two terminal facts are two columns sets and are read back
// independently, because a declaration with no exit is an ordinary state and
// so is the reverse.
func TestTheTwoTerminalFactsRoundTripIndependently(t *testing.T) {
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_100_000).UTC()

	t.Run("a declaration alone", func(t *testing.T) {
		db, dir := newWaveStore(t)
		p := testParticipant("p-decl")
		if err := db.Waves().CommitPrepared(ctx, p); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if _, err := db.Waves().RecordDeclaration(ctx, p.ID, wave.Declaration{OK: true, Summary: "read it", At: at}); err != nil {
			t.Fatalf("declare: %v", err)
		}
		got, err := reopenWaveStore(t, dir).Waves().Participant(ctx, p.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got.Declared == nil {
			t.Fatalf("declaration lost")
		}
		if !got.Declared.OK || got.Declared.Summary != "read it" || !got.Declared.At.Equal(at) {
			t.Fatalf("declaration = %+v", *got.Declared)
		}
		if got.Exited != nil {
			t.Fatalf("an exit was invented: %+v", *got.Exited)
		}
	})

	t.Run("an exit alone", func(t *testing.T) {
		db, dir := newWaveStore(t)
		p := testParticipant("p-exit")
		if err := db.Waves().CommitPrepared(ctx, p); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if _, err := db.Waves().RecordExit(ctx, p.ID, wave.Exit{Cause: "signalled", Code: 9, At: at}); err != nil {
			t.Fatalf("exit: %v", err)
		}
		got, err := reopenWaveStore(t, dir).Waves().Participant(ctx, p.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got.Exited == nil {
			t.Fatalf("exit lost")
		}
		if got.Exited.Cause != "signalled" || got.Exited.Code != 9 || !got.Exited.At.Equal(at) {
			t.Fatalf("exit = %+v", *got.Exited)
		}
		if got.Declared != nil {
			t.Fatalf("a declaration was invented: %+v", *got.Declared)
		}
	})
}

// A terminal record is never re-terminalized, so a compensation that runs
// twice cannot turn a completed participant into an interrupted one.
func TestTerminalizeDoesNotOverwriteAnEstablishedTerminalState(t *testing.T) {
	ctx := context.Background()
	db, dir := newWaveStore(t)
	p := testParticipant("p-1")
	if err := db.Waves().CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Waves().Terminalize(ctx, p.ID, wave.StateCompleted); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	if err := db.Waves().Terminalize(ctx, p.ID, wave.StateInterrupted); err != nil {
		t.Fatalf("second terminalize: %v", err)
	}
	got, err := reopenWaveStore(t, dir).Waves().Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != wave.StateCompleted {
		t.Fatalf("state = %q: a second terminalize overwrote an established one", got.State)
	}
}

// A non-terminal state is not a terminal one, and the store says so rather
// than writing it. The CHECK constraint would catch a nonsense value; this
// catches a legal value used for the wrong thing.
func TestTerminalizeRefusesANonTerminalState(t *testing.T) {
	db, _ := newWaveStore(t)
	p := testParticipant("p-1")
	ctx := context.Background()
	if err := db.Waves().CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Waves().Terminalize(ctx, p.ID, wave.StateLive); err == nil {
		t.Fatalf("terminalizing to live was accepted")
	}
}

// MarkLive names the state it expects, so a record a sweep already closed is
// not resurrected by a late enrolment.
func TestMarkLiveRefusesARecordSomethingElseClosed(t *testing.T) {
	ctx := context.Background()
	db, _ := newWaveStore(t)
	p := testParticipant("p-1")
	if err := db.Waves().CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Waves().Terminalize(ctx, p.ID, wave.StateInterrupted); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	err := db.Waves().MarkLive(ctx, p.ID, testWaveLiveness())
	if !errors.Is(err, wave.ErrNoSuchParticipant) {
		t.Fatalf("mark live err = %v, want ErrNoSuchParticipant", err)
	}
}

// D3, at the store: the coordinator asks its SESSION and is told about its
// workers by name — including one whose control it has lost, because
// membership is not delegation and a taken-over worker is still in the wave.
func TestHoldingsAreAnsweredBySessionAndSurviveALostDelegation(t *testing.T) {
	ctx := context.Background()
	db, dir := newWaveStore(t)
	p := testParticipant("p-1")
	if err := db.Waves().CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := db.Waves().PutDelegation(ctx, wave.Delegation{
		ControllerSession: testCoordSession,
		Participant:       p.ID,
		Epoch:             7,
		CreatedByRunID:    "run-42",
		Effects:           wave.DefaultBundle(),
		State:             wave.DelegationActive,
	}); err != nil {
		t.Fatalf("put delegation: %v", err)
	}
	// The human takes over the pane. Control is suspended; membership is not.
	if err := db.Waves().PutDelegation(ctx, wave.Delegation{
		ControllerSession: testCoordSession,
		Participant:       p.ID,
		Epoch:             7,
		Effects:           wave.DefaultBundle(),
		State:             wave.DelegationInputSuspended,
	}); err != nil {
		t.Fatalf("suspend delegation: %v", err)
	}

	held, err := reopenWaveStore(t, dir).Waves().HeldBy(ctx, testCoordSession)
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].ID != p.ID {
		t.Fatalf("held = %v, want exactly %q — a takeover removed a worker from its own wave", held, p.ID)
	}
}

// The open set is what the reservation counts and what the sweep closes, and
// a terminal participant is in neither.
func TestOnlyOpenParticipantsAreListed(t *testing.T) {
	ctx := context.Background()
	db, dir := newWaveStore(t)
	for _, tc := range []struct {
		id    wave.ParticipantID
		state wave.State
	}{
		{"p-prepared", wave.StatePrepared},
		{"p-live", wave.StateLive},
		{"p-done", wave.StateCompleted},
		{"p-gone", wave.StateAbandoned},
	} {
		p := testParticipant(tc.id)
		if err := db.Waves().CommitPrepared(ctx, p); err != nil {
			t.Fatalf("commit %q: %v", tc.id, err)
		}
		switch tc.state {
		case wave.StateLive:
			if err := db.Waves().MarkLive(ctx, tc.id, testWaveLiveness()); err != nil {
				t.Fatalf("mark live %q: %v", tc.id, err)
			}
		case wave.StateCompleted, wave.StateAbandoned:
			if err := db.Waves().Terminalize(ctx, tc.id, tc.state); err != nil {
				t.Fatalf("terminalize %q: %v", tc.id, err)
			}
		}
	}

	fresh := reopenWaveStore(t, dir).Waves()
	for _, tc := range []struct {
		name string
		list func() ([]wave.Participant, error)
	}{
		{"per wave", func() ([]wave.Participant, error) { return fresh.NonTerminal(ctx, testWaveID) }},
		{"across every wave", func() ([]wave.Participant, error) { return fresh.AllNonTerminal(ctx) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.list()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("listed %d participants, want the 2 that are open: %v", len(got), got)
			}
			for _, p := range got {
				if p.State.Terminal() {
					t.Fatalf("terminal participant %q listed as open", p.ID)
				}
			}
		})
	}
}

// A store that never opened refuses rather than accepting a registration into
// nowhere. This is D4 at the one boundary where a no-op would be silent: the
// stub is what runs when the content key could not be read.
func TestTheStubRefusesEveryWaveCall(t *testing.T) {
	ctx := context.Background()
	w := content.NewStub(log.NewSlogAdapter(nil)).Waves()

	for name, call := range map[string]func() error{
		"create wave":     func() error { return w.CreateWave(ctx, testWaveID, testCoordSession) },
		"commit prepared": func() error { return w.CommitPrepared(ctx, testParticipant("p-1")) },
		"mark live":       func() error { return w.MarkLive(ctx, "p-1", testWaveLiveness()) },
		"terminalize":     func() error { return w.Terminalize(ctx, "p-1", wave.StateInterrupted) },
		"put delegation":  func() error { return w.PutDelegation(ctx, wave.Delegation{Participant: "p-1"}) },
		"non terminal":    func() error { _, err := w.NonTerminal(ctx, testWaveID); return err },
		"all nonterminal": func() error { _, err := w.AllNonTerminal(ctx); return err },
		"participant":     func() error { _, err := w.Participant(ctx, "p-1"); return err },
		"held by":         func() error { _, err := w.HeldBy(ctx, testCoordSession); return err },
		"declare":         func() error { _, err := w.RecordDeclaration(ctx, "p-1", wave.Declaration{}); return err },
		"exit":            func() error { _, err := w.RecordExit(ctx, "p-1", wave.Exit{}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, wave.ErrRecordUnavailable) {
				t.Fatalf("err = %v, want ErrRecordUnavailable — a no-op here creates an agent nothing supervises", err)
			}
		})
	}
}

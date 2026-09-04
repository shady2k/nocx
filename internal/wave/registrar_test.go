package wave

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

var errInjected = errors.New("injected fault")

// memStore is an in-memory Store that can fail the Nth call of a kind. An
// ordinal says WHEN a fault fires and is what an enumeration needs; the op log
// is what lets a test read the ORDER two calls happened in rather than trust
// that the code did them in the order it says it does.
type memStore struct {
	mu     sync.Mutex
	waves  map[ID]string
	parts  map[ParticipantID]Participant
	dels   map[ParticipantID]Delegation
	failOn map[string]int
	counts map[string]int
	ops    []string
}

func newMemStore() *memStore {
	return &memStore{
		waves:  map[ID]string{},
		parts:  map[ParticipantID]Participant{},
		dels:   map[ParticipantID]Delegation{},
		failOn: map[string]int{},
		counts: map[string]int{},
	}
}

func (m *memStore) setFault(kind string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 {
		delete(m.failOn, kind)
		return
	}
	m.failOn[kind] = n
}

func (m *memStore) resetCounts() {
	m.mu.Lock()
	m.counts = map[string]int{}
	m.ops = nil
	m.mu.Unlock()
}

func (m *memStore) opLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ops...)
}

// hit records the call and reports whether this one is the faulted ordinal.
// The caller holds no lock.
func (m *memStore) hit(kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[kind]++
	m.ops = append(m.ops, kind)
	if n, ok := m.failOn[kind]; ok && n == m.counts[kind] {
		return fmt.Errorf("%s: %w", kind, errInjected)
	}
	return nil
}

func (m *memStore) EnsureWave(_ context.Context, id ID, coord string) error {
	if err := m.hit("ensurewave"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waves[id] = coord
	return nil
}

func (m *memStore) AllNonTerminal(ctx context.Context) ([]Participant, error) {
	if err := m.hit("allnonterminal"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	var ids []ID
	for id := range m.waves {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var out []Participant
	for _, id := range ids {
		got, err := m.nonTerminal(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

func (m *memStore) NonTerminal(ctx context.Context, id ID) ([]Participant, error) {
	if err := m.hit("nonterminal"); err != nil {
		return nil, err
	}
	return m.nonTerminal(ctx, id)
}

func (m *memStore) nonTerminal(_ context.Context, id ID) ([]Participant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Participant
	for _, p := range m.parts {
		if p.Wave == id && !p.State.Terminal() {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) CommitPrepared(_ context.Context, p Participant) error {
	if err := m.hit("commitprepared"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parts[p.ID] = p
	return nil
}

func (m *memStore) MarkLive(_ context.Context, id ParticipantID, l Liveness) error {
	if err := m.hit("marklive"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	if !ok {
		return ErrNoSuchParticipant
	}
	p.State = StateLive
	p.Liveness = l
	m.parts[id] = p
	return nil
}

func (m *memStore) Terminalize(_ context.Context, id ParticipantID, s State) error {
	if err := m.hit("terminalize"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	if !ok {
		return ErrNoSuchParticipant
	}
	p.State = s
	m.parts[id] = p
	return nil
}

func (m *memStore) RecordDeclaration(_ context.Context, id ParticipantID, d Declaration) (Participant, error) {
	if err := m.hit("recorddecl"); err != nil {
		return Participant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	if !ok {
		return Participant{}, ErrNoSuchParticipant
	}
	cp := d
	p.Declared = &cp
	m.parts[id] = p
	return p, nil
}

func (m *memStore) RecordExit(_ context.Context, id ParticipantID, e Exit) (Participant, error) {
	if err := m.hit("recordexit"); err != nil {
		return Participant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	if !ok {
		return Participant{}, ErrNoSuchParticipant
	}
	ce := e
	p.Exited = &ce
	m.parts[id] = p
	return p, nil
}

func (m *memStore) PutDelegation(_ context.Context, d Delegation) error {
	if err := m.hit("putdelegation"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dels[d.Participant] = d
	return nil
}

func (m *memStore) Participant(_ context.Context, id ParticipantID) (Participant, error) {
	if err := m.hit("participant"); err != nil {
		return Participant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	if !ok {
		return Participant{}, ErrNoSuchParticipant
	}
	return p, nil
}

func (m *memStore) CoordinatorSession(_ context.Context, id ID) (string, error) {
	if err := m.hit("coordinatorsession"); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	coord, ok := m.waves[id]
	if !ok {
		return "", fmt.Errorf("no such wave %q: %w", id, ErrNoSuchParticipant)
	}
	return coord, nil
}

func (m *memStore) HeldBy(_ context.Context, coord string) ([]Participant, error) {
	if err := m.hit("heldby"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Participant
	for _, p := range m.parts {
		if m.waves[p.Wave] == coord {
			out = append(out, p)
		}
	}
	return out, nil
}

// read is the "freshly constructed reader over the same path" of the house
// pattern, at the scope an in-memory store admits: it never consults what the
// procedure believed it wrote.
func (m *memStore) read(t *testing.T, id ParticipantID) (Participant, bool) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.parts[id]
	return p, ok
}

type fakeSpawned struct {
	live   Liveness
	killed *bool
	mu     *sync.Mutex
}

func (f fakeSpawned) Liveness() Liveness { return f.live }
func (f fakeSpawned) Kill(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.killed = true
	return nil
}

type fakeSpawner struct {
	mu     sync.Mutex
	calls  int
	failOn int
	killed bool
	live   Liveness
	// before is called with the request before the fork is reported, so a
	// test can ask what the record already held at the moment of the fork.
	before func(SpawnRequest)
}

func (f *fakeSpawner) Spawn(_ context.Context, req SpawnRequest) (Spawned, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	before := f.before
	f.mu.Unlock()
	if before != nil {
		before(req)
	}
	if f.failOn == n {
		return nil, fmt.Errorf("spawn: %w", errInjected)
	}
	return fakeSpawned{live: f.live, killed: &f.killed, mu: &f.mu}, nil
}

func (f *fakeSpawner) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

type fakeEnrolments struct {
	mu        sync.Mutex
	calls     int
	failOn    int
	never     bool
	withdrawn bool
	live      Liveness
}

func (f *fakeEnrolments) Await(ctx context.Context, _ ParticipantID) (Liveness, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.never {
		<-ctx.Done()
		return Liveness{}, ErrEnrolmentNeverArrived
	}
	if f.failOn == n {
		return Liveness{}, fmt.Errorf("await: %w", errInjected)
	}
	return f.live, nil
}

func (f *fakeEnrolments) Withdraw(context.Context, ParticipantID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = true
	return nil
}

func (f *fakeEnrolments) wasWithdrawn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.withdrawn
}

type fakeSupervisor struct {
	mu       sync.Mutex
	calls    int
	failOn   int
	attached []ParticipantID
	// seen is the state the record was in when supervision was attached. It
	// is what proves the attach happened AFTER the record existed rather
	// than beside it.
	seen []State
}

func (f *fakeSupervisor) Attach(_ context.Context, p Participant) error {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.failOn == n {
		return fmt.Errorf("attach: %w", errInjected)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, p.ID)
	f.seen = append(f.seen, p.State)
	return nil
}

// harness is one wired Registrar and the doubles behind it.
type harness struct {
	store *memStore
	spawn *fakeSpawner
	enrol *fakeEnrolments
	sup   *fakeSupervisor
	reg   *Registrar
	// The backstop is wired in every harness rather than left to default,
	// so a test that is not about the fact set neither types anywhere nor
	// leaves a five-minute alarm behind it.
	wake   *fakeWaker
	human  *fakeEscalation
	alarms *fakeAlarms
}

const (
	coordSession = "sess-coordinator"
	testWave     = ID("wave-1")
)

func testLiveness() Liveness {
	return Liveness{
		BackendInstance: "backend-A",
		SessionID:       "sess-worker",
		Epoch:           7,
		Lane:            "lane-1",
		Attempt:         1,
		OutputOffset:    4096,
	}
}

func newHarness(t *testing.T) *harness { return newHarnessBound(t, 2) }

// newHarnessBound is newHarness with the participant bound named, so a
// fan-out test can hold more than one worker at a time without every other
// test's bound moving with it.
func newHarnessBound(t *testing.T, bound int) *harness {
	t.Helper()
	h := &harness{
		store:  newMemStore(),
		spawn:  &fakeSpawner{live: testLiveness()},
		enrol:  &fakeEnrolments{live: testLiveness()},
		sup:    &fakeSupervisor{},
		wake:   &fakeWaker{out: delivered()},
		human:  &fakeEscalation{},
		alarms: newFakeAlarms(),
	}
	backstop := NewBackstop(log.NewSlogAdapter(nil), h.wake, h.human,
		WithFactDeadline(90*time.Second))
	// The alarm is replaced by ASSIGNMENT rather than by an option: only the
	// deadline is a product value, so only the deadline earns an exported
	// option. These tests are in-package, which is the whole reason the
	// exported surface does not have to grow for them.
	backstop.alarms = h.alarms
	h.reg = NewRegistrar(h.store, h.spawn, h.enrol, h.sup,
		WithBackstop(backstop),
		WithBound(bound),
		WithEnrolmentDeadline(50*time.Millisecond),
	)
	h.reg.newID = func() ParticipantID {
		return ParticipantID(fmt.Sprintf("p-%d", h.store.counts["commitprepared"]+1))
	}
	h.reg.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	if err := h.store.EnsureWave(context.Background(), testWave, coordSession); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	return h
}

func (h *harness) register(ctx context.Context) (Participant, error) {
	return h.reg.Register(ctx, RegisterRequest{
		Wave:               testWave,
		CoordinatorSession: coordSession,
		Role:               RoleWorker,
		Task:               "read AGENTS.md and report",
		Environment:        "env-local",
		CreatedByRunID:     "run-42",
	})
}

// A fork nobody recorded is permanently undiscoverable, so the record has to
// exist before the fork rather than after it — the vault journal's reason,
// applied to a process instead of a secret.
func TestTheRecordExistsBeforeAnyFork(t *testing.T) {
	h := newHarness(t)
	var atFork []State
	h.spawn.before = func(req SpawnRequest) {
		p, ok := h.store.read(t, req.Participant)
		if !ok {
			t.Errorf("at the moment of the fork the record did not exist")
			return
		}
		atFork = append(atFork, p.State)
	}
	if _, err := h.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(atFork) != 1 || atFork[0] != StatePrepared {
		t.Fatalf("record at fork = %v, want exactly one prepared", atFork)
	}
}

// Step 1 is first because a refusal there is free. A bound checked after the
// fork leaves an orphan shell every time it refuses.
func TestARefusalAtTheBoundForksNothingAndRecordsNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := range 2 {
		if _, err := h.register(ctx); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	h.store.resetCounts()
	h.spawn.mu.Lock()
	before := h.spawn.calls
	h.spawn.mu.Unlock()

	_, err := h.register(ctx)
	if !errors.Is(err, ErrBoundExceeded) {
		t.Fatalf("third register err = %v, want ErrBoundExceeded", err)
	}
	h.spawn.mu.Lock()
	after := h.spawn.calls
	h.spawn.mu.Unlock()
	if after != before {
		t.Fatalf("a refused registration forked: spawn calls %d -> %d", before, after)
	}
	for _, op := range h.store.opLog() {
		if op == "commitprepared" {
			t.Fatalf("a refused registration wrote a record: ops %v", h.store.opLog())
		}
	}
}

// Live is entered on the strength of an enrolment that ARRIVED, never because
// a dispatch returned. Dispatch is not delivery.
func TestAnEnrolmentThatNeverArrivesTerminalizesAndKills(t *testing.T) {
	h := newHarness(t)
	h.enrol.never = true

	p, err := h.register(context.Background())
	if !errors.Is(err, ErrEnrolmentNeverArrived) {
		t.Fatalf("register err = %v, want ErrEnrolmentNeverArrived", err)
	}
	if p.ID == "" {
		t.Fatalf("a failed registration must still name the record it left behind")
	}
	got, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("record vanished")
	}
	if !got.State.Terminal() {
		t.Fatalf("state = %q, want terminal", got.State)
	}
	if got.State == StateCompleted {
		t.Fatalf("a registration that never enrolled must never read completed")
	}
	if !h.spawn.wasKilled() {
		t.Fatalf("the launcher was left running")
	}
}

// Supervision attaches to a record that already exists. The ordering, not a
// lock, is what makes a process exiting between the two still observable.
func TestSupervisionAttachesAfterTheRecordIsLive(t *testing.T) {
	h := newHarness(t)
	if _, err := h.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	ops := h.store.opLog()
	mark := -1
	for i, op := range ops {
		if op == "marklive" {
			mark = i
		}
	}
	if mark < 0 {
		t.Fatalf("never marked live: %v", ops)
	}
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	if len(h.sup.seen) != 1 {
		t.Fatalf("supervisor attached %d times, want 1", len(h.sup.seen))
	}
	if h.sup.seen[0] != StateLive {
		t.Fatalf("supervision saw state %q, want %q — it attached before the record was live",
			h.sup.seen[0], StateLive)
	}
}

// The two terminal facts are independent, and neither alone reaches completed.
func TestNeitherFactAloneCompletes(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1_700_000_100, 0).UTC()

	t.Run("a declaration with no exit stays live", func(t *testing.T) {
		h := newHarness(t)
		p, err := h.register(ctx)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		got, err := h.reg.Declared(ctx, p.ID, testLiveness(), Declaration{OK: true, Summary: "done", At: at})
		if err != nil {
			t.Fatalf("declare: %v", err)
		}
		if got.State != StateLive {
			t.Fatalf("state = %q, want %q: the agent said it finished and is still running",
				got.State, StateLive)
		}
	})

	t.Run("an exit with no declaration is abandoned, never completed", func(t *testing.T) {
		h := newHarness(t)
		p, err := h.register(ctx)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		got, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited", Code: 0, At: at})
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
		if got.State != StateAbandoned {
			t.Fatalf("state = %q, want %q", got.State, StateAbandoned)
		}
	})

	t.Run("both together complete, and the declaration decides which", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ok   bool
			want State
		}{
			{"success", true, StateCompleted},
			{"failure", false, StateFailed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newHarness(t)
				p, err := h.register(ctx)
				if err != nil {
					t.Fatalf("register: %v", err)
				}
				if _, decErr := h.reg.Declared(ctx, p.ID, testLiveness(), Declaration{OK: tc.ok, At: at}); decErr != nil {
					t.Fatalf("declare: %v", decErr)
				}
				got, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited", At: at})
				if err != nil {
					t.Fatalf("exit: %v", err)
				}
				if got.State != tc.want {
					t.Fatalf("state = %q, want %q", got.State, tc.want)
				}
			})
		}
	})

	// The conjunction has to hold in either arrival order. An exit seen
	// before the declaration is abandoned, and the declaration that follows
	// REFINES it — that is the second half of a conjunction arriving late,
	// not a resurrection: nothing about the process becomes untrue.
	t.Run("an exit seen first is refined by the declaration that follows", func(t *testing.T) {
		h := newHarness(t)
		p, err := h.register(ctx)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if _, exitErr := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited", At: at}); exitErr != nil {
			t.Fatalf("exit: %v", exitErr)
		}
		got, err := h.reg.Declared(ctx, p.ID, testLiveness(), Declaration{OK: true, At: at})
		if err != nil {
			t.Fatalf("declare: %v", err)
		}
		if got.State != StateCompleted {
			t.Fatalf("state = %q, want %q", got.State, StateCompleted)
		}
	})
}

// A late fact from a replaced incarnation attaches old evidence to a new one,
// which is the whole reason liveness carries more than an attempt number.
func TestEvidenceFromAnotherIncarnationIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p, err := h.register(ctx)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	stale := testLiveness()
	stale.Attempt = 0
	if _, err := h.reg.Exited(ctx, p.ID, stale, Exit{Cause: "exited"}); !errors.Is(err, ErrStaleEvidence) {
		t.Fatalf("stale exit err = %v, want ErrStaleEvidence", err)
	}
	if _, err := h.reg.Declared(ctx, p.ID, stale, Declaration{OK: true}); !errors.Is(err, ErrStaleEvidence) {
		t.Fatalf("stale declaration err = %v, want ErrStaleEvidence", err)
	}
	got, _ := h.store.read(t, p.ID)
	if got.State != StateLive {
		t.Fatalf("stale evidence moved the record to %q", got.State)
	}
}

// D3: a restarted coordinator asks what its SESSION holds and is told by name.
// The run that spawned the worker has ended by then, which is the whole
// situation the question exists for.
func TestAFreshCoordinatorIsToldWhatItsSessionHolds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p, err := h.register(ctx)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	held, err := h.reg.HeldBy(ctx, coordSession)
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].ID != p.ID {
		t.Fatalf("held = %v, want exactly the worker %q", held, p.ID)
	}
	if held[0].Task != "read AGENTS.md and report" {
		t.Fatalf("the coordinator is told an id and not a task: %q", held[0].Task)
	}
}

// Every boundary Register crosses is faulted in turn, the table DISCOVERED
// from a clean recording run rather than hand-written — a hand-written list
// goes stale the first time a step is added, and this one cannot. The outcome
// is read from the state, not predicted from the ordinal: prepared-and-
// terminalized and never-recorded are both legitimate, and a record left
// non-terminal with a live launcher behind it is not.
func TestFaultAtEveryBoundaryConverges(t *testing.T) {
	ctx := context.Background()

	enum := newHarness(t)
	enum.store.resetCounts()
	if _, err := enum.register(ctx); err != nil {
		t.Fatalf("baseline register: %v", err)
	}
	max := map[string]int{}
	for _, op := range enum.store.opLog() {
		max[op]++
	}
	type position struct {
		kind string
		n    int
	}
	var positions []position
	for kind, n := range max {
		for i := 1; i <= n; i++ {
			positions = append(positions, position{kind, i})
		}
	}
	// The three non-store boundaries the procedure also crosses. They are
	// named rather than counted because each is called at most once.
	for _, kind := range []string{"spawn", "await", "attach"} {
		positions = append(positions, position{kind, 1})
	}
	if len(positions) < 6 {
		t.Fatalf("enumerated only %d positions, which cannot be the whole procedure", len(positions))
	}
	t.Logf("enumerated %d boundary positions across %d store kinds", len(positions), len(max))

	for _, pos := range positions {
		t.Run(fmt.Sprintf("%s#%d", pos.kind, pos.n), func(t *testing.T) {
			h := newHarness(t)
			h.store.resetCounts()
			switch pos.kind {
			case "spawn":
				h.spawn.failOn = pos.n
			case "await":
				h.enrol.failOn = pos.n
			case "attach":
				h.sup.failOn = pos.n
			default:
				h.store.setFault(pos.kind, pos.n)
			}

			p, err := h.register(ctx)
			if err == nil {
				// A fault position the procedure did not reach on this
				// path. Nothing to assert about a run that succeeded.
				return
			}
			if !errors.Is(err, errInjected) && !errors.Is(err, ErrEnrolmentNeverArrived) {
				t.Fatalf("register err = %v, want the injected fault", err)
			}

			// Read the state. Either the record was never written, or it
			// is terminal. A torn state in between is what the ordering
			// exists to make unreachable.
			if p.ID != "" {
				if got, ok := h.store.read(t, p.ID); ok && !got.State.Terminal() {
					// The compensation itself may have been the faulted
					// call, and a failure is never a verdict: a
					// terminalize that could not run leaves the record
					// non-terminal ON PURPOSE, to be retried. That is
					// legitimate exactly when terminalize was the fault.
					if pos.kind != "terminalize" {
						t.Fatalf("record left in %q with fault at %s#%d — neither absent nor terminal",
							got.State, pos.kind, pos.n)
					}
				}
			}

			// Heal the fault and retry. The next attempt must converge with
			// no manual cleanup.
			h.store.setFault(pos.kind, 0)
			h.spawn.failOn = 0
			h.enrol.failOn = 0
			h.enrol.never = false
			h.sup.failOn = 0

			p2, err := h.register(ctx)
			if err != nil {
				t.Fatalf("retry after healing %s#%d did not converge: %v", pos.kind, pos.n, err)
			}
			got, ok := h.store.read(t, p2.ID)
			if !ok {
				t.Fatalf("retry left no record")
			}
			if got.State != StateLive {
				t.Fatalf("retry state = %q, want %q", got.State, StateLive)
			}
		})
	}
}

// A participant that never enrolled holds no delegation, so a coordinator
// cannot act on it — membership and delegation are two records and neither
// implies the other.
func TestAFailedRegistrationLeavesNoDelegation(t *testing.T) {
	h := newHarness(t)
	h.enrol.never = true
	p, _ := h.register(context.Background())
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if _, ok := h.store.dels[p.ID]; ok {
		t.Fatalf("a participant that never enrolled is controllable")
	}
}

// A failure after the enrolment arrived has to undo the enrolment too, or an
// authenticated participant is left that no coordinator may address.
func TestAFailureAfterEnrolmentWithdrawsIt(t *testing.T) {
	h := newHarness(t)
	h.store.setFault("putdelegation", 1)
	if _, err := h.register(context.Background()); err == nil {
		t.Fatalf("expected the injected fault")
	}
	if !h.enrol.wasWithdrawn() {
		t.Fatalf("the enrolment was left standing")
	}
	if !h.spawn.wasKilled() {
		t.Fatalf("the launcher was left running")
	}
}

func TestDefaultBundleWithholdsDelegateFurther(t *testing.T) {
	for _, e := range DefaultBundle() {
		if e == EffectDelegateFurther {
			t.Fatalf("delegate-further is in the default bundle: %v", DefaultBundle())
		}
	}
	d := Delegation{Effects: DefaultBundle(), State: DelegationActive}
	if d.Permits(EffectDelegateFurther) {
		t.Fatalf("the default bundle permits delegate-further")
	}
	for _, e := range []Effect{EffectObserve, EffectReceiveEvents, EffectSendInput, EffectClose} {
		if !d.Permits(e) {
			t.Fatalf("the default bundle withholds %q", e)
		}
	}
}

// A human helping their own worker past a prompt must not permanently sever
// the coordinator from it: input goes, observation stays.
func TestInputSuspendedKeepsObserveAndRefusesInput(t *testing.T) {
	d := Delegation{Effects: DefaultBundle(), State: DelegationInputSuspended}
	if d.Permits(EffectSendInput) {
		t.Fatalf("input-suspended still permits send-input")
	}
	for _, e := range []Effect{EffectObserve, EffectReceiveEvents, EffectClose} {
		if !d.Permits(e) {
			t.Fatalf("input-suspended withheld %q, which a takeover must not touch", e)
		}
	}
	for _, s := range []DelegationState{DelegationScopeSuspended, DelegationRevoked, DelegationExpired} {
		if (Delegation{Effects: DefaultBundle(), State: s}).Permits(EffectObserve) {
			t.Fatalf("%q still permits observe", s)
		}
	}
}

func TestStateNamesDoNotDriftFromTheWire(t *testing.T) {
	// The names are what a coordinator reads, so a rename is a contract
	// change and not a refactor. Asserted literally.
	for state, want := range map[State]string{
		StatePrepared:    "prepared",
		StateLive:        "live",
		StateCompleted:   "completed",
		StateFailed:      "failed",
		StateAbandoned:   "abandoned",
		StateInterrupted: "interrupted",
	} {
		if string(state) != want {
			t.Fatalf("state %v = %q, want %q", state, string(state), want)
		}
	}
	if strings.Contains(string(StateAbandoned), "complet") {
		t.Fatalf("abandoned must never read as a completion")
	}
}

// A compensation that itself fails leaves the record NON-TERMINAL on purpose,
// and the next pass completes exactly what the first one could not. Writing a
// terminal state the compensation did not establish is the one thing it may
// never do.
func TestACompensationThatFailsLeavesTheRecordForTheNextPass(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.spawn.failOn = 1                 // force the compensation path
	h.store.setFault("terminalize", 1) // and fault the compensation itself

	p, err := h.register(ctx)
	if err == nil {
		t.Fatalf("expected the injected fault")
	}
	got, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("record vanished")
	}
	if got.State.Terminal() {
		t.Fatalf("state = %q: a compensation that failed wrote a terminal state it did not establish", got.State)
	}

	// The second pass, with the fault healed, completes it.
	h.store.setFault("terminalize", 0)
	if err := h.reg.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ = h.store.read(t, p.ID)
	if got.State != StateInterrupted {
		t.Fatalf("after the second pass state = %q, want %q", got.State, StateInterrupted)
	}
}

// The startup sweep terminalizes every non-terminal participant after a
// backend restart. It never adopts: the worker is gone with the backend that
// held it, and we could not prove a process found at the far end was ours if
// it were not.
func TestTheSweepTerminalizesAndNeverAdopts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p, err := h.register(ctx)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.reg.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ := h.store.read(t, p.ID)
	if got.State != StateInterrupted {
		t.Fatalf("state = %q, want %q", got.State, StateInterrupted)
	}
	if got.State == StateLive {
		t.Fatalf("the sweep adopted a participant")
	}
}

// A fact about a participant the sweep already closed must not reopen it.
func TestALateFactAgainstAnInterruptedRecordIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p, err := h.register(ctx)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.reg.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := h.reg.Exited(ctx, p.ID, testLiveness(), Exit{Cause: "exited"}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("late exit err = %v, want ErrTerminal", err)
	}
	got, _ := h.store.read(t, p.ID)
	if got.State != StateInterrupted {
		t.Fatalf("a late fact moved an interrupted record to %q", got.State)
	}
}

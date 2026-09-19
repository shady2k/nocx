package app

// What nocx SEES reaches the coordinator's mailbox, over the real socket
// (nocx-luqz9.2; design §3, §4.2–4.5; ADR-0070 decisions 2 and 3).
//
// What is real here, and it is the list that decides whether these tests are
// evidence: the worker RECORD (internal/workers.Registrar over its store, fed
// by the SAME Observe a sweep reaches through WorkerObservation), the ONE
// dispatcher, the published unix socket, the kernel's own SO_PEERCRED stamp,
// the (pid, startTime) pin and its ancestry walk, the session registry, the pane
// grid and the shipped authorizer — and the caller is a REAL second process.
//
// The composition these tests do NOT exercise is the watcher-to-bridge hop:
// that is internal/paneobserve's own second reader (TestEverySweepFeedsTheReadingSink…)
// and internal/app's own mapping assertion below, because a test that drove a
// real agent's screen to a settled idle state would be measuring the driver's
// rules rather than this seam.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

// workerInboxRead is the whole of what a coordinator's inbox answer carries,
// decoded against the contract's own field names. Both arrays are POINTERS: a
// decoder that reads a missing key as an empty slice would make "nocx told me
// nothing" and "the field is not on the wire" the same answer, which is the
// distinction the contract's `required` exists for.
type workerInboxRead struct {
	Messages *[]struct {
		From    string `json:"from"`
		Message string `json:"message"`
	} `json:"messages"`
	Observations *[]struct {
		Worker string `json:"worker"`
		State  string `json:"state"`
		At     string `json:"at"`
	} `json:"observations"`
	Cursor int64 `json:"cursor"`
}

func decodeInbox(t *testing.T, raw json.RawMessage) workerInboxRead {
	t.Helper()
	var read workerInboxRead
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatalf("decode inbox result %s: %v", raw, err)
	}
	if read.Messages == nil {
		t.Fatalf("inbox result has no messages key, which its contract requires: %s", raw)
	}
	return read
}

// observedStand settles a worker's pane and hands back what the coordinator is
// then told, read over the socket as the product's own caller reads it.
type coordinatorInboxStand struct {
	coordinator string
	worker      workers.ParticipantID
	record      *workers.Registrar
	socket      string
}

// prepareCoordinatorInbox builds the stand: one admitted coordinator session, one
// worker it holds, and the endpoint published over the shipped authorizer.
//
// The settle window is ZERO and that is the product's rule rather than a test
// convenience: the record reads time.Now, and a state is a fact on its second
// reading — see internal/workers' own observed_test.go for both ends of the
// window asserted against a clock it moves.
func prepareCoordinatorInbox(t *testing.T) *coordinatorInboxStand {
	t.Helper()
	reg, sess, grid := prepareGroupCaller(t)
	record, _ := newGroupTwoCallersRecordInSession("", workers.WithSettleWindow(0))
	socket := publishGroupEndpoint(t, reg, grid, record, newSharedToolDispatcher(t, record))

	participant, err := record.Register(context.Background(), workers.RegisterRequest{
		CoordinatorSession: string(sess.ID()),
		Role:               workers.RoleWorker,
		Task:               "settle and say nothing",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	})
	if err != nil {
		t.Fatalf("register the coordinator's worker: %v", err)
	}
	return &coordinatorInboxStand{
		coordinator: string(sess.ID()), worker: participant.ID,
		record: record, socket: socket,
	}
}

// observe delivers one classification the way the sweep does.
func (s *coordinatorInboxStand) observe(t *testing.T, state workers.ObservedState, times int) {
	t.Helper()
	p, err := s.record.ParticipantOf(context.Background(), string(s.worker))
	if err != nil {
		// The participant's session is its own id in this stand, so the record
		// is asked for the row by id instead.
		held, heldErr := s.record.HeldBy(context.Background(), s.coordinator)
		if heldErr != nil {
			t.Fatalf("holdings: %v", heldErr)
		}
		for _, candidate := range held {
			if candidate.ID == s.worker {
				p = candidate
			}
		}
		if p.ID == "" {
			t.Fatalf("the worker is not in the record: %v", err)
		}
	}
	for i := range times {
		if obsErr := s.record.Observe(context.Background(), p.ID, p.Liveness, state); obsErr != nil {
			t.Fatalf("observe %s (%d): %v", state, i, obsErr)
		}
	}
}

// AC 6, the whole of it: a coordinator reads its OWN mailbox with workers.inbox
// over the real socket — the tool its worker uses for the same call — and is
// told, in order, that its worker settled idle and then blocked.
func TestACoordinatorReadsObservedStatesFromItsOwnInboxOverTheSocket(t *testing.T) {
	s := prepareCoordinatorInbox(t)

	s.observe(t, workers.ObservedWorking, 1)
	s.observe(t, workers.ObservedIdle, 2)
	s.observe(t, workers.ObservedBlocked, 1)
	s.observe(t, workers.ObservedBlocked, 1)

	inbox := callExternally(t, s.socket, "workers.inbox", `{}`)
	if inbox.Error != nil {
		t.Fatalf("a coordinator was refused its own inbox: %+v", inbox.Error)
	}
	read := decodeInbox(t, inbox.Result)
	if read.Observations == nil {
		t.Fatalf("the answer carries no observations key: %s", inbox.Result)
	}
	got := *read.Observations
	if len(got) != 2 {
		t.Fatalf("the coordinator was handed %d observations, want the settled idle and blocked: %s",
			len(got), inbox.Result)
	}
	if got[0].Worker != string(s.worker) || got[0].State != string(workers.ObservedIdle) {
		t.Fatalf("first observation = %+v, want the worker settled idle", got[0])
	}
	if got[1].Worker != string(s.worker) || got[1].State != string(workers.ObservedBlocked) {
		t.Fatalf("second observation = %+v, want the worker blocked, in that order", got[1])
	}
	// And no text travelled with either: the row carries three fields and the
	// text list is empty, which is the shape ADR-0070 decision 3 requires.
	if got[0].At == "" {
		t.Fatalf("an observation carries no time: %+v", got[0])
	}
	if len(*read.Messages) != 0 {
		t.Fatalf("observations arrived as text mail: %+v", *read.Messages)
	}
	if read.Cursor == 0 {
		t.Fatalf("the cursor did not move over the observations: %s", inbox.Result)
	}

	// Asking again hands nothing over, which is the mailbox's own rule and the
	// reason the wake (nocx-luqz9.3) can say "you have N new messages".
	again := decodeInbox(t, callExternally(t, s.socket, "workers.inbox", `{}`).Result)
	if len(*again.Observations) != 0 {
		t.Fatalf("the observations were handed over twice: %+v", *again.Observations)
	}
}

// The worker's OWN call is untouched by any of this: it reads the mail its
// coordinator left it, and the observations about itself are not in ITS box —
// they are addressed to whoever holds it.
func TestAWorkersInboxStillReadsItsOwnCoordinatorsMail(t *testing.T) {
	w := prepareGroupWorkerSetup(t)
	if _, err := w.dispatcher.Dispatch(inProcessInvocation(
		w.coordinator, "workers.say",
		`{"worker":"`+string(w.participant)+`","message":"read this and report"}`,
	)); err != nil {
		t.Fatalf("coordinator workers.say: %v", err)
	}
	read := decodeInbox(t, callExternally(t, w.socket, "workers.inbox", `{}`).Result)
	if len(*read.Messages) != 1 || (*read.Messages)[0].Message != "read this and report" {
		t.Fatalf("the worker read %+v, want the one message its coordinator left", *read.Messages)
	}
	if read.Observations == nil {
		t.Fatalf("the answer carries no observations key, which its contract requires")
	}
	if len(*read.Observations) != 0 {
		t.Fatalf("a worker was handed observations, which are addressed to its coordinator: %+v", *read.Observations)
	}
}

// AC 5, at the composition level: a coordinator is told about ITS workers and
// nothing about a stranger's. The mailbox is derived from the admitted session
// and never named by the caller, so this cannot be got at by asking.
func TestASecondCoordinatorsInboxIsEmptyOfTheFirstsFacts(t *testing.T) {
	s := prepareCoordinatorInbox(t)
	s.observe(t, workers.ObservedIdle, 2)

	// A second worker, coordinated by a session that is NOT the admitted one.
	strangerCoordinator := "sess-stranger-coordinator"
	if _, err := s.record.Register(context.Background(), workers.RegisterRequest{
		CoordinatorSession: strangerCoordinator,
		Role:               workers.RoleWorker,
		Task:               "belongs to somebody else",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	}); err != nil {
		t.Fatalf("register the stranger's worker: %v", err)
	}

	read := decodeInbox(t, callExternally(t, s.socket, "workers.inbox", `{}`).Result)
	if len(*read.Observations) != 1 {
		t.Fatalf("the coordinator was handed %d observations, want only the one about its own worker: %+v",
			len(*read.Observations), *read.Observations)
	}
	if (*read.Observations)[0].Worker != string(s.worker) {
		t.Fatalf("the observation is about %q, want this coordinator's own worker %q",
			(*read.Observations)[0].Worker, s.worker)
	}
}

// AC 7 at the composition level, read through the same socket a coordinator
// uses: an observation moved the participant's record not at all, and the exit
// is the one fact that does.
func TestObservingAWorkerMovesNoRecordStateAndTheExitDoes(t *testing.T) {
	s := prepareCoordinatorInbox(t)
	before := participantState(t, s, s.worker)

	s.observe(t, workers.ObservedIdle, 2)
	if got := participantState(t, s, s.worker); got != before {
		t.Fatalf("an observation moved the record from %q to %q", before, got)
	}

	p := participantRow(t, s, s.worker)
	if _, err := s.record.Exited(context.Background(), p.ID, p.Liveness, workers.Exit{
		Cause: "exited", Code: 0,
	}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := participantState(t, s, s.worker); got == before {
		t.Fatalf("the record is still %q after its process exited", got)
	}
	read := decodeInbox(t, callExternally(t, s.socket, "workers.inbox", `{}`).Result)
	found := false
	for _, o := range *read.Observations {
		if o.State == string(workers.ObservedExited) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the exit reached no mailbox: %+v", *read.Observations)
	}
}

// The coordinator is now offered the call — which is the half of this bead's
// AC that changes an existing assertion. workers.inbox used to be the
// participant's alone and the pair of tests in worker_two_callers_test.go
// checked the two catalogues were disjoint; this task deliberately makes ONE
// call shared, because a coordinator reading its own mailbox is the same act.
func TestACoordinatorsCatalogueOffersItsOwnInbox(t *testing.T) {
	s := prepareCoordinatorInbox(t)
	response := callExternally(t, s.socket, "tools.catalogue", `{"name":"workers.inbox"}`)
	if response.Error != nil {
		t.Fatalf("coordinator tools.catalogue: %+v", response.Error)
	}
	tools := catalogueToolNames(t, response.Result)
	if _, ok := tools["workers.inbox"]; !ok {
		t.Fatalf("coordinator catalogue lacks workers.inbox: %s", response.Result)
	}
	// And the coordinator keeps every call it had: the new one is an addition
	// rather than a replacement.
	for _, name := range []string{"workers.spawn", "workers.say", "workers.holdings", "workers.close"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("coordinator catalogue lost %q: %s", name, response.Result)
		}
	}
}

// ── the mapping, asserted directly ────────────────────────────────────────

// The mapping from a driver state to what a coordinator is told is a fact about
// the PAIR of vocabularies, and this asserts every member of the driver's closed
// set against it — so a state added to that set cannot be read as one of ours by
// accident, and no member can silently start meaning a different word.
//
// `fact` is "the record is told this reading" and NOT "a message is placed":
// working is handed on because the record has to SEE it (a pane that worked
// between two idles is idle news twice, and only the intervening reading lets
// the machine tell those apart), and the record places no message for it — which
// internal/workers' own ObservedState.Recorded answers and its own tests assert.
func TestEveryDriverStateMapsToTheCoordinatorVocabularyOrToNothing(t *testing.T) {
	cases := []struct {
		state agentdriver.State
		want  workers.ObservedState
		fact  bool
	}{
		{agentdriver.StateFreeText, workers.ObservedIdle, true},
		{agentdriver.StatePermissionChoice, workers.ObservedBlocked, true},
		{agentdriver.StateModalChoice, workers.ObservedBlocked, true},
		{agentdriver.StateError, workers.ObservedBlocked, true},
		{agentdriver.StateWorking, workers.ObservedWorking, true},
		{agentdriver.StateUnknown, workers.ObservedWorking, true},
		// Exited is not a screen reading at all: the door for it is the record's
		// own exit admission, which has already placed it.
		{agentdriver.StateExited, "", false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			got, ok := observedStateFor(tc.state)
			if got != tc.want || ok != tc.fact {
				t.Fatalf("observedStateFor(%q) = (%q, %v), want (%q, %v)",
					tc.state, got, ok, tc.want, tc.fact)
			}
		})
	}
	if got, ok := observedStateFor(agentdriver.State("invented")); ok || got != "" {
		t.Fatalf("an unknown driver state mapped to (%q, %v), want no fact", got, ok)
	}
}

// And the other half of that: a worker that is WORKING settles into no message
// at all, however long it works. The mapping hands the reading on so the record
// can reset its hold; what the coordinator receives is the next state that
// actually settles, which is asserted here end to end — the mailbox, not the
// mapping table.
func TestAWorkingWorkerIsNeverToldToItsCoordinator(t *testing.T) {
	s := prepareCoordinatorInbox(t)
	for range 5 {
		s.observe(t, workers.ObservedWorking, 1)
	}
	read := decodeInbox(t, callExternally(t, s.socket, "workers.inbox", `{}`).Result)
	if read.Observations == nil {
		t.Fatalf("the answer carries no observations key: %s", "")
	}
	if len(*read.Observations) != 0 {
		t.Fatalf("a working worker was reported to its coordinator: %+v", *read.Observations)
	}
	// And the very next settled state does arrive, which is what proves the
	// silence above was "nothing to say" rather than "nothing is wired".
	s.observe(t, workers.ObservedIdle, 2)
	after := decodeInbox(t, callExternally(t, s.socket, "workers.inbox", `{}`).Result)
	if len(*after.Observations) != 1 || (*after.Observations)[0].State != string(workers.ObservedIdle) {
		t.Fatalf("the idle after the working turn was not reported: %+v", *after.Observations)
	}
}

// And the bridge RESOLVES a pane that is somebody's worker: the pane id it is
// handed becomes the participant the record is keyed by, and the liveness it
// passes is the one the enrolment recorded, which is what admit's incarnation
// guard will compare.
//
// This is the link the negative case above cannot cover, and it is the one an
// implementation could plausibly get wrong without any other test noticing: the
// id the watcher reports is a PANE (paneenrol.go opens the observation with the
// session's own id), while the record is keyed by PARTICIPANT, and the only
// place those two meet is workerEnrolments.
func TestTheBridgeResolvesAPaneToItsParticipantAndIncarnation(t *testing.T) {
	const (
		pane        = "sess-the-worker"
		participant = workers.ParticipantID("p-bridge")
		lane        = "lane-1"
	)
	logger := log.NewSlogAdapter(nil)
	reg, _, _ := openWorkerAuthSession(t)
	enrol := newWorkerEnrolments(logger, reg)
	// Arranged the way production arranges it: the enrolment act is what fixes
	// pane → participant, and bySess is the map that outlives it (byPane is a
	// rendezvous consumed at that same act).
	enrol.bySess[session.ID(pane)] = participant
	live := workers.Liveness{
		BackendInstance: "backend-A", SessionID: pane, Lane: lane, Epoch: 3, Attempt: 1,
	}
	enrol.arrived[participant] = live

	type admitted struct {
		id    workers.ParticipantID
		live  workers.Liveness
		state workers.ObservedState
	}
	var got []admitted
	obs := &WorkerObservation{
		enrolments: enrol,
		observe: func(_ context.Context, id workers.ParticipantID, l workers.Liveness, st workers.ObservedState) error {
			got = append(got, admitted{id, l, st})
			return nil
		},
		liveness: enrol.livenessOf,
		log:      logger,
	}

	obs.ObserveSession(paneobserve.Observation{PaneID: pane, Agent: "claude", State: agentdriver.StateFreeText})
	obs.ObserveSession(paneobserve.Observation{PaneID: pane, Agent: "claude", State: agentdriver.StateWorking})
	obs.ObserveSession(paneobserve.Observation{PaneID: pane, Agent: "claude", State: agentdriver.StateModalChoice})

	if len(got) != 3 {
		t.Fatalf("the record received %d readings, want 3: %+v", len(got), got)
	}
	for i, want := range []workers.ObservedState{workers.ObservedIdle, workers.ObservedWorking, workers.ObservedBlocked} {
		if got[i].id != participant {
			t.Fatalf("reading %d named participant %q, want the enrolled %q", i, got[i].id, participant)
		}
		if got[i].live != live {
			t.Fatalf("reading %d carried liveness %+v, want the enrolled %+v — admit compares this against the record",
				i, got[i].live, live)
		}
		if got[i].state != want {
			t.Fatalf("reading %d mapped %q, want %q", i, got[i].state, want)
		}
	}
}

// A participant the enrolment never recorded has no incarnation to admit a fact
// against, and the bridge drops the reading rather than inventing one: a fact
// carrying zero liveness would be compared against the record and refused for
// the wrong reason, which reads as the record's fault rather than this gap's.
func TestTheBridgeDropsAReadingWithNoRecordedIncarnation(t *testing.T) {
	const pane = "sess-enrolled-but-no-incarnation"
	logger := log.NewSlogAdapter(nil)
	reg, _, _ := openWorkerAuthSession(t)
	enrol := newWorkerEnrolments(logger, reg)
	enrol.bySess[session.ID(pane)] = "p-half-enrolled"

	obs := &WorkerObservation{
		enrolments: enrol,
		observe: func(context.Context, workers.ParticipantID, workers.Liveness, workers.ObservedState) error {
			t.Fatal("a reading with no recorded incarnation reached the record")
			return nil
		},
		liveness: enrol.livenessOf,
		log:      logger,
	}
	obs.ObserveSession(paneobserve.Observation{PaneID: pane, Agent: "claude", State: agentdriver.StateFreeText})
}

// And the bridge drops a classification about a pane that is nobody's worker —
// the ordinary case, since a person's own enrolled agent is not one — rather
// than inventing a participant for it.
func TestABridgeReadingOfANonWorkerPaneIsDropped(t *testing.T) {
	obs := &WorkerObservation{
		enrolments: newWorkerEnrolments(log.NewSlogAdapter(nil), nil),
		observe: func(context.Context, workers.ParticipantID, workers.Liveness, workers.ObservedState) error {
			t.Fatal("a pane that is not a worker's reached the record")
			return nil
		},
		liveness: func(workers.ParticipantID) (workers.Liveness, bool) { return workers.Liveness{}, false },
		log:      log.NewSlogAdapter(nil),
	}
	obs.ObserveSession(paneobserve.Observation{
		PaneID: "sess-somebody-elses-pane", Agent: "claude", State: agentdriver.StateFreeText,
	})
}

func participantRow(t *testing.T, s *coordinatorInboxStand, id workers.ParticipantID) workers.Participant {
	t.Helper()
	held, err := s.record.HeldBy(context.Background(), s.coordinator)
	if err != nil {
		t.Fatalf("holdings: %v", err)
	}
	for _, p := range held {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("the worker %q is not in the record: %+v", id, held)
	return workers.Participant{}
}

func participantState(t *testing.T, s *coordinatorInboxStand, id workers.ParticipantID) string {
	t.Helper()
	return string(participantRow(t, s, id).State)
}

// ── the product's state vocabulary, over the socket ───────────────────────
//
// THE ACCEPTANCE CHECK FOR THE REMOVAL ITSELF (nocx-luqz9.6, ADR-0070
// decision 3, design §4.1). A coordinator reads what it holds through the
// shipped authorizer and the published socket, and the states it is told are
// the record's own — never `completed`, `failed` or `abandoned`, because nocx
// records no verdict at all.
//
// The two ends are one sequence and that is why they are one test: a worker
// whose COORDINATOR closed it reads `closed`, one whose process merely exited
// reads `exited`, and the difference is the one thing the process fact cannot
// say. Reading them from different stands would let a stand's own fixture
// answer for the product.
func TestAWorkersEndReadsClosedOrExitedAndNeverAnOutcome(t *testing.T) {
	ctx := context.Background()
	s := prepareCoordinatorInbox(t)
	ended := s.worker

	// 1. A second worker, so the two ends below are read off two rows in the
	//    same holdings answer rather than off two different moments.
	closed, err := s.record.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: s.coordinator,
		Role:               workers.RoleWorker,
		Task:               "told to stop",
		Command:            "claude",
		Environment:        content.EnvironmentIDFor(content.EnvLocal, ""),
	})
	if err != nil {
		t.Fatalf("register the second worker: %v", err)
	}
	if closed.State != workers.StateLive {
		t.Fatalf("a spawned worker reads %q, want %q", closed.State, workers.StateLive)
	}

	// 2. One worker's process exits on its own.
	row := participantRow(t, s, ended)
	if _, err := s.record.Exited(ctx, row.ID, row.Liveness, workers.Exit{
		Cause: string(session.ExitInterrupted), Code: 1,
	}); err != nil {
		t.Fatalf("exit: %v", err)
	}

	// 3. The coordinator ENDS the other one, through the record's own close.
	if _, err := s.record.Close(ctx, s.coordinator, closed.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 4. And what a coordinator reads, over the socket it is admitted on.
	holdings := callExternally(t, s.socket, "workers.holdings", `{}`)
	if holdings.Error != nil {
		t.Fatalf("workers.holdings: %+v", holdings.Error)
	}
	if got := workerParticipantState(t, string(holdings.Result), string(ended)); got != string(workers.StateExited) {
		t.Fatalf("the worker whose process ended reads %q, want %q", got, workers.StateExited)
	}
	if got := workerParticipantState(t, string(holdings.Result), string(closed.ID)); got != string(workers.StateClosed) {
		t.Fatalf("the worker its coordinator closed reads %q, want %q", got, workers.StateClosed)
	}
	// 5. THE NEGATIVE HALF, and it is the whole point: no row may report an
	//    OUTCOME. Checked against the wire text rather than against the
	//    decoded states, so a second state field or an outcome smuggled onto
	//    another key is caught too.
	for _, word := range []string{"completed", "failed", "abandoned"} {
		if strings.Contains(string(holdings.Result), `"`+word+`"`) {
			t.Fatalf("holdings reports %q, which nocx no longer records: %s", word, holdings.Result)
		}
	}
}

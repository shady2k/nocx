package app

// A worker reports, and its coordinator is told — over the real socket, through
// the shipped authorizer and dispatcher (nocx-luqz9.4; ADR-0070 decision 1,
// design §4.2, §5.1).
//
// WHAT IS REAL HERE, and it is the list that makes these evidence: the worker
// RECORD, the ONE dispatcher (NewToolDispatcher, the same pipeline the tool
// endpoint narrows every call through), the published unix socket, the kernel's
// own SO_PEERCRED stamp, the (pid, startTime) pin and its ancestry walk, the
// session registry, the pane grid, and the SHIPPED authorizer minting the two
// disjoint grants. The caller is a REAL second process — the test binary
// re-executed — because the whole question "is this session a worker's" is
// answered from that process's tree, and a stand that answered it in process
// would be asserting the answer it had just written.
//
// The one hop not exercised is the wake TYPING into a pane: that mechanism is
// nocx-luqz9.3's (internal/app/worker_wake_test.go and internal/workers/wake_test.go
// own both ends of it). What these tests assert is the half this bead adds —
// that a report REACHES the mailbox the wake counts, and that it is refused
// where it must be — which is what makes the line possible at all.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/workers"
)

// reportedMail is one text row as the coordinator reads it. The report's own
// fields are the ones under test, so the shape decodes exactly them plus the two
// the mailbox has always carried: a decoder that named a field wrongly would
// report an empty kind and every assertion below it would pass for the wrong
// reason.
type reportedMail struct {
	From     string `json:"from"`
	Message  string `json:"message"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Estimate *int   `json:"estimate"`
	Artifact string `json:"artifact"`
}

// observedRow is one observation as the coordinator reads it.
type observedRow struct {
	Worker string `json:"worker"`
	State  string `json:"state"`
}

// inboxPage is ONE page of the coordinator's mailbox: both lists and the cursor.
// Both list keys are POINTERS for the reason internal/app's own inbox test gives
// — a decoder that read a missing key as an empty slice would make "nocx told me
// nothing" and "the field is not on the wire" the same answer.
//
// ONE PAGE AND NOT TWO READS, and that is the mailbox's own rule rather than a
// convenience: asking HANDS THE MAIL OVER (the cursor advances on the fetch), so
// a test that read the text list and then asked again for the observations would
// be asking for rows it had already been handed and would find none. Both lists
// come out of one page because they are one box in one order.
type inboxPage struct {
	Messages     *[]reportedMail `json:"messages"`
	Observations *[]observedRow  `json:"observations"`
	Cursor       int64           `json:"cursor"`
}

// readOwnInbox reads the stand's coordinator mailbox, as the coordinator.
//
// IN PROCESS, and that is the stand's shape rather than a shortcut: the socket
// admits the test binary's own child as the WORKER (prepareGroupWorkerSetup
// records this process as the worker's owned pid), so a call over the socket can
// only ever be the worker's. The coordinator's end of the same mailbox is
// reached through the SAME dispatcher the endpoint serves, through the same
// capability, for the session that holds the worker — which is why the two ends
// below are one mailbox rather than two views.
func readOwnInbox(t *testing.T, w workerWorkerSetup) inboxPage {
	t.Helper()
	var page inboxPage
	if err := json.Unmarshal(mustDispatch(t, w, "workers.inbox", `{}`), &page); err != nil {
		t.Fatalf("decode inbox result: %v", err)
	}
	if page.Messages == nil || page.Observations == nil {
		t.Fatalf("inbox result is missing a key its contract requires: %+v", page)
	}
	return page
}

// reportedState reads the worker's record state as the coordinator's own
// record holds it, through HeldBy — the read behind workers.holdings.
//
// IT DELIBERATELY DOES NOT CALL workers.holdings, and the reason is a fact
// about the mailbox rather than about this test: holdings CARRIES MAIL (D7), so
// a holdings call advances the coordinator's cursor and the reports this test is
// about would already have been handed over by the time it looked. That is the
// same trap the record's own docs name for the wait — "asking after the fetch
// always answers nothing" — and reading the row directly is how a caller asks
// about state alone.
func reportedState(t *testing.T, w workerWorkerSetup) string {
	t.Helper()
	return string(participantRowInStand(t, w).State)
}

// mustDispatch runs one call in process as the stand's coordinator — its own
// end of the record, through the same dispatcher the endpoint serves.
func mustDispatch(t *testing.T, w workerWorkerSetup, method, params string) json.RawMessage {
	t.Helper()
	out, err := w.dispatcher.Dispatch(inProcessInvocation(w.coordinator, method, params))
	if err != nil {
		t.Fatalf("coordinator %s: %v", method, err)
	}
	return json.RawMessage(out)
}

// THE CRITERION AT THE COMPOSITION LEVEL: a real worker process calls
// workers.report over the socket, and its coordinator reads the report — with
// its kind and its own words intact — out of ITS OWN mailbox.
//
// Nothing below asserts a field the caller could have set: the recipient is
// derived inside the record from the reporting participant, the sender is the
// participant the authorizer established from the caller's process tree, and
// the mailbox is the coordinator's own.
func TestAWorkersReportReachesItsCoordinatorsMailboxOverTheSocket(t *testing.T) {
	w := prepareGroupWorkerSetup(t)

	const text = "the migration landed and the branch is green"
	response := callExternally(t, w.socket, "workers.report", `{"kind":"done","text":"`+text+`"}`)
	if response.Error != nil {
		t.Fatalf("a worker was refused its own report: %+v", response.Error)
	}
	var reported struct {
		ID  string `json:"id"`
		Seq int64  `json:"seq"`
	}
	if err := json.Unmarshal(response.Result, &reported); err != nil {
		t.Fatalf("decode report result %s: %v", response.Result, err)
	}
	if reported.ID == "" || reported.Seq == 0 {
		t.Fatalf("the report answered with no committed row: %s", response.Result)
	}

	read := readOwnInbox(t, w)
	if len(*read.Messages) != 1 {
		t.Fatalf("the coordinator was handed %d messages, want the worker's one report: %+v",
			len(*read.Messages), *read.Messages)
	}
	got := (*read.Messages)[0]
	if got.Message != text {
		t.Fatalf("the report arrived as %q, want the worker's own words %q", got.Message, text)
	}
	if got.Kind != string(workers.KindDone) {
		t.Fatalf("the report's kind arrived as %q, want %q — the coordinator cannot tell a "+
			"finished worker from a stuck one without it", got.Kind, workers.KindDone)
	}
	if got.From != string(w.participant) {
		t.Fatalf("the report's sender is %q, want the worker that sent it %q", got.From, w.participant)
	}
	if got.Estimate != nil || got.Artifact != "" {
		t.Fatalf("a done report arrived with checkpoint extras: %+v", got)
	}
	// AND IT IS DATED, by the record's own clock. The time is what a coordinator
	// compares a worker's words against the states beside them with, and the
	// observation that follows this very report is dated by the same one — so a
	// message that arrived without it would be an undated claim in a dated list.
	if got.At == "" {
		t.Fatalf("the report arrived with no time: %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.At); err != nil {
		t.Fatalf("the report's time %q is not the encoding this surface uses: %v", got.At, err)
	}
}

// The three kinds travel as themselves and in the order they were made, and a
// checkpoint's two optional extras arrive with it — the fields whose absence
// from the wire is exactly the soft degrade this asserts against (a call that
// accepts a field nobody can read back is a feature that does not exist).
func TestACheckpointsExtrasReachTheCoordinatorOverTheSocket(t *testing.T) {
	w := prepareGroupWorkerSetup(t)

	if response := callExternally(t, w.socket, "workers.report",
		`{"kind":"progress","text":"half the store is migrated","estimate":40,"artifact":"commit 4f2a1c9"}`,
	); response.Error != nil {
		t.Fatalf("a checkpoint was refused: %+v", response.Error)
	}
	if response := callExternally(t, w.socket, "workers.report",
		`{"kind":"question","text":"which branch should this land on?"}`,
	); response.Error != nil {
		t.Fatalf("a question was refused: %+v", response.Error)
	}

	read := readOwnInbox(t, w)
	if len(*read.Messages) != 2 {
		t.Fatalf("the coordinator was handed %d messages, want both in order: %+v",
			len(*read.Messages), *read.Messages)
	}
	checkpoint, question := (*read.Messages)[0], (*read.Messages)[1]
	if checkpoint.Kind != string(workers.KindProgress) {
		t.Fatalf("the first message's kind is %q, want the checkpoint", checkpoint.Kind)
	}
	if checkpoint.Estimate == nil || *checkpoint.Estimate != 40 {
		t.Fatalf("the checkpoint's estimate = %v, want 40", checkpoint.Estimate)
	}
	if checkpoint.Artifact != "commit 4f2a1c9" {
		t.Fatalf("the checkpoint's artifact = %q, want the reference the worker named", checkpoint.Artifact)
	}
	if question.Kind != string(workers.KindQuestion) {
		t.Fatalf("the second message's kind is %q, want the question", question.Kind)
	}
	if question.Estimate != nil || question.Artifact != "" {
		t.Fatalf("the question carried checkpoint extras: %+v", question)
	}
	// The two times RUN WITH THE ORDER, which is the whole reason they are on the
	// wire: a coordinator reading them against the observations beside them is
	// reading one clock, and a page whose dates fell backwards would make "what
	// happened first" unanswerable.
	first, err := time.Parse(time.RFC3339, checkpoint.At)
	if err != nil {
		t.Fatalf("the checkpoint's time %q: %v", checkpoint.At, err)
	}
	second, err := time.Parse(time.RFC3339, question.At)
	if err != nil {
		t.Fatalf("the question's time %q: %v", question.At, err)
	}
	if second.Before(first) {
		t.Fatalf("the question is dated %s and the checkpoint before it %s, so the page's times "+
			"run backwards", question.At, checkpoint.At)
	}
}

// A report is a CLAIM and moves nothing: the record's state is what it was
// before the call, across all three kinds. And, because the report moved
// nothing, the observation that follows is still placed — the two facts are
// different facts (design §4.3), and a report that swallowed the idle would be
// the record agreeing with a worker about something only nocx can witness.
func TestAReportMovesNoRecordStateAndTheIdleStillFollows(t *testing.T) {
	// The settle window is ZERO and that is the product's rule rather than a
	// convenience: a state is a fact on its SECOND reading, and this test
	// cannot move the record's clock — see internal/workers' observed_test.go
	// for both ends of a real window asserted against one it can.
	w := prepareGroupWorkerSetupWith(t, workers.WithSettleWindow(0))
	before := reportedState(t, w)

	for _, kind := range []string{"done", "question", "progress"} {
		if response := callExternally(t, w.socket, "workers.report",
			`{"kind":"`+kind+`","text":"reporting `+kind+`"}`); response.Error != nil {
			t.Fatalf("a %s report was refused: %+v", kind, response.Error)
		}
	}
	if got := reportedState(t, w); got != before {
		t.Fatalf("three reports moved the record from %q to %q", before, got)
	}

	// The worker's pane settles idle, delivered the way a sweep delivers it:
	// through the record's own Observe, which is what the composition root's
	// bridge calls.
	p := participantRowInStand(t, w)
	for range 2 {
		if err := w.record.Observe(context.Background(), p.ID, p.Liveness, workers.ObservedIdle); err != nil {
			t.Fatalf("observe idle: %v", err)
		}
	}
	page := readOwnInbox(t, w)
	if len(*page.Messages) != 3 {
		t.Fatalf("the mailbox holds %d reports, want the three still there: %+v",
			len(*page.Messages), *page.Messages)
	}
	if len(*page.Observations) != 1 {
		t.Fatalf("the idle that followed the reports arrived as %+v, want one settled idle",
			*page.Observations)
	}
	observed := (*page.Observations)[0]
	if observed.State != string(workers.ObservedIdle) {
		t.Fatalf("the observation is %+v, want the settled idle", observed)
	}
	if observed.Worker != string(w.participant) {
		t.Fatalf("the observation is about %q, want this coordinator's own worker %q",
			observed.Worker, w.participant)
	}
	if got := reportedState(t, w); got != before {
		t.Fatalf("the observation moved the record from %q to %q", before, got)
	}
}

// participantRowInStand asks the record for the stand's worker, so an
// observation is delivered with the LIVENESS the record holds rather than with
// one this test invented — a stale incarnation is refused, and that refusal
// would be the test's fault rather than the product's.
func participantRowInStand(t *testing.T, w workerWorkerSetup) workers.Participant {
	t.Helper()
	held, err := w.record.HeldBy(context.Background(), string(w.coordinator))
	if err != nil {
		t.Fatalf("holdings: %v", err)
	}
	for _, p := range held {
		if p.ID == w.participant {
			return p
		}
	}
	t.Fatalf("the worker %q is not in the record: %+v", w.participant, held)
	return workers.Participant{}
}

// Criterion 4 at the composition level, and the direction that matters: the call
// IS offered to a coordinator — its grant carries the workspace scope the
// declaration names — and it is REFUSED, because the run is not a participant.
// The refusal is what an agent reads, so its SENTENCE is asserted rather than
// only its code.
func TestACoordinatorIsRefusedAWorkersOwnReportWithASentenceItCanActOn(t *testing.T) {
	reg, _, grid := prepareGroupCaller(t)
	record, _ := newGroupTwoCallersRecord()
	socket := publishGroupEndpoint(t, reg, grid, record, newSharedToolDispatcher(t, record))

	response := callExternally(t, socket, "workers.report", `{"kind":"done","text":"from a coordinator"}`)
	if response.Error == nil {
		t.Fatalf("a coordinator made a worker's own call: %s", response.Result)
	}
	if response.Error.Code != workerRPCDomainError {
		t.Fatalf("refusal = %d %q, want the domain refusal %d",
			response.Error.Code, response.Error.Message, workerRPCDomainError)
	}
	// The reason rides `data.reason` (toolendpoint's rpcErrorData), which is the
	// only part the MCP adapter shows the model — so it is decoded by name
	// rather than read as an opaque any.
	encoded, err := json.Marshal(response.Error.Data)
	if err != nil {
		t.Fatalf("re-encode refusal data: %v", err)
	}
	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("decode refusal data %s: %v", encoded, err)
	}
	if data.Reason == "" {
		t.Fatalf("the refusal carries no reason, which is the only part an agent acts on: %+v", response.Error)
	}
	// WHAT THE AGENT READS: what this session is, and what to call instead. An
	// agent told only "refused" retries; one told nothing about workers.inbox
	// waits for a report that has already arrived.
	reason := data.Reason
	for _, want := range []string{"worker", "workers.inbox"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("the refusal does not mention %q: %q", want, reason)
		}
	}
}

// Criterion 5 at the composition level: there is no argument a worker can pass
// to reach another mailbox. It is asserted OVER THE SOCKET rather than against
// the schema, because `additionalProperties: false` is what turns "I sent a
// worker field" into a refusal rather than into a silently ignored field that
// leaves the caller believing it addressed somebody.
func TestAWorkerCannotNameARecipientOverTheSocket(t *testing.T) {
	w := prepareGroupWorkerSetup(t)
	for _, params := range []string{
		`{"kind":"done","text":"x","worker":"` + string(w.coordinator) + `"}`,
		`{"kind":"done","text":"x","to":"` + string(w.coordinator) + `"}`,
		`{"kind":"done","text":"x","coordinator":"` + string(w.coordinator) + `"}`,
	} {
		t.Run(params, func(t *testing.T) {
			response := callExternally(t, w.socket, "workers.report", params)
			if response.Error == nil {
				t.Fatalf("a report naming a recipient was accepted: %s", response.Result)
			}
			if read := readOwnInbox(t, w); len(*read.Messages) != 0 {
				t.Fatalf("a refused report reached the mailbox anyway: %+v", *read.Messages)
			}
		})
	}

	// The positive control, so the three refusals above are attributable to the
	// unknown field rather than to the call being refused for some other reason.
	if response := callExternally(t, w.socket, "workers.report",
		`{"kind":"done","text":"no recipient named"}`); response.Error != nil {
		t.Fatalf("the same call WITHOUT a recipient was refused too: %+v", response.Error)
	}
	read := readOwnInbox(t, w)
	if len(*read.Messages) != 1 {
		t.Fatalf("the accepted report did not reach the mailbox: %+v", *read.Messages)
	}
}

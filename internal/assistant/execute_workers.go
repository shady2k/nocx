package assistant

// The coordinator's two calls (nocx-dkawo.8).
//
// §7.2 of the orchestration mechanism design names five: one spawn primitive,
// say to a participant, report structurally, check the inbox, and ask what my
// session holds. Two of them are here — spawn and holdings — and the other
// three need more than one worker to mean anything, so they arrive with
// fan-out rather than being written now against nothing.
//
// NOTHING RESTS ON EITHER CALL. The backend holds the record and watches the
// workers whether or not the coordinator ever calls back: a coordinator that
// goes quiet loses its own promptness and nothing else. That is what makes
// these a convenience over an invariant rather than the mechanism, and it is
// the whole difference from the lease this design started with.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/workers"
)

// WorkerRecord is the assistant's seam onto the worker record (AD-8): start one
// worker, and say what this session holds. The assistant depends on this and
// not on internal/worker's registrar, so a run can be tested against a double
// that never opens a pane.
type WorkerRecord interface {
	Register(ctx context.Context, req workers.RegisterRequest) (workers.Registration, error)
	HeldBy(ctx context.Context, coordinatorSession string) ([]workers.Participant, error)
	// Say commits one message into a participant's mailbox. The sender is
	// passed in and never taken from the arguments: a sender a model could
	// name is a sender a model could forge.
	Say(ctx context.Context, id workers.ID, from, to workers.ReaderID, body string) (workers.Message, error)
	// Inbox hands this reader its next page and advances its own cursor.
	Inbox(ctx context.Context, mailbox, reader workers.ReaderID, limit int) (workers.Fetch, error)
	// Undelivered is what this worker's mailboxes hold that their recipients
	// have not taken.
	Undelivered(ctx context.Context, id workers.ID) ([]workers.Message, error)
	// Acknowledge records that this reader finished committing the effects
	// of everything through a sequence. It is what stops a retry of one
	// response committing the same spawn twice.
	Acknowledge(ctx context.Context, mailbox, reader workers.ReaderID, through int64) error
	// Wait blocks until this session has something to be told, then answers
	// what HeldBy answers. It dispatches, exactly as HeldBy does.
	Wait(ctx context.Context, coordinatorSession string, id workers.ID) ([]workers.Participant, error)
	// Close ends a participant. It writes no state: the exit it causes
	// reaches the record by the ordinary path.
	Close(ctx context.Context, coordinatorSession string, id workers.ParticipantID) error
	// Screen reads what a participant's pane is showing, for the session
	// that holds it (nocx-f545a.6). It writes nothing and keeps nothing.
	Screen(ctx context.Context, coordinatorSession string, id workers.ParticipantID) (workers.PaneScreen, error)
	// Answer answers a participant's menu by naming one of its options
	// (nocx-f545a.4). It writes nothing into the record.
	Answer(ctx context.Context, coordinatorSession string, id workers.ParticipantID, option string) (workers.PaneAnswer, error)
	// Undispatched is what the record still owes judgement on. It is read
	// BEFORE HeldBy, because HeldBy is the fetch that clears it (D8): asking
	// afterwards would always answer nothing, which is a truthful answer to
	// the wrong question.
	Undispatched() []workers.Fact
}

// workerParticipantResult is one row of what a coordinator is told. It restates
// the record's vocabulary and never invents one: the states are the record's
// own words, so what the model reads and what the store holds cannot drift.
type workerParticipantResult struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Task    string `json:"task"`
	Summary string `json:"summary,omitempty"`
	// NeedsJudgement marks a worker something happened to that this
	// coordinator has not been told about AND that the routing table decided
	// needs a decision. It is what makes the wake actionable: nocx types
	// "call workers.holdings", and this is what distinguishes the worker it was
	// about from the four that have not moved.
	//
	// A routine completion is deliberately NOT marked — a worker finishing
	// while others still run does not need the coordinator, which is the
	// whole of nocx-dkawo.4's table. Omitted rather than false, so a list
	// where nothing needs deciding reads as nothing needing deciding.
	NeedsJudgement bool `json:"needsJudgement,omitempty"`
}

// workerMailResult is one message the coordinator is handed. It carries the
// sender and the body and nothing else: a message is CONTENT, and a shape
// with an id or a cursor in it would invite the model to think it had
// something to acknowledge, which is a mark the backend advanced for it.
type workerMailResult struct {
	From    string `json:"from"`
	Message string `json:"message"`
}

type workerHoldingsResult struct {
	// Mail rides this call, which is D7: mail rides our own calls rather
	// than being pushed through a hook onto the result of any tool the model
	// happens to have called. The coordinator asks what it holds and is told
	// what was said to it in the same breath, because those are one question
	// — "what has happened since I last looked".
	Mail []workerMailResult `json:"mail,omitempty"`
	// Cursor is where this reader has been read up to. It travels with the
	// mail because §7.2 requires the reader to acknowledge a position
	// TOGETHER WITH the effects it commits from that response: a reader
	// handed messages and no position could only acknowledge by guessing.
	Cursor          int64                     `json:"cursor,omitempty"`
	UndeliveredMail int                       `json:"undeliveredMail,omitempty"`
	Participants    []workerParticipantResult `json:"participants"`
}

type workerHoldingsParams struct {
	// Acknowledge is the cursor from an earlier answer, sent back once the
	// coordinator has finished committing that answer's effects. It is
	// SEPARATE from the fetch on purpose: D8 keeps the four
	// acknowledgements apart, and a fetch that also claimed the fourth
	// would be the record asserting something only the reader can know.
	Acknowledge int64 `json:"acknowledge,omitempty"`
}

// workerWaitParams is holdings' parameters plus a bound. It is a separate
// struct and not holdings' own because the two tools have different costs and
// a person reading the declarations should see that; what they SHARE is the
// answer, and that is one struct.
type workerWaitParams struct {
	Seconds     int   `json:"seconds,omitempty"`
	Acknowledge int64 `json:"acknowledge,omitempty"`
}

// defaultWorkerWait is how long a wait holds when the coordinator names no
// bound. It is a bound on a TURN the coordinator chose to spend, not on the
// supervision — the record watches the workers regardless — so it is generous
// enough to cover an ordinary piece of work and short enough that a
// coordinator is not parked past the point where a person would look.
const defaultWorkerWait = 120 * time.Second

type workerCloseParams struct {
	Worker string `json:"worker"`
}

type workerScreenParams struct {
	Worker string `json:"worker"`
}

// workerScreenResult is a held worker's pane, as rows of text (ADR-0064 §2).
// Rows is always present, and empty when the pane cannot be read, so a
// coordinator never has to tell an absent field from an empty screen.
type workerScreenResult struct {
	Worker   string   `json:"worker"`
	Readable bool     `json:"readable"`
	State    string   `json:"state,omitempty"`
	Rows     []string `json:"rows"`
}

type workerAnswerParams struct {
	Worker string `json:"worker"`
	Option string `json:"option"`
}

// workerAnswerResult is what became of an answer. A refusal at the typing
// gate is a RESULT — outcome refused, with its reason — for the reason
// agent.type answers one: a refusal is an answer a caller acts on, not a fault.
type workerAnswerResult struct {
	Worker  string `json:"worker"`
	Outcome string `json:"outcome"`
	State   string `json:"state,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type workerCloseResult struct {
	ID    string `json:"id"`
	Ended bool   `json:"ended"`
}

type workerSayParams struct {
	Worker  string `json:"worker"`
	Message string `json:"message"`
}

type workerSayResult struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

type workerSpawnParams struct {
	Command string `json:"command"`
	Task    string `json:"task"`
}

type workerSpawnResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// TaskTyped and WaitingOn are what became of the task (nocx-f545a.3). A
	// worker whose pane stopped on a question of its agent's own is still a
	// worker — live, supervised, with its tab — and the coordinator is told
	// that its task was not typed and what the pane is waiting on, rather
	// than being handed an error for a pane nocx read perfectly.
	TaskTyped bool   `json:"taskTyped"`
	WaitingOn string `json:"waitingOn,omitempty"`
}

func workerCoordinatorFrom(cap agenttools.Capability, tool string) (*agenttools.WorkerCoordinator, error) {
	c, ok := cap.(*agenttools.WorkerCoordinator)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not *agenttools.WorkerCoordinator", tool, cap)
	}
	if c.Session() == "" {
		// A coordinator with no session cannot be answered about, and
		// answering an empty holdings would be indistinguishable from a
		// coordinator that holds nothing.
		return nil, fmt.Errorf("%s: this run has no session to answer about", tool)
	}
	return c, nil
}

// workerParticipantFrom is the other half of the type switch A8 asks for. It
// exists beside workerCoordinatorFrom rather than inside it because the two
// capabilities are two types: a function that accepted either and returned a
// role would be the boolean the design rejected, one refactor from being read
// wrong.
func workerParticipantFrom(cap agenttools.Capability, tool string) (*agenttools.WorkerParticipant, error) {
	p, ok := cap.(*agenttools.WorkerParticipant)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not *agenttools.WorkerParticipant", tool, cap)
	}
	if p.Mailbox() == "" {
		// A participant with no mailbox is a caller the authorizer did not
		// establish as a worker. Answering it an empty inbox would be
		// indistinguishable from a worker whose coordinator has said nothing.
		return nil, fmt.Errorf("%s: this run is not a worker participant", tool)
	}
	return p, nil
}

type workerInboxParams struct {
	Acknowledge int64 `json:"acknowledge"`
}

type workerInboxResult struct {
	Messages []workerMailResult `json:"messages"`
	Cursor   int64              `json:"cursor"`
	More     bool               `json:"more"`
}

// executeWorkerInbox hands a worker the mail its coordinator left it.
//
// This is the reader workers.say was always writing for. Until it existed the
// coordinator could commit a message into a mailbox nothing could open — a
// writer with no reader, which is a soft degrade visible nowhere (nocx-rowqt.9).
//
// It names no mailbox. The box is the participant's own id, taken from the
// capability, so a worker has no way to EXPRESS another worker's mail — A9's
// rule, and the reason this is a property of the type rather than of a check.
func executeWorkerInbox(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	participant, err := workerParticipantFrom(cap, "workers.inbox")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.inbox: this backend keeps no worker record")
	}
	var p workerInboxParams
	if len(args) > 0 {
		if argErr := json.Unmarshal(args, &p); argErr != nil {
			return "", fmt.Errorf("workers.inbox: %w", argErr)
		}
	}
	box := workers.ReaderID(participant.Mailbox())
	// Acknowledge BEFORE fetching, for the coordinator's reason: the mark
	// being sent back is about the PREVIOUS answer, and doing it after would
	// let this call's own page slide under an acknowledgement of mail the
	// worker has not seen yet.
	if p.Acknowledge > 0 {
		if ackErr := seams.workerStore.Acknowledge(ctx, box, box, p.Acknowledge); ackErr != nil {
			return "", fmt.Errorf("workers.inbox: acknowledge: %w", ackErr)
		}
	}
	fetched, err := seams.workerStore.Inbox(ctx, box, box, 0)
	if err != nil {
		return "", fmt.Errorf("workers.inbox: %w", err)
	}
	out := workerInboxResult{Messages: []workerMailResult{}, Cursor: fetched.Cursor.Fetched, More: fetched.More}
	for _, m := range fetched.Messages {
		out.Messages = append(out.Messages, workerMailResult{From: string(m.Sender), Message: m.Body})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("workers.inbox: result: %w", err)
	}
	return string(raw), nil
}

// executeWorkerHoldings answers D3: a coordinator asks what its SESSION holds
// and is told by name.
//
// It asks the session and not the run, because the run that spawned the worker
// has ended by the time this question matters — that is the entire situation it
// exists for. An empty list is an honest and ordinary answer.
func executeWorkerHoldings(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.holdings")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.holdings: this backend keeps no worker record")
	}
	var p workerHoldingsParams
	if len(args) > 0 {
		if argErr := json.Unmarshal(args, &p); argErr != nil {
			return "", fmt.Errorf("workers.holdings: %w", argErr)
		}
	}
	return workerAnswer(ctx, "workers.holdings", coordinator, seams, p.Acknowledge, nil)
}

// executeWorkerWait holds the coordinator's turn until its worker has something
// to say, and then answers exactly what holdings answers.
//
// The two share one answer because they are one question asked at two
// moments. A wait with a shape of its own would be a second account of what a
// session holds, and the two would disagree the first time either moved.
//
// NOTHING RESTS ON IT (§7.2). The backend watches the workers whether this is
// ever called or not; a coordinator that never waits loses its own promptness
// and nothing else, which is the whole difference from the blocking call and
// then the lease this design started with.
func executeWorkerWait(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.wait")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.wait: this backend keeps no worker record")
	}
	var p workerWaitParams
	if len(args) > 0 {
		if argErr := json.Unmarshal(args, &p); argErr != nil {
			return "", fmt.Errorf("workers.wait: %w", argErr)
		}
	}
	hold := defaultWorkerWait
	if p.Seconds > 0 {
		hold = time.Duration(p.Seconds) * time.Second
	}
	return workerAnswer(ctx, "workers.wait", coordinator, seams, p.Acknowledge,
		func(ctx context.Context, id workers.ID) ([]workers.Participant, error) {
			// The bound is this call's own. An expired wait is an ANSWER,
			// so the deadline is spent inside Wait and never surfaces as an
			// error here.
			waitCtx, cancel := context.WithTimeout(ctx, hold)
			defer cancel()
			return seams.workerStore.Wait(waitCtx, coordinator.Session(), id)
		})
}

// workerAnswer builds the answer both calls give.
//
// fetch is how the participants are read: holdings reads them now, a wait
// reads them when there is something to read. Everything after that — what is
// new, the mail, the cursor, what nobody took — is identical, because it is
// the same question.
func workerAnswer(
	ctx context.Context,
	tool string,
	coordinator *agenttools.WorkerCoordinator,
	seams toolSeams,
	acknowledge int64,
	fetch func(context.Context, workers.ID) ([]workers.Participant, error),
) (string, error) {
	// Read what is owed BEFORE the fetch, because the fetch is what clears
	// it. The other order would answer this question with the record's state
	// after it had been answered, which is always "nothing new".
	owed := make(map[workers.ParticipantID]bool)
	for _, f := range seams.workerStore.Undispatched() {
		owed[f.Participant] = true
	}
	var held []workers.Participant
	var err error
	if fetch == nil {
		held, err = seams.workerStore.HeldBy(ctx, coordinator.Session())
	} else {
		held, err = fetch(ctx, workers.ID(coordinator.Session()))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	out := workerHoldingsResult{Participants: make([]workerParticipantResult, 0, len(held))}
	for _, p := range held {
		row := workerParticipantResult{
			ID: string(p.ID), State: string(p.State), Task: p.Task,
			NeedsJudgement: owed[p.ID],
		}
		if p.Declared != nil {
			row.Summary = p.Declared.Summary
		}
		out.Participants = append(out.Participants, row)
	}
	// The coordinator's own mailbox is named by its session, which is what
	// makes a RESTARTED coordinator the same reader — the property D3
	// already rests on. Asking is what hands the mail over: the cursor
	// advances on the fetch, and the response says so by carrying what was
	// handed and nothing about what remains behind it.
	box := workers.ReaderID(coordinator.Session())
	// Acknowledge BEFORE fetching. The mark being sent back is about the
	// PREVIOUS answer, and doing it after would let this call's own page
	// slide under an acknowledgement the coordinator made about mail it has
	// not seen yet.
	if acknowledge > 0 {
		if ackErr := seams.workerStore.Acknowledge(ctx, box, box, acknowledge); ackErr != nil {
			return "", fmt.Errorf("%s: acknowledge: %w", tool, ackErr)
		}
	}
	fetched, err := seams.workerStore.Inbox(ctx, box, box, 0)
	if err != nil {
		return "", fmt.Errorf("%s: mail: %w", tool, err)
	}
	for _, m := range fetched.Messages {
		out.Mail = append(out.Mail, workerMailResult{From: string(m.Sender), Message: m.Body})
	}
	out.Cursor = fetched.Cursor.Fetched
	// And what the coordinator itself has said that nobody took. A worker
	// that never looks is a worker that never got the instruction, and this
	// is the only place that difference is visible.
	unread, err := seams.workerStore.Undelivered(ctx, workerOf(coordinator, held))
	if err != nil {
		return "", fmt.Errorf("%s: undelivered mail: %w", tool, err)
	}
	for _, m := range unread {
		if m.Sender == box {
			out.UndeliveredMail++
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("%s: result: %w", tool, err)
	}
	return string(raw), nil
}

// executeWorkerClose ends one worker.
//
// It answers what was ASKED of the worker and never that the worker has
// finished: ending a process is a request, and how it ended is a fact nocx
// observes for itself through the ordinary exit path. A result that claimed
// the second would be the record's only claim it did not witness.
func executeWorkerClose(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.close")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.close: this backend keeps no worker record")
	}
	var p workerCloseParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.close: %w", argErr)
	}
	if p.Worker == "" {
		return "", errors.New("workers.close: name the worker to end")
	}
	if closeErr := seams.workerStore.Close(ctx, coordinator.Session(), workers.ParticipantID(p.Worker)); closeErr != nil {
		return "", fmt.Errorf("workers.close: %w", closeErr)
	}
	raw, err := json.Marshal(workerCloseResult{ID: p.Worker, Ended: true})
	if err != nil {
		return "", fmt.Errorf("workers.close: result: %w", err)
	}
	return string(raw), nil
}

// executeWorkerScreen shows a coordinator one of its workers' panes
// (nocx-f545a.6).
//
// It exists because what nocx CONCLUDED about a pane is exactly the thing that
// can be wrong: a coordinator told its worker is waiting on a question has no
// way to notice nocx misread the screen, and a pane nocx reads as `unknown` is
// the one it cannot describe at all. The screen is the evidence behind the
// verdict. Authority is the record's — the same ownership and delegation
// questions workers.close asks — and nothing read here is kept.
func executeWorkerScreen(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.screen")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.screen: this backend keeps no worker record")
	}
	var p workerScreenParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.screen: %w", argErr)
	}
	if p.Worker == "" {
		return "", errors.New("workers.screen: name the worker whose pane to read")
	}
	screen, err := seams.workerStore.Screen(ctx, coordinator.Session(), workers.ParticipantID(p.Worker))
	if err != nil {
		return "", fmt.Errorf("workers.screen: %w", err)
	}
	rows := screen.Rows
	if rows == nil {
		rows = []string{}
	}
	raw, err := json.Marshal(workerScreenResult{
		Worker: p.Worker, Readable: screen.Readable, State: screen.State, Rows: rows,
	})
	if err != nil {
		return "", fmt.Errorf("workers.screen: result: %w", err)
	}
	return string(raw), nil
}

// executeWorkerAnswer answers one of a coordinator's workers' menus by naming
// an option as the screen drew it (nocx-f545a.4, ADR-0064 §1).
//
// Authority is the record's — ownership, and a delegation that still permits
// send-input — and what may be written is the typing gate's: the keys that
// menu offers, decided from a frame read at the moment of each key.
func executeWorkerAnswer(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.answer")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.answer: this backend keeps no worker record")
	}
	var p workerAnswerParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.answer: %w", argErr)
	}
	if p.Worker == "" || p.Option == "" {
		return "", errors.New("workers.answer: name the worker, and the option to choose exactly as its screen shows it")
	}
	answer, err := seams.workerStore.Answer(ctx, coordinator.Session(), workers.ParticipantID(p.Worker), p.Option)
	if err != nil {
		return "", fmt.Errorf("workers.answer: %w", err)
	}
	raw, err := json.Marshal(workerAnswerResult{
		Worker: p.Worker, Outcome: answer.Outcome, State: answer.State, Reason: answer.Reason,
	})
	if err != nil {
		return "", fmt.Errorf("workers.answer: result: %w", err)
	}
	return string(raw), nil
}

// workerOf names the worker a coordinator's holdings belong to.
//
// It is read off the participants rather than assumed to equal the session,
// even though a coordinator's first spawn does default the worker id to its
// session: the record permits a named worker, and a helper that assumed the
// default would be right until the day somebody used the field.
func workerOf(c *agenttools.WorkerCoordinator, held []workers.Participant) workers.ID {
	for _, p := range held {
		if p.Group != "" {
			return p.Group
		}
	}
	return workers.ID(c.Session())
}

// executeWorkerSay leaves a message in a worker's mailbox.
//
// It does not interrupt and does not make anybody read. That is the whole
// difference from typing into a pane: §7.3 says the coordinator-to-worker
// direction is a wait for the worker's next call, and this is that wait made
// addressable rather than pretended away.
func executeWorkerSay(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.say")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.say: this backend keeps no worker record")
	}
	var p workerSayParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.say: %w", argErr)
	}
	if p.Worker == "" || p.Message == "" {
		return "", errors.New("workers.say: a message needs a worker to leave it for and something to say")
	}
	// The worker is read from what this session holds, so a worker in somebody
	// else's worker is refused by membership rather than by a check here: one
	// owner of "may these two exchange mail", and it is the record's.
	held, err := seams.workerStore.HeldBy(ctx, coordinator.Session())
	if err != nil {
		return "", fmt.Errorf("workers.say: %w", err)
	}
	m, err := seams.workerStore.Say(ctx, workerOf(coordinator, held),
		workers.ReaderID(coordinator.Session()), workers.ReaderID(p.Worker), p.Message)
	if err != nil {
		return "", fmt.Errorf("workers.say: %w", err)
	}
	raw, err := json.Marshal(workerSayResult{ID: string(m.ID), Seq: m.Seq})
	if err != nil {
		return "", fmt.Errorf("workers.say: result: %w", err)
	}
	return string(raw), nil
}

// executeWorkerSpawn starts one worker and returns only when it is LIVE.
//
// Live is not the same as told. A worker whose pane stopped on a question of
// its agent's own before it could take its task is live and untold, and the
// result says which (nocx-f545a.3): the alternative, an error, would describe
// a pane nocx read perfectly as one it could not start.
//
// Live means its enrolment arrived, which is what proves the agent started —
// never that this call returned. A spawn that did not reach live is an error
// and not a result, so there is no half-answer for a coordinator to
// misinterpret as a worker it can address.
func executeWorkerSpawn(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.spawn")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.spawn: this backend keeps no worker record")
	}
	var p workerSpawnParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.spawn: %w", argErr)
	}
	if p.Command == "" || p.Task == "" {
		return "", errors.New("workers.spawn: a worker needs both a command to start it and a task to do")
	}
	// The environment is checked against the CAPABILITY, which holds only
	// what the run's grant named. A spawn outside it is refused and the
	// refusal names what was available; escalating instead is a property of a
	// policy row rather than a special case for one tool.
	environment := seams.workerEnvironment
	if !coordinator.MaySpawnInto(environment) {
		return "", fmt.Errorf("workers.spawn: this run may not start a worker in %q; it may start one in %v",
			environment, coordinator.Environments())
	}
	participant, err := seams.workerStore.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: coordinator.Session(),
		Role:               workers.RoleWorker,
		Task:               p.Task,
		Command:            p.Command,
		Environment:        environment,
		CreatedByRunID:     seams.runID,
	})
	if err != nil {
		return "", fmt.Errorf("workers.spawn: %w", err)
	}
	raw, err := json.Marshal(workerSpawnResult{
		ID:        string(participant.ID),
		State:     string(participant.State),
		TaskTyped: participant.Delivery.Typed,
		WaitingOn: participant.Delivery.WaitingOn,
	})
	if err != nil {
		return "", fmt.Errorf("workers.spawn: result: %w", err)
	}
	return string(raw), nil
}

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
	"github.com/shady2k/nocx/internal/wave"
)

// WaveRecord is the assistant's seam onto the wave record (AD-8): start one
// worker, and say what this session holds. The assistant depends on this and
// not on internal/wave's registrar, so a run can be tested against a double
// that never opens a pane.
type WaveRecord interface {
	Register(ctx context.Context, req wave.RegisterRequest) (wave.Participant, error)
	HeldBy(ctx context.Context, coordinatorSession string) ([]wave.Participant, error)
	// Say commits one message into a participant's mailbox. The sender is
	// passed in and never taken from the arguments: a sender a model could
	// name is a sender a model could forge.
	Say(ctx context.Context, id wave.ID, from, to wave.ReaderID, body string) (wave.Message, error)
	// Inbox hands this reader its next page and advances its own cursor.
	Inbox(ctx context.Context, mailbox, reader wave.ReaderID, limit int) (wave.Fetch, error)
	// Undelivered is what this wave's mailboxes hold that their recipients
	// have not taken.
	Undelivered(ctx context.Context, id wave.ID) ([]wave.Message, error)
	// Acknowledge records that this reader finished committing the effects
	// of everything through a sequence. It is what stops a retry of one
	// response committing the same spawn twice.
	Acknowledge(ctx context.Context, mailbox, reader wave.ReaderID, through int64) error
	// Wait blocks until this session has something to be told, then answers
	// what HeldBy answers. It dispatches, exactly as HeldBy does.
	Wait(ctx context.Context, coordinatorSession string, id wave.ID) ([]wave.Participant, error)
	// Close ends a participant. It writes no state: the exit it causes
	// reaches the record by the ordinary path.
	Close(ctx context.Context, coordinatorSession string, id wave.ParticipantID) error
	// Undispatched is what the record still owes judgement on. It is read
	// BEFORE HeldBy, because HeldBy is the fetch that clears it (D8): asking
	// afterwards would always answer nothing, which is a truthful answer to
	// the wrong question.
	Undispatched() []wave.Fact
}

// waveParticipantResult is one row of what a coordinator is told. It restates
// the record's vocabulary and never invents one: the states are the record's
// own words, so what the model reads and what the store holds cannot drift.
type waveParticipantResult struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Task    string `json:"task"`
	Summary string `json:"summary,omitempty"`
	// NeedsJudgement marks a worker something happened to that this
	// coordinator has not been told about AND that the routing table decided
	// needs a decision. It is what makes the wake actionable: nocx types
	// "call wave.holdings", and this is what distinguishes the worker it was
	// about from the four that have not moved.
	//
	// A routine completion is deliberately NOT marked — a worker finishing
	// while others still run does not need the coordinator, which is the
	// whole of nocx-dkawo.4's table. Omitted rather than false, so a list
	// where nothing needs deciding reads as nothing needing deciding.
	NeedsJudgement bool `json:"needsJudgement,omitempty"`
}

// waveMailResult is one message the coordinator is handed. It carries the
// sender and the body and nothing else: a message is CONTENT, and a shape
// with an id or a cursor in it would invite the model to think it had
// something to acknowledge, which is a mark the backend advanced for it.
type waveMailResult struct {
	From    string `json:"from"`
	Message string `json:"message"`
}

type waveHoldingsResult struct {
	// Mail rides this call, which is D7: mail rides our own calls rather
	// than being pushed through a hook onto the result of any tool the model
	// happens to have called. The coordinator asks what it holds and is told
	// what was said to it in the same breath, because those are one question
	// — "what has happened since I last looked".
	Mail []waveMailResult `json:"mail,omitempty"`
	// Cursor is where this reader has been read up to. It travels with the
	// mail because §7.2 requires the reader to acknowledge a position
	// TOGETHER WITH the effects it commits from that response: a reader
	// handed messages and no position could only acknowledge by guessing.
	Cursor          int64                   `json:"cursor,omitempty"`
	UndeliveredMail int                     `json:"undeliveredMail,omitempty"`
	Participants    []waveParticipantResult `json:"participants"`
}

type waveHoldingsParams struct {
	// Acknowledge is the cursor from an earlier answer, sent back once the
	// coordinator has finished committing that answer's effects. It is
	// SEPARATE from the fetch on purpose: D8 keeps the four
	// acknowledgements apart, and a fetch that also claimed the fourth
	// would be the record asserting something only the reader can know.
	Acknowledge int64 `json:"acknowledge,omitempty"`
}

// waveWaitParams is holdings' parameters plus a bound. It is a separate
// struct and not holdings' own because the two tools have different costs and
// a person reading the declarations should see that; what they SHARE is the
// answer, and that is one struct.
type waveWaitParams struct {
	Seconds     int   `json:"seconds,omitempty"`
	Acknowledge int64 `json:"acknowledge,omitempty"`
}

// defaultWaveWait is how long a wait holds when the coordinator names no
// bound. It is a bound on a TURN the coordinator chose to spend, not on the
// supervision — the record watches the workers regardless — so it is generous
// enough to cover an ordinary piece of work and short enough that a
// coordinator is not parked past the point where a person would look.
const defaultWaveWait = 120 * time.Second

type waveCloseParams struct {
	Worker string `json:"worker"`
}

type waveCloseResult struct {
	ID    string `json:"id"`
	Ended bool   `json:"ended"`
}

type waveSayParams struct {
	Worker  string `json:"worker"`
	Message string `json:"message"`
}

type waveSayResult struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

type waveSpawnParams struct {
	Command string `json:"command"`
	Task    string `json:"task"`
}

type waveSpawnResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func waveCoordinatorFrom(cap agenttools.Capability, tool string) (*agenttools.WaveCoordinator, error) {
	c, ok := cap.(*agenttools.WaveCoordinator)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not *agenttools.WaveCoordinator", tool, cap)
	}
	if c.Session() == "" {
		// A coordinator with no session cannot be answered about, and
		// answering an empty holdings would be indistinguishable from a
		// coordinator that holds nothing.
		return nil, fmt.Errorf("%s: this run has no session to answer about", tool)
	}
	return c, nil
}

// executeWaveHoldings answers D3: a coordinator asks what its SESSION holds
// and is told by name.
//
// It asks the session and not the run, because the run that spawned the worker
// has ended by the time this question matters — that is the entire situation it
// exists for. An empty list is an honest and ordinary answer.
func executeWaveHoldings(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.holdings")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.holdings: this backend keeps no wave record")
	}
	var p waveHoldingsParams
	if len(args) > 0 {
		if argErr := json.Unmarshal(args, &p); argErr != nil {
			return "", fmt.Errorf("wave.holdings: %w", argErr)
		}
	}
	return waveAnswer(ctx, "wave.holdings", coordinator, seams, p.Acknowledge, nil)
}

// executeWaveWait holds the coordinator's turn until its wave has something
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
func executeWaveWait(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.wait")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.wait: this backend keeps no wave record")
	}
	var p waveWaitParams
	if len(args) > 0 {
		if argErr := json.Unmarshal(args, &p); argErr != nil {
			return "", fmt.Errorf("wave.wait: %w", argErr)
		}
	}
	hold := defaultWaveWait
	if p.Seconds > 0 {
		hold = time.Duration(p.Seconds) * time.Second
	}
	return waveAnswer(ctx, "wave.wait", coordinator, seams, p.Acknowledge,
		func(ctx context.Context, id wave.ID) ([]wave.Participant, error) {
			// The bound is this call's own. An expired wait is an ANSWER,
			// so the deadline is spent inside Wait and never surfaces as an
			// error here.
			waitCtx, cancel := context.WithTimeout(ctx, hold)
			defer cancel()
			return seams.waves.Wait(waitCtx, coordinator.Session(), id)
		})
}

// waveAnswer builds the answer both calls give.
//
// fetch is how the participants are read: holdings reads them now, a wait
// reads them when there is something to read. Everything after that — what is
// new, the mail, the cursor, what nobody took — is identical, because it is
// the same question.
func waveAnswer(
	ctx context.Context,
	tool string,
	coordinator *agenttools.WaveCoordinator,
	seams toolSeams,
	acknowledge int64,
	fetch func(context.Context, wave.ID) ([]wave.Participant, error),
) (string, error) {
	// Read what is owed BEFORE the fetch, because the fetch is what clears
	// it. The other order would answer this question with the record's state
	// after it had been answered, which is always "nothing new".
	owed := make(map[wave.ParticipantID]bool)
	for _, f := range seams.waves.Undispatched() {
		owed[f.Participant] = true
	}
	var held []wave.Participant
	var err error
	if fetch == nil {
		held, err = seams.waves.HeldBy(ctx, coordinator.Session())
	} else {
		held, err = fetch(ctx, wave.ID(coordinator.Session()))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	out := waveHoldingsResult{Participants: make([]waveParticipantResult, 0, len(held))}
	for _, p := range held {
		row := waveParticipantResult{
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
	box := wave.ReaderID(coordinator.Session())
	// Acknowledge BEFORE fetching. The mark being sent back is about the
	// PREVIOUS answer, and doing it after would let this call's own page
	// slide under an acknowledgement the coordinator made about mail it has
	// not seen yet.
	if acknowledge > 0 {
		if ackErr := seams.waves.Acknowledge(ctx, box, box, acknowledge); ackErr != nil {
			return "", fmt.Errorf("%s: acknowledge: %w", tool, ackErr)
		}
	}
	fetched, err := seams.waves.Inbox(ctx, box, box, 0)
	if err != nil {
		return "", fmt.Errorf("%s: mail: %w", tool, err)
	}
	for _, m := range fetched.Messages {
		out.Mail = append(out.Mail, waveMailResult{From: string(m.Sender), Message: m.Body})
	}
	out.Cursor = fetched.Cursor.Fetched
	// And what the coordinator itself has said that nobody took. A worker
	// that never looks is a worker that never got the instruction, and this
	// is the only place that difference is visible.
	unread, err := seams.waves.Undelivered(ctx, waveOf(coordinator, held))
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

// executeWaveClose ends one worker.
//
// It answers what was ASKED of the worker and never that the worker has
// finished: ending a process is a request, and how it ended is a fact nocx
// observes for itself through the ordinary exit path. A result that claimed
// the second would be the record's only claim it did not witness.
func executeWaveClose(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.close")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.close: this backend keeps no wave record")
	}
	var p waveCloseParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("wave.close: %w", argErr)
	}
	if p.Worker == "" {
		return "", errors.New("wave.close: name the worker to end")
	}
	if closeErr := seams.waves.Close(ctx, coordinator.Session(), wave.ParticipantID(p.Worker)); closeErr != nil {
		return "", fmt.Errorf("wave.close: %w", closeErr)
	}
	raw, err := json.Marshal(waveCloseResult{ID: p.Worker, Ended: true})
	if err != nil {
		return "", fmt.Errorf("wave.close: result: %w", err)
	}
	return string(raw), nil
}

// waveOf names the wave a coordinator's holdings belong to.
//
// It is read off the participants rather than assumed to equal the session,
// even though a coordinator's first spawn does default the wave id to its
// session: the record permits a named wave, and a helper that assumed the
// default would be right until the day somebody used the field.
func waveOf(c *agenttools.WaveCoordinator, held []wave.Participant) wave.ID {
	for _, p := range held {
		if p.Wave != "" {
			return p.Wave
		}
	}
	return wave.ID(c.Session())
}

// executeWaveSay leaves a message in a worker's mailbox.
//
// It does not interrupt and does not make anybody read. That is the whole
// difference from typing into a pane: §7.3 says the coordinator-to-worker
// direction is a wait for the worker's next call, and this is that wait made
// addressable rather than pretended away.
func executeWaveSay(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.say")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.say: this backend keeps no wave record")
	}
	var p waveSayParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("wave.say: %w", argErr)
	}
	if p.Worker == "" || p.Message == "" {
		return "", errors.New("wave.say: a message needs a worker to leave it for and something to say")
	}
	// The wave is read from what this session holds, so a worker in somebody
	// else's wave is refused by membership rather than by a check here: one
	// owner of "may these two exchange mail", and it is the record's.
	held, err := seams.waves.HeldBy(ctx, coordinator.Session())
	if err != nil {
		return "", fmt.Errorf("wave.say: %w", err)
	}
	m, err := seams.waves.Say(ctx, waveOf(coordinator, held),
		wave.ReaderID(coordinator.Session()), wave.ReaderID(p.Worker), p.Message)
	if err != nil {
		return "", fmt.Errorf("wave.say: %w", err)
	}
	raw, err := json.Marshal(waveSayResult{ID: string(m.ID), Seq: m.Seq})
	if err != nil {
		return "", fmt.Errorf("wave.say: result: %w", err)
	}
	return string(raw), nil
}

// executeWaveSpawn starts one worker and returns only when it is LIVE.
//
// Live means its enrolment arrived, which is what proves the agent started —
// never that this call returned. A spawn that did not reach live is an error
// and not a result, so there is no half-answer for a coordinator to
// misinterpret as a worker it can address.
func executeWaveSpawn(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := waveCoordinatorFrom(cap, "wave.spawn")
	if err != nil {
		return "", err
	}
	if seams.waves == nil {
		return "", errors.New("wave.spawn: this backend keeps no wave record")
	}
	var p waveSpawnParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("wave.spawn: %w", argErr)
	}
	if p.Command == "" || p.Task == "" {
		return "", errors.New("wave.spawn: a worker needs both a command to start it and a task to do")
	}
	// The environment is checked against the CAPABILITY, which holds only
	// what the run's grant named. A spawn outside it is refused and the
	// refusal names what was available; escalating instead is a property of a
	// policy row rather than a special case for one tool.
	environment := seams.waveEnvironment
	if !coordinator.MaySpawnInto(environment) {
		return "", fmt.Errorf("wave.spawn: this run may not start a worker in %q; it may start one in %v",
			environment, coordinator.Environments())
	}
	participant, err := seams.waves.Register(ctx, wave.RegisterRequest{
		CoordinatorSession: coordinator.Session(),
		Role:               wave.RoleWorker,
		Task:               p.Task,
		Command:            p.Command,
		Environment:        environment,
		CreatedByRunID:     seams.runID,
	})
	if err != nil {
		return "", fmt.Errorf("wave.spawn: %w", err)
	}
	raw, err := json.Marshal(waveSpawnResult{
		ID:    string(participant.ID),
		State: string(participant.State),
	})
	if err != nil {
		return "", fmt.Errorf("wave.spawn: result: %w", err)
	}
	return string(raw), nil
}

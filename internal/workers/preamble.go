package workers

// The rules nocx hands a worker before its task (nocx-luqz9.5; design §6;
// ADR-0070).
//
// # One text, and why it lives HERE rather than in the composition root
//
// A spawn now hands a worker two messages, in this order: these rules, and the
// task. They are one BRIEFING — the task is unreadable without the rules, and a
// worker that got the task alone has no way to say anything about it — so the
// text belongs to whoever owns what a worker is, and that is this package:
//
//   - The KINDS it names are this package's vocabulary (`KindDone`,
//     `KindQuestion`, `KindProgress`, and `reportKinds` which says which of
//     them a worker's tool may send). A copy of those words in the composition
//     root would be a second spelling of an enum the schema also carries, and
//     the day one of the three is renamed the other two go on saying the old
//     name to every worker in the fleet.
//   - It is the SECOND sentence nocx types into an agent's input box, and the
//     first — `wakeText` (wake.go), the line an idle coordinator is woken with
//     — already lives here for the same reason: what nocx SAYS to an agent
//     about the mailbox is a fact about the mailbox.
//   - internal/app is a composition root: it owns the joins nothing else can
//     make (worker_preamble_test.go is one of them — the tool NAMES in this
//     text are pinned against the assembled registry there, because that is
//     the only place both halves exist) and not the product's prose.
//
// # What the text owes the worker
//
// Three things, and each one is a defect when it is missing:
//
//   - WHO ITS COORDINATOR IS. A worker is a thing that reports, and it cannot
//     describe its own situation without knowing who is reading. It is the
//     session, which is how a coordinator is named everywhere else (AD-7): the
//     worker cannot ADDRESS it — nothing in a worker's grant reaches another
//     session — and naming it is not a way to reach it.
//   - THE CALLS IT HAS. `workers.report` with its three kinds and
//     `workers.inbox`. Named rather than described, and pinned by a test: a
//     worker sent to a tool it does not have learns nothing, and the failure
//     is silent — it simply never reports. The names are the ones the
//     declarations carry (internal/agenttools), and app's test refuses to let
//     this text name one the registry does not declare or the worker's own
//     grant does not offer.
//   - THE RULE THAT CANNOT BE GUESSED: A QUESTION DOES NOT BLOCK. Every tool
//     call you have ever made returns an answer; `workers.report` with
//     `question` returns as soon as it is recorded and the answer arrives as
//     the worker's next message. A worker that assumes an ask blocks holds its
//     turn open on a reply that is waiting for that turn to end — which is
//     exactly the deadlock this epic was filed from (nocx-9f1d4), so the
//     sentence is asserted on the text by a test rather than trusted to survive
//     an edit.
//
// # What it deliberately does NOT say
//
// No repository rules. Role rules taken from a repo file are deferred to
// nocx-k2csf.1: the preamble text lives in this code for now, and a second
// audience (a worker in a repo that teaches its own rules) is not something to
// guess at here. And no verdict: the text says in as many words that nocx
// records no success and no failure, because a worker that believes one exists
// reports one.

import (
	"errors"
	"strings"
)

// Briefing is what a worker is told, in the order it is told: the rules it
// reports under, and then the work.
//
// THE TWO TRAVEL TOGETHER, which is why this is one value and not two
// parameters. A registration hands its queue both, and the queue refuses half a
// briefing outright (Validate): the alternative is a worker whose task reached
// it while its rules did not, which is a worker that cannot say anything about
// the work it was given — the defect this bead's fourth criterion names.
//
// It is also what makes the ORDER the queue's rather than a race: one call
// enqueues both messages in one locked append, so their arrival order is the
// order they were written in and not which of two goroutines got there first.
type Briefing struct {
	// Preamble is the rules: who the coordinator is, the calls this worker
	// has, and how to report through them. It is Preamble(...), built by the
	// registration that has the coordinator session in hand.
	Preamble string
	// Task is what the worker is for, in the coordinator's own words.
	Task string
}

// Validate refuses half a briefing.
//
// It is a value-level check rather than two separate parameters for the reason
// the record refuses a report with no kind: the callers here are an
// in-process seam, a test and a composition root, and a shape nothing checks is
// one a later edit can leave half-built. See the type's own doc for the defect
// it prevents.
func (b Briefing) Validate() error {
	switch {
	case strings.TrimSpace(b.Preamble) == "" && strings.TrimSpace(b.Task) == "":
		return errors.New("workers: a briefing with no rules and no task tells a worker nothing")
	case strings.TrimSpace(b.Preamble) == "":
		return errors.New("workers: a briefing with no rules would hand a worker work it cannot report on")
	case strings.TrimSpace(b.Task) == "":
		return errors.New("workers: a briefing with no task would type rules into a worker with nothing to do")
	}
	return nil
}

// Preamble is the rules a worker is handed before its task. See this file's
// own header for what it owes the worker, why the text lives in this package
// rather than in the composition root, and what it deliberately leaves out.
//
// ONE LINE, AND THAT IS A MEASUREMENT RATHER THAN A STYLE (nocx-xn63t.4.5).
// The delivery of this message is confirmed by reading the agent's own input
// box back and matching what was pasted (pane_messages.go's waitForEcho) — and
// the shape that reading is MEASURED on is a paste with no newline in it:
// `claude-2.1.272-wrapped-echo`, one paragraph drawn over four content rows,
// whose reading is asserted by agentdriver's own
// TestInputTextReadsASingleParagraphAcrossEveryWrappedRow. A paste that carries
// newlines is answered with the agent's `[Pasted text #N +M lines]` placeholder
// instead, which boxContainsEcho also accepts — so both shapes work, and the
// one this text takes is the one with a capture and a test behind its reading
// rather than a formatting decision of the agent's.
//
// It is also why the text is kept to a paragraph rather than three: the box
// grows with what is pasted into it, and the length this must not reach is the
// length at which the agent stops growing the box and starts scrolling inside
// it — where a reading of "the whole box" would no longer be the whole paste.
// The rules read as one paragraph of four sentences; nothing in them needs a
// hard break.
func Preamble(coordinatorSession string) string {
	who := "You are a worker, and the nocx session that started you is your coordinator."
	if coordinatorSession != "" {
		who = "You are a worker, and your coordinator is nocx session " + coordinatorSession + "."
	}
	return who +
		" Tell it how you are doing with workers.report: kind \"done\" when you finish, " +
		"\"progress\" at a milestone, and \"question\" when you cannot continue without an " +
		"answer — ask, then END YOUR TURN: the call does not wait, and the answer arrives as " +
		"your next message. workers.inbox reads the mail your coordinator leaves you. " +
		"nocx records no success and no failure — your coordinator judges what your words mean."
}

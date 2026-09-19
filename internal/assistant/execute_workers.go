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
	// Report commits one worker's report — its kind and its own words — into
	// the mailbox of the coordinator that holds it (nocx-luqz9.4). The
	// recipient is not an argument and there is nowhere to put one: the box is
	// read off the reporting participant's own record, which is what makes
	// "a worker cannot write to somebody else's coordinator" a property of the
	// shape rather than of a check.
	Report(ctx context.Context, id workers.ParticipantID, rep workers.Report) (workers.Message, error)
	// Inbox hands this reader its next page and advances its own cursor.
	Inbox(ctx context.Context, mailbox, reader workers.ReaderID, limit int) (workers.Fetch, error)
	// Undelivered is what this worker's mailboxes hold that their recipients
	// have not taken.
	Undelivered(ctx context.Context, id workers.ID) ([]workers.Message, error)
	// Acknowledge records that this reader finished committing the effects
	// of everything through a sequence. It is what stops a retry of one
	// response committing the same spawn twice.
	Acknowledge(ctx context.Context, mailbox, reader workers.ReaderID, through int64) error
	// Close ends a participant and records that its coordinator ended it: the
	// record reads `closed` (workers.StateClosed) rather than only the exit
	// the close caused. What it also gives back is the participant's place —
	// its tab leaves the window (nocx-xn63t.4.6), which is the layout chain's
	// row rather than the record's — and, when the participant had a checkout
	// of its own, what is left of it: the close never removes the checkout
	// (owner's decision, 2026-09-18), so the answer is the only account of
	// the worker's work this call gives (nocx-xn63t.1.3).
	Close(ctx context.Context, coordinatorSession string, id workers.ParticipantID) (workers.CloseResult, error)
	// Undispatched is what the record still owes judgement on. It is read
	// BEFORE HeldBy, because HeldBy is the fetch that clears it (D8): asking
	// afterwards would always answer nothing, which is a truthful answer to
	// the wrong question.
	Undispatched() []workers.Fact
	// LeftoverCheckouts answers which nocx-made checkouts of the repository
	// the coordinator stands in no live worker holds (nocx-xn63t.1.4), with
	// the record's own honesty flag: Complete false says the list may be
	// missing rows, and never reads as "there are none". The failure mode is
	// deliberately IN the answer and not an error — a holdings answer marked
	// incomplete is a usable answer, and an error would make the checkouts
	// invisible exactly when they most need seeing. The repository is
	// resolved from the coordinator's own session, never from an argument.
	LeftoverCheckouts(ctx context.Context, coordinatorSession string) workers.CheckoutSurvey
	// RemoveCheckouts removes nocx-made checkouts of the coordinator's own
	// repository (nocx-xn63t.1.5) through the same walk the leftovers
	// answer reads, under the same refusals the automatic sweep will one
	// day remove through: a checkout holding uncommitted work, a checkout
	// a live worker holds, and anything that is not one of nocx's
	// checkouts of this repository are all refused BY NAME, and a read the
	// decision needed that failed answers unresolved — never a guess. The
	// branch always stays. The repository is resolved from the
	// coordinator's own session, never from an argument.
	RemoveCheckouts(ctx context.Context, coordinatorSession string, refs []workers.CheckoutRef) workers.CheckoutRemoval
}

// workerParticipantResult is one row of what a coordinator is told. It restates
// the record's vocabulary and never invents one: the states are the record's
// own words, so what the model reads and what the store holds cannot drift.
type workerParticipantResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Task  string `json:"task"`
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
	// Worktree is present only when the worker lives in a checkout its
	// spawn created (nocx-xn63t.1.4): where it is and the branch checked out
	// in it. A worker named here is WHY the checkout is not in
	// leftoverCheckouts — it is not abandoned, it is this worker's.
	Worktree *workerParticipantWorktree `json:"worktree,omitempty"`
}

// workerParticipantWorktree names one worker's checkout. Base is the spawn
// result's to carry; identifying the checkout needs only where it is and
// what is checked out in it.
type workerParticipantWorktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// workerLeftoverCheckoutResult is one row of the repository's nocx-made
// checkouts that no live worker holds. Readable false means Uncommitted and
// Ahead carry NO answer — the state could not be read, which is a different
// fact from clean, and the difference is the one that stops a coordinator
// deleting a checkout whose state nobody read.
type workerLeftoverCheckoutResult struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Uncommitted bool   `json:"uncommitted"`
	Ahead       int    `json:"ahead"`
	Readable    bool   `json:"readable"`
	// LastUsed is when nocx last had a pane open in this checkout, UTC
	// RFC 3339 — the encoding every other timestamp on this surface uses.
	// Absent when no record of it exists.
	LastUsed string `json:"lastUsed,omitempty"`
	// Name and Task say which worker the spawn was for, when the record
	// knows; a checkout older than the record lists with neither.
	Name string `json:"name,omitempty"`
	Task string `json:"task,omitempty"`
	// Expired, HoldReason and HoldDetail are the SWEEP's verdict on the
	// checkout (nocx-xn63t.1.6): expired means a sweep found it past the
	// idle period and could not remove it, HoldReason names why from a
	// closed set — the removal refusals, or "pane-open" for a checkout a
	// live pane of nocx stands in — and HoldDetail says what is true on
	// disk. Absent entirely for a checkout no sweep has judged, which is
	// every checkout before the first pass runs.
	Expired    bool   `json:"expired,omitempty"`
	HoldReason string `json:"holdReason,omitempty"`
	HoldDetail string `json:"holdDetail,omitempty"`
}

// workerMailResult is one message the coordinator is handed. It carries the
// sender, the body and WHEN it was committed — plus the three fields a worker's
// REPORT adds (nocx-luqz9.4), because a report is a claim of a named kind rather
// than ordinary mail and the difference decides what the coordinator does next.
//
// THE TIME IS REQUIRED and not optional, which is the design rather than a
// convenience: a row is committed by the record's own clock (`Say`, `Report`),
// and a coordinator comparing what a worker said against what it was seen to be
// doing has to compare two readings of one clock. The observations list beside
// this one has carried its `at` since nocx-luqz9.2 for exactly that reason, and
// a report whose time existed on the row but reached nobody would be the
// recorded-and-unreadable value this surface exists to refuse.
//
// There is still no id or cursor in it: a message is CONTENT, and a shape with
// a position in it would invite the model to think it had something to
// acknowledge, which is a mark the backend advanced for it.
type workerMailResult struct {
	From    string `json:"from"`
	Message string `json:"message"`
	// At is when the record committed the row, in UTC RFC 3339 — the same
	// encoding every other timestamp on this surface uses, so a caller never has
	// two parsers.
	At string `json:"at"`
	// Kind is set only when this row IS a worker's report (workers.report), and
	// it is the difference the coordinator acts on: `done` and `question` woke
	// it, `progress` did not. Empty for a message somebody left the holder,
	// which is text and not a claim about anybody's work.
	Kind string `json:"kind,omitempty"`
	// Estimate and Artifact ride a `progress` checkpoint only, and each is a
	// claim too: the estimate is the worker's own approximation, unmeasured and
	// allowed to sit still, and the artifact is the one part the coordinator can
	// check against what nocx already owns. Both are omitted when the worker
	// gave neither — a report is worth as much without them (mesh design P2, P3
	// make both optional on purpose).
	Estimate *int   `json:"estimate,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// workerObservationResult is one state a coordinator's worker was SEEN to be in
// (nocx-luqz9.2; ADR-0070 decision 2).
//
// THREE FIELDS AND NO FOURTH, and that is the contract rather than a shape
// somebody chose: an observation carries no screen content, ever. The reason is
// in ADR-0070 and in internal/workers' own wakeText — text read off a worker's
// screen, handed to a coordinator as something to act on, is prompt injection
// performed by nocx itself. What a coordinator gets is the worker, the state and
// when it settled, and it decides what that means.
type workerObservationResult struct {
	Worker string `json:"worker"`
	State  string `json:"state"`
	// At is when the state settled, in UTC RFC 3339 — the same encoding every
	// other timestamp on this surface uses (internal/transport's
	// RFC3339Nano/RFC3339 sites), so a caller never has two parsers.
	At string `json:"at"`
}

type workerHoldingsResult struct {
	// Mail rides this call, which is D7: mail rides our own calls rather
	// than being pushed through a hook onto the result of any tool the model
	// happens to have called. The coordinator asks what it holds and is told
	// what was said to it in the same breath, because those are one question
	// — "what has happened since I last looked".
	Mail []workerMailResult `json:"mail,omitempty"`
	// Observations is the second kind of row the same mailbox holds
	// (nocx-luqz9.2): the settled states nocx saw of this coordinator's own
	// workers. Two lists and not one, for workers.inbox's reason — an
	// observation has no text and a message has no state, so one field would
	// have to mean two things — and named here rather than left out for a
	// sharper reason: this call READS the mailbox, which advances the cursor,
	// so an observation rendered as an empty message would be both a lie to
	// the coordinator and invisible to the workers.inbox it might have reached
	// instead.
	Observations []workerObservationResult `json:"observations,omitempty"`
	// Cursor is where this reader has been read up to. It travels with the
	// mail because §7.2 requires the reader to acknowledge a position
	// TOGETHER WITH the effects it commits from that response: a reader
	// handed messages and no position could only acknowledge by guessing.
	Cursor          int64                     `json:"cursor,omitempty"`
	UndeliveredMail int                       `json:"undeliveredMail,omitempty"`
	Participants    []workerParticipantResult `json:"participants"`
	// LeftoverCheckouts is the repository's own answer, beneath the
	// session's (nocx-xn63t.1.4): the checkouts spawns of this repository
	// created that NO live worker holds — including ones other sessions
	// started, because "left over" is a fact about the checkout and not
	// about who is asking. A checkout named in a participant row above is
	// deliberately absent from this list.
	LeftoverCheckouts []workerLeftoverCheckoutResult `json:"leftoverCheckouts,omitempty"`
	// CheckoutsComplete is the answer's honesty about itself. False says
	// the durable record or git's listing could not be read and the list
	// MAY be missing rows — it never reads as "there are none". Absent
	// (true) only when every read behind the list succeeded.
	CheckoutsComplete bool `json:"checkoutsComplete"`
}

type workerHoldingsParams struct {
	// Acknowledge is the cursor from an earlier answer, sent back once the
	// coordinator has finished committing that answer's effects. It is
	// SEPARATE from the fetch on purpose: D8 keeps the four
	// acknowledgements apart, and a fetch that also claimed the fourth
	// would be the record asserting something only the reader can know.
	Acknowledge int64 `json:"acknowledge,omitempty"`
}

type workerCloseParams struct {
	Worker string `json:"worker"`
}

type workerCloseResult struct {
	ID    string `json:"id"`
	Ended bool   `json:"ended"`
	// Worktree is what is left of the worker's own checkout, present only
	// when it had one: the close never removes the checkout, so this is the
	// coordinator's only account of the work sitting there (nocx-xn63t.1.3).
	Worktree *workerCloseWorktreeResult `json:"worktree,omitempty"`
}

// workerCloseWorktreeResult is the checkout's answer. Uncommitted and Ahead
// are pointers because their ABSENCE is a value: they ride only state "read",
// and a result that answered uncommitted:false over a read that failed would
// claim a cleanliness nobody saw.
type workerCloseWorktreeResult struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	State       string `json:"state"`
	Uncommitted *bool  `json:"uncommitted,omitempty"`
	Ahead       *int   `json:"ahead,omitempty"`
}

// workerRemoveRef is one checkout a removal ask names: by path, by branch,
// or both — both must agree, which the service refuses rather than guesses
// about.
type workerRemoveRef struct {
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// workerRemoveCheckoutParams is what the removal ask carries: the
// checkouts, in the order the answer keeps.
type workerRemoveCheckoutParams struct {
	Checkouts []workerRemoveRef `json:"checkouts"`
}

// workerRemoveItemResult is one asked checkout and what became of it. Path
// and Branch are the checkout as nocx resolved it on disk — a branch-named
// ask is answered with the path it resolved to — never merely the words of
// the ask. Refusal and Detail ride only a row that was NOT removed, and a
// removed row carries neither: nothing is claimed about a removed checkout
// beyond its removal, and its branch is still in the repository.
type workerRemoveItemResult struct {
	Path    string `json:"path,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Removed bool   `json:"removed"`
	Refusal string `json:"refusal,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// workerRemoveCheckoutResult is the answer: one row per asked checkout, in
// the order asked.
type workerRemoveCheckoutResult struct {
	Checkouts []workerRemoveItemResult `json:"checkouts"`
}

type workerSayParams struct {
	Worker  string `json:"worker"`
	Message string `json:"message"`
}

type workerSayResult struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

// workerReportParams is what a worker's report carries (nocx-luqz9.4). The
// spelling of P2 and P3 is the mesh design's own vocabulary, read literally and
// on purpose: §6 calls them "an optional approximate estimate, an optional
// artifact reference", and a name invented here would be a second word for a
// thing the design already named.
type workerReportParams struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	// Estimate is a POINTER because the field is optional in a way an int
	// cannot express: zero is a report — "I estimate nothing is done yet" — and
	// the absence of a number is a different report. A value type would make
	// them the same row.
	Estimate *int   `json:"estimate,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// workerReportResult is what a report answers. Two fields and no verdict: what
// became of the report is the coordinator's to decide, and whether it has read
// the row is a fact this call cannot see.
type workerReportResult struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

type workerSpawnParams struct {
	Command string `json:"command"`
	Task    string `json:"task"`
	// Worktree is the ask for a fresh checkout the worker's pane will live
	// in. The executor validates only what the schema cannot — a branch
	// with no name — and carries the rest as asked: where the checkout is
	// and what commit it starts from are the spawner's (the git seam's)
	// answers, read back from the record, never decided here.
	Worktree *workerSpawnWorktreeParams `json:"worktree,omitempty"`
}

type workerSpawnWorktreeParams struct {
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"`
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
	// BriefingQueued is the OTHER half of the answer, and the one that decides
	// what the rest of it means (nocx-luqz9.5, design §6): whether nocx handed
	// this worker its briefing — the rules it reports under, and then the task
	// — to the queue that delivers it.
	//
	// FALSE IS NOT A SMALLER TRUE. The two messages are one call, so a false
	// here means the worker has been told NOTHING and nothing further will be
	// delivered: it is running, it will never report, and the coordinator's
	// only move is workers.close and another spawn. That is why it is a field
	// and not a log line — a live worker nobody was told about is exactly the
	// silent degrade AGENTS.md refuses to ship.
	//
	// It comes straight from the registration (workers.TaskDelivery), which is
	// the only party that knows whether the queue took the briefing: no
	// derivation, no second opinion about the same fact.
	BriefingQueued bool `json:"briefingQueued"`
	// Worktree is present only when the spawn actually created a checkout:
	// where it is, the branch checked out in it, and the resolved commit it
	// starts from. It comes from the record (workers.Participant.Worktree),
	// which accepted the checkout at MarkLive — the executor copies, and
	// does not re-derive, the one place that fact lives.
	Worktree *workerSpawnWorktreeResult `json:"worktree,omitempty"`
	// LeftoverCheckouts is how many of the repository's nocx-made checkouts
	// NO live worker holds, after this spawn (nocx-xn63t.1.4) — the same
	// list workers.holdings carries, counted, so a coordinator that never
	// asks still hears that its repository has checkouts standing. The
	// checkout this spawn just made is held by the worker it started and is
	// never counted.
	LeftoverCheckouts int `json:"leftoverCheckouts"`
	// CheckoutsComplete is the count's honesty about itself, the same flag
	// holdings carries: false says the true number may be higher.
	CheckoutsComplete bool `json:"checkoutsComplete"`
}

type workerSpawnWorktreeResult struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
}

// workerCoordinatorFrom is the ONE assertion of a concrete worker capability,
// and nocx-luqz9.2 is why there is only one. Every call that names an AUTHORITY
// — spawn, say, close, holdings, wait — is a coordinator's alone, so there is
// one type to assert. The participant assertion this stood beside is deleted
// with its last caller rather than kept for symmetry: A8's two types are still
// two, and what vanished is a second assertion of a capability nothing but
// workers.inbox ever narrowed to.
//
// workers.inbox is the exception and asserts NEITHER concrete type, because its
// two holders do the same thing — read their own mailbox — and which box that is
// follows from the run rather than from a role. See workerMailboxFrom.
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

// splitMailbox renders one page of a mailbox into the two lists everything that
// reads a mailbox hands back (nocx-luqz9.2).
//
// ONE RENDERER AND NOT ONE PER CALLER, because two readers share the same box —
// workers.inbox and workers.holdings — and a second
// copy of "what does a row become" is a second answer that agrees everywhere
// until an observation lands in the copy somebody forgot. The kind is decided by
// the row itself (Message.Observed is nil for text), so a caller cannot choose
// wrongly and cannot forget: it has nothing to choose.
//
// Both lists are non-nil for an empty page, so a reader of the JSON always sees
// the keys and "nothing new" is an empty array rather than an absent field. The
// time is UTC RFC 3339, the encoding every other timestamp on this surface uses.
func splitMailbox(messages []workers.Message) ([]workerMailResult, []workerObservationResult) {
	text := make([]workerMailResult, 0, len(messages))
	observed := make([]workerObservationResult, 0, len(messages))
	for _, m := range messages {
		if m.Observed == nil {
			// The kind is carried through as the row holds it, and an EMPTY one
			// stays empty: ordinary mail from a coordinator has no kind, and a
			// renderer that defaulted it to a report would be inventing a claim
			// nobody made. Whether a report wakes was decided by the record's
			// wake — this is what the coordinator is told it was.
			//
			// The time is the record's own stamp on the row and is rendered for
			// every message, in the encoding the observations beside it use.
			text = append(text, workerMailResult{
				From: string(m.Sender), Message: m.Body,
				At:   m.CommittedAt.UTC().Format(time.RFC3339),
				Kind: string(m.Kind), Estimate: m.Estimate, Artifact: m.Artifact,
			})
			continue
		}
		observed = append(observed, workerObservationResult{
			Worker: string(m.Observed.Worker),
			State:  string(m.Observed.State),
			At:     m.Observed.At.UTC().Format(time.RFC3339),
		})
	}
	return text, observed
}

// renderLeftoverCheckouts maps the record's rows onto the result shape: the
// one derivation is the timestamp's encoding, UTC RFC 3339 like every other
// time on this surface, and a zero time — a checkout the record never wrote
// a last-used stamp for — renders as absent rather than as a date in 1970.
func renderLeftoverCheckouts(rows []workers.LeftoverCheckout) []workerLeftoverCheckoutResult {
	out := make([]workerLeftoverCheckoutResult, 0, len(rows))
	for _, r := range rows {
		row := workerLeftoverCheckoutResult{
			Path: r.Path, Branch: r.Branch,
			Uncommitted: r.Uncommitted, Ahead: r.Ahead, Readable: r.Readable,
			Name: r.Name, Task: r.Task,
			Expired:    r.Expired,
			HoldReason: r.HoldReason,
			HoldDetail: r.HoldDetail,
		}
		if !r.LastUsed.IsZero() {
			row.LastUsed = r.LastUsed.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

type workerInboxParams struct {
	Acknowledge int64 `json:"acknowledge"`
}

// workerInboxResult is what one mailbox read answers, and it carries BOTH kinds
// of mail because one mailbox carries both: text a coordinator left, and states
// nocx saw (nocx-luqz9.2).
//
// They are two lists and never one, and the reason is the contract rather than
// taste: an observation has no text and a message has no state, so a merged
// shape would need one field to mean two things and the renderer would have to
// guess. Two lists also let "no observations" be an honest empty array rather
// than a missing key a reader has to tell from an absent feature.
type workerInboxResult struct {
	Messages []workerMailResult `json:"messages"`
	// Observations is the settled states of this coordinator's own workers, in
	// the order they settled. Empty for a worker, whose mailbox is written by
	// its coordinator's words alone.
	Observations []workerObservationResult `json:"observations"`
	Cursor       int64                     `json:"cursor"`
	More         bool                      `json:"more"`
}

// executeWorkerInbox hands a caller the mail in its own mailbox — the mail a
// coordinator left a worker, and, for a coordinator, the state changes nocx saw
// of its workers (nocx-luqz9.2, design §4.5).
//
// It names no mailbox. The box is the capability's own identity — a
// participant's id, or the session a coordinator is — so a caller has no way to
// EXPRESS another mailbox: A9's rule, and the reason this is a property of the
// type rather than of a check.
//
// The two callers share this ONE function rather than getting one each, because
// they are doing one thing: reading their own box. What the box IS differs and
// that is the capability's whole contribution; everything after it — acknowledge
// first, then fetch, then render — is identical, and a second copy would be a
// second answer to "what does a read hand over".
func executeWorkerInbox(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	mailbox, err := workerMailboxFrom(cap, "workers.inbox")
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
	box := workers.ReaderID(mailbox.Mailbox())
	// Acknowledge BEFORE fetching, for the coordinator's reason: the mark being
	// sent back is about the PREVIOUS answer, and doing it after would let this
	// call's own page slide under an acknowledgement of mail the caller has not
	// seen yet.
	if p.Acknowledge > 0 {
		if ackErr := seams.workerStore.Acknowledge(ctx, box, box, p.Acknowledge); ackErr != nil {
			return "", fmt.Errorf("workers.inbox: acknowledge: %w", ackErr)
		}
	}
	fetched, err := seams.workerStore.Inbox(ctx, box, box, 0)
	if err != nil {
		return "", fmt.Errorf("workers.inbox: %w", err)
	}
	messages, observations := splitMailbox(fetched.Messages)
	out := workerInboxResult{
		Messages:     messages,
		Observations: observations,
		Cursor:       fetched.Cursor.Fetched,
		More:         fetched.More,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("workers.inbox: result: %w", err)
	}
	return string(raw), nil
}

// workerMailboxFrom is the one thing workers.inbox needs from the capability it
// was narrowed to: which box is this holder's own.
//
// It asserts agenttools.Mailbox and not either concrete type, and that is a
// deliberate asymmetry with workerCoordinatorFrom/workerParticipantFrom above:
// those two exist where the two AUTHORITIES differ — spawning, closing, holdings
// — and this call is the one act where they do not. The narrow has already
// proved which holder this is (a run context with no participant identity cannot
// narrow to a participant, and one with no session cannot narrow to a
// coordinator); re-deciding it here with a second type switch would be the
// second owner of a question already answered.
func workerMailboxFrom(cap agenttools.Capability, tool string) (agenttools.Mailbox, error) {
	m, ok := cap.(agenttools.Mailbox)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, which owns no mailbox", tool, cap)
	}
	if m.Mailbox() == "" {
		// An empty mailbox belongs to nobody, and answering it an empty inbox
		// would be indistinguishable from a holder whose mail nobody has
		// written.
		return nil, fmt.Errorf("%s: this run owns no mailbox", tool)
	}
	return m, nil
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
	return workerAnswer(ctx, "workers.holdings", coordinator, seams, p.Acknowledge)
}

// workerAnswer builds the answer holdings gives.
//
// ONE CALLER IS LEFT (ADR-0070): workers.wait read the same record when there
// was something to read, and it is gone. What did not change is the answer —
// holdings, the mail, the cursor and what nobody took are one question asked
// once, and a second shape for any part of it would be a second account of what
// a session holds.
func workerAnswer(
	ctx context.Context,
	tool string,
	coordinator *agenttools.WorkerCoordinator,
	seams toolSeams,
	acknowledge int64,
) (string, error) {
	// Read what is owed BEFORE the fetch, because the fetch is what clears
	// it. The other order would answer this question with the record's state
	// after it had been answered, which is always "nothing new".
	owed := make(map[workers.ParticipantID]bool)
	for _, f := range seams.workerStore.Undispatched() {
		owed[f.Participant] = true
	}
	held, err := seams.workerStore.HeldBy(ctx, coordinator.Session())
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	out := workerHoldingsResult{Participants: make([]workerParticipantResult, 0, len(held))}
	for _, p := range held {
		row := workerParticipantResult{
			ID: string(p.ID), State: string(p.State), Task: p.Task,
			NeedsJudgement: owed[p.ID],
		}
		if p.Worktree.Path != "" {
			row.Worktree = &workerParticipantWorktree{Path: p.Worktree.Path, Branch: p.Worktree.Branch}
		}
		out.Participants = append(out.Participants, row)
	}
	// The repository's own answer, beside the session's (nocx-xn63t.1.4).
	// The survey's Complete flag rides verbatim: marking a failed read as
	// "no leftovers" would be the lie this flag exists to prevent.
	survey := seams.workerStore.LeftoverCheckouts(ctx, coordinator.Session())
	out.LeftoverCheckouts = renderLeftoverCheckouts(survey.Leftovers)
	out.CheckoutsComplete = survey.Complete
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
	messages, observations := splitMailbox(fetched.Messages)
	out.Mail = messages
	out.Observations = observations
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
//
// What it answers BESIDE the ask is the checkout the close left (nocx-xn63t.1.3):
// carried from the record's own close answer, never re-read here — this
// package has no git seam and must not grow one. A worker with no checkout
// answers nothing about one (the field is absent), and a reading the close
// could not make arrives as unknown rather than as a clean that was never
// seen.
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
	res, closeErr := seams.workerStore.Close(ctx, coordinator.Session(), workers.ParticipantID(p.Worker))
	if closeErr != nil {
		return "", fmt.Errorf("workers.close: %w", closeErr)
	}
	out := workerCloseResult{ID: p.Worker, Ended: true}
	if res.Worktree != (workers.Leftover{}) {
		wt := &workerCloseWorktreeResult{
			Path:   res.Worktree.Path,
			Branch: res.Worktree.Branch,
			State:  string(res.Worktree.State),
		}
		// The reading's two values ride ONLY a read: absent is what unknown
		// looks like on the wire, because uncommitted:false would claim a
		// cleanliness nobody saw.
		if res.Worktree.State == workers.CheckoutRead {
			wt.Uncommitted = &res.Worktree.Uncommitted
			wt.Ahead = &res.Worktree.Ahead
		}
		out.Worktree = wt
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("workers.close: result: %w", err)
	}
	return string(raw), nil
}

// workerOf names the worker a coordinator's holdings belong to.
//
// It is read off the participants rather than assumed to equal the session,
// even though a coordinator's first spawn does default the worker id to its
// session: the record permits a named worker, and a helper that assumed the
// default would be right until the day somebody used the field.
// executeWorkerRemoveCheckout removes one or more of nocx's own leftover
// checkouts of the coordinator's repository (nocx-xn63t.1.5).
//
// THE REPOSITORY IS THE SESSION'S, never an argument, exactly as for
// holdings: the walk that decides which checkouts are nocx's starts at the
// coordinator capability's own session. What the model names is only the
// checkouts — by path, branch, or both — and the answer names what became
// of each, in the order asked.
//
// THE REFUSALS ARE THE SERVICE'S, not re-decided here: uncommitted work, a
// live worker's hold, not-ours and unresolved arrive named and detailed
// from the same walk and under the same refusals the automatic sweep of
// task 1.6 will remove through, and this executor's whole job is to carry
// them to the caller verbatim — a refusal reworded here would be a second
// account of the same disk.
func executeWorkerRemoveCheckout(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	coordinator, err := workerCoordinatorFrom(cap, "workers.removeCheckout")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.removeCheckout: this backend keeps no worker record")
	}
	var p workerRemoveCheckoutParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.removeCheckout: %w", argErr)
	}
	if len(p.Checkouts) == 0 {
		return "", errors.New("workers.removeCheckout: name at least one checkout to remove, by path or by branch")
	}
	refs := make([]workers.CheckoutRef, 0, len(p.Checkouts))
	for i, ask := range p.Checkouts {
		if ask.Path == "" && ask.Branch == "" {
			return "", fmt.Errorf("workers.removeCheckout: checkout %d names neither a path nor a branch", i+1)
		}
		refs = append(refs, workers.CheckoutRef{Path: ask.Path, Branch: ask.Branch})
	}
	removal := seams.workerStore.RemoveCheckouts(ctx, coordinator.Session(), refs)
	items := make([]workerRemoveItemResult, 0, len(removal.Items))
	for _, item := range removal.Items {
		row := workerRemoveItemResult{Path: item.Path, Branch: item.Branch, Removed: item.Removed}
		if !item.Removed {
			row.Refusal = string(item.Refusal)
			row.Detail = item.Detail
		}
		items = append(items, row)
	}
	raw, err := json.Marshal(workerRemoveCheckoutResult{Checkouts: items})
	if err != nil {
		return "", fmt.Errorf("workers.removeCheckout: result: %w", err)
	}
	return string(raw), nil
}

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

// executeWorkerReport commits a worker's report into its coordinator's mailbox
// (nocx-luqz9.4; design §4.2, §5.1; ADR-0070 decision 1).
//
// IT NAMES NO RECIPIENT AND IT WAITS FOR NOTHING. The first is A9's rule: the
// box is derived from the reporting participant's own record, so a worker has no
// way to EXPRESS another mailbox — not its coordinator's sibling, not another
// worker's, not its own. The second is ADR-0070's "why not a blocking ask": a
// question is a report that says it needs an answer, and the answer arrives as
// the worker's next message, so there is no timeout, no held call and no state
// to resume. Both are properties of this function's shape rather than rules it
// enforces.
//
// The KIND is passed through and never re-derived from the text. A verdict read
// out of prose is the second derivation this whole surface exists to avoid, and
// the record validates it (checkReport) because a row carrying a kind its
// contract does not allow would fail the next READER's schema, not this one's.
func executeWorkerReport(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	participant, err := workerParticipantFrom(cap, "workers.report")
	if err != nil {
		return "", err
	}
	if seams.workerStore == nil {
		return "", errors.New("workers.report: this backend keeps no worker record")
	}
	var p workerReportParams
	if argErr := json.Unmarshal(args, &p); argErr != nil {
		return "", fmt.Errorf("workers.report: %w", argErr)
	}
	m, err := seams.workerStore.Report(ctx, workers.ParticipantID(participant.Participant()), workers.Report{
		Kind:     workers.MessageKind(p.Kind),
		Text:     p.Text,
		Estimate: p.Estimate,
		Artifact: p.Artifact,
	})
	if err != nil {
		return "", fmt.Errorf("workers.report: %w", err)
	}
	raw, err := json.Marshal(workerReportResult{ID: string(m.ID), Seq: m.Seq})
	if err != nil {
		return "", fmt.Errorf("workers.report: result: %w", err)
	}
	return string(raw), nil
}

// workerParticipantFrom is the ONE assertion of a worker's own capability, at
// the one call that is a worker's alone (nocx-luqz9.4).
//
// It is workers.inbox's asymmetry read the other way round. That call asserts
// NEITHER concrete type, because its two holders do one thing; this call is the
// reverse — reporting is a worker's act and nobody else's — so the type switch
// is what proves the distinction exhaustive, exactly as it does for the five an
// authority call.
//
// A coordinator never reaches the branch this refuses. Its run context carries
// no participant identity, so narrowWorkerParticipant refused the call before
// any executor ran; what this assertion catches is the one thing the narrow
// cannot: a declaration whose Narrow and whose executor disagree about which
// capability the call takes.
func workerParticipantFrom(cap agenttools.Capability, tool string) (*agenttools.WorkerParticipant, error) {
	p, ok := cap.(*agenttools.WorkerParticipant)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not a worker's own", tool, cap)
	}
	if p.Participant() == "" {
		// An empty participant names a mailbox belonging to nobody, which must
		// not be written to. The narrow refuses this already; the check is here
		// so the failure is a refusal rather than a row addressed to "".
		return nil, fmt.Errorf("%s: this run is not a worker participant", tool)
	}
	return p, nil
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
	// The worktree ask's own shape, repeated here for a caller that bypassed
	// the schema it was shown. Everything else about a checkout — where it
	// can go, whether the branch is free, what it starts from — is the git
	// seam's refusal to answer, with its own names, far from here.
	if p.Worktree != nil && p.Worktree.Branch == "" {
		return "", errors.New("workers.spawn: a worktree needs the branch to check out in it")
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
	req := workers.RegisterRequest{
		CoordinatorSession: coordinator.Session(),
		// The coordinator's OWN incarnation, never the spawned participant's
		// liveness epoch: Delegation.ControllerIdentity is what a
		// revocation's chain resolution reads back (nocx-bm99e), and this is
		// the one production call site that mints it.
		CoordinatorIdentity: coordinator.Identity(),
		Role:                workers.RoleWorker,
		Task:                p.Task,
		Command:             p.Command,
		Environment:         environment,
		CreatedByRunID:      seams.runID,
	}
	// The worktree ask travels AS ASKED: branch required, base optional.
	// Resolving it — where the checkout goes, what commit it starts from —
	// is the spawner's answer, and the result reads it back from the record
	// below rather than from anything decided here.
	if p.Worktree != nil {
		req.Worktree = &workers.WorktreeAsk{Branch: p.Worktree.Branch, Base: p.Worktree.Base}
	}
	participant, err := seams.workerStore.Register(ctx, req)
	if err != nil {
		return "", fmt.Errorf("workers.spawn: %w", err)
	}
	result := workerSpawnResult{
		ID:             string(participant.ID),
		State:          string(participant.State),
		TaskTyped:      participant.Delivery.Typed,
		WaitingOn:      participant.Delivery.WaitingOn,
		BriefingQueued: participant.Delivery.BriefingQueued,
	}
	if participant.Worktree.Path != "" {
		result.Worktree = &workerSpawnWorktreeResult{
			Path:   participant.Worktree.Path,
			Branch: participant.Worktree.Branch,
			Base:   participant.Worktree.Base,
		}
	}
	// HOW MANY ARE LEFT OVER (nocx-xn63t.1.4), after this spawn: the worker
	// just started holds its own checkout, so the count never includes it.
	// A read that failed answers zero-and-incomplete rather than a smaller
	// number dressed as the truth.
	survey := seams.workerStore.LeftoverCheckouts(ctx, coordinator.Session())
	result.LeftoverCheckouts = len(survey.Leftovers)
	result.CheckoutsComplete = survey.Complete
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("workers.spawn: result: %w", err)
	}
	return string(raw), nil
}

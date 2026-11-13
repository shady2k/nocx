package hostconn

// The transport state machine (design §5.3), as a pure function of events.
//
// WHY A FUNCTION AND NOT METHODS ON THE CONNECTION. Every state a person is
// shown is an answer to "what is this connection doing", and the answer has to
// be the same whether it is computed after a dial, after a probe, or after the
// socket died under a shell. Writing the transitions once, with no clock, no
// channel and no connection in reach, is what lets the whole table be asserted
// in one test — including the states a real host produces once in a month.
//
// The state names are the design's own: opening(stage), waiting_person(kind),
// connected, stalled, reconnecting(attempt), failed(cause), ended(cause).

import "fmt"

// Kind is one of the transport's states.
type Kind uint8

const (
	// Opening is a dial in progress, at Stage.
	Opening Kind = iota
	// WaitingPerson is a step that cannot continue without an answer from a
	// person, and the no-progress clock stops here.
	WaitingPerson
	// Connected is a usable connection: signed in, answering.
	Connected
	// Stalled is a connection that has not answered, and has not been shown
	// to be gone. Silence alone ends nothing (design §5.3).
	Stalled
	// Reconnecting is a connection whose transport ended while somebody still
	// held it. The next attempt starts here.
	Reconnecting
	// Failed is a connection the far side refused in a way no retry changes:
	// a changed host key, a rejected sign-in. Only a person's Reconnect leaves
	// it.
	Failed
	// Ended is a connection nobody holds any more.
	Ended
)

func (k Kind) String() string {
	switch k {
	case Opening:
		return "opening"
	case WaitingPerson:
		return "waiting_person"
	case Connected:
		return "connected"
	case Stalled:
		return "stalled"
	case Reconnecting:
		return "reconnecting"
	case Failed:
		return "failed"
	case Ended:
		return "ended"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Stage is one step of an opening.
type Stage string

const (
	StageResolve     Stage = "resolve"
	StageTCP         Stage = "tcp"
	StageSSHVersion  Stage = "ssh-version"
	StageKeyExchange Stage = "key-exchange"
	StageSignIn      Stage = "sign-in"
	StageShell       Stage = "shell"
)

// WaitKind is what a waiting_person state is waiting for.
type WaitKind string

const (
	WaitHostKey       WaitKind = "host-key"
	WaitHelperConsent WaitKind = "helper-consent"
	WaitVaultUnlock   WaitKind = "vault-unlock"
	WaitPassphrase    WaitKind = "passphrase"
	WaitPassword      WaitKind = "password"
	WaitSecondFactor  WaitKind = "second-factor"
	WaitKeySignature  WaitKind = "key-signature"
)

// Cause names why a connection stopped where it did.
type Cause string

const (
	// CauseHostKeyChanged and CauseAuthRejected are the far side saying no in
	// a way a second attempt cannot change. Nothing retries from them.
	CauseHostKeyChanged Cause = "host-key-changed"
	CauseAuthRejected   Cause = "auth-rejected"
	// CauseNoAuthMethod is this side having nothing to offer: a configuration
	// answer, not a server one.
	CauseNoAuthMethod Cause = "no-auth-method"
	// CauseLostLink is the transport ending under a connection somebody still
	// holds: a cut wire, a rebooted host, a NAT that dropped the flow.
	CauseLostLink Cause = "lost-link"
	// CauseReleased is the last holder letting go.
	CauseReleased Cause = "released"
	// CauseCancelled is the caller giving up on the dial.
	CauseCancelled Cause = "cancelled"
)

// State is one point of the machine. Only the fields its Kind names are set;
// the rest are zero, so a state compares as a value and a test can state what
// it expects in one literal.
type State struct {
	Kind  Kind
	Stage Stage    // Opening: where the dial is.
	Wait  WaitKind // WaitingPerson: what the answer is for.
	// Stage on a WaitingPerson state is the step that asked, so the answer
	// resumes where it stopped rather than at the beginning.
	Attempt int   // Reconnecting: how many attempts have been made since it last worked.
	Cause   Cause // Failed, Ended: why.
}

func (s State) String() string {
	switch s.Kind {
	case Opening:
		return "opening(" + string(s.Stage) + ")"
	case WaitingPerson:
		return "waiting_person(" + string(s.Wait) + ")"
	case Reconnecting:
		return fmt.Sprintf("reconnecting(%d)", s.Attempt)
	case Failed:
		return "failed(" + string(s.Cause) + ")"
	case Ended:
		return "ended(" + string(s.Cause) + ")"
	}
	return s.Kind.String()
}

// EventKind is what happened.
type EventKind uint8

const (
	// StageReached: the dial got to another step.
	StageReached EventKind = iota
	// NeedsPerson: the step that just ran cannot continue without an answer.
	NeedsPerson
	// PersonAnswered: the answer arrived; the dial resumes where it stopped.
	PersonAnswered
	// SignedIn: the far side accepted the sign-in and the connection is
	// usable. It is the carrier's word, and the only way into Connected.
	SignedIn
	// Silent: a probe has not come back inside its deadline. Evidence of a
	// problem, never a verdict.
	Silent
	// Answered: a probe came back on a connection that was stalled.
	Answered
	// Lost: the transport ended under a connection somebody still holds.
	Lost
	// Refused: the far side will not accept this connection and a retry
	// cannot change that.
	Refused
	// Closed: nobody holds the connection any more.
	Closed
	// Attempt starts a new dial: the way out of failed and ended, and how a
	// supervisor begins one. It is separate from StageReached because a stage
	// reported after a refusal must not resurrect a failed attempt, while a
	// person's Reconnect must.
	Attempt
)

// Event is one fact the supervisor feeds the machine.
type Event struct {
	Kind  EventKind
	Stage Stage
	Wait  WaitKind
	Cause Cause
}

// Next is the whole machine: one state and one event in, one state out.
//
// It is total, and that is deliberate. An event that does not apply to the
// current state leaves it alone rather than being an error: a watcher that
// noticed the transport end a moment after the last holder released it must
// not move an ended connection back to reconnecting, and a carrier that
// reports a stage after a refusal must not resurrect a failed attempt.
func Next(state State, event Event) State {
	// A person's Reconnect is the one event that leaves a terminal state
	// (design §5.3: "Reconnect is the way out"), and it also starts the next
	// attempt after a lost link.
	if event.Kind == Attempt {
		switch state.Kind {
		// Opening is included because the zero State IS an opening: the first
		// attempt of a supervisor starts there, before any stage was reached.
		case Failed, Ended, Reconnecting, WaitingPerson, Opening:
			return State{Kind: Opening, Stage: StageResolve}
		}
		return state
	}
	switch state.Kind {
	case Failed, Ended:
		// Terminal for everything the transport reports.
		return state

	case Opening:
		switch event.Kind {
		case StageReached:
			return State{Kind: Opening, Stage: event.Stage, Attempt: state.Attempt}
		case NeedsPerson:
			return State{Kind: WaitingPerson, Stage: state.Stage, Wait: event.Wait, Attempt: state.Attempt}
		case SignedIn:
			return State{Kind: Connected, Attempt: state.Attempt}
		case Lost:
			return State{Kind: Reconnecting, Attempt: state.Attempt + 1, Cause: event.Cause}
		case Refused:
			return State{Kind: Failed, Cause: event.Cause}
		case Closed:
			return State{Kind: Ended, Cause: event.Cause}
		}

	case WaitingPerson:
		switch event.Kind {
		case PersonAnswered:
			return State{Kind: Opening, Stage: state.Stage, Attempt: state.Attempt}
		case Lost:
			return State{Kind: Reconnecting, Attempt: state.Attempt + 1, Cause: event.Cause}
		case Closed:
			return State{Kind: Ended, Cause: event.Cause}
		}

	case Connected:
		switch event.Kind {
		case Silent:
			return State{Kind: Stalled, Attempt: state.Attempt}
		case Lost:
			return State{Kind: Reconnecting, Attempt: state.Attempt + 1, Cause: event.Cause}
		case Closed:
			return State{Kind: Ended, Cause: event.Cause}
		}

	case Stalled:
		switch event.Kind {
		case Answered:
			return State{Kind: Connected, Attempt: state.Attempt}
		case Lost:
			return State{Kind: Reconnecting, Attempt: state.Attempt + 1, Cause: event.Cause}
		case Closed:
			return State{Kind: Ended, Cause: event.Cause}
		}

	case Reconnecting:
		switch event.Kind {
		case StageReached:
			// The next attempt is under way: an attempt is an opening, and
			// this is the one edge back into it.
			return State{Kind: Opening, Stage: event.Stage, Attempt: state.Attempt}
		case Closed:
			return State{Kind: Ended, Cause: event.Cause}
		}
	}
	return state
}

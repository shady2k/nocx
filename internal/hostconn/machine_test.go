package hostconn_test

// The transport state machine asserted as a table, with no connection anywhere
// near it: every state the design names is reachable from an event, and the
// three answers a person acts on differently — waiting, refused, lost — are one
// test each so a change to one cannot silently become a change to all.

import (
	"testing"

	"github.com/shady2k/nocx/internal/hostconn"
)

func TestTheMachinePassesEachOpeningStage(t *testing.T) {
	state := hostconn.State{Kind: hostconn.Opening, Stage: hostconn.StageResolve}
	for _, stage := range []hostconn.Stage{
		hostconn.StageTCP, hostconn.StageSSHVersion,
		hostconn.StageKeyExchange, hostconn.StageSignIn, hostconn.StageShell,
	} {
		state = hostconn.Next(state, hostconn.Event{Kind: hostconn.StageReached, Stage: stage})
		if state.Kind != hostconn.Opening || state.Stage != stage {
			t.Fatalf("stage %q read as %v", stage, state)
		}
	}
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.SignedIn})
	if state.Kind != hostconn.Connected {
		t.Fatalf("a signed-in connection reads %v, want connected", state)
	}
}

func TestTheMachineWaitsForAPersonAndResumesWhereItStopped(t *testing.T) {
	state := hostconn.State{Kind: hostconn.Opening, Stage: hostconn.StageSignIn}
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.NeedsPerson, Wait: hostconn.WaitPassword})
	if state.Kind != hostconn.WaitingPerson || state.Wait != hostconn.WaitPassword || state.Stage != hostconn.StageSignIn {
		t.Fatalf("a step needing a password reads %v", state)
	}
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.PersonAnswered})
	if state.Kind != hostconn.Opening || state.Stage != hostconn.StageSignIn {
		t.Fatalf("an answered prompt resumed at %v, want opening(sign-in)", state)
	}
}

func TestTheMachineFailsOnARefusalAndNeverRetries(t *testing.T) {
	for _, cause := range []hostconn.Cause{hostconn.CauseAuthRejected, hostconn.CauseHostKeyChanged} {
		state := hostconn.State{Kind: hostconn.Opening, Stage: hostconn.StageSignIn}
		state = hostconn.Next(state, hostconn.Event{Kind: hostconn.Refused, Cause: cause})
		if state.Kind != hostconn.Failed || state.Cause != cause {
			t.Fatalf("a refusal (%s) reads %v", cause, state)
		}
		// Nothing the transport says moves a failed connection: not the wire
		// dying, not a late stage, not a probe. That is what "no retry" means
		// at this layer.
		for _, event := range []hostconn.Event{
			{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink},
			{Kind: hostconn.StageReached, Stage: hostconn.StageSignIn},
			{Kind: hostconn.Silent},
			{Kind: hostconn.Closed, Cause: hostconn.CauseReleased},
		} {
			if got := hostconn.Next(state, event); got != state {
				t.Fatalf("a failed connection moved on %v: %v", event.Kind, got)
			}
		}
		// A person's Reconnect is the way out.
		if got := hostconn.Next(state, hostconn.Event{Kind: hostconn.Attempt}); got.Kind != hostconn.Opening {
			t.Fatalf("a reconnect from failed reads %v, want opening", got)
		}
	}
}

func TestTheMachineReconnectsOnALostTransport(t *testing.T) {
	state := hostconn.State{Kind: hostconn.Connected}
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink})
	if state.Kind != hostconn.Reconnecting || state.Attempt != 1 {
		t.Fatalf("a lost transport reads %v, want reconnecting(1)", state)
	}
	// The next attempt is an opening again, and the count carries over.
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageTCP})
	if state.Kind != hostconn.Opening || state.Attempt != 1 {
		t.Fatalf("the next attempt reads %v, want opening(tcp) at attempt 1", state)
	}
}

func TestTheMachineStallsAndReturns(t *testing.T) {
	state := hostconn.State{Kind: hostconn.Connected}
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.Silent})
	if state.Kind != hostconn.Stalled {
		t.Fatalf("silence reads %v, want stalled", state)
	}
	// SILENCE ENDS NOTHING (design §5.3): the state is the report, and the
	// connection is still there to answer.
	state = hostconn.Next(state, hostconn.Event{Kind: hostconn.Answered})
	if state.Kind != hostconn.Connected {
		t.Fatalf("an answer on a stalled connection reads %v, want connected", state)
	}
}

func TestTheMachineEndsWhenNobodyHoldsIt(t *testing.T) {
	for _, start := range []hostconn.State{
		{Kind: hostconn.Opening, Stage: hostconn.StageTCP},
		{Kind: hostconn.Connected},
		{Kind: hostconn.Stalled},
		{Kind: hostconn.WaitingPerson, Wait: hostconn.WaitHostKey},
		{Kind: hostconn.Reconnecting, Attempt: 2},
	} {
		got := hostconn.Next(start, hostconn.Event{Kind: hostconn.Closed, Cause: hostconn.CauseReleased})
		if got.Kind != hostconn.Ended || got.Cause != hostconn.CauseReleased {
			t.Fatalf("a release from %v reads %v, want ended(released)", start, got)
		}
	}
}

func TestAnEndedConnectionStaysEnded(t *testing.T) {
	state := hostconn.State{Kind: hostconn.Ended, Cause: hostconn.CauseReleased}
	for _, event := range []hostconn.Event{
		{Kind: hostconn.Lost},
		{Kind: hostconn.StageReached, Stage: hostconn.StageTCP},
		{Kind: hostconn.SignedIn},
		{Kind: hostconn.Silent},
	} {
		if got := hostconn.Next(state, event); got != state {
			t.Fatalf("an ended connection moved on %v: %v", event.Kind, got)
		}
	}
}

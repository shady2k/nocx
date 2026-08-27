package assistant

// A CANCELLATION HAS A NAME, AND A RESUME RUNS UNDER THE ASK THAT RESUMED IT
// (nocx-d6gn4.8.1). Both were bought by one live failure: a program parked on
// an approval, the person said yes, and the run died with "the connection was
// lost while the answer was streaming" while the socket was perfectly healthy.
//
// The cause was that the released effect went on calling the kernel of the
// ask that had PARKED it — whose sink writes through a context that died when
// that ask returned. It came back as a bare context.Canceled, and
// context.Canceled is what the transport maps to a lost connection. So there
// are two rules here, and each is asserted below: the program's locals
// survive an approval and its kernel does not, and every cancellation this
// process performs says which one it was.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingInvoker is a kernel that answers every call and remembers its
// name, which is how a test can say WHICH kernel an effect went through.
type recordingInvoker struct {
	name string
	mu   sync.Mutex
	got  []string
}

func (r *recordingInvoker) Invoke(_ context.Context, tool, callID, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, tool+"/"+callID)
	return "{}", nil
}

func (r *recordingInvoker) Declares(string) bool { return true }

func (r *recordingInvoker) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// parkedProgram is a stand-in for a carrier that owns its control flow: it
// parks on one question, and after the answer it calls the kernel it was
// rebound to — the shape every real carrier has.
type parkedProgram struct {
	mu       sync.Mutex
	kernel   invoker
	suspends chan *Suspension
	ctx      chan context.Context
	finished chan struct{}
}

func newParkedProgram(k invoker) *parkedProgram {
	return &parkedProgram{
		kernel:   k,
		suspends: make(chan *Suspension),
		ctx:      make(chan context.Context, 1),
		finished: make(chan struct{}),
	}
}

func (p *parkedProgram) setKernel(k invoker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kernel = k
}

func (p *parkedProgram) kernelNow() invoker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kernel
}

func (p *parkedProgram) run(runCtx context.Context) (string, error) {
	p.ctx <- runCtx
	defer close(p.finished)
	return invokeParking(runCtx, p.kernelNow, p.suspends, "run", "prog-1", "{}")
}

// park drives the program to the question it stops on.
func park(t *testing.T, runs *parkedRuns, runID string, k invoker) (*parkedProgram, context.Context) {
	t.Helper()
	prog := newParkedProgram(k)
	_, err := runs.drive(context.Background(), runID, k,
		func() (<-chan *Suspension, func(context.Context) (string, error), func(invoker)) {
			return prog.suspends, prog.run, prog.setKernel
		})
	var ask *ApprovalRequestedError
	if !errors.As(err, &ask) {
		t.Fatalf("drive returned %v, want the question the program stopped on", err)
	}
	return prog, <-prog.ctx
}

// askingOnce is a kernel that escalates the first call and answers the
// second — the policy's "ask every time" as the parked path meets it.
type askingOnce struct {
	mu    sync.Mutex
	asked bool
	inner *recordingInvoker
	runID string
}

func (a *askingOnce) Invoke(ctx context.Context, tool, callID, rawArgs string) (string, error) {
	a.mu.Lock()
	first := !a.asked
	a.asked = true
	a.mu.Unlock()
	if first {
		return "", &ApprovalRequestedError{Request: &ApprovalRequest{
			RunID: a.runID, Attempt: 1, Tool: tool, CallID: callID,
		}}
	}
	return a.inner.Invoke(ctx, tool, callID, rawArgs)
}

func (a *askingOnce) Declares(string) bool { return true }

// THE ONE THE LIVE FAILURE BOUGHT: the effect a person approved runs through
// the kernel of the drive that RESUMED it, never the one that parked it.
// Through the parked kernel it announced itself into a stream that had
// already returned — a durable write on a dead context, a bare
// context.Canceled, and a person told their connection had dropped.
func TestParkedRuns_TheApprovedEffectRunsThroughTheDriveThatResumedIt(t *testing.T) {
	runs := newParkedRuns(nil)
	parkedAsk := &recordingInvoker{name: "the ask that parked it"}
	prog, _ := park(t, runs, "352", &askingOnce{inner: parkedAsk, runID: "352"})

	// The person answers, and THIS drive — a second ask, with its own live
	// stream — resumes the program.
	resumingAsk := &recordingInvoker{name: "the ask that resumed it"}
	if _, err := runs.drive(context.Background(), "352", resumingAsk,
		func() (<-chan *Suspension, func(context.Context) (string, error), func(invoker)) {
			t.Fatal("the resume built a second carrier: the program was restarted, not continued")
			return nil, nil, nil
		}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	select {
	case <-prog.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the resumed program never ran on")
	}

	if got := resumingAsk.calls(); len(got) != 1 || got[0] != "run/prog-1" {
		t.Fatalf("the resuming ask saw %v, want the approved effect", got)
	}
	if got := parkedAsk.calls(); len(got) != 0 {
		t.Fatalf("the approved effect went through the ask that had already returned: %v", got)
	}
}

// THE CAUSE REACHES THE PROGRAM. Discard is what a terminal run does to a
// continuation nobody may resume, and the program that wakes to it can say so.
func TestParkedRuns_ADiscardedProgramIsToldWhatEndedIt(t *testing.T) {
	runs := newParkedRuns(nil)
	prog, runCtx := park(t, runs, "349", &askingOnce{inner: &recordingInvoker{}, runID: "349"})

	runs.discard("349")

	select {
	case <-prog.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the discarded program never woke")
	}
	cause := context.Cause(runCtx)
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("cause = %v; every cause must still read as a cancellation to the layers that check for one", cause)
	}
	if !errors.Is(cause, errRunDiscarded) {
		t.Fatalf("cause = %v, want the discard named", cause)
	}
	sentence, ok := ProgramEndedSentence(cause)
	if !ok || sentence == "" {
		t.Fatalf("ProgramEndedSentence(%v) = %q, %v — a person is told nothing about what ended their run", cause, sentence, ok)
	}
}

// AND THE SENTENCE IS NOT THE OTHER ONE. context.Canceled on its own still
// means what it always meant — the transport's lost-connection arm is
// reachable — so the new arm must not swallow it.
func TestProgramEndedSentence_SaysNothingAboutAnOrdinaryCancellation(t *testing.T) {
	if _, ok := ProgramEndedSentence(context.Canceled); ok {
		t.Fatal("a bare cancellation was reported as a program this process ended")
	}
	if _, ok := ProgramEndedSentence(errors.New("something else")); ok {
		t.Fatal("an unrelated error was reported as a program this process ended")
	}
}

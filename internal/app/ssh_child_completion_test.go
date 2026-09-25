package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

// recordingLaneObserver records every completion the lane's pane runtime is
// told about.
type recordingLaneObserver struct {
	fences []lifecycle.FenceNonce
	exits  []int
}

func (r *recordingLaneObserver) ObserveEnvironmentEntry() {}

func (r *recordingLaneObserver) Accept(ingest func() error, c *lifecycle.Complete) error {
	if err := ingest(); err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	r.fences = append(r.fences, c.Fence)
	code := -1
	if c.ExitCode != nil {
		code = *c.ExitCode
	}
	r.exits = append(r.exits, code)
	return nil
}

// sshChildStand is a real kernel and publisher with a parent domain on the
// pane's own transport, suspended for a child, and the child's domain bound
// to a transport of its OWN driven through sshChildKernel — the shape
// buildSSHChildBootstrap gives an ssh child's lifecycle listener.
type sshChildStand struct {
	pub   *lifecyclepub.Publisher
	child lifecycle.DomainHandle
	lane  lifecycle.LaneID
	k     interface {
		Ingest(lifecycle.TransportID, lifecycle.Envelope) error
	}
}

const sshChildTransport = lifecycle.TransportID("t-ssh-child")

func newSSHChildStand(t *testing.T, observers *environmentEntryRegistry) *sshChildStand {
	t.Helper()
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	const parentTransport = lifecycle.TransportID("t-pane")
	const lane = lifecycle.LaneID("lane-ssh-child")
	if err := pub.BindTransport(parentTransport, silentPort{}); err != nil {
		t.Fatalf("bind the pane's transport: %v", err)
	}
	parent, err := pub.RequestDomain(lane, nil, parentTransport)
	if err != nil {
		t.Fatalf("request the parent domain: %v", err)
	}
	penv := func(seq uint64, evt lifecycle.Event) lifecycle.Envelope {
		return lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: lane, Domain: parent.Domain,
			Epoch: parent.Epoch, Sequence: seq, Capability: parent.Capability, Event: evt,
		}
	}
	if err = pub.Ingest(parentTransport, penv(1, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("parent hello: %v", err)
	}
	if err = pub.Ingest(parentTransport, penv(2, lifecycle.Event{Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{}})); err != nil {
		t.Fatalf("parent suspends for the child: %v", err)
	}

	k := sshChildKernel(pub, lane, observers)
	if err = k.BindTransport(sshChildTransport, silentPort{}); err != nil {
		t.Fatalf("bind the child's own transport: %v", err)
	}
	child, err := k.RequestDomain(lane, &parent.Domain, sshChildTransport)
	if err != nil {
		t.Fatalf("request the child domain: %v", err)
	}
	st := &sshChildStand{pub: pub, child: child, lane: lane, k: k}
	if err = k.Ingest(sshChildTransport, st.env(1, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("child hello: %v", err)
	}
	return st
}

func (st *sshChildStand) env(seq uint64, evt lifecycle.Event) lifecycle.Envelope {
	return lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: st.lane, Domain: st.child.Domain,
		Epoch: st.child.Epoch, Sequence: seq, Capability: st.child.Capability, Event: evt,
	}
}

// runRemoteCommand runs one command in the child domain to its completion
// through the child's own transport and answers the fence it completed with.
func (st *sshChildStand) runRemoteCommand(t *testing.T, seq uint64, fenceByte byte, code int) (lifecycle.FenceNonce, error) {
	t.Helper()
	att, err := st.pub.SubmitAttempt(st.child.Domain, "echo remote", "/", "", "submit-remote")
	if err != nil {
		t.Fatalf("submit the remote command: %v", err)
	}
	id := att.ID
	if err = st.k.Ingest(sshChildTransport, st.env(seq, lifecycle.Event{
		Kind:  lifecycle.KindStart,
		Start: &lifecycle.Start{AttemptID: &id, Command: "echo remote"},
	})); err != nil {
		t.Fatalf("remote start: %v", err)
	}
	var fence lifecycle.FenceNonce
	for i := range fence {
		fence[i] = fenceByte
	}
	return fence, st.k.Ingest(sshChildTransport, st.env(seq+1, lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{AttemptID: &id, ExitCode: &code, Fence: fence},
	}))
}

// TestSSHChildCompletionReachesThePaneRuntime pins nocx-2v80t.3.24: a command
// that completes inside an ssh child is accepted on the child's OWN transport
// (buildSSHChildBootstrap's listener), which the pane's observing kernel never
// wraps. Its completion must still reach the pane's runtime, or the runtime
// never ends that command's interval — no end marker, no fence for the block
// stream to match — and every block after it lost its rows to the live region
// (measured on nocxify-journey.spec.ts: from the first remote command on, no
// block held its own output).
func TestSSHChildCompletionReachesThePaneRuntime(t *testing.T) {
	observers := newEnvironmentEntryRegistry()
	obs := &recordingLaneObserver{}
	observers.register("lane-ssh-child", obs)
	st := newSSHChildStand(t, observers)

	fence, err := st.runRemoteCommand(t, 2, 0x5a, 3)
	if err != nil {
		t.Fatalf("the kernel refused the remote completion: %v", err)
	}
	if len(obs.fences) != 1 {
		t.Fatalf("the pane runtime was told %d completions for one accepted remote finish, want exactly 1", len(obs.fences))
	}
	if obs.fences[0] != fence || obs.exits[0] != 3 {
		t.Fatalf("the pane runtime was told fence %x exit %d, want the kernel's fence %x and exit 3",
			obs.fences[0], obs.exits[0], fence)
	}

	// A completion the kernel REFUSES reaches nothing: the same sequence
	// again is a replay, and the observer is not a second gate.
	var replay lifecycle.FenceNonce
	replay[0] = 0x77
	code := 0
	if err = st.k.Ingest(sshChildTransport, st.env(3, lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{Fence: replay, ExitCode: &code},
	})); err == nil {
		t.Fatal("the kernel accepted a replayed sequence; the stand is not exercising a refusal")
	}
	if len(obs.fences) != 1 {
		t.Fatalf("a refused completion reached the pane runtime (%d observed)", len(obs.fences))
	}
}

// TestSSHChildWithNoRegisteredPaneStillRuns is the paired ordinary half: a
// lane with no pane registered (or no registry at all) gets the plain kernel,
// and the child's commands complete exactly as they did before.
func TestSSHChildWithNoRegisteredPaneStillRuns(t *testing.T) {
	for name, observers := range map[string]*environmentEntryRegistry{
		"no registry":         nil,
		"no pane on the lane": newEnvironmentEntryRegistry(),
	} {
		t.Run(name, func(t *testing.T) {
			st := newSSHChildStand(t, observers)
			if _, err := st.runRemoteCommand(t, 2, 0x5b, 0); err != nil {
				t.Fatalf("the remote completion was refused: %v", err)
			}
		})
	}
}

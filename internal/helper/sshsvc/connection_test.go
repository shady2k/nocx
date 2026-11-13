//go:build nocx_local_ssh

package sshsvc_test

// ONE SUPERVISOR PER CONNECTION, AND A DEAD TRANSPORT LEAVES THE POOL AT ONCE
// (nocx-oalo1.4.2 and nocx-oalo1.4.6).
//
// The owner met the second on the shipped app on 2026-11-13: a host was slow,
// the link dropped, and after that every new tab on that saved host failed with
// "the far side refused the lifecycle listener: read tcp <local>:<port> ->
// <host>:22: read: operation timed out" — the SAME local port every time —
// until the old tab was closed. Two defects, one cause: the pool marks a
// connection dead only when somebody closes it, nothing noticed the transport
// ending on its own, and the pane called every failure a refusal.
//
// These tests are the fault harness of design §10 in its first real shape — the
// switchable proxy from internal/sshtest in front of a real in-process sshd —
// and each assertion is about a fact a person met, not about a mechanism:
// a healthy host is dialled once for two panes, a cut link makes the next pane
// dial afresh, a `false` answer is a refusal, and a transport that ended is
// never called one.

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// destinationAt is the resolved destination a caller sends for the address its
// resolution produced: the same user, credential and auth a lane or a pane
// names (identityKey answers "#cred-1" for it, which is what lets the two
// halves of these tests meet on ONE connection).
func destinationAt(t *testing.T, addr string) proto.SSHDestination {
	t.Helper()
	hostName, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return proto.SSHDestination{
		Host: hostName, Port: port, User: "test",
		Identity: proto.SSHIdentity{
			Credential: &proto.SSHCredential{Ref: wantRef},
			Auth:       proto.SSHAuthPassword,
		},
	}
}

// paneAt opens a pane's far-side listeners through the SERVICE, which is the
// seam a session uses (helper/session's spawner calls OpenPaneListeners).
func (s *stand) paneAt(t *testing.T, destination proto.SSHDestination) (*sshsvc.PaneListeners, error) {
	t.Helper()
	ctx := host.WithConnection(context.Background(), s.helper)
	return s.service.OpenPaneListeners(ctx, sshsvc.PaneSpec{Destination: destination, Lifecycle: true})
}

// TestAPooledAcquireReachesConnectedThroughTheSupervisor is nocx-oalo1.4.2's
// fault-harness half: a real dial through a real proxy to a real server, and the
// states it passed are the test's to read — opening(resolve) through each stage
// to connected, with nothing from internal/ssh in the assertion.
func TestAPooledAcquireReachesConnectedThroughTheSupervisor(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	destination := destinationAt(t, proxy.Addr())
	lease, err := stand.carrier.Acquire(context.Background(), ssh.PooledSpec{
		Host: destination.Host, Port: destination.Port, User: destination.User,
		Identity: wantRef,
		Config: &gossh.ClientConfig{
			User: "test",
			Auth: []gossh.AuthMethod{gossh.PublicKeys(f.userSigner)},
			// The fault tests are about the connection's state, not about
			// trust: the fixture server is the one this test started.
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("Acquire through the supervisor: %v", err)
	}
	t.Cleanup(lease.Release)

	states := lease.States()
	if len(states) == 0 {
		t.Fatal("the supervisor recorded no state at all")
	}
	if last := states[len(states)-1]; last.Kind != hostconn.Connected {
		t.Fatalf("a healthy acquire ended in %v, want connected (path %v)", last, states)
	}
	for _, want := range []hostconn.Stage{
		hostconn.StageResolve, hostconn.StageTCP, hostconn.StageSSHVersion,
		hostconn.StageKeyExchange, hostconn.StageSignIn,
	} {
		if !pathHasStage(states, want) {
			t.Fatalf("the path %v never reached the %q stage", states, want)
		}
	}
	if lease.Lost() {
		t.Fatal("a connection nobody interfered with reads as lost")
	}
}

// pathHasStage reports whether the state path passed the stage.
func pathHasStage(states []hostconn.State, stage hostconn.Stage) bool {
	for _, state := range states {
		if state.Kind == hostconn.Opening && state.Stage == stage {
			return true
		}
	}
	return false
}

// TestAHealthyPooledConnectionIsReusedWithNoRedial is the ordinary case the
// defect hid behind: two panes on one host are one connection, one sign-in, one
// accept at the server.
func TestAHealthyPooledConnectionIsReusedWithNoRedial(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})
	destination := destinationAt(t, proxy.Addr())

	first, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("first pane: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if first.LifecyclePort() <= 0 {
		t.Fatalf("the first pane has no lifecycle port: %d", first.LifecyclePort())
	}

	second, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("second pane: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if got := f.connections(); got != 1 {
		t.Fatalf("two panes on one host made %d connections, want 1 (AD-4: one authenticated connection, channels multiplexed)", got)
	}
	if second.LifecyclePort() <= 0 {
		t.Fatalf("the second pane has no lifecycle port: %d", second.LifecyclePort())
	}
}

// TestACutLinkLeavesThePoolSoTheNextPaneDialsAfresh is the owner's bug, end to
// end: a pooled connection whose transport was cut, WHILE ANOTHER HOLDER STILL
// HOLDS IT and with no prober armed, must not be handed to the next pane.
func TestACutLinkLeavesThePoolSoTheNextPaneDialsAfresh(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})
	destination := destinationAt(t, proxy.Addr())

	// THE HOLDER THAT STAYS: a lease on the same connection, acquired through
	// the same supervisor the service uses. It keeps the connection "in use"
	// exactly as the owner's old tab did, and it arms no keepalive — every
	// helper path but a shell passes 0, which is why nothing noticed.
	holder, err := stand.carrier.Acquire(context.Background(), ssh.PooledSpec{
		Host: destination.Host, Port: destination.Port, User: destination.User,
		Identity: wantRef,
		Config: &gossh.ClientConfig{
			User:            "test",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(f.userSigner)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	pane, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("the pane before the cut: %v", err)
	}
	t.Cleanup(func() { _ = pane.Close() })
	if got := f.connections(); got != 1 {
		t.Fatalf("the pane and the holder made %d connections, want 1", got)
	}

	// THE WIRE IS CUT with the holder still holding. The wait is on the
	// observable the supervisor itself watches — the transport's end — so the
	// assertion below is not racing the client's own read loop.
	proxy.Cut()
	select {
	case <-holder.Link().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the cut link never ended")
	}

	// THE NEXT PANE. Before the fix this was handed the corpse: same local
	// port, a stored read error, and "the far side refused" to the person.
	next, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("a pane opened after the cut: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if next.LifecyclePort() <= 0 {
		t.Fatalf("the pane after the cut has no lifecycle port: %d", next.LifecyclePort())
	}

	ports := f.connectionPorts()
	if len(ports) != 2 {
		t.Fatalf("the server accepted %d connections %v, want 2: a fresh dial is the fix (the same local port on every attempt was the defect)", len(ports), ports)
	}
	if ports[0] == ports[1] {
		t.Fatalf("both connections came from local port %d: the second pane was handed the dead transport", ports[0])
	}
}

// TestARefusedForwardSaysRefused is one half of the pair: the far side really
// answered `false` — AllowTcpForwarding off — and the person is told the host
// refused, with the class the coordinator already switches on.
func TestARefusedForwardSaysRefused(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	f.mu.Lock()
	f.allowForward = false
	f.mu.Unlock()
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	_, err := stand.paneAt(t, destinationAt(t, f.addr))
	if err == nil {
		t.Fatal("a host that refuses to forward answered no error")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a real refusal is not called one: %v", err)
	}
	var refusal *proto.Refusal
	if !errors.As(err, &refusal) || refusal.Code != proto.ErrCodeChannelRefused {
		t.Fatalf("a refusal crosses as %v, want the channel_refused class", err)
	}
}

// TestATransportErrorIsNeverCalledARefusal is the other half, and the one the
// owner met: the far side dies instead of answering, and the sentence must not
// blame it for refusing.
func TestATransportErrorIsNeverCalledARefusal(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	f.mu.Lock()
	f.closeOnForward = true
	f.mu.Unlock()
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	_, err := stand.paneAt(t, destinationAt(t, f.addr))
	if err == nil {
		t.Fatal("a connection that died mid-request answered no error")
	}
	if strings.Contains(err.Error(), "refused") {
		t.Fatalf("a transport error is reported as the far side refusing: %v", err)
	}
}

// ─── silence and closing (nocx-oalo1.4.3) ────────────────────────────────
//
// The policy's time is the injected clock's: these tests advance it, and never
// sleep. What they assert is the design's own sentences — stalled within 30 s,
// a plain shell that stays, a helper-owned connection closed on the evidence,
// and a reboot behind a silent flow that ends as a transport error.

// supervisedSpec is the same connection the service acquires for the
// destination, taken through the carrier the stand's service dials through — so
// the lease and the service's own references are ONE connection and one
// supervisor (identityKey answers the credential reference for a password
// destination, which is what makes the two meet).
func supervisedSpec(t *testing.T, f *fixture, destination proto.SSHDestination, holds bool) ssh.PooledSpec {
	t.Helper()
	return ssh.PooledSpec{
		Host: destination.Host, Port: destination.Port, User: destination.User,
		Identity:        wantRef,
		HoldsPlainShell: holds,
		Config: &gossh.ClientConfig{
			User:            "test",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(f.userSigner)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
	}
}

// openShell opens a real plain shell on the stand's service: the shipped path a
// pane takes, which is the only thing that can prove a pane's terminal declares
// itself as one.
func (s *stand) openShell(t *testing.T, destination proto.SSHDestination) *sshsvc.ShellChannel {
	t.Helper()
	return s.openShellWith(t, destination, nil)
}

// openShellWith is openShell with the liveness observer a coordinator session
// registers: what every shell on a connection is told.
func (s *stand) openShellWith(t *testing.T, destination proto.SSHDestination, onLiveness ssh.LivenessObserver) *sshsvc.ShellChannel {
	t.Helper()
	ctx := host.WithConnection(context.Background(), s.helper)
	shell, err := s.service.OpenShell(ctx, sshsvc.ShellSpec{
		Destination: destination,
		Cols:        80, Rows: 24,
		OnLiveness: onLiveness,
	})
	if err != nil {
		t.Fatalf("open a shell: %v", err)
	}
	t.Cleanup(func() { _ = shell.Close() })
	return shell
}

// advanceUntil moves the injected clock one step at a time until cond holds.
// The policy's time is simulated, so "how long" is the test's; the loop exists
// only to let the guard see each step, and its outer bound is waittest's.
func advanceUntil(t *testing.T, clock *hostconntest.Clock, step time.Duration, what string, cond func() bool) {
	t.Helper()
	waittest.WaitFor(t, what, func() bool {
		if cond() {
			return true
		}
		clock.Advance(step)
		return false
	})
}

// lastState is the state a connection ended at, or the zero state when it has
// no history.
func lastState(states []hostconn.State) hostconn.State {
	if len(states) == 0 {
		return hostconn.State{}
	}
	return states[len(states)-1]
}

// TestASilentConnectionReadsStalledWithinThirtySeconds is Q3 through the shipped
// path: a working connection whose flow goes deaf is reported stalled fifteen
// seconds after the probe that did not come back, and a plain shell keeps it
// open for ten more minutes.
func TestASilentConnectionReadsStalledWithinThirtySeconds(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	shell := stand.openShell(t, destination)
	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)
	if got := f.connections(); got != 1 {
		t.Fatalf("the shell and the holder made %d connections, want 1", got)
	}

	proxy.SilenceEstablished()
	clock.Advance(30 * time.Second)

	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})
	if got := lastState(holder.States()).Silent; got != 15*time.Second {
		t.Fatalf("stalled reports %v of silence, want 15s: the probe that was sent at 15s and never answered", got)
	}

	// TEN MINUTES OF BLACKHOLE, and the shell is still there: silence ends
	// nothing on a connection that carries a plain shell (design §5.3).
	clock.Advance(10 * time.Minute)
	waittest.WaitFor(t, "the clock to be seen", func() bool {
		return clock.Now() > 0
	})
	if got := lastState(holder.States()).Kind; got != hostconn.Stalled {
		t.Fatalf("a plain-shell connection reads %v after ten minutes of silence, want stalled", got)
	}
	if holder.Lost() {
		t.Fatal("a plain-shell connection was closed for silence")
	}
	// The terminal the person is in is still usable as far as the helper is
	// concerned: writing to it does not fail.
	if _, err := shell.Write([]byte("still here\n")); err != nil {
		t.Fatalf("a plain shell refused a write after ten minutes of silence: %v", err)
	}

	// And the host comes back: the parked probe answers, the state is
	// connected, and the shell's bytes reach the far side again.
	proxy.Pass()
	waittest.WaitFor(t, "the connection to answer again", func() bool {
		return lastState(holder.States()).Kind == hostconn.Connected
	})
	if _, err := shell.Write([]byte("back\n")); err != nil {
		t.Fatalf("the plain shell did not survive the silence: %v", err)
	}
}

// TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook is the other
// half: nothing is lost by closing a connection whose shells are all
// helper-owned, so a host that still answers new connections plus the grace
// ends it.
func TestAHelperOwnedConnectionClosesThirtySecondsAfterAnAnsweredLook(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	proxy.SilenceEstablished()
	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})

	// The look dials a NEW connection, which this proxy lets through: that is
	// the host answering while the flow stays deaf, and it is the evidence the
	// grace is measured from.
	advanceUntil(t, clock, time.Second, "the host to answer a look-only dial", func() bool {
		return f.connections() >= 2
	})
	// Thirty seconds of grace later, the policy closes it: nothing is riding
	// this connection whose loss a reattach could not undo.
	advanceUntil(t, clock, time.Second, "the connection to be closed for silence", func() bool {
		return holder.Lost()
	})
	waittest.WaitFor(t, "the close to be reported as reconnecting", func() bool {
		state := lastState(holder.States())
		return state.Kind == hostconn.Reconnecting && state.Cause == hostconn.CauseSilence
	})
}

// TestARebootBehindASilentFlowReadsAsATransportErrorAndReconnects is Q5's
// detection half, through the proxy: the flow was silent, the host came back at
// the network layer by resetting the state it no longer holds, and the
// supervisor reads a transport error rather than a longer silence.
func TestARebootBehindASilentFlowReadsAsATransportErrorAndReconnects(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	proxy.SilenceEstablished()
	clock.Advance(30 * time.Second)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})

	// The reboot: the host holds no state for this flow, so the next thing it
	// says is a reset. On a real host the nudge is what provokes it (a request
	// with no reply wanted passes the parked probe); the proxy stages the
	// outcome the client sees, which is the fact this test is about.
	proxy.Reset()
	waittest.WaitFor(t, "the transport's end", func() bool { return holder.Lost() })
	waittest.WaitFor(t, "the loss to be reported as reconnecting", func() bool {
		state := lastState(holder.States())
		return state.Kind == hostconn.Reconnecting && state.Cause == hostconn.CauseLostLink
	})
}

// TestAHealthyConnectionNeverReadsStalledOverTenMinutes is the paired success:
// a connection that answers is never reported as silent, for forty probes.
func TestAHealthyConnectionNeverReadsStalledOverTenMinutes(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	// The probe's ANSWER is the observable that has to happen before the clock
	// moves again: the deadline is fifteen simulated seconds, and advancing
	// without waiting for the answer would be asking the policy to judge a
	// probe the far side has not been given the chance to answer.
	var answered atomic.Int32
	holder.Observe(func(o hostconn.Observation) {
		if o.Answered {
			answered.Add(1)
		}
	})
	for i := 0; i < 40; i++ {
		clock.Advance(hostconn.ProbeInterval)
		waittest.WaitFor(t, "the probe to be answered", func() bool {
			return answered.Load() >= int32(i+1)
		})
		if got := lastState(holder.States()).Kind; got != hostconn.Connected {
			t.Fatalf("a healthy connection reads %v after %v", got, time.Duration(i+1)*hostconn.ProbeInterval)
		}
	}
	for _, state := range holder.States() {
		if state.Kind == hostconn.Stalled {
			t.Fatalf("a healthy connection was reported stalled: %v", holder.States())
		}
	}
}

// ─── wake from sleep (nocx-oalo1.4.5) ────────────────────────────────────
//
// The machine's suspend is the test's to report: no CI machine can sleep, so the
// stand's wake source is pulled and the clock is advanced. What is asserted is
// the design's own sentence — one probe per connection at once, five seconds of
// silence reads stalled (Q4), thirty ends a helper-owned connection, and a plain
// shell follows the silence rule.

// TestAWakeProbesAHealthyConnectionWithoutRedialingIt — a wake asks the
// connection to prove itself, and a link that answers stays exactly where it is.
func TestAWakeProbesAHealthyConnectionWithoutRedialingIt(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	stand.wake.Wake()
	waittest.WaitFor(t, "the wake's probe to be answered", func() bool {
		probes, _ := f.keepalivesSeen()
		return probes == 1
	})
	// No clock advance at all: the probe is the wake's, sent at the signal.
	if got := lastState(holder.States()).Kind; got != hostconn.Connected {
		t.Fatalf("a healthy connection reads %v after a wake, want connected", got)
	}
	if got := f.connections(); got != 1 {
		t.Fatalf("%d connections after a wake on a healthy link, want 1: a wake proves the connection, it does not redial it", got)
	}
}

// TestAWakeOverAParkedProbeReadsStalledInFiveSecondsAndIsNotDoubled is Q4 and
// the "one probe in flight" half together: the link is held, the wake's probe is
// the one that does not come back, a second wake while it is parked sends
// nothing, and when the wire is released exactly one probe reaches the host.
func TestAWakeOverAParkedProbeReadsStalledInFiveSecondsAndIsNotDoubled(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	// Everything on the wire is held from here: the wake's probe cannot be
	// answered, and a look-only dial has nowhere to get its version line from.
	proxy.HoldBanner()
	stand.wake.Wake()
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the connection to read stalled within the wake's five seconds", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})
	if got := lastState(holder.States()).Silent; got != hostconn.WakeProbeDeadline {
		t.Fatalf("stalled reports %v of silence, want %v (the wake's probe)", got, hostconn.WakeProbeDeadline)
	}

	// A second signal while that probe is parked: one probe is in flight, and a
	// second would queue behind the mux's send lock and measure nothing.
	stand.wake.Wake()

	proxy.Release()
	waittest.WaitFor(t, "the parked probe's answer", func() bool {
		return lastState(holder.States()).Kind == hostconn.Connected
	})
	probes, _ := f.keepalivesSeen()
	if probes != 1 {
		t.Fatalf("the host answered %d probes, want 1: a wake with a parked probe must not double it", probes)
	}
}

// TestAWakeClosesAHelperOwnedConnectionAndReconnects — thirty seconds after the
// signal with no answer, and nothing is lost by closing it.
func TestAWakeClosesAHelperOwnedConnectionAndReconnects(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	proxy.SilenceEstablished()
	stand.wake.Wake()
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})

	clock.Advance(hostconn.WakeCloseAfter)
	waittest.WaitFor(t, "the connection to be closed after the wake", func() bool {
		state := lastState(holder.States())
		return state.Kind == hostconn.Reconnecting && state.Cause == hostconn.CauseSilence
	})
	if !holder.Lost() {
		t.Fatal("the transport outlived the wake's close")
	}
	// The next acquire dials afresh: the closed connection is not handed out.
	next, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("Acquire after the wake's close: %v", err)
	}
	t.Cleanup(next.Release)
	if f.connections() < 2 {
		t.Fatalf("%d connections after the wake's close, want a fresh dial", f.connections())
	}
}

// TestAPlainShellConnectionSurvivesAWake — the person's terminal is not the
// policy's to close, wake or no wake.
func TestAPlainShellConnectionSurvivesAWake(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	shell := stand.openShell(t, destination)
	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	proxy.SilenceEstablished()
	stand.wake.Wake()
	clock.Advance(hostconn.WakeProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})

	clock.Advance(10 * time.Minute)
	if got := lastState(holder.States()).Kind; got != hostconn.Stalled {
		t.Fatalf("a plain-shell connection reads %v ten minutes after a wake, want stalled", got)
	}
	if holder.Lost() {
		t.Fatal("a plain-shell connection was closed after a wake")
	}
	if _, err := shell.Write([]byte("still here\n")); err != nil {
		t.Fatalf("a plain shell refused a write after a wake: %v", err)
	}

	proxy.Pass()
	waittest.WaitFor(t, "the connection to answer again", func() bool {
		return lastState(holder.States()).Kind == hostconn.Connected
	})
}

// ─── one connection, one identity, one prober (nocx-oalo1.4.4) ────────────
//
// The connection is the key, not the host name and not the pane: every holder
// rides one supervisor, every holder's connection is probed, and a finding
// reaches every shell on the connection and no other.

// TestAFarHelperLaneAloneOnAConnectionReadsStalled is the survey's own list —
// lane, channel, forward, tool socket, pane, probe lease — reduced to the
// case that used to have no prober at all: a connection whose only holder is a
// far helper's LANE. Nothing on it is a shell, and it still reads stalled when
// the link goes deaf.
func TestAFarHelperLaneAloneOnAConnectionReadsStalled(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	hold := make(chan struct{})
	f.execPeer = func(in io.Reader, out io.Writer) int {
		// The far helper's lane stays open for the test's duration: its bytes
		// are not what this test is about.
		<-hold
		return 0
	}
	t.Cleanup(func() { close(hold) })

	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	// The lane the far helper runs, through the same destination a pane uses.
	if _, err := stand.lane(t, proto.LaneParams{
		Destination: destination,
		Machine:     proto.Machine{Dir: laneDir()},
		Generation:  proto.GenerationID(laneGeneration),
	}); err != nil {
		t.Fatalf("open a lane: %v", err)
	}

	// The same connection, read: the lease the test holds IS a holder of it,
	// and its path is the supervisor's.
	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the lane's connection: %v", err)
	}
	t.Cleanup(holder.Release)
	if got := f.connections(); got != 1 {
		t.Fatalf("the lane and the holder made %d connections, want 1", got)
	}

	proxy.SilenceEstablished()
	clock.Advance(hostconn.ProbeInterval)
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "a lane-only connection to read stalled", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})
}

// TestTheSecondPaneKeepsReportingAfterTheFirstCloses is the survey's "stops when
// the arming pane closes": every shell registers its own observer on the one
// connection, so closing one leaves the other being told.
func TestTheSecondPaneKeepsReportingAfterTheFirstCloses(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	var firstReports atomic.Int32
	first := stand.openShellWith(t, destination, func(ssh.Reachability) { firstReports.Add(1) })
	secondReports := make(chan bool, 8)
	_ = stand.openShellWith(t, destination, func(r ssh.Reachability) {
		select {
		case secondReports <- r.Responsive:
		default:
		}
	})

	if got := f.connections(); got != 1 {
		t.Fatalf("two shells on one host made %d connections, want 1", got)
	}
	// The first pane closes; its observer goes with its lease.
	if err := first.Close(); err != nil {
		t.Fatalf("close the first pane: %v", err)
	}

	proxy.SilenceEstablished()
	clock.Advance(hostconn.ProbeInterval)
	clock.Advance(hostconn.ProbeDeadline)

	// The survivor is still being told, and the finding is silence.
	waittest.WaitFor(t, "the second pane to be told the connection stopped answering", func() bool {
		select {
		case responsive := <-secondReports:
			if responsive {
				t.Fatalf("the second pane was told the silent connection is responsive")
			}
			return true
		default:
			return false
		}
	})
	if firstReports.Load() != 0 {
		t.Fatalf("the closed pane's observer fired %d times after it closed", firstReports.Load())
	}
	if got := lastStateFrom(t, stand, destination, f); got != hostconn.Stalled {
		t.Fatalf("the connection reads %v, want stalled", got)
	}
}

// lastStateFrom reads the stand's own supervisor for a destination: the state
// the panes on that connection are being told about.
func lastStateFrom(t *testing.T, stand *stand, destination proto.SSHDestination, f *fixture) hostconn.Kind {
	t.Helper()
	lease, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		return hostconn.Connected
	}
	defer lease.Release()
	return lastState(lease.States()).Kind
}

// TestASingleHealthyPaneStillReports is the paired success: one pane on one
// connection, a wake, and the answer reaches the coordinator exactly as it did
// before this change.
func TestASingleHealthyPaneStillReports(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	reports := make(chan ssh.Reachability, 8)
	stand.openShellWith(t, destination, func(r ssh.Reachability) {
		select {
		case reports <- r:
		default:
		}
	})

	// A wake makes the probe happen now, so the test does not wait an interval.
	stand.wake.Wake()
	waittest.WaitFor(t, "the pane to be told its connection answered", func() bool {
		select {
		case r := <-reports:
			if !r.Responsive {
				t.Fatalf("a healthy connection was reported unresponsive: %+v", r)
			}
			if r.RoundTrip <= 0 {
				t.Fatalf("an answered probe carried no round trip: %+v", r)
			}
			return true
		default:
			return false
		}
	})
}

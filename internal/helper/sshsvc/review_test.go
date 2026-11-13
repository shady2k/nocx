//go:build nocx_local_ssh

package sshsvc_test

// The stage-2 review's findings and acceptance checks in this package:
// socket-first pane teardown (finding 2) and the tests finding 14 named —
// the thirty-second grace boundary, a reboot provoked by the post-look nudge,
// and a host silent for five minutes that still connects on the same attempt.

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// TestClosingAPaneOnAStalledConnectionReturns — finding 2: closing the remote
// lifecycle listener sends cancel-tcpip-forward with a reply wanted, and on a
// connection whose probe is parked that request waits on the mux's send lock.
// The pane releases its connection FIRST, so its teardown cannot wait on a
// stuck connection's probe.
func TestClosingAPaneOnAStalledConnectionReturns(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	pane, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("open a pane: %v", err)
	}
	holder, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)

	proxy.SilenceEstablished()
	clock.Advance(hostconn.ProbeInterval)
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "the connection to read stalled", func() bool {
		return lastState(holder.States()).Kind == hostconn.Stalled
	})

	closed := make(chan struct{})
	go func() {
		_ = pane.Close()
		close(closed)
	}()
	waittest.WaitFor(t, "the pane's close to return on a stalled connection", func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	})
}

// TestTheGraceBoundaryIsNotCrossedEarly — finding 14: the helper-owned close is
// thirty seconds after the answered look, and not before it. The lower bound is
// asserted exactly on the injected clock; the upper bound is a bounded wait for
// the closure the policy owes.
func TestTheGraceBoundaryIsNotCrossedEarly(t *testing.T) {
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
	// The look answers (a new connection passes) and the grace starts there.
	waittest.WaitFor(t, "the host to answer a look-only dial", func() bool { return f.connections() >= 2 })

	// One second before the grace, measured from the look's own answer: not
	// closed. The answer was processed at some reading inside this second, so
	// the assertion is a lower bound and cannot race the policy.
	clock.Advance(hostconn.LookGrace - time.Second)
	if holder.Lost() {
		t.Fatal("the connection was closed before the thirty-second grace elapsed")
	}
	// And the close the policy owes follows.
	clock.Advance(2 * time.Second)
	waittest.WaitFor(t, "the connection to be closed for silence", func() bool {
		return lastState(holder.States()).Kind == hostconn.Reconnecting
	})
}

// TestARebootedHostResetsTheFlowWhenTheNudgeArrives — finding 14: the reboot is
// provoked by the post-look nudge with a probe already parked, not by a direct
// Reset() call in the test.
func TestARebootedHostResetsTheFlowWhenTheNudgeArrives(t *testing.T) {
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

	// The flow goes deaf, the cadence's probe parks, and the host reboots
	// behind it: the flow's state is gone, so the next byte this side writes
	// is answered with a reset — but nothing has been written yet.
	proxy.SilenceEstablished()
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the probe to be consumed and held", func() bool {
		return proxy.HeldChunks() > 0
	})
	// The host reboots while the flow is parked: its state is gone, so the
	// next byte written on it will be answered with a reset.
	proxy.Reboot()
	clock.Advance(hostconn.ProbeDeadline)
	waittest.WaitFor(t, "the stall", func() bool {
		for _, state := range holder.States() {
			if state.Kind == hostconn.Stalled {
				return true
			}
		}
		return false
	})

	// The look proves the host is back; the nudge that follows is the first
	// byte written on the dead flow, and the reset is what the client sees.
	waittest.WaitFor(t, "the look to be answered", func() bool { return f.connections() >= 2 })
	waittest.WaitFor(t, "the reset the nudge provokes", func() bool { return holder.Lost() })
	waittest.WaitFor(t, "the loss to be reported", func() bool {
		state := lastState(holder.States())
		return state.Kind == hostconn.Reconnecting && state.Cause == hostconn.CauseLostLink
	})
}

// TestAHostSilentForFiveMinutesStillConnectsOnTheSameAttempt — finding 14,
// Q2: a host that takes the TCP connection and then goes silent for five
// minutes connects unaided, on that same attempt, when it answers.
func TestAHostSilentForFiveMinutesStillConnectsOnTheSameAttempt(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	proxy.HoldBanner()
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	type result struct {
		lease *hostconn.Lease
		err   error
	}
	done := make(chan result, 1)
	go func() {
		lease, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
		done <- result{lease: lease, err: err}
	}()

	// The attempt is under way and has not failed: the version exchange is
	// being held. Five simulated minutes pass; the dial has no clock deadline,
	// because a host that is slow after accept must not be given up on.
	waittest.WaitFor(t, "the attempt to reach the held banner", func() bool { return f.connections() == 1 })
	clock.Advance(5 * time.Minute)
	proxy.Release()

	var got result
	waittest.WaitFor(t, "the attempt to connect", func() bool {
		select {
		case got = <-done:
			return true
		default:
			return false
		}
	})
	if got.err != nil {
		t.Fatalf("the attempt failed instead of connecting: %v", got.err)
	}
	t.Cleanup(got.lease.Release)
	if conns := f.connections(); conns != 1 {
		t.Fatalf("%d connections, want 1: the SAME attempt must connect", conns)
	}
	if kind := lastState(got.lease.States()).Kind; kind != hostconn.Connected {
		t.Fatalf("the attempt ended in %v, want connected", kind)
	}
}

// TestAHopIsASupervisedConnection — finding 4: a hop gets its own link, watcher
// and prober. A routed connection through a real bastion is dialled, and BOTH
// the bastion's connection and the destination's are asked to answer: with the
// old path the bastion was a bare pool entry nothing probed.
func TestAHopIsASupervisedConnection(t *testing.T) {
	const password = "correct horse battery staple"
	target := newFixture(t, password, newSigner(t))
	bastion := newFixture(t, password, newSigner(t))
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: target.hostKeyFingerprint(),
	}, discardLogger(), clock)

	targetHost, targetPort := target.hostPort(t)
	bastionHost, bastionPort := bastion.hostPort(t)
	spec := ssh.PooledSpec{
		Host: targetHost, Port: targetPort, User: "test", Identity: "routed-target",
		Config: &gossh.ClientConfig{
			User: "test", Auth: []gossh.AuthMethod{gossh.PublicKeys(target.userSigner)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
		Route: []ssh.PooledHop{{
			Host: bastionHost, Port: bastionPort, User: "test", Identity: "hop",
			Config: &gossh.ClientConfig{
				User: "test", Auth: []gossh.AuthMethod{gossh.PublicKeys(bastion.userSigner)},
				HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
				Timeout:         10 * time.Second,
			},
		}},
	}
	lease, err := stand.carrier.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatalf("Acquire through a bastion: %v", err)
	}
	t.Cleanup(lease.Release)
	if bastion.connections() != 1 || target.connections() != 1 {
		t.Fatalf("connections bastion=%d target=%d, want one each", bastion.connections(), target.connections())
	}

	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the bastion's own connection to be probed", func() bool {
		probes, _ := bastion.keepalivesSeen()
		return probes >= 1
	})
	waittest.WaitFor(t, "the destination's connection to be probed", func() bool {
		probes, _ := target.keepalivesSeen()
		return probes >= 1
	})
}

// lookOf answers the look-only dial of a connection the test holds: the Link
// interface is the supervisor's, and Look is the carrier's optional capability.
func lookOf(t *testing.T, lease *hostconn.Lease) hostconn.Looker {
	t.Helper()
	looker, ok := lease.Link().(hostconn.Looker)
	if !ok {
		t.Fatalf("the connection's link offers no look-only dial (%T)", lease.Link())
	}
	return looker
}

// TestPaneCloseWithAParkedProbeBeforeItsDeadlineReturns — the verification's
// finding 2: `Askable` was a snapshot, so with another plain-shell holder and a
// probe parked BEFORE its deadline it read connected and the remote cancel
// parked on the mux's send lock. The cancel now runs off the caller's path, so
// the close returns whatever the connection is doing.
func TestPaneCloseWithAParkedProbeBeforeItsDeadlineReturns(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())

	pane, err := stand.paneAt(t, destination)
	if err != nil {
		t.Fatalf("open a pane: %v", err)
	}
	// The sibling that keeps the transport alive after this pane releases, and
	// the reader for the connection's state (an acquire during the stall would
	// wait — correctly — so it is taken before it).
	_ = stand.openShell(t, destination)
	reader, err := stand.carrier.Acquire(context.Background(), supervisedSpec(t, f, destination, false))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(reader.Release)

	// The probe is parked, and NOT past its deadline: the state still reads
	// connected, which is the window the snapshot missed.
	proxy.SilenceEstablished()
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitFor(t, "the probe to be consumed and held", func() bool {
		return proxy.HeldChunks() > 0
	})
	if got := lastState(reader.States()).Kind; got == hostconn.Stalled {
		t.Fatalf("the probe is already past its deadline (%v); the window this test is about has gone", got)
	}

	closed := make(chan struct{})
	go func() {
		_ = pane.Close()
		close(closed)
	}()
	waittest.WaitFor(t, "the pane's close to return with a parked probe", func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	})
}

// TestARoutedLookReachesTheDestinationThroughTheBastion — the verification's
// finding 11: the look on a route is the ROUTE TO THE DESTINATION, not the first
// bastion. It opens a direct-tcpip channel through the supervised bastion and
// reads the destination's version line; a silent bastion makes it fail.
func TestARoutedLookReachesTheDestinationThroughTheBastion(t *testing.T) {
	const password = "correct horse battery staple"
	target := newFixture(t, password, newSigner(t))
	bastion := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, bastion.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: target.hostKeyFingerprint(),
	}, discardLogger(), clock)

	targetHost, targetPort := target.hostPort(t)
	proxyHost, proxyPortStr, err := net.SplitHostPort(proxy.Addr())
	if err != nil {
		t.Fatalf("split the bastion address: %v", err)
	}
	proxyPort, err := strconv.Atoi(proxyPortStr)
	if err != nil {
		t.Fatalf("bastion port: %v", err)
	}
	spec := ssh.PooledSpec{
		Host: targetHost, Port: targetPort, User: "test", Identity: "routed-look",
		Config: &gossh.ClientConfig{
			User: "test", Auth: []gossh.AuthMethod{gossh.PublicKeys(target.userSigner)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
			Timeout:         10 * time.Second,
		},
		Route: []ssh.PooledHop{{
			Host: proxyHost, Port: proxyPort, User: "test", Identity: "hop",
			Config: &gossh.ClientConfig{
				User: "test", Auth: []gossh.AuthMethod{gossh.PublicKeys(bastion.userSigner)},
				HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // fixture server started by this test
				Timeout:         10 * time.Second,
			},
		}},
	}
	lease, err := stand.carrier.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatalf("Acquire through the bastion: %v", err)
	}
	t.Cleanup(lease.Release)

	// The look reaches the DESTINATION through the bastion: a direct-tcpip
	// channel, and the destination's own version line on it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := lookOf(t, lease).Look(ctx); err != nil {
		t.Fatalf("a routed look through a healthy bastion: %v (bastion saw %v, targets %v, connections bastion=%d target=%d)", err, bastion.observed(), bastion.directTargetsSeen(), bastion.connections(), target.connections())
	}
	seen := bastion.directTargetsSeen()
	if len(seen) == 0 {
		t.Fatal("the bastion was never asked for a direct-tcpip channel: the look did not go through the route")
	}

	// The bastion goes silent: the look's channel cannot open, so the look
	// fails instead of answering about the bastion alone.
	proxy.SilenceEstablished()
	blocked, cancelBlocked := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelBlocked()
	if err := lookOf(t, lease).Look(blocked); err == nil {
		t.Fatal("a look through a silent bastion answered")
	}
}

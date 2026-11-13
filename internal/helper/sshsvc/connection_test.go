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
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/sshtest"
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

//go:build nocx_local_ssh

package session_test

// An ssh pane's authenticated lifecycle channel, end to end over the helper's
// own forward (nocx-50w7p.14).
//
// # What is real here, and what is stood in for
//
// Real: the ssh connection to a real in-process ssh server, the remote forward
// the far side granted, the helper's carrier and window, the helper protocol,
// the coordinator's codec and its kernel — the SAME `lifecyclepub.Publisher`
// over the same `lifecycle.Kernel` the composition root wires, driven through
// the same `lifecyclechannel.NewStream` adapter a helper-hosted pane uses.
//
// Stood in for: the far side's INTEGRATED SHELL. The real one is the launcher's
// published generation, and nothing publishes to the fixture's "far host" yet
// (nocx-50w7p.15 owns that half); on a host with no generation stage-1 names
// `generation-unavailable` and execs a native login shell. So the far side's
// process here is the test's, and it speaks the SAME protocol on the SAME port
// the shell's rcfile dials — through the real codec, with the capability the
// kernel minted.
//
// The launch path is asserted separately and byte for byte: the far side
// RECEIVES the frame that carries the port, and the port in it is the one the
// far host actually bound.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// coordinatorPaneLifecycle is the coordinator's half of one pane's channel: the
// kernel's publisher, the adapter that speaks the protocol, and the launch the
// SPAWN carries.
//
// It is the same three objects the composition root builds for a helper-hosted
// pane (internal/app's hostedSpawn): the kernel mints, the publisher delivers
// the accept as soon as it is minted, and the adapter frames it over one
// stream — which is the stream a helper-hosted pane reaches through the
// session's own lifecycle carrier rather than a descriptor of its own.
type coordinatorPaneLifecycle struct {
	adapter *lifecyclechannel.Adapter
	launch  lifecyclechannel.Launch
	// peer is the adapter's stream end; the test bridges it to the session's
	// lifecycle stream once the session is attached.
	peer net.Conn
}

// newCoordinatorPaneLifecycle builds the coordinator's kernel, publisher and
// adapter over a pipe whose far end the caller bridges to the session.
func newCoordinatorPaneLifecycle(t *testing.T) *coordinatorPaneLifecycle {
	t.Helper()
	kernel := lifecyclepub.New(lifecycle.New(lifecycle.Options{Rand: rand.Reader}))
	coordinatorConn, peer := net.Pipe()
	adapter, err := lifecyclechannel.NewStream(log.NewSlogAdapter(standLog()), kernel, coordinatorConn)
	if err != nil {
		t.Fatalf("lifecycle adapter: %v", err)
	}
	t.Cleanup(func() {
		_ = adapter.Close()
		_ = peer.Close()
	})
	return &coordinatorPaneLifecycle{adapter: adapter, launch: adapter.Launch(), peer: peer}
}

// spawnParamsFor returns the stand's ordinary request with the lifecycle
// launch the kernel minted attached to it.
func (c *coordinatorPaneLifecycle) spawnParamsFor(t *testing.T, stand *sshStand, mode proto.SSHMode) proto.SSHSpawnParams {
	t.Helper()
	params := stand.spawnParams(t, mode)
	params.Lifecycle = &proto.LifecycleLaunch{
		Lane:       string(c.launch.Lane),
		Domain:     string(c.launch.Domain),
		Epoch:      c.launch.Epoch,
		Capability: c.launch.Capability,
		Recovery:   c.launch.Recovery,
	}
	return params
}

// bridge connects the coordinator's adapter to the session's lifecycle stream,
// exactly as the composition root does (internal/app's bridgeLifecycle): two
// copies, and either end closing ends both.
func (c *coordinatorPaneLifecycle) bridge(attached *client.AttachedSession) {
	carrier := attached.Lifecycle()
	closeBoth := func() {
		_ = c.peer.Close()
		_ = carrier.Close()
	}
	go func() {
		_, _ = io.Copy(carrier, c.peer)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(c.peer, carrier)
		closeBoth()
	}()
}

// mustAttach attaches to a session with the lifecycle stream requested, which
// is what a coordinator does before it can read anything a session produces.
func (s *sshStand) mustAttach(t *testing.T, entry client.SessionEntry) *client.AttachedSession {
	t.Helper()
	subscriber := proto.SubscriberID(strings.Repeat("ab", 16))
	attached, err := s.client.Attach(context.Background(), proto.AttachParams{
		Subscriber: subscriber,
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset:          proto.StreamOffset(entry.Window.Base),
		Fresh:           true,
		LifecycleOffset: 0,
		LifecycleFresh:  true,
		RequestWrite:    true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = attached.Close() })
	return attached
}

// farSideHello is the far host's process speaking the protocol the shell's
// rcfile speaks: dial the port its own sshd granted, send the authenticated
// hello, and read the answer.
type farSideHello struct {
	conn    net.Conn
	decoder *lifecyclecodec.Decoder
}

// dialFarSide dials the far host's granted lifecycle port. The port is the far
// host's own answer (the fixture recorded what it bound), which is what a
// shell that reads NOCX_LIFECYCLE_PORT has too.
func dialFarSide(t *testing.T, port int) *farSideHello {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("the far side could not dial its own lifecycle port %d: %v", port, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &farSideHello{conn: conn, decoder: lifecyclecodec.NewDecoder(conn, lifecyclecodec.Config{}, nil)}
}

// hello sends the authenticated hello the shell sends first.
func (f *farSideHello) hello(t *testing.T, launch lifecyclechannel.Launch, shell string) {
	t.Helper()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       launch.Lane,
		Domain:     launch.Domain,
		Epoch:      launch.Epoch,
		Sequence:   1,
		Capability: capabilityFromHex(t, launch.Capability),
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: shell}},
	}
	if _, err := lifecyclecodec.Encode(f.conn, env); err != nil {
		t.Fatalf("the far side could not send its hello: %v", err)
	}
}

// capabilityFromHex reads the adapter's own hex bearer back into the type the
// protocol's envelope carries it as. The shell does the same thing in its own
// language: the launch gives it hex, and the frame it sends carries the bytes.
func capabilityFromHex(t *testing.T, hexed string) lifecycle.Capability {
	t.Helper()
	raw, err := hex.DecodeString(hexed)
	if err != nil || len(raw) != len(lifecycle.Capability{}) {
		t.Fatalf("the launch's capability %q is not a bearer: %v", hexed, err)
	}
	var cap lifecycle.Capability
	copy(cap[:], raw)
	return cap
}

// awaitAccept reads frames until the kernel's accept arrives.
//
// It accepts nothing else, and that is the assertion: an authenticated hello
// the kernel took is answered with an accept, and any other frame here means
// the handshake did not complete.
func (f *farSideHello) awaitAccept(t *testing.T) lifecycle.Envelope {
	t.Helper()
	type result struct {
		env lifecycle.Envelope
		err error
	}
	done := make(chan result, 1)
	go func() {
		env, err := f.decoder.ReadFrame()
		done <- result{env: env, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the far side read no answer to its hello: %v", r.err)
		}
		if r.env.Event.Kind != lifecycle.KindAccept {
			t.Fatalf("the far side was answered with %q, want the kernel's accept: the lifecycle domain was never established",
				r.env.Event.Kind)
		}
		return r.env
	case <-time.After(paneWait):
		t.Fatal("the far side's hello was never answered: no accept arrived over the helper's forward")
		return lifecycle.Envelope{}
	}
}

// TestAnSSHPaneCompletesTheLifecycleHelloOverAHelperForward is this bead's
// first acceptance criterion: a pane the HELPER spawns completes the
// authenticated lifecycle handshake over a forward on its own connection.
//
// The four links, each observable on its own:
//
//  1. the far host granted a LOOPBACK LISTENER at the helper's request — the
//     forward exists (the fixture's own record of what it bound);
//  2. the pane's launch and its forward share ONE ssh connection, which is
//     AD-4's pool doing what it is for;
//  3. the shell's hello — sent by the far side's process, over the far host's
//     own loopback port — is answered by the COORDINATOR's kernel with an
//     accept, through the helper's carrier and the helper protocol;
//  4. and the session did not stay conventional: `adopt-lifecycle` answers the
//     launch the helper recorded, which it only keeps when a lifecycle window
//     exists behind it.
func TestAnSSHPaneCompletesTheLifecycleHelloOverAHelperForward(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	pane := newCoordinatorPaneLifecycle(t)

	entry := stand.mustSpawn(t, pane.spawnParamsFor(t, stand, proto.SSHModeAuto))
	attached := stand.mustAttach(t, entry)
	pane.bridge(attached)

	// 1. The far host's own record: it was asked for a listener and granted one.
	port := f.waitLifecyclePort(t)
	if port <= 0 {
		t.Fatalf("the far host granted port %d", port)
	}

	// 2. ONE connection carries the listener and the shell channel.
	if got := f.connections(); got != 1 {
		t.Fatalf("the far host saw %d ssh connections for a pane with a lifecycle listener; the listener and the shell must ride one pooled connection", got)
	}

	// The far side's process — the stand-in for the integrated shell — dials
	// the port its own host granted, speaks the protocol, and is answered.
	far := dialFarSide(t, port)
	far.hello(t, pane.launch, "bash")
	far.awaitAccept(t)

	// 4. Not conventional. adopt-lifecycle answers the launch, and only a
	// session with a lifecycle window keeps one.
	launch, err := stand.client.AdoptLifecycle(context.Background(), entry.HostSessionID)
	if err != nil {
		t.Fatalf("adopt-lifecycle: %v", err)
	}
	if launch == nil {
		t.Fatal("adopt-lifecycle answered null: the session stayed conventional despite a completed handshake")
	}
	if launch.Lane != string(pane.launch.Lane) || launch.Domain != string(pane.launch.Domain) {
		t.Fatalf("adopt-lifecycle answered %+v, want the launch the spawn carried (%s/%s)",
			launch, pane.launch.Lane, pane.launch.Domain)
	}

	// And the port the far side was TOLD is the port that answered. The frame
	// that carries it is written by the bootstrap, so the wait is on the far
	// side's own outcome token — the bootstrap's last word — and never on a
	// duration: the bytes are then a record of what it received.
	f.waitFarOutput(t, shellintegration.OutcomePrefix)
	delivered := string(f.programInputSeen())
	if !strings.Contains(delivered, "\n"+strconv.Itoa(port)+"\n") {
		t.Fatalf("the frame the far side received does not carry the port it dialled (%d); the launch named a listener nothing bound\ndelivered tail: %q", port, tail(delivered, 400))
	}

	// The session's end takes the far-side listener with it: a pane that is
	// over leaves nothing listening on somebody else's machine.
	if err := stand.client.CloseSession(context.Background(), entry.HostSessionID); err != nil {
		t.Fatalf("close-session: %v", err)
	}
	stopFarSide(t, f)
}

// stopFarSide returns once the far host has withdrawn its listener.
func stopFarSide(t *testing.T, f *sshFixture) {
	t.Helper()
	f.waitNoForwards(t)
}

// tail is a bounded excerpt of delivered bytes, for a failure message that has
// to show what actually arrived.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

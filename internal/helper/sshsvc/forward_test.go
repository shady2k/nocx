//go:build nocx_local_ssh

package sshsvc_test

// The FORWARD half of the ssh service, end to end: the two ops a listener needs
// (`forward` and `unforward`), the `direct-tcpip` kind a caller dials with, and
// the announcement a connection on a listener arrives as — each driven through
// the real client, against the real service, with a real ssh server on the far
// side.
//
// It is the sibling of channel_test.go, and it exists separately because the
// two are different planes with different ends: a CHANNEL is one stream a
// caller asked for and holds, a FORWARD is a listener plus the streams nobody
// here asked for (proto.OpForward's own note says why they are not one op). The
// assertions are, in both files, about what actually crossed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// destination is the fixture as a wire destination, authenticated with its
// own password — the same three facts probe parameters carry.
func destination(t *testing.T, f *fixture) proto.SSHDestination {
	t.Helper()
	host, port := f.hostPort(t)
	return proto.SSHDestination{
		Host: host, Port: port, User: "test",
		Identity: proto.SSHIdentity{
			Credential: &proto.SSHCredential{Ref: wantRef},
			Auth:       proto.SSHAuthPassword,
		},
	}
}

// echoTarget listens on a loopback port and echoes what it is sent: a
// destination on the FAR side's network, standing in for the service a
// direct-tcpip channel is opened to.
func echoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo target listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func targetOf(t *testing.T, addr string) *proto.ChannelTarget {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return &proto.ChannelTarget{Host: host, Port: port}
}

// acceptForward bounds one Forward.Accept.
//
// A test that waits on a connection that will never arrive would otherwise hang
// until the package timeout instead of failing, and nothing is proven by a
// hang: the mutation probe this helper was written for (the forwarded-tcpip
// announcement removed) made this package time out at 120s rather than name an
// assertion.
func acceptForward(t *testing.T, f *client.Forward, d time.Duration) (*client.ChannelStream, error) {
	t.Helper()
	type result struct {
		s   *client.ChannelStream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := f.Accept()
		ch <- result{s: s, err: err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("no connection arrived within %s", d)
	}
}

// forwardStand is the stand every test here uses: the fixture, with a
// coordinator that trusts its host key and answers its password.
func forwardStand(t *testing.T) (*fixture, *stand) {
	t.Helper()
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	s := newStand(t, coord)
	t.Cleanup(s.stop)
	return f, s
}

// TestADirectTCPIPChannelCarriesBytesToItsTarget is the outbound half of every
// forward: the coordinator names an address, the FAR side connects to it on its
// own network, and the bytes come back through the helper.
func TestADirectTCPIPChannelCarriesBytesToItsTarget(t *testing.T) {
	f, s := forwardStand(t)
	target := echoTarget(t)

	stream, err := s.client.OpenChannel(context.Background(), proto.OpenChannelParams{
		Destination: destination(t, f),
		Kind:        proto.ChannelDirectTCPIP,
		Target:      targetOf(t, target),
	})
	if err != nil {
		t.Fatalf("open a direct-tcpip channel: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if seen := f.directTargetsSeen(); len(seen) != 1 || seen[0] != target {
		t.Fatalf("the fixture was asked for direct-tcpip targets %v, want [%s]", seen, target)
	}

	payload := []byte("ping through a direct-tcpip channel")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestADirectTCPIPChannelToARefusedTargetIsRefused is the pair to the success
// above: the far side cannot reach the address, so the OPEN is refused — a
// refusal the caller can act on rather than a stream that silently carries
// nothing.
func TestADirectTCPIPChannelToARefusedTargetIsRefused(t *testing.T) {
	f, s := forwardStand(t)

	// A port nothing listens on: the listener is closed before the open.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dead target listen: %v", err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	stream, err := s.client.OpenChannel(context.Background(), proto.OpenChannelParams{
		Destination: destination(t, f),
		Kind:        proto.ChannelDirectTCPIP,
		Target:      targetOf(t, dead),
	})
	if err == nil {
		_ = stream.Close()
		t.Fatal("opening a channel to a refused target succeeded")
	}
	if code := refusalCode(err); code != proto.ErrCodeChannelRefused {
		t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeChannelRefused)
	}
}

// TestAForwardListenCarriesAnArrivingConnection is the -R plane and the remote
// lifecycle channel's: the far side listens, a connection arrives there, and it
// is announced to the coordinator as a channel of its own.
func TestAForwardListenCarriesAnArrivingConnection(t *testing.T) {
	f, s := forwardStand(t)

	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err != nil {
		t.Fatalf("open a forward: %v", err)
	}
	defer func() { _ = fwd.Close() }()

	// The port is the server's allocation, never a guessed 0.
	if wantHost, wantPort := f.lastForwardBind(); wantHost != "127.0.0.1" || wantPort == 0 || fwd.Bind().Port != wantPort {
		t.Fatalf("the forward reported %+v but the server bound %q:%d", fwd.Bind(), wantHost, wantPort)
	}

	// The "far machine" dials the listener the server opened.
	remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fwd.Bind().Port)))
	if err != nil {
		t.Fatalf("dial the far side's listener: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_ = remote.SetDeadline(time.Now().Add(10 * time.Second))

	accepted, err := acceptForward(t, fwd, 10*time.Second)
	if err != nil {
		t.Fatalf("accept the announced connection: %v", err)
	}
	defer func() { _ = accepted.Close() }()

	payload := "ping through a forwarded channel"
	if _, err := remote.Write([]byte(payload)); err != nil {
		t.Fatalf("write from the far side: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(accepted, buf); err != nil {
		t.Fatalf("read on the announced channel: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestAForwardRefusedByTheServerIsRefused is the pair every remote forward
// needs: a server that will not forward refuses the op, and the caller gets a
// refusal naming what the server said rather than a listener that never
// produced a connection.
func TestAForwardRefusedByTheServerIsRefused(t *testing.T) {
	f, s := forwardStand(t)
	f.mu.Lock()
	f.allowForward = false
	f.mu.Unlock()

	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err == nil {
		_ = fwd.Close()
		t.Fatal("opening a forward the server refuses succeeded")
	}
	if !strings.Contains(err.Error(), "denied by peer") {
		t.Fatalf("error = %q, want the server's own refusal in it", err)
	}
	if !strings.Contains(err.Error(), "channel") {
		t.Fatalf("error = %q, want the act that failed named in it", err)
	}
}

// TestUnforwardEndsTheListenerAndItsChannels is the other end of a forward: the
// optimistic Close cancels the remote listen, the accepted streams end with it,
// and a second unforward is answered as done rather than refused (the ordinary
// caller is a lease being released, and a second release is not a
// disagreement).
func TestUnforwardEndsTheListenerAndItsChannels(t *testing.T) {
	f, s := forwardStand(t)

	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err != nil {
		t.Fatalf("open a forward: %v", err)
	}
	remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fwd.Bind().Port)))
	if err != nil {
		t.Fatalf("dial the far side's listener: %v", err)
	}
	defer func() { _ = remote.Close() }()
	accepted, err := acceptForward(t, fwd, 10*time.Second)
	if err != nil {
		t.Fatalf("accept the announced connection: %v", err)
	}
	defer func() { _ = accepted.Close() }()

	if err := fwd.Close(); err != nil {
		t.Fatalf("close the forward: %v", err)
	}

	// The accepted channel ends with the listener it arrived on.
	done := make(chan error, 1)
	go func() {
		_, rerr := accepted.Read(make([]byte, 1))
		done <- rerr
	}()
	select {
	case rerr := <-done:
		if rerr == nil {
			t.Fatal("a read on a channel of a closed forward returned a byte")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a read on a channel of a closed forward is still parked")
	}

	// And the idempotent second unforward is an answer, not a refusal.
	if err := s.client.CloseChannel(context.Background(), accepted.ID()); err != nil {
		t.Fatalf("a second close of the forwarded channel was refused: %v", err)
	}
}

// TestTheForwardOpsRefuseParamsTheyCannotHonour pins the four shapes a caller
// can get wrong, each refused BEFORE anything is dialed: a target on a
// subsystem open, a direct-tcpip open with no target, a direct-tcpip open with
// an unusable target port, and a forward with no bind host.
func TestTheForwardOpsRefuseParamsTheyCannotHonour(t *testing.T) {
	cases := []struct {
		name   string
		params proto.OpenChannelParams
	}{
		{
			name: "a target on a subsystem open",
			params: proto.OpenChannelParams{
				Kind:   proto.ChannelSFTP,
				Target: &proto.ChannelTarget{Host: "127.0.0.1", Port: 22},
			},
		},
		{
			name:   "a direct-tcpip open with no target",
			params: proto.OpenChannelParams{Kind: proto.ChannelDirectTCPIP},
		},
		{
			name: "a direct-tcpip open with port 0",
			params: proto.OpenChannelParams{
				Kind:   proto.ChannelDirectTCPIP,
				Target: &proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
			},
		},
		{
			name: "an unknown kind",
			params: proto.OpenChannelParams{
				Kind: proto.ChannelKind("shell"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, s := forwardStand(t)
			p := tc.params
			p.Destination = destination(t, f)
			if stream, err := s.client.OpenChannel(context.Background(), p); err == nil {
				_ = stream.Close()
				t.Fatal("the open succeeded")
			}
			if seen := f.directTargetsSeen(); len(seen) != 0 {
				t.Fatalf("the fixture was asked to connect to %v for a refused open", seen)
			}
		})
	}
}

// TestAForwardWithNoBindHostIsRefused is the forward op's own params check: a
// listener with no address is not something this helper will ask a far host to
// open.
func TestAForwardWithNoBindHostIsRefused(t *testing.T) {
	f, s := forwardStand(t)
	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Port: 0},
	})
	if err == nil {
		_ = fwd.Close()
		t.Fatal("a forward with no bind host succeeded")
	}
	if !strings.Contains(err.Error(), "bind host") {
		t.Fatalf("error = %q, want the missing field named", err)
	}
}

// TestForwardIDsAreNotChannelIDs pins the separation the two identities exist
// for: an id minted for a listener is not a channel, and the coordinator's own
// ops keep them apart — closing a forward's id as a CHANNEL is answered as an
// unknown channel (a no-op) rather than ending the listener.
func TestForwardIDsAreNotChannelIDs(t *testing.T) {
	f, s := forwardStand(t)

	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err != nil {
		t.Fatalf("open a forward: %v", err)
	}
	defer func() { _ = fwd.Close() }()

	// The same 16 bytes as a channel id: an id this helper does not hold as a
	// channel, so the close is answered as closed and the listener survives.
	asChannel := proto.ChannelID(fwd.ID())
	if closeErr := s.client.CloseChannel(context.Background(), asChannel); closeErr != nil {
		t.Fatalf("closing a non-existent channel was refused: %v", closeErr)
	}
	remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fwd.Bind().Port)))
	if err != nil {
		t.Fatalf("the listener is gone after a channel close naming its id: %v", err)
	}
	defer func() { _ = remote.Close() }()
	accepted, err := fwd.Accept()
	if err != nil {
		t.Fatalf("Accept after a channel close naming the listener's id: %v", err)
	}
	_ = accepted.Close()
}

// TestTheForwardOpsConformToTheirContractsOverTheWire is the third contract
// check for this generation's additions: the payloads OFF THE WIRE — the params
// the coordinator sent, the results the helper answered, and the two
// announcements — validated against contracts/helper instead of against the
// structs a test built.
//
// Both halves of the plane are driven first, because a contract check needs the
// payloads to exist: a direct-tcpip channel to a real target, a forward with a
// connection arriving on it, and the unforward that ends it.
func TestTheForwardOpsConformToTheirContractsOverTheWire(t *testing.T) {
	f, s := forwardStand(t)
	target := echoTarget(t)

	stream, err := s.client.OpenChannel(context.Background(), proto.OpenChannelParams{
		Destination: destination(t, f),
		Kind:        proto.ChannelDirectTCPIP,
		Target:      targetOf(t, target),
	})
	if err != nil {
		t.Fatalf("open a direct-tcpip channel: %v", err)
	}
	if _, writeErr := stream.Write([]byte("x")); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	buf := make([]byte, 1)
	if _, readErr := io.ReadFull(stream, buf); readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	_ = stream.Close()

	fwd, err := s.client.OpenForward(context.Background(), proto.ForwardParams{
		Destination: destination(t, f),
		Bind:        proto.ChannelTarget{Host: "127.0.0.1", Port: 0},
	})
	if err != nil {
		t.Fatalf("open a forward: %v", err)
	}
	remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fwd.Bind().Port)))
	if err != nil {
		t.Fatalf("dial the far side's listener: %v", err)
	}
	announced, err := acceptForward(t, fwd, 10*time.Second)
	if err != nil {
		t.Fatalf("accept the announced connection: %v", err)
	}
	_ = announced.Close()
	_ = remote.Close()
	if err := fwd.Close(); err != nil {
		t.Fatalf("close the forward: %v", err)
	}

	// ── what the COORDINATOR sent ──────────────────────────────────────
	openParams := loadHelperSchema(t, "ssh.open.params.schema.json")
	forwardParams := loadHelperSchema(t, "ssh.forward.params.schema.json")
	unforwardParams := loadHelperSchema(t, "ssh.unforward.params.schema.json")
	paramsSchema := map[string]*jsonschema.Schema{
		proto.OpOpen:      openParams,
		proto.OpForward:   forwardParams,
		proto.OpUnforward: unforwardParams,
	}
	seenParams := map[string]int{}
	opByID := map[uint64]string{}
	for _, payload := range framesOf(t, s.toCoord.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		if schema, ok := paramsSchema[req.Op]; ok {
			seenParams[req.Op]++
			if err := validateHelperJSON(schema, req.Params); err != nil {
				t.Errorf("%s params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", req.Op, err, req.Params)
			}
		}
		opByID[req.ID] = req.Op
	}
	for _, op := range []string{proto.OpOpen, proto.OpForward, proto.OpUnforward} {
		if seenParams[op] == 0 {
			t.Errorf("no %s request was recorded, so its params contract was never checked on the wire", op)
		}
	}

	// ── what the HELPER answered ───────────────────────────────────────
	resultSchema := map[string]*jsonschema.Schema{
		proto.OpOpen:      loadHelperSchema(t, "ssh.open.schema.json"),
		proto.OpForward:   loadHelperSchema(t, "ssh.forward.schema.json"),
		proto.OpUnforward: loadHelperSchema(t, "ssh.unforward.schema.json"),
	}
	seenResults := map[string]int{}
	for _, payload := range framesOf(t, s.toHelper.bytes(), proto.TypeResponse) {
		resp := decodeResponse(t, payload)
		if resp.Error != nil || len(resp.Result) == 0 {
			continue
		}
		op, ok := opByID[resp.ID]
		if !ok {
			continue
		}
		schema, ok := resultSchema[op]
		if !ok {
			continue
		}
		seenResults[op]++
		if err := validateHelperJSON(schema, resp.Result); err != nil {
			t.Errorf("%s result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s", op, err, resp.Result)
		}
	}
	for _, op := range []string{proto.OpOpen, proto.OpForward, proto.OpUnforward} {
		if seenResults[op] == 0 {
			t.Errorf("no %s result was recorded, so its contract was never checked on the wire", op)
		}
	}

	// ── the two ANNOUNCEMENTS ──────────────────────────────────────────
	eventSchema := map[string]*jsonschema.Schema{
		proto.EventForwardedTCPIP: loadHelperSchema(t, "ssh.forwarded-tcpip.schema.json"),
		proto.EventForwardClosed:  loadHelperSchema(t, "ssh.forward-closed.schema.json"),
	}
	seenEvents := map[string]int{}
	// The helper's own writes carry them: toHelper records the frames the HELPER
	// sent, exactly as toCoord records the coordinator's (see harness_test.go).
	for _, payload := range framesOf(t, s.toHelper.bytes(), proto.TypeNotify) {
		var n proto.Notification
		if err := json.Unmarshal(payload, &n); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		if n.Service != proto.ServiceSSH {
			continue
		}
		schema, ok := eventSchema[n.Event]
		if !ok {
			continue
		}
		raw, err := json.Marshal(n.Params)
		if err != nil {
			t.Fatalf("re-marshal notification params: %v", err)
		}
		seenEvents[n.Event]++
		if err := validateHelperJSON(schema, raw); err != nil {
			t.Errorf("the %s announcement does not satisfy its contract:\n%v\n\npayload was:\n%s", n.Event, err, raw)
		}
	}
	for _, event := range []string{proto.EventForwardedTCPIP, proto.EventForwardClosed} {
		if seenEvents[event] == 0 {
			t.Errorf("no %s announcement was recorded, so its contract was never checked on the wire", event)
		}
	}
}

// TestTheOpenParamsSchemaDiscriminatesAKindFromItsTarget proves the frozen
// schema's if/then is live rather than decorative: the two shapes a caller can
// get wrong — a direct-tcpip open with no target, and a subsystem open carrying
// one — are REJECTED by the contract a helper generation is judged against, and
// the two shapes that are right are accepted.
//
// It matters because the schema is the only thing that can refuse a payload
// from a future coordinator: the Go struct's zero value is a legal object, so a
// schema that merely listed `target` would accept both mistakes.
func TestTheOpenParamsSchemaDiscriminatesAKindFromItsTarget(t *testing.T) {
	schema := loadHelperSchema(t, "ssh.open.params.schema.json")
	dest := `"destination":{"host":"127.0.0.1","port":22,"user":"test","identity":{"credential":{"ref":"cred-1"},"auth":"password"}}`
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "a direct-tcpip open with its target",
			payload: `{` + dest + `,"kind":"direct-tcpip","target":{"host":"db.internal","port":5432},"acceptOnTrust":false}`,
			want:    true,
		},
		{
			name:    "a subsystem open with no target",
			payload: `{` + dest + `,"kind":"sftp","acceptOnTrust":false}`,
			want:    true,
		},
		{
			name:    "a direct-tcpip open with no target",
			payload: `{` + dest + `,"kind":"direct-tcpip","acceptOnTrust":false}`,
			want:    false,
		},
		{
			name:    "a subsystem open carrying a target",
			payload: `{` + dest + `,"kind":"sftp","target":{"host":"db.internal","port":5432},"acceptOnTrust":false}`,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHelperJSON(schema, []byte(tc.payload))
			if tc.want && err != nil {
				t.Fatalf("a legal open was refused: %v", err)
			}
			if !tc.want && err == nil {
				t.Fatal("an illegal open satisfied the contract")
			}
		})
	}
}

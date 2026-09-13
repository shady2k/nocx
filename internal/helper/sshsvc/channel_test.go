//go:build nocx_local_ssh

package sshsvc_test

// The PROXIED-CHANNEL half of the ssh service, end to end: a real coordinator
// client asks a real helper host to open a channel, a real sftp CLIENT speaks
// the real protocol through the helper's bytes to a real sftp SERVER on the far
// side, and the assertions are about what actually crossed.
//
// # What each layer here is, and why it is not a mock
//
//   - the helper host, the service, the framing and the pool are the SHIPPED
//     code (harness_test.go's stand);
//   - the ssh server is somebody else's machine, with a real subsystem;
//   - the sftp client is pkg/sftp's — the same library the coordinator's file
//     and install paths use — so "the bytes are moved correctly" is answered by
//     a protocol implementation rather than by a string comparison.
//
// The only thing scripted is the coordinator's own answers, which is what the
// probe tests script too and for the same reason.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// openChannel asks the helper for one channel through the real client.
func (s *stand) openChannel(t *testing.T, p proto.OpenChannelParams) (*client.ChannelStream, error) {
	t.Helper()
	return s.client.OpenChannel(context.Background(), p)
}

// sftpParams builds the open params for the fixture's address, authenticated
// with the fixture's password. The identity is the coordinator's: a reference
// the helper echoes and a kind, and the public half is absent because this is
// password auth.
func sftpParams(t *testing.T, f *fixture) proto.OpenChannelParams {
	t.Helper()
	host, port := f.hostPort(t)
	return proto.OpenChannelParams{
		Destination: proto.SSHDestination{
			Host: host, Port: port, User: "test",
			Identity: proto.SSHIdentity{
				Credential: proto.SSHCredential{Ref: wantRef},
				Auth:       proto.SSHAuthPassword,
			},
		},
		Kind: proto.ChannelSFTP,
	}
}

// TestTheHelperOpensAnSFTPChannelAndMovesItsBytes is the happy path the whole
// proxied-channel plane exists for: a file written through the helper's bytes
// arrives on the far disk, and a listing read back through them is the real
// listing.
//
// It is deliberately driven by a REAL sftp client over the stream, because the
// question "did the bytes survive the middle hop" is not answerable by
// comparing them at the middle: only a protocol implementation on the far side
// can say whether what arrived was a file or a coincidence.
func TestTheHelperOpensAnSFTPChannelAndMovesItsBytes(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)
	t.Cleanup(stand.stop)

	stream, err := stand.openChannel(t, sftpParams(t, f))
	if err != nil {
		t.Fatalf("open an sftp channel: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if seen := f.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Fatalf("the fixture was asked for subsystems %v, want exactly [sftp]", seen)
	}

	// The real client, over the helper's bytes. Both directions are used: a
	// write crosses helper←coordinator and the server's reply crosses back.
	cl, err := sftp.NewClientPipe(stream, stream)
	if err != nil {
		t.Fatalf("sftp over the proxied stream: %v", err)
	}
	defer func() { _ = cl.Close() }()

	const payload = "the bytes crossed a process boundary"
	fh, err := cl.Create("crossed.txt")
	if err != nil {
		t.Fatalf("create through the channel: %v", err)
	}
	if _, writeErr := fh.Write([]byte(payload)); writeErr != nil {
		t.Fatalf("write through the channel: %v", writeErr)
	}
	if closeErr := fh.Close(); closeErr != nil {
		t.Fatalf("close the file: %v", closeErr)
	}

	// Read it from DISK, not through the channel: the assertion is about what
	// arrived on the far side, and reading it back through the same path would
	// pass for a stream that looped in the middle.
	onDisk, err := os.ReadFile(filepath.Join(f.rootDir, "crossed.txt"))
	if err != nil {
		t.Fatalf("read the file the channel wrote: %v", err)
	}
	if string(onDisk) != payload {
		t.Fatalf("the file on the far disk is %q, want %q", onDisk, payload)
	}

	// And the other direction of the data plane: a listing the server produces
	// and the helper carries back.
	entries, err := cl.ReadDir(".")
	if err != nil {
		t.Fatalf("list through the channel: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "crossed.txt" {
		t.Fatalf("the listing through the channel is %v, want [crossed.txt]", names)
	}

	// The close is the OP, and the helper must answer it: the idempotent second
	// close is the proof that the first one was applied rather than lost.
	if err := stream.Close(); err != nil {
		t.Fatalf("close the channel: %v", err)
	}
	if err := stand.client.CloseChannel(context.Background(), stream.ID()); err != nil {
		t.Fatalf("a second close of the same channel was refused: %v", err)
	}
}

// TestAClosedChannelEndsTheCoordinatorsStream is the END of a channel, which is
// the half a data plane cannot express: a zero-length frame is a legitimate
// write of no bytes, so the end has to be said out loud (proto's own note) and
// this asserts that it is.
func TestAClosedChannelEndsTheCoordinatorsStream(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)
	t.Cleanup(stand.stop)

	stream, err := stand.openChannel(t, sftpParams(t, f))
	if err != nil {
		t.Fatalf("open an sftp channel: %v", err)
	}
	// The helper's close is what ends the channel, and it is reached through
	// the client's own close path so the notification is what ends THIS side.
	if err := stream.Close(); err != nil {
		t.Fatalf("close the channel: %v", err)
	}

	// A reader must be released rather than parked: the stream is over.
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := stream.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("the reader ended with %v, want the stream's end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a reader on a closed channel was never released: the stream's end is not signalled")
	}
}

// TestAnOpenForAHostTheFixtureDoesNotKnowIsRefused: the failure path, and its
// point is the CLASS of the answer. A host that is not there must not come back
// as a helper-internal failure — a caller reading that would be sent to look at
// this machine's daemon instead of at the host — and it must not hang.
func TestAnOpenForAHostTheFixtureDoesNotKnowIsRefused(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)
	t.Cleanup(stand.stop)

	host, _ := f.hostPort(t)
	params := sftpParams(t, f)
	// Port 1 on loopback: reserved, nothing listens there, and the failure is a
	// refused connection rather than a timeout.
	params.Destination.Host = host
	params.Destination.Port = 1

	stream, err := stand.openChannel(t, params)
	if err == nil {
		_ = stream.Close()
		t.Fatal("an open to a port nothing listens on was answered with a channel")
	}
	var refusal *client.RefusalError
	if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeInternal {
		t.Fatalf("an unreachable host was refused as %q: a caller cannot tell that from this helper being broken", refusal.Code)
	}
}

// TestAnOpenWithNoKindIsRefusedBeforeAnythingIsDialed: D3's own boundary,
// restated at the op that could widen it. A kind is a member of a closed set
// and never a command, so an open that names nothing — or names something this
// generation does not implement — is refused by name.
func TestAnOpenWithNoKindIsRefusedBeforeAnythingIsDialed(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)
	t.Cleanup(stand.stop)

	for _, kind := range []proto.ChannelKind{"", "exec", "shell"} {
		params := sftpParams(t, f)
		params.Kind = kind
		if _, err := stand.openChannel(t, params); err == nil {
			t.Fatalf("an open with kind %q was accepted", kind)
		}
	}
	if passwords, keys := f.authAttempts(); len(passwords) != 0 || len(keys) != 0 {
		t.Fatalf("a refused open still authenticated: passwords %v, keys %v", passwords, keys)
	}
	if seen := f.subsystemsSeen(); len(seen) != 0 {
		t.Fatalf("a refused open still asked for subsystems %v", seen)
	}
}

// TestOneIdentitySharesOneConnectionAndAnotherDoesNot is AD-4's whole claim
// observed from outside: one connection per (destination, identity), channels
// multiplexed over it, a different principal on its own transport.
//
// The evidence is the FIXTURE's own auth log rather than anything this side
// remembers: a server authenticates once per connection, so the number of
// authentication attempts is the number of connections the helper dialed. Two
// channels opened with one credential must therefore produce ONE attempt, and a
// third opened with another credential must produce a second — which is the
// difference between a pool keyed by identity and a pool keyed by address that
// would let one credential's transport carry another's traffic.
func TestOneIdentitySharesOneConnectionAndAnotherDoesNot(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)
	t.Cleanup(stand.stop)

	open := func(t *testing.T, params proto.OpenChannelParams) *client.ChannelStream {
		t.Helper()
		stream, err := stand.openChannel(t, params)
		if err != nil {
			t.Fatalf("open an sftp channel: %v", err)
		}
		return stream
	}

	first := open(t, sftpParams(t, f))
	defer func() { _ = first.Close() }()
	second := open(t, sftpParams(t, f))
	defer func() { _ = second.Close() }()

	if passwords, _ := f.authAttempts(); len(passwords) != 1 {
		t.Fatalf("two channels on one credential made %d authentication attempts, want 1: they are not sharing a connection", len(passwords))
	}

	other := sftpParams(t, f)
	other.Destination.Identity.Credential.Ref = "cred-2"
	third := open(t, other)
	defer func() { _ = third.Close() }()

	if passwords, _ := f.authAttempts(); len(passwords) != 2 {
		t.Fatalf("a second credential made %d authentication attempts in total, want 2: it is riding the first credential's transport", len(passwords))
	}
}

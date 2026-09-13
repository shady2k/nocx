//go:build nocx_local_ssh

package sshsvc_test

// The stand this package's tests run against: a REAL helper host with the ssh
// service registered, a REAL coordinator client, a real socket between them,
// and a real ssh server on the other side of the probe.
//
// Nothing here is a mock of the thing under test. The two things that ARE
// scripted are the two that are not this package's: the coordinator (which in
// production is internal/app's handlers over a vault and known_hosts) and the
// ssh SERVER (which is somebody else's machine). Everything between them — the
// framing, the handshake, the reverse requests, the auth, the host-key
// question — is the shipped code path.
//
// The connection is wrapped on BOTH sides by a recorder, because three
// assertions in this package are about bytes rather than about results: that a
// private key never crosses in either direction, and that every new op's params
// and result satisfy their frozen schema as they were actually sent.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const testHash = "testhash"

// wantRef is the credential reference the scripted coordinator answers for. It
// is the coordinator's own opaque handle and the helper never interprets it —
// which is exactly what this value being arbitrary proves.
const wantRef = "cred-1"

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recorder is one end of the wire, keeping every byte ITS side wrote. Reads
// pass through untouched: what a test asks about the wire is what each side
// sent, and a recording of what arrived would answer a different question.
type recorder struct {
	net.Conn
	mu   sync.Mutex
	sent bytes.Buffer
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.sent.Write(p)
	r.mu.Unlock()
	return r.Conn.Write(p)
}

func (r *recorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.sent.Bytes()...)
}

// testKey is one keypair the test owns BOTH halves of: the signer the
// coordinator signs with, and the PEM text of its private half — which is what
// the "no private key crosses" assertion searches the wire for.
type testKey struct {
	signer gossh.Signer
	pem    []byte
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "nocx-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return testKey{signer: signer, pem: pem.EncodeToMemory(block)}
}

// ── the ssh server on the far side ─────────────────────────────────────

// fixture is an in-process ssh server that answers the way a host does while a
// credential is being tested: it authenticates, and that is all this probe ever
// gets to.
//
// A fixture of its own rather than a shared one, for the reason every other
// package in this repository gives: the fixtures that exist are `_test.go`
// symbols in other packages, and Go does not export those across packages
// (internal/tunnel/tunnel_real_test.go and internal/discovery/e2e_test.go say
// the same thing about the same server).
type fixture struct {
	addr       string
	hostSigner gossh.Signer
	// userSigner is the key this server ACCEPTS. A probe signing with anything
	// else is rejected by the server itself, which is how the "wrong key" case
	// is produced without asking the helper to lie about anything.
	userSigner gossh.Signer

	// rootDir is what an sftp subsystem serves: a real directory this test
	// seeds and reads back.
	rootDir string

	mu         sync.Mutex
	passwords  []string
	keys       []string
	subsystems []string
	// the forward plane (nocx-50w7p.8): the policy the server applies, the
	// listeners it bound, and the direct-tcpip targets it was asked to reach.
	allowForward  bool
	permitListen  func(host string, port int) bool
	forwards      map[string]net.Listener
	forwardBinds  []forwardBind
	directTargets []string
	// the exec plane (nocx-50w7p.10): the commands a LANE was asked to run, in
	// order, and the program each lane channel runs. The program is the far
	// side of the bridge — a function over the channel's bytes that answers an
	// exit status — because what this fixture must be able to produce is a
	// process, not a stream.
	execs    []string
	execPeer func(in io.Reader, out io.Writer) int
	// execRun, when set, runs the lane's command FOR REAL — the process's
	// stdin and stdout are the channel's — and answers its exit status. It is
	// how the end-to-end case runs the SHIPPED bridge binary instead of a
	// scripted echo, which is the difference between asserting the command and
	// asserting that the command reaches a helper.
	execRun bool
	execEnv []string
	// noExit drops the exit-status request, which is what an sshd does for a
	// channel that dies rather than one that exits.
	noExit bool
}

func newFixture(t *testing.T, password string, acceptedKey gossh.Signer) *fixture {
	t.Helper()
	f := &fixture{
		hostSigner:   newSigner(t),
		userSigner:   acceptedKey,
		rootDir:      t.TempDir(),
		allowForward: true,
		forwards:     map[string]net.Listener{},
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			f.mu.Lock()
			f.passwords = append(f.passwords, string(pw))
			f.mu.Unlock()
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("password refused")
		},
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			f.mu.Lock()
			f.keys = append(f.keys, gossh.FingerprintSHA256(key))
			f.mu.Unlock()
			if bytes.Equal(key.Marshal(), f.userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("key refused")
		},
	}
	config.AddHostKey(f.hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.addr = ln.Addr().String()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, fl := range f.forwards {
			_ = fl.Close()
		}
		f.mu.Unlock()
	})

	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go f.serve(conn, config)
		}
	}()
	return f
}

// serve runs one connection to the end. A probe never opens a channel — it
// authenticates and closes — so the session loop exists to keep a client that
// asks for one from hanging; a CHANNEL request, since nocx-50w7p.3, is served,
// and since nocx-50w7p.8 so are the two shapes a FORWARD needs: a direct-tcpip
// channel (which is a channel, but one that names a target rather than a
// subsystem) and the tcpip-forward global request (which is not a channel at
// all and is answered out of band).
func (f *fixture) serve(conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	sconn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go f.serveGlobalRequests(sconn, reqs)
	for newChan := range chans {
		if newChan.ChannelType() == "direct-tcpip" {
			f.serveDirectTCPIP(newChan, newChan.ExtraData())
			continue
		}
		ch, chReqs, aerr := newChan.Accept()
		if aerr != nil {
			continue
		}
		go f.serveChannel(ch, chReqs)
	}
}

// serveGlobalRequests answers the forwarding requests a remote listener needs.
// tcpip-forward binds a real loopback listener and replies with the allocated
// port (for a port-0 request); cancel-tcpip-forward drops it. Everything else
// is refused, so a test that asks for something this fixture does not implement
// fails where it asked instead of hanging on a reply that never comes.
//
// The forwarding payloads are decoded by hand: the protocol writes them as
// string/uint32 sequences (RFC 4254 §7) with lowercase field names, and
// gossh's Unmarshal cannot set an external struct's unexported fields.
func (f *fixture) serveGlobalRequests(sconn *gossh.ServerConn, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			f.handleForward(sconn, req)
		case "cancel-tcpip-forward":
			f.cancelForward(req)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// forwardBind is one successful tcpip-forward bind: what was requested and what
// the server actually bound (a requested port 0 is resolved here, so the reply
// has to carry this number).
type forwardBind struct {
	requestedHost string
	requestedPort int
	allocatedPort int
}

// lastForwardBind is the most recent successful bind.
func (f *fixture) lastForwardBind() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forwardBinds) == 0 {
		return "", 0
	}
	b := f.forwardBinds[len(f.forwardBinds)-1]
	return b.requestedHost, b.allocatedPort
}

func (f *fixture) handleForward(sconn *gossh.ServerConn, req *gossh.Request) {
	p := newForwardPayloadReader(req.Payload)
	addr, ok := p.str()
	rport, okPort := p.u32()
	if !ok || !okPort {
		_ = req.Reply(false, nil)
		return
	}
	f.mu.Lock()
	allow := f.allowForward && (f.permitListen == nil || f.permitListen(addr, int(rport)))
	f.mu.Unlock()
	if !allow {
		_ = req.Reply(false, nil)
		return
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(rport))))
	if err != nil {
		_ = req.Reply(false, nil)
		return
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		_ = req.Reply(false, nil)
		return
	}
	f.mu.Lock()
	if f.forwards == nil {
		f.forwards = map[string]net.Listener{}
	}
	f.forwards[net.JoinHostPort(addr, strconv.Itoa(tcpAddr.Port))] = ln
	f.forwardBinds = append(f.forwardBinds, forwardBind{requestedHost: addr, requestedPort: int(rport), allocatedPort: tcpAddr.Port})
	f.mu.Unlock()

	if rport == 0 {
		_ = req.Reply(true, gossh.Marshal(struct{ Port uint32 }{uint32(tcpAddr.Port)})) //nolint:gosec // SSH protocol values fit uint32
	} else {
		_ = req.Reply(true, nil)
	}
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			f.relayForwarded(sconn, addr, tcpAddr.Port, c)
		}
	}()
}

// cancelForward ends the listener the cancel names. The payload is the same
// bind address the request carried.
func (f *fixture) cancelForward(req *gossh.Request) {
	p := newForwardPayloadReader(req.Payload)
	addr, ok := p.str()
	rport, okPort := p.u32()
	if !ok || !okPort {
		_ = req.Reply(false, nil)
		return
	}
	f.mu.Lock()
	for key, ln := range f.forwards {
		if strings.HasPrefix(key, net.JoinHostPort(addr, strconv.Itoa(int(rport)))+":") ||
			key == net.JoinHostPort(addr, strconv.Itoa(int(rport))) {
			_ = ln.Close()
			delete(f.forwards, key)
		}
	}
	f.mu.Unlock()
	_ = req.Reply(true, nil)
}

// relayForwarded delivers one accepted connection to the client as a
// forwarded-tcpip channel — the exact -R data path OpenSSH provides.
func (f *fixture) relayForwarded(sconn *gossh.ServerConn, requestedHost string, allocatedPort int, c net.Conn) {
	defer func() { _ = c.Close() }()
	originHost, originPortStr, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return
	}
	originPort, err := strconv.Atoi(originPortStr)
	if err != nil {
		return
	}
	payload := gossh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{
		Addr:       requestedHost,
		Port:       uint32(allocatedPort), //nolint:gosec // SSH protocol values fit uint32
		OriginAddr: originHost,
		OriginPort: uint32(originPort), //nolint:gosec // SSH protocol values fit uint32
	})
	ch, chReqs, err := sconn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(chReqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(c, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, c); done <- struct{}{} }()
	<-done
}

// serveDirectTCPIP connects to the target the channel names and proxies it. The
// dial happens BEFORE the channel is accepted, so a refused target rejects the
// open itself — which is the refusal the coordinator's dial sees.
func (f *fixture) serveDirectTCPIP(newChan gossh.NewChannel, extraData []byte) {
	p := newForwardPayloadReader(extraData)
	raddr, ok := p.str()
	rport, okPort := p.u32()
	if _, okOrigin := p.str(); !ok || !okPort || !okOrigin {
		_ = newChan.Reject(gossh.ConnectionFailed, "connect failed: malformed direct-tcpip payload")
		return
	}
	f.mu.Lock()
	f.directTargets = append(f.directTargets, net.JoinHostPort(raddr, strconv.Itoa(int(rport))))
	f.mu.Unlock()
	targetConn, err := net.DialTimeout("tcp", net.JoinHostPort(raddr, strconv.Itoa(int(rport))), 5*time.Second)
	if err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, "connect failed: "+err.Error())
		return
	}
	defer func() { _ = targetConn.Close() }()
	ch, chReqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(chReqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(targetConn, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, targetConn); done <- struct{}{} }()
	<-done
}

// directTargetsSeen reports the direct-tcpip targets this fixture was asked to
// connect to, in order.
func (f *fixture) directTargetsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.directTargets...)
}

// forwardPayloadReader decodes the forwarding payloads the protocol puts on the
// wire as string/uint32 sequences (RFC 4254 §7): gossh's Marshal writes
// lowercase field names, and its Unmarshal cannot set an external package's
// unexported fields.
type forwardPayloadReader struct{ r *bytes.Reader }

func newForwardPayloadReader(b []byte) *forwardPayloadReader {
	return &forwardPayloadReader{r: bytes.NewReader(b)}
}

func (p *forwardPayloadReader) str() (string, bool) {
	var l uint32
	if err := binary.Read(p.r, binary.BigEndian, &l); err != nil {
		return "", false
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(p.r, b); err != nil {
		return "", false
	}
	return string(b), true
}

func (p *forwardPayloadReader) u32() (uint32, bool) {
	var v uint32
	if err := binary.Read(p.r, binary.BigEndian, &v); err != nil {
		return 0, false
	}
	return v, true
}

// serveChannel answers a session channel's requests. The `sftp` subsystem is
// served by pkg/sftp's own SERVER over the channel, which is what makes a
// channel test here a real sftp session and not an echo: the coordinator side
// of the test speaks the real protocol to a real server through the helper's
// proxied bytes.
//
// Anything else is refused rather than dropped, so a test that asks for the
// wrong subsystem fails where it asked instead of hanging.
func (f *fixture) serveChannel(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		// The lane's request comes FIRST, before the subsystem guard below: an
		// exec is not a subsystem, so a fixture that reached that guard first
		// would refuse every lane with "not a subsystem" — which is how this
		// branch was written the first time.
		if req.Type == "exec" {
			f.serveExec(ch, req)
			return
		}
		if req.Type != "subsystem" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct {
			Name string
		}
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		// The record goes BEFORE the reply, and the order is load-bearing
		// rather than cosmetic. The reply is what an assertion synchronizes
		// on: the helper returns from RequestSubsystem on it and answers the
		// open after that, so a record written once the reply is on the wire
		// is a record an assertion can outrun — which is what it did, in 15
		// of 300 repetitions under load, with the open already answered and an
		// empty list beside it. Written first, the reply carries the record
		// with it: whoever saw the answer saw the append.
		f.mu.Lock()
		f.subsystems = append(f.subsystems, payload.Name)
		f.mu.Unlock()
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(f.rootDir))
		if err != nil {
			_ = ch.Close()
			return
		}
		_ = server.Serve()
		_ = server.Close()
		_ = ch.Close()
		return
	}
	_ = ch.Close()
}

// serveExec runs ONE command on a channel — the far side of a lane — and
// answers its exit status.
//
// The command is recorded BEFORE the reply, for the reason the subsystem
// record is: the reply is what the helper synchronizes on, so a record written
// after it is a record an assertion can outrun.
func (f *fixture) serveExec(ch gossh.Channel, req *gossh.Request) {
	var payload struct {
		Command string
	}
	if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		_ = ch.Close()
		return
	}
	f.mu.Lock()
	f.execs = append(f.execs, payload.Command)
	peer, noExit, run, env := f.execPeer, f.noExit, f.execRun, append([]string(nil), f.execEnv...)
	f.mu.Unlock()
	if req.WantReply {
		_ = req.Reply(true, nil)
	}
	code := 0
	switch {
	case run:
		code = runExecCommand(ch, payload.Command, env)
	case peer != nil:
		code = peer(ch, ch)
	}
	if !noExit {
		_, _ = ch.SendRequest("exit-status", false,
			gossh.Marshal(struct{ Status uint32 }{Status: uint32(code)})) // #nosec G115 -- an exit status is 0-255
	}
	_ = ch.Close()
}

// runExecCommand runs one command line as sshd would, with the channel as its
// terminal-less stdin and stdout, and reports the exit status.
//
// The line is split on spaces rather than handed to a shell, and that is a
// deliberate simplification with a named reason: the command this op builds is
// three space-separated words with no quoting and no expansion (the install
// directory, the bridge subcommand and the generation), so a shell would add
// nothing a test could not see — while making the fixture depend on a POSIX
// shell being present, which a NixOS box does not promise and which would make
// the case skip exactly where it is needed. The command STRING is asserted
// separately, byte for byte.
func runExecCommand(ch gossh.Channel, command string, env []string) int {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return 127
	}
	cmd := exec.Command(fields[0], fields[1:]...) // #nosec G204 — this test's own build output and its own argv
	cmd.Env = append(os.Environ(), env...)
	// The child's stdin is OUR pipe, pumped from the channel by a goroutine,
	// and not the channel itself. Handing the channel to exec would make
	// Wait() block on exec's own stdin copier, which parks on a channel the
	// caller has not written to yet: the fixture then never reports an exit
	// status, and a lane whose bridge failed in milliseconds is reported as a
	// sentinel timeout ten seconds later (measured — this is how the first
	// version of the refusal case below read).
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 127
	}
	cmd.Stdout = ch
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 127
	}
	go func() {
		_, _ = io.Copy(stdin, ch)
		_ = stdin.Close()
	}()
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 127
	}
	return 0
}

// execsSeen reports the commands this fixture was asked to run, in order: the
// assertion that a lane runs the bridge this repository installs and nothing
// else.
func (f *fixture) execsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

// subsystemsSeen reports the subsystem names this fixture was asked for, in
// order: the assertion that the helper opened the channel the caller asked for
// and not a shell.
func (f *fixture) subsystemsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subsystems...)
}

func (f *fixture) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, portStr, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatalf("split %q: %v", f.addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return addr, port
}

func (f *fixture) authAttempts() (passwords, keyFingerprints []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passwords...), append([]string(nil), f.keys...)
}

func (f *fixture) hostKeyFingerprint() string {
	return gossh.FingerprintSHA256(f.hostSigner.PublicKey())
}

// ── the coordinator on this side ───────────────────────────────────────

// coordinator is this side of the reverse channel: the four ops a helper may
// ask a coordinator, scripted.
//
// It stands in for internal/app's handlers, and it stands in for them exactly
// in shape: same service, same ops, same result types, same refusal codes. What
// it does NOT reproduce is where a coordinator gets its answers (a vault, a
// known_hosts file) — that is internal/app's own test, and scripting it here
// keeps this package's tests about the helper.
type coordinator struct {
	password string
	// signer is the key this coordinator signs with. When it is not the key the
	// server accepts, the probe comes back rejected — a wrong credential,
	// produced on the far side rather than pretended here.
	signer gossh.Signer

	verdict     proto.HostKeyVerdict
	fingerprint string
	expected    string
	// sealed makes the material read fail the way a sealed vault does: a named
	// refusal with its own code, and not an internal error.
	sealed bool

	mu    sync.Mutex
	ops   []string
	trust []proto.TrustHostKeyParams
}

func (c *coordinator) registry() *client.ReverseRegistry {
	r := client.NewReverseRegistry()
	r.Register(proto.ServiceSSH, proto.OpSecret, func(_ context.Context, _ json.RawMessage) (any, error) {
		c.record(proto.OpSecret)
		if c.sealed {
			return nil, &proto.Refusal{Code: proto.ErrCodeVaultSealed, Message: "the vault is sealed"}
		}
		return proto.SecretResult{Secret: []byte(c.password)}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpSign, func(_ context.Context, raw json.RawMessage) (any, error) {
		c.record(proto.OpSign)
		var p proto.SignParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Credential.Ref != wantRef {
			return nil, &proto.Refusal{Code: proto.ErrCodeBadParams, Message: "unknown credential"}
		}
		sig, err := c.signer.Sign(rand.Reader, p.Challenge)
		if err != nil {
			return nil, err
		}
		return proto.SignResult{Signature: gossh.Marshal(sig)}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpVerifyHostKey, func(_ context.Context, _ json.RawMessage) (any, error) {
		c.record(proto.OpVerifyHostKey)
		return proto.VerifyHostKeyResult{
			Verdict:     c.verdict,
			Fingerprint: c.fingerprint,
			Expected:    c.expected,
		}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpTrustHostKey, func(_ context.Context, raw json.RawMessage) (any, error) {
		c.record(proto.OpTrustHostKey)
		var p proto.TrustHostKeyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		key, err := gossh.ParsePublicKey(p.Key)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.trust = append(c.trust, p)
		c.mu.Unlock()
		return proto.TrustHostKeyResult{Fingerprint: gossh.FingerprintSHA256(key)}, nil
	})
	return r
}

func (c *coordinator) record(op string) {
	c.mu.Lock()
	c.ops = append(c.ops, op)
	c.mu.Unlock()
}

func (c *coordinator) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

func (c *coordinator) trusted() []proto.TrustHostKeyParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]proto.TrustHostKeyParams(nil), c.trust...)
}

// ── the stand ─────────────────────────────────────────────────────────

// stand is one helper connection: a host with the ssh service on it, a client
// with a scripted coordinator behind it, and the bytes each side sent.
type stand struct {
	client    *client.Client
	helper    *host.Host
	service   *sshsvc.Service
	toHelper  *recorder
	toCoord   *recorder
	cancel    context.CancelFunc
	served    chan struct{}
	closeOnce sync.Once
}

func newStand(t *testing.T, c *coordinator) *stand {
	t.Helper()

	helperEnd, coordEnd := net.Pipe()
	s := &stand{
		toHelper: &recorder{Conn: helperEnd},
		toCoord:  &recorder{Conn: coordEnd},
		served:   make(chan struct{}),
	}
	// The dialer is the REAL one: DialAuth is the seam the daemon's client
	// offers, and a probe tested against a fake dialer would be a probe tested
	// against nothing.
	realClient, err := ssh.NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = realClient.Close() })

	s.service = sshsvc.New(realClient, discardLogger())
	s.helper = host.New(s.toHelper, s.toHelper, testHash, "instance-1", discardLogger())
	s.helper.Register(s.service)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.served)
		_ = s.helper.Serve(ctx)
	}()

	c2, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(s.toCoord),
		ExpectHash:  testHash,
		SentinelTTL: 5 * time.Second,
		Reverse:     c.registry(),
		Log:         discardLogger(),
	})
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	s.client = c2
	t.Cleanup(s.stop)
	return s
}

func (s *stand) stop() {
	s.closeOnce.Do(func() {
		_ = s.client.Close()
		s.cancel()
		select {
		case <-s.served:
		case <-time.After(5 * time.Second):
		}
	})
}

// probe runs one probe over the wire and answers what came back.
func (s *stand) probe(t *testing.T, p proto.ProbeParams) (proto.ProbeResult, error) {
	t.Helper()
	var out proto.ProbeResult
	err := s.client.Call(context.Background(), proto.ServiceSSH, proto.OpProbe, p, &out)
	return out, err
}

// ── small helpers ─────────────────────────────────────────────────────

func newSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// passwordProbeParams builds the params a coordinator sends for a password
// credential.
func passwordProbeParams(t *testing.T, f *fixture) proto.ProbeParams {
	t.Helper()
	host, port := f.hostPort(t)
	return proto.ProbeParams{
		Host: host, Port: port, User: "test",
		Identity: proto.SSHIdentity{
			Credential: proto.SSHCredential{Ref: wantRef},
			Auth:       proto.SSHAuthPassword,
		},
	}
}

// keyProbeParams builds the params for a key credential: the reference and the
// PUBLIC half, which is what the helper needs to declare the key it offers.
func keyProbeParams(t *testing.T, f *fixture, offer gossh.Signer) proto.ProbeParams {
	t.Helper()
	p := passwordProbeParams(t, f)
	p.Identity.Auth = proto.SSHAuthKey
	p.Identity.PublicKey = offer.PublicKey().Marshal()
	return p
}

// refusalCode answers the wire code of a Call failure, or "" when the call did
// not fail.
func refusalCode(err error) string {
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

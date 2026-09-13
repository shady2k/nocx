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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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
}

func newFixture(t *testing.T, password string, acceptedKey gossh.Signer) *fixture {
	t.Helper()
	f := &fixture{
		hostSigner: newSigner(t),
		userSigner: acceptedKey,
		rootDir:    t.TempDir(),
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
	t.Cleanup(func() { _ = ln.Close() })

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
// asks for one from hanging; a CHANNEL request, since nocx-50w7p.3, is served.
func (f *fixture) serve(conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	sconn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go gossh.DiscardRequests(reqs)
	for newChan := range chans {
		ch, chReqs, aerr := newChan.Accept()
		if aerr != nil {
			continue
		}
		go f.serveChannel(ch, chReqs)
	}
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
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		f.mu.Lock()
		f.subsystems = append(f.subsystems, payload.Name)
		f.mu.Unlock()
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

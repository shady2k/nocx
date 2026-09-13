//go:build nocx_local_ssh

package app

// A ROUTE and every CREDENTIAL KIND, through THIS MACHINE'S HELPER
// (nocx-50w7p.11).
//
// # What is real here
//
// The endpoint (a Unix socket the daemon serves), the host protocol over it,
// the `ssh` service and its ssh client, the ssh SERVER (the fixtures this
// package already uses for the open and probe paths, extended to proxy
// `direct-tcpip` so one of them can stand as a bastion), the coordinator's
// reverse handlers and their real known_hosts, and the coordinator's own
// resolution — as the resolver and the host-key authority, never as a dialer.
//
// # Why each case is paired
//
// Every kind of credential this bead makes reachable has a state in which it is
// NOT offered, and the two are the same code path: an agent that is there and
// one that is not, a key file that opens and one that is locked, a person who
// answers and one who does not. A test that proved only the success would leave
// the refusals as sentences nobody has ever seen produced, and the refusals are
// what a person reads when it does not work.
//
// # What is asserted about the wire, and why it is asserted on BYTES
//
// Two of the criteria are negative and structural: no private key and no agent
// socket may reach the helper. Both are checked by reading what each side
// actually SENT (the same recorders sshsvc's suite uses), because a claim about
// what did not cross cannot be made from a result that merely came back right.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// jumpProfile is the option set that routes a destination through bastion: the
// hop's own address, account and credential, exactly as the connection resolver
// builds them for a profile that names a jump host.
func jumpProfile(t *testing.T, bastion *pwSSHServer) []ssh.ConnectOption {
	t.Helper()
	return []ssh.ConnectOption{
		ssh.WithJumpHost("127.0.0.1", bastion.port(t), "jumper", "password"),
		// The hop's credential is bound to the endpoint its profile names,
		// exactly as the destination's is — the binding is what stops a key
		// being spent on a machine it was not saved for, and a hop is a machine
		// like any other.
		ssh.WithJumpAuthorizedEndpoint(bastion.addr),
		ssh.WithJumpConfig(&ssh.ConnectConfig{
			User:               "jumper",
			Port:               bastion.port(t),
			AuthMode:           "password",
			Secrets:            rememberedPassword{value: openPasswordFixturePassword},
			SecretID:           "sec:jump:1",
			AuthorizedEndpoint: bastion.addr,
		}),
	}
}

// port is the fixture's listening port, taken from the address it bound.
func (s *pwSSHServer) port(t *testing.T) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("split %q: %v", s.addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port %q: %v", portText, err)
	}
	return port
}

// TestAFilesLeaseReachesAHostBehindAJumpHost is the Files half of the route
// criterion: the provider a remote session is handed lists and reads a
// directory on a host that is only reachable through a bastion, and every
// channel of the lease rode ONE connection per hop.
//
// Its first half is the accept-on-first-use flow, because a ROUTED destination
// is a separate entry in known_hosts from the same host dialed directly: the
// refusal carries the storage identity to record, the test records it the way
// the accept act does, and the lease that follows is the acceptance. The
// bastion's own record of which address it was asked to reach is the evidence
// that the dial went through it — both fixtures are on loopback, so a helper
// that dropped the hops would still have listed the directory.
func TestAFilesLeaseReachesAHostBehindAJumpHost(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "routed.txt"), []byte("through the bastion"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	bastion := startPasswordSSHServer(t)
	target := startPasswordSFTPSSHServer(t, root)
	secrets := &askRecorder{value: openPasswordFixturePassword}
	stand := startFilesStand(t, target, secrets)
	stand.opts = append(stand.opts, jumpProfile(t, bastion)...)
	// A route has a host key per hop, and the coordinator is the party that
	// decides about each one; the bastion's is the ordinary direct identity.
	recordHostKey(t, stand.khPath, bastion.addr, bastion.hostSigner.PublicKey())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First contact with the destination: its key is unknown, and the evidence
	// names the STORAGE identity it belongs to under a route — a digest, never
	// the address.
	first, err := stand.provider(t)
	if err == nil {
		_ = first.Close()
		t.Fatal("a routed destination with an unrecorded key opened a lease")
	}
	var unknown *ssh.ErrUnknownHostKey
	if !errors.As(err, &unknown) {
		t.Fatalf("the first lease on a routed destination = %v (%T), want *ssh.ErrUnknownHostKey", err, err)
	}
	if unknown.KnownHostsAddr == "" || unknown.KnownHostsAddr == target.addr {
		t.Fatalf("a routed destination's storage identity is %q, want a route digest rather than %q",
			unknown.KnownHostsAddr, target.addr)
	}
	// The refusal happened at the host key, which is decided BEFORE a
	// credential is offered: the bastion was dialed, the destination was not
	// authenticated at all.
	if got := target.authAttempts(); len(got) != 0 {
		t.Fatalf("the destination was offered %q before its key was accepted", got)
	}
	if got := bastion.connCount(); got != 1 {
		t.Fatalf("the bastion authenticated %d connection(s), want 1 for the refused attempt", got)
	}

	// The accept, naming the identity the evidence carried. It APPENDS: a route
	// has a key per hop, and the bastion's line is already there.
	appendHostKey(t, stand.khPath, unknown.KnownHostsAddr, mustParseKey(t, unknown.Key))

	p, err := stand.provider(t)
	if err != nil {
		t.Fatalf("the Files provider after the routed key was accepted: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	listing, err := p.List(ctx, stand.root, filesystem.Page{Limit: 50})
	if err != nil {
		t.Fatalf("List through the helper's routed channel: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "routed.txt" {
		t.Fatalf("the listing through the route is %+v, want the seeded file", listing.Entries)
	}

	// The pool's own property, measured across two more operations and a
	// deliberate wait for a connection that would have been made by now: the
	// lease's connection is shared, so listing twice and reading once do not
	// add a dial on either hop. This is AD-4 asserted on the servers' counters
	// rather than on the pool's key.
	beforeBastion, beforeTarget := bastion.connCount(), target.connCount()
	if _, relistErr := p.List(ctx, stand.root, filesystem.Page{Limit: 50}); relistErr != nil {
		t.Fatalf("second List through the route: %v", relistErr)
	}
	content, err := p.Read(ctx, stand.path("routed.txt"), 0)
	if err != nil {
		t.Fatalf("Read through the helper's routed channel: %v", err)
	}
	if content.Text != "through the bastion" {
		t.Fatalf("the file read through the route is %q", content.Text)
	}
	if got := bastion.connCount(); got != beforeBastion {
		t.Fatalf("the bastion authenticated %d time(s) in total, want %d: a second operation on one lease must not dial again", got, beforeBastion)
	}
	if got := target.connCount(); got != beforeTarget {
		t.Fatalf("the destination authenticated %d time(s) in total, want %d: a second operation on one lease must not dial again", got, beforeTarget)
	}

	// ONE connection per hop for the lease: the bastion was dialed twice in
	// total (the refused attempt and the accepted one) and the destination
	// exactly once — and every channel of the lease rode that one.
	if got := bastion.connCount(); got != 2 {
		t.Fatalf("the bastion authenticated %d connection(s), want exactly 2 (one per attempt)", got)
	}
	if got := target.connCount(); got != 1 {
		t.Fatalf("the destination authenticated %d connection(s), want exactly 1", got)
	}
	if seen := bastion.directTargetsSeen(); len(seen) != 2 || seen[0] != target.addr || seen[1] != target.addr {
		t.Fatalf("the bastion was asked to reach %v, want the destination on both attempts", seen)
	}
	// The hop's credential was asked for SEPARATELY, by the reference the hop's
	// own profile names — one credential is not spent on two machines.
	if refs := secrets.asked(); !containsString(refs, "sec:jump:1") || !containsString(refs, filesFixtureSecret) {
		t.Fatalf("the hop and the destination did not each ask for their own credential; the helper asked for %v", refs)
	}
	t.Logf("MEASURED bastion %d auth(s) reaching %v, destination %d auth(s), asks %v",
		bastion.connCount(), bastion.directTargetsSeen(), target.connCount(), secrets.asked())
}

// appendHostKey adds one known_hosts line to the file the coordinator reads,
// leaving every line already there in place: a route's keys are recorded one
// hop at a time, and a write that replaced the file would un-trust the hop a
// person already accepted.
func appendHostKey(t *testing.T, khPath, addr string, key gossh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{addr}, key) + "\n"
	// #nosec G304 -- khPath is the known_hosts FILE this test's own stack
	// created, under a t.TempDir; there is no user input on this path.
	f, err := os.OpenFile(khPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open known_hosts: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append to known_hosts: %v", err)
	}
}

// mustParseKey reads back a public key blob the evidence carried.
func mustParseKey(t *testing.T, blob []byte) gossh.PublicKey {
	t.Helper()
	key, err := gossh.ParsePublicKey(blob)
	if err != nil {
		t.Fatalf("parse the offered key: %v", err)
	}
	return key
}

// containsString reports whether a recorded list names want.
func containsString(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}

// TestTheSettingsProbeReachesAHostBehindAJumpHost is the probe half of the route
// criterion: the settings surface's connection test dials the bastion, dials the
// destination through it, and reports the host key it met.
//
// The two probes are the accept-on-first-use flow, which is why there are two: a
// routed destination's key is stored under the coordinator's ROUTE identity
// rather than under its address, so the first probe is what tells the caller
// which identity to record. The counts then say what the probe does with a
// route: one connection per hop, per probe — a probe pools nothing, because a
// connection held open for a question that has already been answered is a
// resource whose lifetime nothing owns.
func TestTheSettingsProbeReachesAHostBehindAJumpHost(t *testing.T) {
	bastion := startPasswordSSHServer(t)
	target := startPasswordSSHServer(t)
	st := startProbeStack(t, target)
	// The bastion's own key is recorded, so the only host key this probe has to
	// decide about is the destination's.
	recordHostKey(t, st.khPath, bastion.addr, bastion.hostSigner.PublicKey())

	cfg, host := probeCfg(target)
	for _, opt := range jumpProfile(t, bastion) {
		opt(cfg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First contact: the destination's key is unknown, and the evidence says
	// which STORAGE identity it belongs to — a route digest, never the address.
	_, err := st.helper.ProbeWithResult(ctx, host, cfg)
	var unknown *ssh.ErrUnknownHostKey
	if !errors.As(err, &unknown) {
		t.Fatalf("the first routed probe = %v (%T), want *ssh.ErrUnknownHostKey", err, err)
	}
	if unknown.KnownHostsAddr == "" || unknown.KnownHostsAddr == target.addr {
		t.Fatalf("a routed destination's storage identity is %q, want a route digest rather than its dial address %q",
			unknown.KnownHostsAddr, target.addr)
	}
	if unknown.Addr != target.addr {
		t.Fatalf("the offered address is %q, want the address the handshake ran against (%q)", unknown.Addr, target.addr)
	}
	// A key nobody has accepted yet means no credential was offered.
	if attempts := target.authAttempts(); len(attempts) != 0 {
		t.Fatalf("the destination was offered %q before its key was accepted", attempts)
	}

	// The accept, through the one write path this repository has for
	// known_hosts, naming the identity the evidence carried.
	if _, trustErr := st.client.TrustHostKey(unknown.KnownHostsAddr, unknown.Key); trustErr != nil {
		t.Fatalf("record the routed host key: %v", trustErr)
	}

	fingerprint, err := st.helper.ProbeWithResult(ctx, host, cfg)
	if err != nil {
		t.Fatalf("the routed probe after the key was recorded: %v", err)
	}
	if fingerprint != pwServerFingerprint(target) {
		t.Fatalf("the probe reported %q, want the destination's host key", fingerprint)
	}
	if attempts := target.authAttempts(); len(attempts) != 1 || attempts[0] != openPasswordFixturePassword {
		t.Fatalf("the destination authenticated %q, want exactly the stored password once", attempts)
	}

	// One connection per hop, per probe — a probe pools nothing, because a
	// connection held open for a question already answered is a resource whose
	// lifetime nothing owns. The two hops differ by exactly one, and that is
	// the handshake's own order rather than a miscount: the FIRST probe ended at
	// the destination's unknown host key, which is checked before any credential
	// is offered, so the bastion was dialed twice and the destination
	// authenticated once.
	if got := bastion.connCount(); got != 2 {
		t.Fatalf("the bastion authenticated %d connection(s), want exactly 2 (one per probe)", got)
	}
	if got := target.connCount(); got != 1 {
		t.Fatalf("the destination authenticated %d connection(s), want exactly 1 (the probe whose key was accepted)", got)
	}
	if seen := bastion.directTargetsSeen(); len(seen) != 2 || seen[0] != target.addr || seen[1] != target.addr {
		t.Fatalf("the bastion was asked to reach %v, want the destination twice", seen)
	}
	if attempts := target.authAttempts(); len(attempts) != 1 || attempts[0] != openPasswordFixturePassword {
		t.Fatalf("the destination authenticated %q, want exactly the stored password once", attempts)
	}
	t.Logf("MEASURED bastion %d auth(s), destination %d auth(s), storage identity %s",
		bastion.connCount(), target.connCount(), unknown.KnownHostsAddr)
}

// TestAProbeWithAnInlineKeyFileSignsHereAndNeverHandsTheKeyOver is the inline
// key file's success: the file is read and parsed in THIS process, the helper
// authenticates by asking for a signature, and the private bytes never cross.
//
// The negative half is checked on the wire rather than on the result: the
// recorder holds every byte each side sent, and neither direction may contain
// the key's own text.
func TestAProbeWithAnInlineKeyFileSignsHereAndNeverHandsTheKeyOver(t *testing.T) {
	keyPath, keySigner := writeInlineKey(t)
	srv := startPasswordSSHServer(t)
	srv.acceptKey(keySigner.PublicKey())
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		KeyFile:            keyPath,
		ConnectionName:     "Inline Key",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fingerprint, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	if err != nil {
		t.Fatalf("probe with an inline key file through the helper: %v", err)
	}
	if fingerprint != pwServerFingerprint(srv) {
		t.Fatalf("the probe reported %q, want the host key it met", fingerprint)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != 1 || got[0] != gossh.FingerprintSHA256(keySigner.PublicKey()) {
		t.Fatalf("the destination authenticated key(s) %v, want the offered key exactly once", got)
	}
	assertNoKeyBytesOnTheWire(t, st, keyPath)
}

// TestAProbeWithALockedKeyFileNeedsAPersonAndSaysSo is the paired refusal: an
// encrypted key with no passphrase to read is the `needs-interactive` answer —
// the key is fine, it is only locked — and never a rejected credential, which
// would send a person to look at a host that is not the problem.
func TestAProbeWithALockedKeyFileNeedsAPersonAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, encryptedTestKey(t), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		KeyFile:            path,
		ConnectionName:     "Locked Key",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	var locked *ssh.ErrEncryptedKey
	if !errors.As(err, &locked) {
		t.Fatalf("a probe with a locked key file = %v (%T), want *ssh.ErrEncryptedKey", err, err)
	}
	if outcome, _, _ := ssh.ClassifyProbeError(err); outcome != ssh.OutcomeNeedsInteractive {
		t.Fatalf("classified as %q, want %s", outcome, ssh.OutcomeNeedsInteractive)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != 0 {
		t.Fatalf("a locked key was offered anyway: %v", got)
	}
}

// TestAProbeWithTheAgentsKeySignsThroughTheAgent is the ssh-agent success: the
// coordinator finds the key in the agent, signs through it when the far side
// challenges, and the agent's SOCKET never crosses — what crosses is a
// fingerprint in a reference.
func TestAProbeWithTheAgentsKeySignsThroughTheAgent(t *testing.T) {
	agentPublic := startAgent(t)
	srv := startPasswordSSHServer(t)
	srv.acceptKey(agentPublic)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "agent",
		ConnectionName:     "Agent",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg); err != nil {
		t.Fatalf("probe with the agent's key through the helper: %v", err)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != 1 || got[0] != gossh.FingerprintSHA256(agentPublic) {
		t.Fatalf("the destination authenticated key(s) %v, want the agent's key exactly once", got)
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		t.Fatal("the test set no agent socket, so there was nothing to keep off the wire")
	}
	assertNoSubstringOnTheWire(t, st, sock, "the agent socket path")
}

// TestAProbeWithNoAgentIsRefusedByName is the paired refusal, named: the
// connection says agent and no agent is reachable, which is a fact about this
// machine's desktop session rather than about the host being probed.
func TestAProbeWithNoAgentIsRefusedByName(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "agent",
		ConnectionName:     "Agent",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	var noMethod *ssh.ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("a probe with no agent = %v (%T), want *ssh.ErrNoAuthMethod", err, err)
	}
	if noMethod.Mode != "agent" {
		t.Fatalf("refused mode = %q, want agent", noMethod.Mode)
	}
	if got := srv.authAttempts(); len(got) != 0 {
		t.Fatalf("the destination was offered %q, want nothing: there was no credential to offer", got)
	}
}

// TestAProbeWithOnlyThePromptRungDeclinesWithoutAsking is the paired refusal for
// the interactive rung, and it is the probe's own boundary: a connection whose
// only credential is a person resolves to nothing a probe may present, so it
// declines HERE — before the helper is woken — rather than raising a dialog the
// settings surface never asked for.
func TestAProbeWithOnlyThePromptRungDeclinesWithoutAsking(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "keyboardInteractive",
		ConnectionName:     "Prompt",
		AuthorizedEndpoint: srv.addr,
		// A requester IS wired on the session's options — the resolver always
		// sets one for a saved profile — and the probe is what takes it back
		// off. That is the boundary this test is about.
		PasswordRequester: &promptSeam{answer: "unused"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	var noMethod *ssh.ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("a probe of a prompt-only profile = %v (%T), want *ssh.ErrNoAuthMethod", err, err)
	}
	if got := srv.authAttempts(); len(got) != 0 {
		t.Fatalf("the destination was offered %q, want nothing", got)
	}
}

// promptSeam is a session's prompt seam: it answers whatever it is told to and
// counts the asks, so a test can say whether a probe raised one.
type promptSeam struct {
	answer string
	asks   int
}

func (r *promptSeam) RequestConnectionPassword(context.Context, ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	r.asks++
	return ssh.PasswordAnswer{Password: r.answer}, nil
}

// ── the fixture's key and bastion accessors ─────────────────────────────
//
// They live in THIS file because the fixture they reach into is compiled into
// the untagged build too, where nothing asks a host for its key fingerprints or
// its bastion targets: a method nothing calls is a lint failure, and the honest
// fix is to hang it where it is used rather than to fabricate a caller.

// acceptKey arms this host with a public key it authenticates: the key half of
// the fixture, for the credentials that prove possession by signing.
func (s *pwSSHServer) acceptKey(key gossh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acceptedKey = key
}

// keyFingerprintsSeen reports the keys that were OFFERED at this host, in
// order — accepted or not, because "the wrong key was offered" is as much a
// fact about a credential as "the right one was".
func (s *pwSSHServer) keyFingerprintsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keyFingerprints...)
}

// directTargetsSeen reports the addresses this host was asked to reach on a
// `direct-tcpip` channel, in order: what makes one fixture able to stand as a
// bastion, and the evidence that a dial went THROUGH it.
func (s *pwSSHServer) directTargetsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.directTargets...)
}

// ── fixtures ────────────────────────────────────────────────────────────

// writeInlineKey writes one fresh private key and answers its path and signer.
func writeInlineKey(t *testing.T) (string, gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pemEncode(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, signer
}

// encryptedTestKey is one private key with a passphrase nobody here has: the
// state a key is in when it cannot be opened without a person.
func encryptedTestKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("a passphrase"))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// pemEncode wraps one PEM block as the bytes a key file holds.
func pemEncode(block *pem.Block) []byte { return pem.EncodeToMemory(block) }

// startAgent runs a real ssh-agent holding one real key on a unix socket and
// points SSH_AUTH_SOCK at it, answering that key's public half.
func startAgent(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("agent signer: %v", err)
	}
	keyring := agent.NewKeyring()
	if addErr := keyring.Add(agent.AddedKey{PrivateKey: priv, Comment: "nocx-test"}); addErr != nil {
		t.Fatalf("add key to the agent: %v", addErr)
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
	return signer.PublicKey()
}

// assertNoKeyBytesOnTheWire searches BOTH recorded directions for the private
// key's text: the PEM body, and the base64 of the raw bytes an embedded copy
// would have to wear.
func assertNoKeyBytesOnTheWire(t *testing.T, st *probeStack, keyPath string) {
	t.Helper()
	// #nosec G304 -- keyPath is the key file this test wrote a moment ago,
	// under a t.TempDir; there is no user input on this path.
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read the key back: %v", err)
	}
	assertNoSubstringOnTheWire(t, st, string(keyPEM), "the private key's PEM text")
	// The body without its armour: a copy that stripped the PEM headers would
	// still carry this.
	body := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(keyPEM),
		"-----BEGIN OPENSSH PRIVATE KEY-----", ""), "-----END OPENSSH PRIVATE KEY-----", ""))
	if len(body) > 64 {
		assertNoSubstringOnTheWire(t, st, body[:64], "the private key's base64 body")
	}
}

// assertNoSubstringOnTheWire fails when either direction of the recorded
// connection contains want. It is what makes a negative claim about a wire
// checkable: the recorders hold what each side actually SENT.
func assertNoSubstringOnTheWire(t *testing.T, st *probeStack, want, what string) {
	t.Helper()
	if want == "" {
		t.Fatalf("%s is empty, so the assertion would pass for the wrong reason", what)
	}
	if wire := st.wire.bytes(); bytesContains(wire, want) {
		t.Fatalf("%s crossed the wire (%d byte(s) recorded): it must stay on the coordinator's side",
			what, len(wire))
	}
	if len(st.wire.bytes()) == 0 {
		t.Fatal("no bytes were recorded on the wire, so the negative assertion passed for the wrong reason")
	}
}

// bytesContains is a substring search over recorded bytes.
func bytesContains(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}

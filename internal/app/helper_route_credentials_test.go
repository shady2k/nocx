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
	return startAgentHolding(t, freshAgentPrivates(t, 1)...)[0]
}

// freshAgentPrivates generates n private keys in the order an agent will offer
// them, so a test can name one of them on disk as well.
func freshAgentPrivates(t *testing.T, n int) []ed25519.PrivateKey {
	t.Helper()
	privates := make([]ed25519.PrivateKey, 0, n)
	for i := range n {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate agent key %d: %v", i, err)
		}
		privates = append(privates, priv)
	}
	return privates
}

// startAgentHolding runs a real ssh-agent holding exactly these private keys, in
// this order, and points SSH_AUTH_SOCK at it.
//
// The private halves are the CALLER's so that one of them can also be an
// identity file on disk — which is the setup IdentitiesOnly exists for, and the
// one this fixture has to be able to build.
func startAgentHolding(t *testing.T, privates ...ed25519.PrivateKey) []gossh.PublicKey {
	t.Helper()
	keyring := agent.NewKeyring()
	public := make([]gossh.PublicKey, 0, len(privates))
	for i, priv := range privates {
		signer, err := gossh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatalf("agent signer %d: %v", i, err)
		}
		if addErr := keyring.Add(agent.AddedKey{PrivateKey: priv, Comment: "nocx-test"}); addErr != nil {
			t.Fatalf("add key %d to the agent: %v", i, addErr)
		}
		public = append(public, signer.PublicKey())
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
	return public
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

// ── the ordinary local setup, through the helper (nocx-50w7p.19) ────────────
//
// Everything below is a profile that names NO credential. That is the setup a
// person has on the machine in front of them — "connect to this host with my
// keys" — and OpenSSH answers it from the agent and from the identity files its
// configuration lists, which is ssh's own default list when it lists none.
//
// Each case runs the REAL resolver (ssh -G, against the disposable home's own
// ~/.ssh), the REAL helper over a real socket and a REAL ssh server, and the
// negative halves are read off the recorded wire as above.

// sshHomeKey writes a fresh private key into the DISPOSABLE home's ~/.ssh under
// the given name, exactly where ssh looks for a default key, and answers its path
// and signer.
func sshHomeKey(t *testing.T, name string) (string, gossh.Signer) {
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
	path := filepath.Join(disposableSSHDir(t), name)
	if err := os.WriteFile(path, pemEncode(block), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path, signer
}

// writeHomeKey writes an EXISTING private key into the disposable home's ~/.ssh
// under the given name and answers the path. It is the half a test needs when the
// same key must be an identity file AND an agent key, which is the setup
// IdentitiesOnly exists for.
func writeHomeKey(t *testing.T, name string, priv ed25519.PrivateKey) string {
	t.Helper()
	dir := disposableSSHDir(t)
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pemEncode(block), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// disposableSSHDir answers the ~/.ssh of the DISPOSABLE home, creating it if
// needed — and REFUSES to hand out a path anywhere else.
//
// The refusal is the point. $HOME is replaced per test by the harness, and a
// fixture called before that replacement would otherwise write a private key and
// an ssh config into the developer's real ~/.ssh. That is not a theoretical
// failure: it happened while this file was being written, and the check below is
// what makes it impossible rather than unlikely.
func disposableSSHDir(t *testing.T) string {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("the fixture left no HOME, so a default key or a config would go somewhere unintended")
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp dir: %v", err)
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("resolve HOME %q: %v", home, err)
	}
	if !strings.HasPrefix(real, tmp+string(filepath.Separator)) {
		t.Fatalf("HOME is %q, which is not under %q: this fixture writes keys and ssh config, and it must only ever write them into a disposable home", real, tmp)
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}

// sshHomeConfig writes the disposable home's ~/.ssh/config, which is the file the
// REAL ssh -G oracle reads for these cases.
func sshHomeConfig(t *testing.T, body string) {
	t.Helper()
	dir := disposableSSHDir(t)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatalf("write the home's ssh config: %v", err)
	}
}

// TestAProbeWithNoNamedCredentialUsesADefaultKey is the default-key criterion
// through the whole stack: a profile that names nothing authenticates with a key
// from ~/.ssh, resolved and signed in the coordinator, and the private half never
// reaches the helper.
//
// Nothing else in this home says anything about the key, so what makes this work
// is the discovery and not a profile field — and the home is a disposably empty
// one apart from the key this test wrote, which is what keeps a developer's own
// ~/.ssh out of it.
func TestAProbeWithNoNamedCredentialUsesADefaultKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	keyPath, keySigner := sshHomeKey(t, "id_ed25519")
	srv.acceptKey(keySigner.PublicKey())

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		ConnectionName:     "Default Key",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fingerprint, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	if err != nil {
		t.Fatalf("a probe naming no credential, with a default key in this home: %v", err)
	}
	if fingerprint != pwServerFingerprint(srv) {
		t.Fatalf("the probe reported %q, want the host key it met", fingerprint)
	}
	want := gossh.FingerprintSHA256(keySigner.PublicKey())
	if got := srv.keyFingerprintsSeen(); len(got) != 1 || got[0] != want {
		t.Fatalf("the destination authenticated key(s) %v, want the default key %s exactly once", got, want)
	}
	t.Logf("MEASURED default key discovery authenticated with %s through the helper", want)
	assertNoKeyBytesOnTheWire(t, st, keyPath)
}

// TestAProbeWithNoDefaultKeyAndNoAgentIsRefused is the paired refusal: the same
// profile in a home with no key ssh would offer and no agent to ask. It is a
// refusal by NAME — nothing was named and nothing was found — and the far side
// is not contacted with a credential at all.
func TestAProbeWithNoDefaultKeyAndNoAgentIsRefused(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		ConnectionName:     "Nothing At All",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	var noMethod *ssh.ErrNoAuthMethod
	if !errors.As(err, &noMethod) {
		t.Fatalf("a probe with nothing to offer = %v (%T), want *ssh.ErrNoAuthMethod", err, err)
	}
	if got := srv.authAttempts(); len(got) != 0 {
		t.Fatalf("the destination was offered %q, want nothing at all", got)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != 0 {
		t.Fatalf("the destination was shown key(s) %v, want none", got)
	}
}

// TestAProbeWithAnAgentHoldingSeveralKeysUsesTheOneTheHostAccepts is the
// multi-key criterion end to end: the agent holds three keys, the host accepts
// only the LAST, and the probe authenticates — because the helper declares the
// whole queue inside one `publickey` method and the far side answers each entry
// in turn.
//
// The ORDER is asserted and not just the outcome: a queue that reached the helper
// reversed, or truncated to its first key, is a different connection, and the
// recording at the far side is what tells them apart.
func TestAProbeWithAnAgentHoldingSeveralKeysUsesTheOneTheHostAccepts(t *testing.T) {
	held := startAgentHolding(t, freshAgentPrivates(t, 3)...)
	srv := startPasswordSSHServer(t)
	srv.acceptKey(held[2])
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "agent",
		ConnectionName:     "Three Keys",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg); err != nil {
		t.Fatalf("a probe whose agent holds three keys, with the host accepting the last: %v", err)
	}
	declared := srv.keyFingerprintsSeen()
	if len(declared) != len(held) {
		t.Fatalf("the destination was shown %v, want all %d keys the agent holds", declared, len(held))
	}
	for i, key := range held {
		if want := gossh.FingerprintSHA256(key); declared[i] != want {
			t.Fatalf("the destination was shown %v, want %s at position %d (the agent's own order)",
				declared, want, i)
		}
	}
	t.Logf("MEASURED the multi-key offer %v, authenticated with the third", declared)

	// The agent's socket stays here, and so does every private half: what
	// crosses is a fingerprint per key, and one signature per challenge.
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		t.Fatal("the test set no agent socket, so there was nothing to keep off the wire")
	}
	assertNoSubstringOnTheWire(t, st, sock, "the agent socket path")
}

// TestAProbeWithARejectedQueueStillOffersEveryKey is the refusal pair for the
// case above: a host that accepts NONE of the three keys rejects the connection,
// and it saw the whole queue before it did — which is the difference between a
// refused credential and a queue that never arrived.
func TestAProbeWithARejectedQueueStillOffersEveryKey(t *testing.T) {
	held := startAgentHolding(t, freshAgentPrivates(t, 3)...)
	// A key the agent does NOT hold, so every key of the queue is refused. It is
	// generated here rather than through a second agent, because a second agent
	// would replace SSH_AUTH_SOCK and the probe would meet a key that is
	// actually there.
	acceptedSigner, err := gossh.NewSignerFromKey(freshAgentPrivates(t, 1)[0])
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	srv := startPasswordSSHServer(t)
	srv.acceptKey(acceptedSigner.PublicKey())
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "agent",
		ConnectionName:     "Three Keys, None Accepted",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	if err == nil {
		t.Fatal("a probe whose every key the host refuses succeeded")
	}
	if outcome, _, _ := ssh.ClassifyProbeError(err); outcome != ssh.OutcomeRejected {
		t.Fatalf("classified as %q, want %s (err %v)", outcome, ssh.OutcomeRejected, err)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != len(held) {
		t.Fatalf("the destination was shown %v, want every key of the queue", got)
	}
}

// TestAProbeHonoursIdentityFileAndIdentitiesOnlyFromTheConfig is the config half
// of the criterion, driven through the REAL resolver: the home's ~/.ssh/config
// names one identity file and sets IdentitiesOnly, the agent holds that key AND
// another, and only the named one is offered.
//
// IdentitiesOnly is what a person uses when their agent holds many keys, and it
// must suppress the keys the configuration does not name WITHOUT suppressing the
// agent itself — the named key here is signed for by the agent, so a resolution
// that dropped the agent's keys would refuse this connection entirely.
func TestAProbeHonoursIdentityFileAndIdentitiesOnlyFromTheConfig(t *testing.T) {
	privates := freshAgentPrivates(t, 2)
	// The UNNAMED key is added FIRST, which is the order an agent offers them
	// in — and the suppression is only observable that way: a key that would be
	// offered after the named one is never reached, because the handshake ends
	// at the key the host accepts.
	held := startAgentHolding(t, privates[1], privates[0])

	srv := startPasswordSSHServer(t)
	srv.acceptKey(held[1]) // the key the config names, which the agent signs for
	// The stack REPLACES $HOME with a disposable one, so everything that writes
	// into that home happens after it — a config written before would land in
	// whatever home the process was started with.
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	namedPath := writeHomeKey(t, "id_ed25519", privates[0])
	sshHomeConfig(t, "Host 127.0.0.1\n    IdentityFile "+namedPath+"\n    IdentitiesOnly yes\n")

	cfg := &ssh.ConnectConfig{
		User:               "e2euser",
		ConnectionName:     "IdentitiesOnly",
		AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg); err != nil {
		t.Fatalf("a probe under IdentityFile + IdentitiesOnly: %v", err)
	}
	declared := srv.keyFingerprintsSeen()
	if len(declared) != 1 {
		t.Fatalf("the destination was shown %v, want only the key the config names", declared)
	}
	if want := gossh.FingerprintSHA256(held[1]); declared[0] != want {
		t.Fatalf("the destination was shown %v, want %s", declared, want)
	}
	if withheld := gossh.FingerprintSHA256(held[0]); declared[0] == withheld {
		t.Fatal("the agent's UNNAMED key was offered under IdentitiesOnly")
	}
	t.Logf("MEASURED IdentitiesOnly offered %v and withheld the agent's unnamed key %s",
		declared, gossh.FingerprintSHA256(held[0]))
	assertNoKeyBytesOnTheWire(t, st, namedPath)
}

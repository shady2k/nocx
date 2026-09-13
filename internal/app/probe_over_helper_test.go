//go:build nocx_local_ssh

package app

// The settings connection probe, through THIS MACHINE'S HELPER, against a real
// in-process ssh server (nocx-50w7p.10).
//
// # What is real here and what is a double
//
// Real: the endpoint (a Unix socket the daemon serves), the host protocol over
// it, the `ssh` service and its ssh client (internal/helper/sshsvc, over a real
// `ssh.RealClient`), the ssh SERVER (the password fixture this package already
// uses for the open path), the coordinator's reverse handlers and their real
// known_hosts, and the coordinator's own ssh client — as the RESOLVER and the
// host-key authority, never as a dialer.
//
// The double is the artifact source, exactly as in openPasswordStack: nothing
// here installs anything.
//
// # Why the criterion needs this file
//
// `ProbeConfig` used to dial from the coordinator. Moving it means the
// coordinator keeps the DECISION — the verdict on a key it has never seen, the
// material a dial must present — while the dial itself becomes the helper's,
// and the evidence the renderer shows has to survive the process boundary in
// one piece. Each case below is paired: the success beside its refusal, and the
// classification beside the evidence.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// probeStack is one coordinator talking to one local daemon: the ssh-over-helper
// transport, the known_hosts both sides consult, and the daemon's own counter of
// how many connections it served.
type probeStack struct {
	helper *sshOverHelper
	khPath string
	// client is the coordinator's own ssh client: the known_hosts the verdicts
	// come from, and the one write path for a key a person accepts.
	client *ssh.RealClient
	// wire records every byte the connections between the two processes
	// carried, in either direction. It is what makes a NEGATIVE claim about
	// this ABI checkable — no private key and no agent socket may reach the
	// helper — rather than merely plausible from a result that came back.
	wire *wireLog
}

// wireTee records the bytes of one accepted connection in both directions. The
// two processes are on opposite ends of a real Unix socket, so a tee here is
// the whole of the wire between them and not a sample of it.
type wireTee struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *wireTee) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	w.record(p[:n])
	return n, err
}

func (w *wireTee) Write(p []byte) (int, error) {
	n, err := w.Conn.Write(p)
	w.record(p[:n])
	return n, err
}

func (w *wireTee) record(b []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(b)
}

func (w *wireTee) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// wireLog collects every tee one stack served. A stack serves several
// connections — a probe dials its own and a lease holds one — and a claim about
// what did NOT cross has to cover all of them.
type wireLog struct {
	mu   sync.Mutex
	tees []*wireTee
}

func (l *wireLog) add(w *wireTee) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tees = append(l.tees, w)
}

func (l *wireLog) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []byte
	for _, w := range l.tees {
		all = append(all, w.bytes()...)
	}
	return all
}

// startProbeStack brings up an endpoint served by the REAL ssh service, and the
// coordinator side that reaches it — with no install at all when src is nil,
// which is the "this machine has no helper" state.
func startProbeStack(t *testing.T, srv *pwSSHServer) *probeStack {
	t.Helper()
	return startProbeStackWithStore(t, srv, rememberedPassword{value: openPasswordFixturePassword})
}

// startProbeStackWithStore is the same stack with a different credential store.
// It exists because of how the material reaches the dial after the migration:
// the PROFILE carries a reference, the helper asks the coordinator for what it
// names, and the coordinator resolves it through the store its composition root
// wired — so a test that wants a refused credential, or a sealed vault, has to
// change the STORE and not the profile.
func startProbeStackWithStore(t *testing.T, srv *pwSSHServer, store credential.Resolver) *probeStack {
	t.Helper()
	logger := log.NewSlogAdapter(discardLogger())

	home := storagetest.IsolateWithHome(t)

	// The coordinator's ssh client: the known_hosts the verdicts come from, the
	// credential resolver's authority, and the resolver of the destination. It
	// dials NOTHING in this file — a dial would mean the migration had not
	// happened.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	coordinatorClient, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("coordinator ssh client: %v", err)
	}
	t.Cleanup(func() { _ = coordinatorClient.Close() })

	// The DAEMON's ssh client: the one whose DialAuth performs the handshake.
	helperClient, err := ssh.NewReal(logger)
	if err != nil {
		t.Fatalf("helper ssh client: %v", err)
	}
	t.Cleanup(func() { _ = helperClient.Close() })

	// The generation both ends agree on. It is a content hash because that is
	// what a generation IS; nothing here installs, so it names no real binary.
	const generation = proto.GenerationID("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	dir := endpoint.Dir(home)
	ln, err := endpoint.Listen(dir, generation)
	if err != nil {
		t.Fatalf("serving this machine's endpoint: %v", err)
	}

	svc := sshsvc.New(helperClient, discardLogger())
	wire := &wireLog{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			// Every accepted connection is teed: the negative claims about
			// this wire (no key material, no agent socket) are about what was
			// SENT, and a later connection would leak just as much as the
			// first.
			tee := &wireTee{Conn: conn}
			wire.add(tee)
			h := host.New(tee, tee, string(generation), "instance-1", discardLogger())
			h.Register(svc)
			_ = h.Serve(ctx)
		})
	}()

	opener := &localHelperOpener{
		log:     discardLogger(),
		dir:     dir,
		reverse: helperReverseHandlers(coordinatorClient, store, &helperPrompt{log: discardLogger()}, discardLogger()),
	}
	opener.installedLocalGeneration(helperlocal.Installed{
		// Never started: the endpoint above is already serving, so the binary is
		// only what `reach` would fall back to.
		Binary:     "/nonexistent/nocx-helper",
		Generation: generation,
	})

	t.Cleanup(func() {
		_ = ln.Close()
		cancel()
	})
	return &probeStack{
		helper: &sshOverHelper{local: opener, resolve: coordinatorClient, log: discardLogger()},
		khPath: khPath,
		client: coordinatorClient,
		wire:   wire,
	}
}

// noHelperStack is the same coordinator with NOTHING installed on this machine:
// no endpoint, no generation, no binary. It is the "remote helper absent" case,
// and it must refuse by name rather than dial.
func noHelperStack(t *testing.T) *probeStack {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	logger := log.NewSlogAdapter(discardLogger())
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	client, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("coordinator ssh client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	opener := &localHelperOpener{log: discardLogger(), dir: endpoint.Dir(home)}
	return &probeStack{
		helper: &sshOverHelper{local: opener, resolve: client, log: discardLogger()},
		khPath: khPath,
	}
}

// probeCfg is the resolved profile the settings surface would hand the probe: a
// password credential the coordinator holds, authorized for exactly this
// endpoint (ADR-0017's binding, which the resolver re-checks).
func probeCfg(srv *pwSSHServer) (*ssh.ConnectConfig, string) {
	return &ssh.ConnectConfig{
		User:               "e2euser",
		AuthMode:           "password",
		ConnectionName:     "Probe Proof",
		Secrets:            rememberedPassword{value: openPasswordFixturePassword},
		SecretID:           "sec:probe:1",
		AuthorizedEndpoint: srv.addr,
	}, srv.addr
}

// pwServerFingerprint is the key the fixture presents, in the spelling every
// host-key error and every known_hosts line carries.
func pwServerFingerprint(srv *pwSSHServer) string {
	return gossh.FingerprintSHA256(srv.hostSigner.PublicKey())
}

func trustFixtureHostKey(t *testing.T, khPath string, srv *pwSSHServer) {
	t.Helper()
	recordHostKey(t, khPath, srv.addr, srv.hostSigner.PublicKey())
}

// pwServerFingerprintKey is the fixture's public key, for a known_hosts line.
func pwServerFingerprintKey(srv *pwSSHServer) gossh.PublicKey { return srv.hostSigner.PublicKey() }

// recordHostKey writes one known_hosts line: the address to cover, and the key
// to record for it.
func recordHostKey(t *testing.T, khPath, addr string, key gossh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{addr}, key)
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

// TestTheSettingsProbeAuthenticatesThroughThisMachinesHelper is the
// criterion's success half: a stored password works against a real host, the
// dial is the HELPER's, and the fingerprint the settings surface stores comes
// back with the accepted outcome.
func TestTheSettingsProbeAuthenticatesThroughThisMachinesHelper(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)
	trustFixtureHostKey(t, st.khPath, srv)

	// A fresh client: the known_hosts was written after this one was built, and
	// knownhosts snapshots the file.
	cfg, host := probeCfg(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fingerprint, err := st.helper.ProbeWithResult(ctx, host, cfg)
	if err != nil {
		t.Fatalf("probe through this machine's helper: %v", err)
	}
	if fingerprint != pwServerFingerprint(srv) {
		t.Fatalf("the probe reported fingerprint %q, want the host key it met (%q): the settings surface "+
			"stores exactly this", fingerprint, pwServerFingerprint(srv))
	}
	// The server saw the stored password, once: exactly one method, as the
	// coordinator's own probe always sent.
	attempts := srv.authAttempts()
	if len(attempts) != 1 || attempts[0] != openPasswordFixturePassword {
		t.Fatalf("the server authenticated %q, want exactly the remembered password once", attempts)
	}
	// And the outcome the RENDERER is handed is the one it always was: the
	// transport classifies the error, so `accepted` is an absent error.
	if outcome, _, unclassified := ssh.ClassifyProbeError(err); unclassified != nil || outcome != ssh.OutcomeAccepted {
		t.Fatalf("classified as (%q, %v), want accepted", outcome, unclassified)
	}
}

// TestASettingsProbeOfAHostKeyNobodyRecordedCarriesItsEvidence is first
// contact: the key is refused BEFORE any credential is offered, and the
// evidence the accept sheet is built from survives the helper.
func TestASettingsProbeOfAHostKeyNobodyRecordedCarriesItsEvidence(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv) // known_hosts absent: nothing is recorded

	cfg, host := probeCfg(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, host, cfg)
	var unknown *ssh.ErrUnknownHostKey
	if !errors.As(err, &unknown) {
		t.Fatalf("probe of an unrecorded host key = %v (%T), want *ssh.ErrUnknownHostKey", err, err)
	}
	// The evidence, field by field: the sheet shows the fingerprint and echoes
	// the key back for connections.trustHostKey, so a rebuild that lost either
	// is a sheet nobody can answer.
	if unknown.Fingerprint != pwServerFingerprint(srv) {
		t.Fatalf("evidence fingerprint = %q, want the offered key's %q", unknown.Fingerprint, pwServerFingerprint(srv))
	}
	if len(unknown.Key) == 0 {
		t.Fatal("the evidence carries no key blob, so accepting this host would need a second handshake")
	}
	if unknown.KnownHostsAddr == "" || unknown.Addr == "" {
		t.Fatalf("evidence lost the addresses: addr %q known_hosts addr %q", unknown.Addr, unknown.KnownHostsAddr)
	}
	if unknown.KeyAlgo == "" {
		t.Fatal("the evidence lost the algorithm")
	}
	// The credential was never offered: a person who has not decided does not
	// hand their password to whoever answered the port.
	if attempts := srv.authAttempts(); len(attempts) != 0 {
		t.Fatalf("the server was offered %q, want no credential before the host key is accepted", attempts)
	}
	if outcome, detail, _ := ssh.ClassifyProbeError(err); outcome != ssh.OutcomeHostKeyUnknown {
		t.Fatalf("classified as %q (%s), want %s", outcome, detail, ssh.OutcomeHostKeyUnknown)
	}
}

// TestASettingsProbeOfAChangedHostKeyCarriesWhatItChangedFrom is the expensive
// case: a recorded key and a different one offered is the one signature of a
// machine in the middle, and a warning rendered without the value it changed
// FROM is a warning nobody can act on.
func TestASettingsProbeOfAChangedHostKeyCarriesWhatItChangedFrom(t *testing.T) {
	srv := startPasswordSSHServer(t)
	other := startPasswordSSHServer(t) // a different host key, and nothing else
	st := startProbeStack(t, srv)
	// The line is written for THE HOST WE DIAL (srv.addr) and carries the OTHER
	// server's key, which is what "a recorded key and a different one offered"
	// means: writing other's own address would be an unknown host, not a changed
	// one — and the two are different answers to a person.
	recordHostKey(t, st.khPath, srv.addr, pwServerFingerprintKey(other))

	cfg, host := probeCfg(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, host, cfg)
	var changed *ssh.ErrHostKeyMismatch
	if !errors.As(err, &changed) {
		t.Fatalf("probe of a changed host key = %v (%T), want *ssh.ErrHostKeyMismatch", err, err)
	}
	if changed.Fingerprint != pwServerFingerprint(srv) {
		t.Fatalf("offered fingerprint = %q, want %q", changed.Fingerprint, pwServerFingerprint(srv))
	}
	if changed.Expected != pwServerFingerprint(other) {
		t.Fatalf("recorded fingerprint = %q, want %q — the changed warning compares these two", changed.Expected, pwServerFingerprint(other))
	}
	if len(changed.Key) == 0 {
		t.Fatal("the changed-key evidence carries no key blob")
	}
	if outcome, _, _ := ssh.ClassifyProbeError(err); outcome != ssh.OutcomeHostKeyChanged {
		t.Fatalf("classified as %q, want %s", outcome, ssh.OutcomeHostKeyChanged)
	}
}

// TestASettingsProbeOfARefusedCredentialIsAnOutcome is the refusal half of the
// success above: the server says no, and that is an ANSWER the settings surface
// renders (a classified outcome) rather than an opaque failure.
func TestASettingsProbeOfARefusedCredentialIsAnOutcome(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := startProbeStackWithStore(t, srv, rememberedPassword{value: "the wrong password"})
	trustFixtureHostKey(t, st.khPath, srv)

	cfg, host := probeCfg(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, host, cfg)
	if err == nil {
		t.Fatal("a probe with a refused credential reported success")
	}
	if outcome, detail, unclassified := ssh.ClassifyProbeError(err); unclassified != nil || outcome != ssh.OutcomeRejected {
		t.Fatalf("classified as (%q, %v) detail %q, want %s", outcome, unclassified, detail, ssh.OutcomeRejected)
	}
	// The server saw the wrong password the store holds, and not the one that
	// would have worked: the credential that crossed is the one the profile's
	// REFERENCE resolved to, at the coordinator, at the moment the server
	// challenged for it.
	if attempts := srv.authAttempts(); len(attempts) != 1 || attempts[0] != "the wrong password" {
		t.Fatalf("the server was offered %q, want exactly the credential the profile's reference resolved to", attempts)
	}
}

// TestASettingsProbeOfAnUnreachableHostIsStillUnreachable is the classification
// the migration was most likely to break: the coordinator no longer has a
// net.OpError of its own to show, so the helper's dial failure is rebuilt as the
// type the transport classifies — and a rebuilt error that landed in
// "unclassifiable" would reach a person as an RPC error where they used to see
// "unreachable".
func TestASettingsProbeOfAnUnreachableHostIsStillUnreachable(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := startProbeStack(t, srv)

	// A port on loopback with nothing listening: a refused connection rather
	// than a timeout.
	dead := "127.0.0.1:1"
	cfg := &ssh.ConnectConfig{
		User: "e2euser", AuthMode: "password",
		Secrets:  rememberedPassword{value: openPasswordFixturePassword},
		SecretID: "sec:probe:1", AuthorizedEndpoint: dead,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, dead, cfg)
	if err == nil {
		t.Fatal("a probe of a port nothing listens on reported success")
	}
	outcome, detail, unclassified := ssh.ClassifyProbeError(err)
	if unclassified != nil {
		t.Fatalf("an unreachable host came back unclassifiable (%v): the settings surface would show an RPC error "+
			"where it used to show %s", unclassified, ssh.OutcomeUnreachable)
	}
	if outcome != ssh.OutcomeUnreachable {
		t.Fatalf("classified as %q (%s), want %s", outcome, detail, ssh.OutcomeUnreachable)
	}
}

// TestASettingsProbeWithoutAHelperOnThisMachineIsRefusedByName is the
// dependency's failure half: no local generation means no dial, and the
// refusal names that fact (the §6 state the pane open already reports) rather
// than reporting it as a network problem with the far host.
func TestASettingsProbeWithoutAHelperOnThisMachineIsRefusedByName(t *testing.T) {
	st := noHelperStack(t)
	cfg := &ssh.ConnectConfig{
		User: "e2euser", AuthMode: "password",
		Secrets:  rememberedPassword{value: openPasswordFixturePassword},
		SecretID: "sec:probe:1", AuthorizedEndpoint: "127.0.0.1:1",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fingerprint, err := st.helper.ProbeWithResult(ctx, "127.0.0.1:1", cfg)
	if err == nil {
		t.Fatal("a probe without a helper on this machine reported success")
	}
	if fingerprint != "" {
		t.Fatalf("a probe that never dialed reported fingerprint %q", fingerprint)
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("probe without a local helper = %v, want the named refusal that says this machine's helper is "+
			"not installed — never a sentence about the far host", err)
	}
	if outcome, _, unclassified := ssh.ClassifyProbeError(err); unclassified == nil && outcome == ssh.OutcomeUnreachable {
		t.Fatalf("the missing helper was reported as %q — a fact about this machine read as one about the far host", outcome)
	}
}

// TestASettingsProbeThatCannotRunNeverTouchesTheFarHost is the negative half
// of the migration, in the shape the epic's invariant needs: the coordinator
// must not have reached the host itself, with a dial of its own, when the
// helper cannot.
//
// It is asserted on the SERVER's evidence rather than on the coordinator's
// source: the fixture records every authentication, so a coordinator that
// dialed would be the one in that record. The success above authenticates
// exactly once — by the helper's client — and this test pins the other
// direction: a probe the coordinator cannot make (no helper installed on this
// machine) never reaches the host at all.
func TestASettingsProbeThatCannotRunNeverTouchesTheFarHost(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := noHelperStack(t)
	cfg := &ssh.ConnectConfig{
		User: "e2euser", AuthMode: "password",
		Secrets:  rememberedPassword{value: openPasswordFixturePassword},
		SecretID: "sec:probe:1", AuthorizedEndpoint: srv.addr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	trusted, err := st.helper.ProbeWithResult(ctx, srv.addr, cfg)
	if err == nil && trusted != "" {
		t.Fatal("a probe with no helper on this machine somehow authenticated")
	}
	if attempts := srv.authAttempts(); len(attempts) != 0 {
		t.Fatalf("the far host was offered %q by a coordinator that has no helper to dial through — the dial "+
			"belongs to the helper, and this process must not fall back to its own", attempts)
	}
}

// sealedVault is a store that cannot be read: the state a locked vault is in,
// at the seam the helper asks through.
type sealedVault struct{}

func (sealedVault) Resolve(context.Context, credential.SecretID, credential.Stance) (credential.Secret, error) {
	return credential.Secret{}, vault.ErrVaultSealed
}

// TestASettingsProbeOfASealedVaultRaisesTheUnlock is the failure path the
// migration was most likely to flatten.
//
// The material lives in the vault and the VAULT IS THE COORDINATOR'S, so a
// sealed one must reach the renderer as the reason it can act on — the unlock
// dialog — and not as "the helper could not authenticate". The helper's refusal
// code is its own spelling of that fact; what the surfaces switch on is this
// package's vault sentinel, and the assertion is exactly that: the error the
// probe returns still carries it (rpcErrorFor builds the wire's `data.reason`
// from this, which is what raises the sheet).
func TestASettingsProbeOfASealedVaultRaisesTheUnlock(t *testing.T) {
	srv := startPasswordSSHServer(t)
	st := startProbeStackWithStore(t, srv, sealedVault{})
	trustFixtureHostKey(t, st.khPath, srv)

	cfg, host := probeCfg(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.helper.ProbeWithResult(ctx, host, cfg)
	if err == nil {
		t.Fatal("a probe whose vault is sealed reported success")
	}
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("probe of a sealed vault = %v (%T), want the vault's own sentinel: the unlock sheet is raised "+
			"off exactly this, and a refusal that lost it would show a person an authentication error instead", err, err)
	}
	// And nothing was offered to the host: the credential could not be read, so
	// no method was sent.
	if attempts := srv.authAttempts(); len(attempts) != 0 {
		t.Fatalf("the server was offered %q with the vault sealed, want nothing", attempts)
	}
}

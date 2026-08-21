package app

// Live-sshd proofs (nocx-u7uh.17): the remote lifecycle channel driven
// against a REAL OpenSSH server instead of a fake seam.
//
// The fixture is a real sshd spawned by the test as the current user
// (non-root, on a high port, key auth only — verified working on
// OpenSSH 9.2 without the removed UsePrivilegeSeparation option), with host
// and client keys generated in Go. The session's HOME is overridden via
// sshd's SetEnv to a fresh directory owned by the test, so the fixture is
// hermetic: the launcher's publish prelude (nocx-k47n) writes its bundle
// there, the shell's hook always loads, and the developer's real home is
// never touched. The production composition is exercised unmodified:
// ssh.RealClient connects to it, the app package's own
// remoteLifecycleProvider asks it for a loopback tcpip-forward via
// lifecycleremote.New, and the app package's remoteLauncherAdapter builds
// the start command the sshd actually runs. The shell that reaches the
// forwarded port is a real interactive bash/zsh on the other side of a real
// SSH session — nothing is faked.
//
// Fixture gates — fail, never skip (nocx-gd84; a skipped test reporting
// success is the silent gap this bead exists to close):
//
//   - sshd must exist. Provision it the way the suite runs it: the
//     containerized runner's image (.githooks/images/go-tests/Dockerfile)
//     carries openssh-server; Debian/Ubuntu: apt-get install openssh-server;
//     macOS ships /usr/sbin/sshd.
//   - the current uid must have a passwd entry with a login shell. A
//     non-root sshd serves only the uid it runs as, so the test user IS the
//     passwd user. The containerized runner's root phase provisions one for
//     the setpriv uid; on a host the developer's own account is it.
//
// "Nothing installed" is asserted in the sense the channel promises
// (ADR-0024 decision 2, lifecycleremote: "Nothing is installed on the remote
// host"): the per-epoch capability is substituted into the transient rcfile
// text and must never persist anywhere on the remote host — after the
// session, no file under the (fixture-owned) session home contains it. The
// launcher's own ~/.nocx bundle (nocx-k47n, a pre-existing contract) is
// expected; the CHANNEL adds no artifact beyond the rcfile text it already
// carries, and the test asserts the home gains nothing else.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// Fixture: a real OpenSSH server as a subprocess.

type liveSshd struct {
	addr      string       // "127.0.0.1:<port>"
	user      string       // passwd name of the current uid
	home      string       // the session HOME (SetEnv override, fixture-owned)
	signer    gossh.Signer // client key installed in authorized_keys
	hostKey   gossh.PublicKey
	clientRaw ed25519.PrivateKey // the raw client key, for the child-line ssh-agent fixture
	client    *ssh.RealClient    // the pooled client, for the connection-loss proof
	cmd       *exec.Cmd
	logBuf    *lockedBuffer
	// registeredLanes records the lane→session bindings the provider
	// reported (the production RegisterLifecycleLane wiring); the tests
	// assert the minted lane reached the session it belongs to.
	registeredLanes []string

	// The seams the epic's end-to-end check (nocx-m8jwn.8) needs and the
	// proofs above do not. Each is nil/empty by default, so every existing
	// caller composes exactly as it did.
	//
	// logger is the PRODUCT logger this fixture's compositions are wired
	// with. "Product logs" is one of the surfaces the taint canary must not
	// appear on, and a surface nothing captures cannot be asserted.
	logger log.Logger
	// rand is the kernel's randomness. The canary is placed IN the
	// capability and the fence by minting them from a reader that stamps a
	// marker into every 32-byte read — which is exactly those two values
	// and nothing else the kernel reads.
	rand io.Reader
	// launcher, when set, replaces the launcher adapter connect() would
	// build. It is how the emitted remote command is recorded: the product
	// deliberately never logs it (it used to carry both bearers).
	launcher ssh.RemoteLauncher
	// tmpRoot is the session's TMPDIR, fixture-owned so "any remote root we
	// write to, including the temp root" is a directory the test can walk.
	tmpRoot string
	// recDir holds the far-side recordings: the argv and the environment of
	// every non-interactive bash the session ran, and the shell history.
	// It lives OUTSIDE the session home so the "nothing installed" walk is
	// unaffected by the recording.
	recDir string
	// histFile is $HISTFILE for the far shell, when recording is on.
	histFile string
}

// liveSshdOption tunes the fixture for one test. Variadic and additive: the
// existing callers pass none and get the server they always got.
type liveSshdOption func(*liveSshdConfig)

type liveSshdConfig struct {
	extraConfig string
	record      bool
}

// withSshdConfig appends lines to sshd_config. A Match block must be last,
// and only one caller may use it, so it is a single string rather than a
// list.
func withSshdConfig(lines string) liveSshdOption {
	return func(c *liveSshdConfig) { c.extraConfig = lines }
}

// withFarSideRecording turns on the three fixture-owned surfaces the canary
// is asserted against on the far host: a private TMPDIR, a real HISTFILE, and
// a recorder that writes the argv and the environment of the very process
// that runs our exec request.
//
// The recorder has to run INSIDE that process, because the argv exists
// nowhere else and not for long: sshd runs an exec request as
// `<login shell> -c <request>`, and the loader immediately execs, which
// replaces the argv. Nothing outside can look in time.
//
// The seam is `~/.bashrc`, and which seam it is was MEASURED rather than
// assumed. $BASH_ENV is the obvious answer and it is the wrong one here: bash
// reads $BASH_ENV for a non-interactive shell only when it does not think it
// was started by sshd, and when SSH_CLIENT is in the environment it sources
// `~/.bashrc` INSTEAD. A first attempt set BASH_ENV through sshd's SetEnv;
// the variable arrived on the far side (verified in the session's own
// environment) and the file was never sourced. So the recorder is sourced
// from the fixture's own `~/.bashrc`, and BASH_ENV is left pointing at it as
// well, so either rule fires and the records simply append.
//
// It writes to files and never to a descriptor of the session: a byte on
// stdout here would land in the middle of the loader's frame protocol.
func withFarSideRecording() liveSshdOption {
	return func(c *liveSshdConfig) { c.record = true }
}

// log is the product logger this fixture's compositions use.
func (fx *liveSshd) log() log.Logger {
	if fx.logger != nil {
		return fx.logger
	}
	return log.NewSlogAdapter(nil)
}

// authCount is how many times the server accepted an authentication — the
// observable that says whether a refusal cost the user a second credential
// use. LogLevel VERBOSE is what makes it observable.
func (fx *liveSshd) authCount() int {
	return strings.Count(fx.logBuf.String(), "Accepted publickey")
}

// sshdBinary returns the sshd path, failing (not skipping) when absent.
func sshdBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("sshd"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		return "/usr/sbin/sshd"
	}
	t.Fatalf("sshd is required by this test and missing from PATH and /usr/sbin.\n" +
		"The live-sshd suite proves the remote lifecycle channel against a REAL OpenSSH\n" +
		"server and must not silently skip (nocx-gd84). Provision it, then re-run:\n" +
		"  containerized runner: .githooks/containerized-tests.sh (the go-tests image\n" +
		"                        carries openssh-server)\n" +
		"  Debian/Ubuntu:        sudo apt-get install -y openssh-server\n" +
		"  macOS:                ships /usr/sbin/sshd\n")
	return ""
}

// requireLoginUser returns the current uid's passwd name, failing (not
// skipping) when no passwd entry exists — a non-root sshd cannot serve a
// user the passwd database does not know.
func requireLoginUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	if _, err := user.Lookup(u.Username); err != nil {
		t.Fatalf("no passwd entry for the current user %q (%v).\n"+
			"A non-root sshd serves only the uid it runs as, so this test's login user\n"+
			"IS the passwd user. Run the suite via the containerized runner (its root\n"+
			"phase provisions the setpriv uid as nocx-sshtest with /bin/bash), or ensure\n"+
			"your own account has a passwd entry with a login shell.", u.Username, err)
	}
	return u.Username
}

// genSigner generates an ed25519 keypair: the raw private key (for
// OpenSSH-format PEM marshalling) and the SSH signer (for auth and the
// host-key callback).
func genSigner(t *testing.T) (ed25519.PrivateKey, gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrap signer: %v", err)
	}
	return priv, signer
}

func reservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("reserved addr is %T, want *net.TCPAddr", ln.Addr())
	}
	port := tcpAddr.Port
	_ = ln.Close()
	return port
}

// startLiveSshd stands up a real OpenSSH server on 127.0.0.1:<free port>
// with key-only auth for the current user, AllowTcpForwarding per the
// caller, a fresh host key, and a fresh session HOME (SetEnv override) that
// carries a .bashrc naming the native prompt. The host and client keys are
// generated in Go; nothing beyond the sshd binary is required of the
// environment.
func startLiveSshd(t *testing.T, allowForward bool, opts ...liveSshdOption) *liveSshd {
	t.Helper()
	var fxCfg liveSshdConfig
	for _, o := range opts {
		o(&fxCfg)
	}
	sshdPath := sshdBinary(t)
	userName := requireLoginUser(t)

	dir := t.TempDir()
	hostKeyRaw, hostSigner := genSigner(t)
	clientRaw, clientSigner := genSigner(t)

	hostKeyPEM, err := gossh.MarshalPrivateKey(hostKeyRaw, "")
	if err != nil {
		t.Fatalf("marshal host key: %v", err)
	}
	hostKeyPath := filepath.Join(dir, "hostkey")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(hostKeyPEM), 0o600); err != nil {
		t.Fatalf("write host key: %v", err)
	}

	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, gossh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	// The session HOME: a fresh fixture-owned directory (SetEnv override,
	// verified on OpenSSH 9.2). Hermeticity is the point — the launcher's
	// publish writes its bundle here, the hook always loads, and the
	// developer's real home is never read or written. The planted .bashrc
	// names the native prompt deterministically (the refusal proof) and
	// redirects history away so the session leaves nothing behind.
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("mkdir session home: %v", err)
	}
	// $HISTFILE is redirected away by default so the session leaves nothing
	// behind. With recording on it is redirected to a fixture-owned file
	// INSTEAD, because "the shell's history" is one of the surfaces the
	// canary must not appear on and /dev/null cannot be searched.
	sessionEnv := []string{"HOME=" + home}
	rcHist := "HISTFILE=/dev/null\n"
	rcRecorder := ""
	var recDir, tmpRoot, histFile string
	if fxCfg.record {
		recDir = filepath.Join(dir, "rec")
		if err := os.Mkdir(recDir, 0o700); err != nil {
			t.Fatalf("mkdir recording dir: %v", err)
		}
		tmpRoot = filepath.Join(dir, "tmp")
		if err := os.Mkdir(tmpRoot, 0o700); err != nil {
			t.Fatalf("mkdir session TMPDIR: %v", err)
		}
		histFile = filepath.Join(recDir, "history")
		// The recorder. Every write is redirected to a file: a byte on this
		// process's stdout would arrive in the middle of the loader's frame
		// protocol. /proc is the exact answer and `ps` the portable one; the
		// redirection makes a failure of either silent rather than visible
		// on the terminal, and an empty recording is what the test reads as
		// "the recorder never fired".
		recScript := "{ tr '\\0' ' ' < /proc/$$/cmdline || ps -o args= -p $$ ; printf '\\n' ; } >> " +
			shellQuoteForSh(filepath.Join(recDir, "argv")) + " 2>/dev/null\n" +
			"{ tr '\\0' '\\n' < /proc/$$/environ || env ; } >> " +
			shellQuoteForSh(filepath.Join(recDir, "environ")) + " 2>/dev/null\n"
		recPath := filepath.Join(recDir, "rec.sh")
		if err := os.WriteFile(recPath, []byte(recScript), 0o600); err != nil {
			t.Fatalf("write far-side recorder: %v", err)
		}
		sessionEnv = append(sessionEnv,
			"TMPDIR="+tmpRoot,
			"HISTFILE="+histFile,
			"BASH_ENV="+recPath)
		rcRecorder = ". " + shellQuoteForSh(recPath) + "\n"
		// The rc must not overwrite the $HISTFILE the environment carries:
		// which rc file a far shell reads depends on how the launcher starts
		// it, and the environment reaches every one of them.
		rcHist = ""
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte(rcRecorder+"PS1='NATIVE_PROMPT> '\n"+rcHist), 0o600); err != nil {
		t.Fatalf("write fixture .bashrc: %v", err)
	}

	port := reservePort(t)
	forward := "yes"
	if !allowForward {
		forward = "no"
	}
	cfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
ChallengeResponseAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
AllowTcpForwarding %s
Subsystem sftp internal-sftp
SetEnv %s
LogLevel VERBOSE
%s
`, port, hostKeyPath, filepath.Join(dir, "sshd.pid"), authKeys, forward,
		strings.Join(sessionEnv, " "), fxCfg.extraConfig)
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}
	cmd := exec.Command(sshdPath, "-D", "-e", "-f", cfgPath) // #nosec G204 — sshdPath is a LookPath-validated binary; a spawned daemon is the only way to observe real sshd.
	// sshd -D re-execs its listener into a child of this process (OpenSSH
	// 9.8+), so killing only the parent would orphan the listener — one
	// leak per live-sshd test, and accumulated orphans load the machine
	// into flaking unrelated suites. Kill the whole group instead
	// (nocx-u7uh.29 found the leak this way).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// And the bound for a session child that has left that group, holding
	// the log pipe: see fixtureWaitDelay. The group kill above is what ends
	// this in the ordinary case; without the bound, one survivor turns this
	// cleanup into a package timeout.
	cmd.WaitDelay = fixtureWaitDelay
	logBuf := &lockedBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("sshd start: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // #nosec G206 — the group is this test's own sshd
		_ = cmd.Wait()
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	want := fmt.Sprintf("Server listening on 127.0.0.1 port %d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), want) {
			return &liveSshd{
				addr:      addr,
				user:      userName,
				home:      home,
				signer:    clientSigner,
				clientRaw: clientRaw,
				hostKey:   hostSigner.PublicKey(),
				cmd:       cmd,
				logBuf:    logBuf,
				tmpRoot:   tmpRoot,
				recDir:    recDir,
				histFile:  histFile,
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sshd did not report listening within 10s; log:\n%s", logBuf.String())
	return nil
}

// knownHostsPath writes a known_hosts file carrying the fixture's host key
// for the dial address and returns its path.
func (fx *liveSshd) knownHostsPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{fx.addr}, fx.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// rawClient opens a production-compatible SSH client to the fixture. Tests
// that exercise SFTP publication use this instead of reaching through
// ssh.RealClient's connection pool.
func (fx *liveSshd) rawClient(t *testing.T) *gossh.Client {
	t.Helper()
	client, err := gossh.Dial("tcp", fx.addr, &gossh.ClientConfig{
		User: fx.user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(fx.signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), fx.hostKey.Marshal()) {
				return fmt.Errorf("host key mismatch")
			}
			return nil
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial live sshd: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// forceInstalledVersion turns the current committed bundle into an older,
// still-valid activation. The next EnsureInstalledRemote must therefore
// stage a new generation and atomically replace the existing manifest.
func forceInstalledVersion(t *testing.T, home, oldVersion string) {
	t.Helper()
	root := filepath.Join(home, ".nocx")
	manifestPath := filepath.Join(root, "manifest.json")
	data, readErr := os.ReadFile(manifestPath) // #nosec G304 — manifestPath is under the fixture-owned t.TempDir home.
	if readErr != nil {
		t.Fatalf("read installed manifest: %v", readErr)
	}
	var manifest map[string]any
	if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil {
		t.Fatalf("decode installed manifest: %v", decodeErr)
	}
	generation, ok := manifest["generation"].(string)
	if !ok || generation == "" {
		t.Fatalf("installed manifest generation = %#v", manifest["generation"])
	}
	oldGeneration := "v" + oldVersion
	if renameErr := os.Rename(
		filepath.Join(root, "integration", generation),
		filepath.Join(root, "integration", oldGeneration),
	); renameErr != nil {
		t.Fatalf("rename installed generation: %v", renameErr)
	}
	manifest["version"] = oldVersion
	manifest["generation"] = oldGeneration
	data, encodeErr := json.MarshalIndent(manifest, "", "  ")
	if encodeErr != nil {
		t.Fatalf("encode older manifest: %v", encodeErr)
	}
	data = append(data, '\n')
	if writeErr := os.WriteFile(manifestPath, data, 0o600); writeErr != nil {
		t.Fatalf("write older manifest: %v", writeErr)
	}
}

// homeEntries lists the session home recursively, relative paths.
func homeEntries(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// assertSessionLeftOnlyTheLauncherBundle fails unless the session home holds
// nothing but the fixture's .bashrc and the launcher's own ~/.nocx bundle
// (nocx-k47n), and no file anywhere contains the per-epoch capability — the
// channel installs nothing and persists nothing beyond the rcfile text
// (ADR-0024 decision 2).
func assertSessionLeftOnlyTheLauncherBundle(t *testing.T, home, capability string) {
	t.Helper()
	for _, e := range homeEntries(home) {
		if e == ".bashrc" || e == ".nocx" || strings.HasPrefix(e, ".nocx/") {
			continue
		}
		t.Fatalf("session left an artifact in the remote home: %s", e)
	}
	found := ""
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 — p comes from WalkDir over the fixture-owned session home.
		if err == nil && strings.Contains(string(b), capability) {
			found = p
		}
		return nil
	})
	if found != "" {
		t.Fatalf("the per-epoch capability persisted on the remote host at %s — "+
			"the channel must install nothing and persist nothing", found)
	}
}

// stripControl removes ANSI/OSC escape sequences from terminal bytes,
// leaving the text a user could see. The policy-diagnostic check runs on
// this: the terminal legitimately carries structured markers (OSC 133
// lifecycle, OSC 636 command snapshots, OSC 7 cwd) whose payloads — command
// lists, function names — are telemetry, not diagnostics naming a policy.
func stripControl(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI ... final byte
			for j := i + 2; j < len(s); j++ {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					i = j
					break
				}
			}
		case ']', 'P', '^', '_': // OSC / DCS / PM / APC ... BEL or ST
			for j := i + 2; j < len(s); j++ {
				if s[j] == 0x07 {
					i = j
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					i = j + 1
					break
				}
			}
		default: // plain two-byte escape; drop both
			i++
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Observation seam: the real provider drives a kernel the test can watch.

// recordingKernel wraps the PUBLISHER — the same kernel → publisher →
// adapter composition production wires (internal/app/app.go) — and records
// what one establishment mints, so the test can assert on the kernel read
// model (State/Attempt/Domain) without reimplementing any wiring. The raw
// *lifecycle.Kernel no longer satisfies lifecyclechannel.Kernel by design:
// it hands outbound (accept, refresh_request) back unsent, and only the
// publisher orders and gates delivery (ADR-0024 decision 9).
type recordingKernel struct {
	*lifecyclepub.Publisher

	mu         sync.Mutex
	lane       lifecycle.LaneID
	domain     lifecycle.DomainID
	capability lifecycle.Capability // the per-epoch bearer, from the handle
	recovery   lifecycle.FenceNonce // the one-shot recovery fence, from the handle
	minted     int
}

func (r *recordingKernel) RequestDomain(lane lifecycle.LaneID, parent *lifecycle.DomainID, t lifecycle.TransportID) (lifecycle.DomainHandle, error) {
	h, err := r.Publisher.RequestDomain(lane, parent, t)
	if err == nil {
		r.mu.Lock()
		r.lane = lane
		r.domain = h.Domain
		r.capability = h.Capability
		r.recovery = h.Recovery
		r.minted++
		r.mu.Unlock()
	}
	return h, err
}

// ackingEmitter acknowledges every published establishment immediately, as
// the renderer does after committing the editor presentation (decision 9).
// The live-sshd proofs drive the real shell over the real sshd against the
// production composition; without the acknowledgement the accept is never
// flushed and the session never enters enhanced mode, so the proofs would
// assert against a conventional terminal.
type ackingEmitter struct {
	pub *lifecyclepub.Publisher
}

func (e ackingEmitter) PublishLifecycle(f lifecyclepub.Fact) {
	if f.Generation == "" || f.Domain == "" {
		return
	}
	_ = e.pub.AcknowledgeEstablishment(
		lifecycle.LaneID(f.Lane), lifecycle.DomainID(f.Domain), f.Epoch, f.Generation)
}

// newRecordingKernel builds the observation seam the way production wires
// it: publisher over the raw kernel, acking emitter bound, the publisher
func newRecordingKernel(opts ...lifecyclepub.Option) *recordingKernel {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, opts...)
	pub.SetEmitter(ackingEmitter{pub: pub})
	return &recordingKernel{Publisher: pub}
}

// capabilityHex is the bearer as the launch embedded it: the provider
// hex-encodes the handle's capability into the rcfile text (never the
// environment), so this is byte-for-byte the value the shell received.
func (r *recordingKernel) capabilityHex() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%x", r.capability)
}

// recoveryHex is the one-shot recovery fence as the launch embedded it. It is
// the second bearer §11 assertion 7 names, and it is asserted against every
// surface alongside the capability — "neither bearer" is two statements.
func (r *recordingKernel) recoveryHex() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%x", r.recovery)
}

// ---------------------------------------------------------------------------
// Connect through the production composition.

type outputBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (o *outputBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.b = append(o.b, p...)
	return len(p), nil
}

func (o *outputBuffer) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.b)
}

// fixtureWaitDelay bounds what a fixture's cleanup may cost when a process it
// spawned leaves a descendant behind.
//
// A lockedBuffer is an io.Writer and not a file, so os/exec gives the child a
// PIPE and copies it on a goroutine that Cmd.Wait waits for. Every descendant
// inherits the write end, and Wait cannot finish while one of them holds it —
// the process itself being long dead makes no difference. Killing the process
// GROUP is what ends that in the ordinary case, and every fixture here does
// it; this is for the descendant that has left the group, which no kill can
// reach: sshd gives each connection its own session, and ssh's backgrounded
// multiplex master detaches and keeps STDERR (it sends stdin and stdout to
// /dev/null, which is why exactly one copier survives it).
//
// Measured on 2026-08-21, CI run 32474316825: a mux fixture cleanup parked in
// awaitGoroutines for 8m33s after ITS TEST HAD ALREADY FAILED, and Go's
// 10-minute panic took the whole internal/app package with it — a one-line
// failure reported as a dead suite. Reproduced here with a real ssh that
// backgrounds a master: killing the pid alone left the cleanup blocked past
// 60s, killing the group returned it in 0.30s, and for the descendant no
// kill reaches this bound is what ends it — at a cost of seconds, and to
// this test rather than to the package.
//
// It is a HANG DETECTOR, never an expectation: a run that has to wait it out
// has already failed for its own reasons, and no assertion may depend on it.
const fixtureWaitDelay = 10 * time.Second

type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}

// connect opens a real SSH session to the fixture sshd through ssh.RealClient
// with the app package's own lifecycle provider and launcher adapter — the
// same composition root types app.go wires — and starts collecting the
// terminal output. An optional installer exercises the saved-profile
// publication path inside RealClient.Connect.
func (fx *liveSshd) connect(t *testing.T, kernel *recordingKernel, shell ssh.ShellKind, installers ...ssh.RemoteInstaller) (ssh.Channel, *outputBuffer) {
	t.Helper()
	logger := fx.log()
	client, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(fx.knownHostsPath(t)))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	fx.client = client

	provider := &remoteLifecycleProvider{
		client: client,
		kernel: kernel,
		logger: logger,
		registerLane: func(lane lifecycle.LaneID, sid string) {
			fx.registeredLanes = append(fx.registeredLanes, string(lane)+"->"+sid)
		},
	}
	var launcher ssh.RemoteLauncher = &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: logger}
	if fx.launcher != nil {
		launcher = fx.launcher
	}

	opts := []ssh.ConnectOption{
		ssh.WithUser(fx.user),
		ssh.WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(fx.signer)}),
		ssh.WithPTYSize(100, 30, 0, 0),
		ssh.WithTimeout(20 * time.Second),
		ssh.WithSessionID("sid-live-sshd"),
		ssh.WithEnhanced(),
		ssh.WithShell(shell),
		ssh.WithRemoteLifecycle(provider),
		ssh.WithRemoteLauncher(launcher),
	}
	if len(installers) > 0 {
		opts = append(opts, ssh.WithRemoteInstaller(installers[0]))
	}
	ch, err := client.Connect(context.Background(), fx.addr, opts...)
	if err != nil {
		t.Fatalf("connect to %s: %v", fx.addr, err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	out := &outputBuffer{}
	go func() { _, _ = io.Copy(out, ch) }()
	return ch, out
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// runLine types one command line into the remote shell and waits for its
// authenticated completion, returning the completed attempt.
func runLine(t *testing.T, ch ssh.Channel, kernel *recordingKernel, line string, wantExit int) lifecycle.ExecutionAttempt {
	t.Helper()
	kernel.mu.Lock()
	domain := kernel.domain
	kernel.mu.Unlock()
	if domain == "" {
		t.Fatal("runLine called before a domain was minted")
	}
	if _, err := ch.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
	// The command is deliberately slow enough to observe the open attempt:
	// the kernel mints the attempt id on start, and the test needs it to
	// follow the completion.
	var att lifecycle.ExecutionAttempt
	waitFor(t, "an open attempt for "+line, 15*time.Second, func() bool {
		a, ok := kernel.OpenAttempt(domain)
		if ok {
			att = a
		}
		return ok
	})
	waitFor(t, "completion of "+line, 15*time.Second, func() bool {
		a, ok := kernel.Attempt(att.ID)
		if !ok {
			return false
		}
		if a.State != lifecycle.AttemptCompleted {
			return false
		}
		if a.ExitCode == nil || *a.ExitCode != wantExit {
			t.Fatalf("attempt %s completed with exit %v, want %d", att.ID, a.ExitCode, wantExit)
		}
		att = a // the completed record: the fence it carries is the one on the wire
		return true
	})
	return att
}

// ---------------------------------------------------------------------------
// The proofs.

// TestLiveSshd_BashReachesAcceptedDomain proves the primary path (nocx-u7uh.4
// acceptance): a real bash shell on the other side of a real sshd reaches the
// accepted domain with nothing installed there, and commands run there
// produce authenticated start/complete with the right exit status and the
// render fence on the terminal.
func TestLiveSshd_BashReachesAcceptedDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, shellintegration.New(log.NewSlogAdapter(nil)))

	waitFor(t, "domain established", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// The minted lane reached the session that owns it: the production
	// RegisterLifecycleLane wiring (without it, every published fact is
	// dropped at the transport and enhanced mode never engages).
	if len(fx.registeredLanes) != 1 || !strings.HasSuffix(fx.registeredLanes[0], "->sid-live-sshd") {
		t.Fatalf("registered lanes = %v, want exactly one lane bound to sid-live-sshd", fx.registeredLanes)
	}

	// Line 1: print a sentinel and stay open long enough for the test to
	// observe the open attempt.
	att0 := runLine(t, ch, kernel, "printf 'PROOF_BASH_123\\n'; sleep 0.3", 0)
	// The render fence the kernel recorded must be the exact bytes the shell
	// wrote to the terminal (protocol doc §8).
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att0.Fence)
	waitFor(t, "sentinel output and fence", 10*time.Second, func() bool {
		return strings.Contains(out.String(), "PROOF_BASH_123") &&
			strings.Contains(out.String(), fence)
	})

	// Line 2: a failing command completes with exit status 1.
	runLine(t, ch, kernel, "sh -c 'sleep 0.3; exit 1'", 1)

	// The lane is back at a ready prompt for the domain.
	waitFor(t, "lane back at PromptReady", 10*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		st, err := kernel.State(kernel.lane)
		if err != nil {
			return false
		}
		return st.Lifecycle == lifecycle.LifecyclePromptReady && st.Domain == kernel.domain
	})

	// The session ends with the shell. domain_closed is deliberately
	// best-effort (the hook's own contract: the process exit may race the
	// send, and the kernel then ends the domain via the transport-loss
	// path, which the connection-loss test proves separately), so the
	// assertion is the session ending, not a promised terminal state.
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waitFor(t, "session end after exit", 15*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})

	// Nothing installed: the session home holds only the fixture .bashrc and
	// the launcher's own ~/.nocx bundle, and no file carries the capability.
	assertSessionLeftOnlyTheLauncherBundle(t, fx.home, kernel.capabilityHex())
}

// TestLiveSshd_RemoteBundleRepublishReplacesManifest proves nocx-340t
// against OpenSSH itself: after a host has a committed older activation, a
// second SFTP publish atomically replaces manifest.json instead of receiving
// SSH_FX_FAILURE, and a subsequent enhanced session establishes its domain.
func TestLiveSshd_RemoteBundleRepublishReplacesManifest(t *testing.T) {
	fx := startLiveSshd(t, true)
	installer := shellintegration.New(log.NewSlogAdapter(nil))
	client := fx.rawClient(t)
	if err := installer.EnsureInstalledRemote(context.Background(), client, fx.home); err != nil {
		t.Fatalf("first remote publish: %v", err)
	}
	forceInstalledVersion(t, fx.home, "0")

	kernel := newRecordingKernel()
	ch, _ := fx.connect(t, kernel, ssh.ShellBash, installer)
	waitFor(t, "domain established after republish", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ForwardingRefusedStaysConventional proves the refusal
// contract: a host whose sshd will not forward (AllowTcpForwarding no)
// produces a conventional terminal with a visible native prompt, no dialog,
// and no diagnostic naming a policy — refusal is detectable synchronously
// but not distinguishable (ADR-0024 decision 4).
func TestLiveSshd_ForwardingRefusedStaysConventional(t *testing.T) {
	fx := startLiveSshd(t, false)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, shellintegration.New(log.NewSlogAdapter(nil)))

	// The refusal is synchronous: no domain may ever be minted.
	time.Sleep(500 * time.Millisecond)
	kernel.mu.Lock()
	minted := kernel.minted
	kernel.mu.Unlock()
	if minted != 0 {
		t.Fatalf("refused forwarding still minted %d domain(s)", minted)
	}

	// The fixture .bashrc names the prompt NATIVE_PROMPT>; with no live
	// channel the marker-only overlay keeps it visible (ADR-0024 decision 9).
	// Run a command: the terminal is an ordinary usable shell.
	if _, err := ch.Write([]byte("echo CONVENTIONAL_OK\n")); err != nil {
		t.Fatalf("write echo: %v", err)
	}
	waitFor(t, "a usable conventional terminal", 20*time.Second, func() bool {
		s := out.String()
		return strings.Contains(s, "NATIVE_PROMPT>") && strings.Contains(s, "CONVENTIONAL_OK")
	})

	// No diagnostic naming the policy may leak into the user-visible output
	// (the Go client's tcpip-forward refusal is not a terminal message, and
	// nothing in the launcher or the hooks may print one). The scan runs on
	// the control-stripped text: the terminal's structured markers (OSC 636
	// command snapshots etc.) are telemetry, not diagnostics.
	low := strings.ToLower(stripControl(out.String()))
	for _, word := range []string{"forward", "tcpip", "AllowTcpForwarding", "refused"} {
		if strings.Contains(low, word) {
			t.Fatalf("refusal leaked a policy diagnostic (%q) into the terminal:\n%s", word, out.String())
		}
	}

	// And the shell still ends cleanly.
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ConnectionLossRevokesDomain proves protocol §12: losing the
// SSH connection revokes the domain and abandons its open attempt as
// unknown — never success.
func TestLiveSshd_ConnectionLossRevokesDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, _ := fx.connect(t, kernel, ssh.ShellBash, shellintegration.New(log.NewSlogAdapter(nil)))

	waitFor(t, "domain established", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// Open a long-running attempt, then lose the SSH connection under it.
	// Closing the pooled client is the faithful loss trigger: real OpenSSH
	// forks per connection, so killing the sshd parent would leave the
	// session's connection (and the forwarded port) alive — the transport
	// loss path protocol §12 is about is the connection shutting down, which
	// is what the client's Close does.
	if _, err := ch.Write([]byte("sleep 60\n")); err != nil {
		t.Fatalf("write sleep: %v", err)
	}
	var att lifecycle.ExecutionAttempt
	waitFor(t, "the sleep attempt to be open", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		a, ok := kernel.OpenAttempt(kernel.domain)
		if ok {
			att = a
		}
		return ok
	})
	if err := fx.client.Close(); err != nil {
		t.Fatalf("close pooled client: %v", err)
	}

	// The domain is lost and the open attempt becomes unknown — never
	// completed, never successful.
	waitFor(t, "domain lost", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainLost
	})
	waitFor(t, "open attempt unknown", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		a, ok := kernel.Attempt(att.ID)
		return ok && a.State == lifecycle.AttemptUnknown && a.ExitCode == nil
	})
}

// TestLiveSshd_ZshAdapterReachesAcceptedDomain proves the zsh tier end to
// end (deliverable 2): the zsh hook reaches the forwarded port through
// zmodload zsh/net/tcp + ztcp, performs the same hello/accept handshake with
// the same capability gating, and reports start/complete with the exit
// status — no prompt suppression happens before accept.
func TestLiveSshd_ZshAdapterReachesAcceptedDomain(t *testing.T) {
	// zsh is a hard prerequisite (the launcher execs it on the far host);
	// fail, never skip, with the container guidance — the go-tests image
	// carries zsh, the host may not (nocx-gd84).
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Fatalf("zsh is required by this test and missing from PATH.\n" +
			"The zsh tier proof must not silently skip (nocx-gd84). Run the suite in the\n" +
			"containerized runner: .githooks/containerized-tests.sh (the go-tests image\n" +
			"carries zsh), or provision zsh on this host and re-run.")
	}

	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellZsh, shellintegration.New(log.NewSlogAdapter(nil)))

	waitFor(t, "domain established", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	att := runLine(t, ch, kernel, "printf 'PROOF_ZSH_123\\n'; sleep 0.3", 0)
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att.Fence)
	waitFor(t, "zsh sentinel output and fence", 10*time.Second, func() bool {
		return strings.Contains(out.String(), "PROOF_ZSH_123") &&
			strings.Contains(out.String(), fence)
	})

	// A failing command completes with exit status 1 over the zsh hook.
	runLine(t, ch, kernel, "sh -c 'sleep 0.3; exit 1'", 1)

	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	// domain_closed is best-effort (see the bash proof); assert the session
	// ended, not a promised terminal state.
	waitFor(t, "session end after exit", 15*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})
}

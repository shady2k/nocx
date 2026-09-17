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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
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
	// clientKey is the same key written to a file, when the journey dials with
	// a NAMED identity (`withClientKeyFile`). Empty means the fixture dials
	// with the in-memory signer, which is every caller that predates it.
	clientKey string
	// liveSshdDial is the state only a DIALING build has: the pooled client the
	// journeys open sessions with, the lanes the provider registered, and the
	// launcher a stand replaces. Its DECLARATION is per build
	// (live_sshd_dial_state_absent_test.go, live_sshd_dial_state_local_test.go)
	// for the same reason RealClient's is — this fixture is one type in both
	// builds and a struct's fields cannot carry a build tag. Nothing on this
	// side of the split reads it.
	liveSshdDial
	cmd    *exec.Cmd
	logBuf *lockedBuffer

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
	// clientKeyFile makes the fixture's own dial use an inline key FILE rather
	// than an in-memory signer. The two authenticate with the SAME key and
	// differ only in how the credential is named — which is what a profile
	// difference looks like: `IdentityFile` names a path, while an explicit
	// `AuthMethods` list names a value — and the publish reads the profile, so
	// it is the option that decides whether a helper can be handed the
	// credential at all.
	clientKeyFile bool
}

// log is the product logger this fixture's compositions use.
func (fx *liveSshd) log() log.Logger {
	if fx.logger != nil {
		return fx.logger
	}
	return log.NewSlogAdapter(nil)
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
// sshdDefaultPath is the search path sshd compiles in (_PATH_STDPATH) and
// hands to a session: the whole of what a session gets, since sshd runs a
// request as `<login shell> -c <request>` and no profile is read.
const sshdDefaultPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// shellDirsOffTheDefaultPath is the directory of every shell this fixture's
// sessions start BY NAME that the default path above does not already cover.
// It is empty on a runner and on any FHS machine, which is why the rc line it
// feeds is written only when it is not.
//
// WHY A FIXTURE NEEDS THIS AT ALL. The launcher execs its shell by name —
// `exec zsh -l`, launcher_zsh.go — because a remote host keeps its shells
// wherever it keeps them and nocx cannot know the path. On this developer's
// NixOS box zsh is installed in the user's own nix profile, under
// $HOME/.nix-profile/bin, and the fixture deliberately MOVES $HOME to a
// disposable directory; the host then recomputes the session's PATH from that
// new HOME, so the profile the zsh lives in is no longer on it. The tier
// proof failed after fifteen seconds with `/bin/sh: line 1: exec: zsh: not
// found` — a sentence about the machine, with nothing in it about nocx
// (nocx-9jomd).
//
// It is fed through the planted .bashrc and NOT through sshd_config's SetEnv,
// which was measured and does not survive: this host's own bashrc rebuilds
// PATH after sshd has set it, so SetEnv PATH is overwritten before the
// launcher's command runs.
//
// Nothing is weakened. The launcher still resolves its shell through PATH and
// still has to complete the whole handshake; the directory is APPENDED, so no
// binary the session would otherwise have found is shadowed; and on a machine
// that keeps its shells in /usr/bin — every CI runner, which is where
// scripts/install-zsh-ci.sh puts zsh — the list is empty and the fixture is
// byte-for-byte what it was.
func shellDirsOffTheDefaultPath() []string {
	covered := make(map[string]bool)
	for _, dir := range strings.Split(sshdDefaultPath, ":") {
		covered[dir] = true
	}
	var extra []string
	for _, name := range []string{"bash", "zsh", "sh"} {
		resolved, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		dir := filepath.Dir(resolved)
		if !covered[dir] {
			covered[dir] = true
			extra = append(extra, dir)
		}
	}
	return extra
}

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

	// The same key as a FILE, for the journeys that dial with a named identity
	// rather than an in-memory signer. It lives beside the fixture's other
	// material rather than under the session home: what a person's profile
	// names is their own file, and the far side never reads it.
	clientKeyPath := ""
	if fxCfg.clientKeyFile {
		clientKeyPEM, err := gossh.MarshalPrivateKey(clientRaw, "")
		if err != nil {
			t.Fatalf("marshal client key: %v", err)
		}
		clientKeyPath = filepath.Join(dir, "client_key")
		if err := os.WriteFile(clientKeyPath, pem.EncodeToMemory(clientKeyPEM), 0o600); err != nil {
			t.Fatalf("write client key: %v", err)
		}
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
	// Never an empty element: an empty entry in PATH means the current
	// directory, so the line is written only when there is something to add.
	rcPath := ""
	if dirs := shellDirsOffTheDefaultPath(); len(dirs) > 0 {
		rcPath = "PATH=\"$PATH:" + strings.Join(dirs, ":") + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte(rcRecorder+"PS1='NATIVE_PROMPT> '\n"+rcPath+rcHist), 0o600); err != nil {
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
	waittest.WaitForTimeoutDetail(t, "sshd listening", 10*time.Second,
		func() string { return fmt.Sprintf("log:\n%s", logBuf.String()) },
		func() bool {
			return strings.Contains(logBuf.String(), want)
		})
	return &liveSshd{
		liveSshdDial: newLiveSshdDial(),
		addr:         addr,
		user:         userName,
		home:         home,
		clientKey:    clientKeyPath,
		signer:       clientSigner,
		clientRaw:    clientRaw,
		hostKey:      hostSigner.PublicKey(),
		cmd:          cmd,
		logBuf:       logBuf,
		tmpRoot:      tmpRoot,
		recDir:       recDir,
		histFile:     histFile,
	}
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

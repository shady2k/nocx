package app

// A real-OpenSSH fixture for the live typed-ssh tests (typed_mux_live_test.go
// and its friends).
//
// It started life as the in-process half of the `exec`-refused measurement
// (nocx-m8jwn.11 / P4), and that measurement's probe tests are gone with the
// coordinator's own dial stack (nocx-oalo1.3.2): a journey that opened a
// session through `ssh.RealClient.Connect` no longer exists. What survives is
// the fixture itself — a real sshd subprocess, several ways to restrict what
// it will run, and an authentication count read from its own log — because the
// live typed-ssh journeys drive the user's own `ssh` and need a server.
//
// It skips when no sshd binary exists: it proves nothing about the product on
// its own, so a machine without sshd is a missing measurement, not a silent
// product gap.
import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// Fixture: a real OpenSSH server, restricted per the mechanism under test.

type execProbeSshd struct {
	addr    string
	user    string
	home    string
	signer  gossh.Signer
	hostKey gossh.PublicKey
	log     *lockedBuffer
	// keyPath is the client private key on disk, for the probes that drive
	// a REAL ssh client rather than gossh (the mux measurement in
	// typed_mux_live_test.go). The fixture writes it either way; a probe
	// that does not need it simply never reads the field.
	keyPath string
}

// execProbeSshdOrSkip returns the sshd path, skipping the probe where the
// binary is absent.
func execProbeSshdOrSkip(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("sshd"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		return "/usr/sbin/sshd"
	}
	t.Skip("no sshd binary on PATH or in /usr/sbin: the real-server half of the " +
		"exec-refusal probe cannot run here (the in-process half is in internal/ssh)")
	return ""
}

func execProbeGenSigner(t *testing.T) (ed25519.PrivateKey, gossh.Signer) {
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

func execProbeReservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("reserved addr is %T, want *net.TCPAddr", ln.Addr())
	}
	_ = ln.Close()
	return addr.Port
}

// startExecProbeSshd spawns a real sshd for the current uid on a free
// loopback port, key auth only, with a fixture-owned HOME. keyOptions are
// prefixed to the authorized_keys line; extraConfig is appended to
// sshd_config (a Match block therefore has to come last, which it does).
func startExecProbeSshd(t *testing.T, keyOptions, extraConfig string) *execProbeSshd {
	t.Helper()
	sshdPath := execProbeSshdOrSkip(t)

	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	if _, lookupErr := user.Lookup(u.Username); lookupErr != nil {
		t.Skipf("no passwd entry for %q (%v): a non-root sshd can serve only the uid it runs as", u.Username, lookupErr)
	}

	dir := t.TempDir()
	hostKeyRaw, hostSigner := execProbeGenSigner(t)
	clientKeyRaw, clientSigner := execProbeGenSigner(t)

	hostKeyPEM, err := gossh.MarshalPrivateKey(hostKeyRaw, "")
	if err != nil {
		t.Fatalf("marshal host key: %v", err)
	}
	hostKeyPath := filepath.Join(dir, "hostkey")
	if wErr := os.WriteFile(hostKeyPath, pem.EncodeToMemory(hostKeyPEM), 0o600); wErr != nil {
		t.Fatalf("write host key: %v", wErr)
	}

	clientKeyPEM, err := gossh.MarshalPrivateKey(clientKeyRaw, "")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	clientKeyPath := filepath.Join(dir, "clientkey")
	if err := os.WriteFile(clientKeyPath, pem.EncodeToMemory(clientKeyPEM), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(clientSigner.PublicKey())))
	if keyOptions != "" {
		line = keyOptions + " " + line
	}
	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("mkdir session home: %v", err)
	}

	port := execProbeReservePort(t)
	cfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
X11Forwarding no
AllowTcpForwarding no
Subsystem sftp internal-sftp
SetEnv HOME=%s
LogLevel VERBOSE
%s
`, port, hostKeyPath, filepath.Join(dir, "sshd.pid"), authKeys, home, extraConfig)
	cfgPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	cmd := exec.Command(sshdPath, "-D", "-e", "-f", cfgPath) // #nosec G204 — sshdPath is a LookPath-validated binary; a spawned daemon is the only way to observe a real server.
	// sshd -D re-execs its listener into a child (OpenSSH 9.8+), so the whole
	// group has to go or the listener is orphaned (as live_sshd_test.go found).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// And the bound for a session child that has left that group, holding
	// the log pipe: see fixtureWaitDelay. This fixture is where it was
	// measured — with the mux master's own cleanup fixed, the hang moved
	// here, to sshd's Wait, on the very next run.
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

	fx := &execProbeSshd{
		addr:    fmt.Sprintf("127.0.0.1:%d", port),
		user:    u.Username,
		home:    home,
		signer:  clientSigner,
		hostKey: hostSigner.PublicKey(),
		log:     logBuf,
		keyPath: clientKeyPath,
	}
	want := fmt.Sprintf("Server listening on 127.0.0.1 port %d", port)
	waittest.WaitForTimeoutDetail(t, "sshd listening", 20*time.Second,
		func() string { return fmt.Sprintf("log:\n%s", logBuf.String()) },
		func() bool {
			return strings.Contains(logBuf.String(), want)
		})
	return fx
}

// authCount is how many times the server has accepted an authentication —
// the observable that says whether a credential was used a second time.
func (fx *execProbeSshd) authCount() int {
	return strings.Count(fx.log.String(), "Accepted publickey")
}

// ---------------------------------------------------------------------------
// Channel-level helpers.

// ptyDeviceName is what `tty(1)` prints when its standard input IS a
// pseudo-terminal, on either platform this suite runs on: Linux's devpts
// names a slave /dev/pts/N, and macOS (BSD) names it /dev/ttysNNN. Both are
// the same fact — the shell's stdin is the slave side of a pty — spelled by
// two different kernels, so the assertion matches the fact rather than one
// kernel's spelling of it.
//
// It stays a specific claim: a real device path, minted by the pty layer.
// `tty` prints "not a tty" for anything else, /dev/console and /dev/ttyN
// (a Linux virtual console) do not match, and neither does empty output —
// which is why this is not softened to "the shell said something".

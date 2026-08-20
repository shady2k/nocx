package app

// Probe (nocx-m8jwn.11 / P4): §6.4's `exec`-refused row, measured rather
// than assumed.
//
// The carrier design's refusal matrix leaves one row open on purpose: after
// an `exec` request is REFUSED on a session channel that already has a pty,
// may a `shell` request still succeed on that same channel and reach a
// usable native prompt? That is a property of the server, so it is measured
// here against a real OpenSSH server, and in internal/ssh against the
// in-process golang.org/x/crypto/ssh fixture our own tests exercise.
//
// This file measures only. It wires no production code and changes none.
//
// The fixture is deliberately independent of the live-sshd fixture in
// live_sshd_test.go: the probe needs several differently-restricted servers
// (ForceCommand, an authorized_keys command= restriction, a Match block),
// which that fixture does not parameterise, and the probe must not
// restructure it. It skips when no sshd binary exists — unlike the live-sshd
// suite, this file proves nothing about the product, so a machine without
// sshd is a missing measurement, not a silent product gap.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// Wire payloads. The probe sends channel requests by hand rather than through
// gossh.Session, because the whole question is what may be sent on a channel
// AFTER a request on it was refused.

type execProbePTYReq struct {
	Term                         string
	Columns, Rows, Width, Height uint32
	Modelist                     string
}

type execProbeExecReq struct{ Command string }

type execProbeSubsystemReq struct{ Name string }

type execProbeX11Req struct {
	SingleConnection bool
	AuthProtocol     string
	AuthCookie       string
	ScreenNumber     uint32
}

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
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), want) {
			return fx
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sshd did not report listening; log:\n%s", logBuf.String())
	return nil
}

// dial opens one client connection and authenticates once.
func (fx *execProbeSshd) dial(t *testing.T) *gossh.Client {
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
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial probe sshd: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// authCount is how many times the server has accepted an authentication —
// the observable that says whether a credential was used a second time.
func (fx *execProbeSshd) authCount() int {
	return strings.Count(fx.log.String(), "Accepted publickey")
}

// ---------------------------------------------------------------------------
// Channel-level helpers.

// execProbeSession opens a session channel and drains its inbound requests
// into a slice, so exit-status and the like are observable afterwards.
func execProbeSession(t *testing.T, client *gossh.Client) (gossh.Channel, *lockedBuffer) {
	t.Helper()
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open session channel: %v", err)
	}
	seen := &lockedBuffer{}
	go func() {
		for req := range reqs {
			label := req.Type
			if req.Type == "exit-status" {
				var m struct{ Status uint32 }
				if gossh.Unmarshal(req.Payload, &m) == nil {
					label = fmt.Sprintf("exit-status=%d", m.Status)
				}
			}
			_, _ = seen.Write([]byte(label + "\n"))
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()
	return ch, seen
}

func execProbePTY(t *testing.T, ch gossh.Channel) bool {
	t.Helper()
	ok, err := ch.SendRequest("pty-req", true, gossh.Marshal(&execProbePTYReq{
		Term: "xterm-256color", Columns: 80, Rows: 24, Width: 640, Height: 480,
		Modelist: string([]byte{0}), // TTY_OP_END
	}))
	if err != nil {
		t.Fatalf("pty-req: %v", err)
	}
	return ok
}

// readInto pumps the channel into a buffer until it ends.
func execProbeRead(ch gossh.Channel) *lockedBuffer {
	out := &lockedBuffer{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

// execProbeWaitFor blocks until the buffer contains want, failing with the
// captured output if it never does. It waits on an observable state change,
// never on a duration: the deadline exists only so a hang reports.
func execProbeWaitFor(t *testing.T, out *lockedBuffer, want, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: never saw %q; channel output was:\n%s", what, want, out.String())
}

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
var ptyDeviceName = regexp.MustCompile(`/dev/(pts/[0-9]+|ttys[0-9]+)`)

// execProbeWaitForPTYName blocks until the buffer carries such a name.
func execProbeWaitForPTYName(t *testing.T, out *lockedBuffer, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ptyDeviceName.MatchString(out.String()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: `tty` never named a pty device (%s); channel output was:\n%s",
		what, ptyDeviceName, out.String())
}

// execProbeProveInteractive proves the channel carries a real interactive
// shell on a real pty: the shell executes a line we type and its OUTPUT
// comes back (the marker is written split so the pty's echo of the typed
// line cannot be mistaken for the shell's answer), and `tty` names a pty.
func execProbeProveInteractive(t *testing.T, ch gossh.Channel, out *lockedBuffer, what string) {
	t.Helper()
	if _, err := ch.Write([]byte("echo NOCX\"\"PROBE_ALIVE; tty\n")); err != nil {
		t.Fatalf("%s: write to shell: %v", what, err)
	}
	execProbeWaitFor(t, out, "NOCXPROBE_ALIVE", what+": shell executed a typed line")
	execProbeWaitForPTYName(t, out, what+": the shell is on a pty")
}

// ---------------------------------------------------------------------------
// Measurement 1: can a real OpenSSH server be made to REFUSE the `exec`
// request?
//
// §6.4's open row is about a refused request. The mechanisms a restricted
// account is normally built from are surveyed here, and every one of them
// ACCEPTS the exec request and substitutes what runs behind it. The
// assertion is therefore "accepted", and a future OpenSSH that refuses
// instead turns this red — which is exactly when the row would need
// re-measuring.
func TestExecRefusalProbe_OpenSSHAcceptsExecUnderEveryRestrictionWeCanConfigure(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	cases := []struct {
		name       string
		keyOptions string
		config     string
	}{
		{name: "unrestricted account"},
		{name: "ForceCommand in the server config", config: "ForceCommand exit 7"},
		{name: "command= restriction on the authorized key", keyOptions: `command="exit 7"`},
		{name: "transfer-only account (ForceCommand internal-sftp)", config: "ForceCommand internal-sftp"},
		{name: "Match block for this user with ForceCommand", config: "Match User " + u.Username + "\n    ForceCommand exit 7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := startExecProbeSshd(t, tc.keyOptions, tc.config)
			client := fx.dial(t)
			ch, _ := execProbeSession(t, client)
			defer func() { _ = ch.Close() }()

			if !execProbePTY(t, ch) {
				t.Fatalf("pty-req refused: this case cannot reach the exec step")
			}
			ok, err := ch.SendRequest("exec", true, gossh.Marshal(&execProbeExecReq{Command: "nocx-probe-launch"}))
			if err != nil {
				t.Fatalf("exec request errored: %v", err)
			}
			if !ok {
				t.Fatalf("exec request was REFUSED under %q — a real request-level exec "+
					"refusal is now reachable on OpenSSH, so §6.4's exec-refused row must "+
					"be re-measured against this configuration", tc.name)
			}
			t.Logf("exec ACCEPTED under %q (the restriction substitutes what runs; it does not refuse the request)", tc.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Measurement 2: a refused session request leaves the channel — and the pty
// already granted on it — usable, and a `shell` on that same channel reaches
// a real interactive prompt.
//
// `exec` cannot be refused by OpenSSH (test 1), so the refusal is driven
// through the two other requests that travel the same code path on a
// not-yet-started session channel: `subsystem`, which like `exec` asks the
// server to start a program, and `x11-req`. Both are genuinely refused at
// the request level by a real server, which is the property §6.4's row turns
// on.
func TestExecRefusalProbe_RefusedSessionRequestLeavesTheChannelAndItsPTYUsable(t *testing.T) {
	fx := startExecProbeSshd(t, "", "")
	client := fx.dial(t)

	refusals := []struct {
		name    string
		request string
		payload []byte
	}{
		{
			name:    "subsystem (asks the server to start a program, like exec)",
			request: "subsystem",
			payload: gossh.Marshal(&execProbeSubsystemReq{Name: "nocx-no-such-subsystem"}),
		},
		{
			name:    "x11-req (X11Forwarding no)",
			request: "x11-req",
			payload: gossh.Marshal(&execProbeX11Req{
				AuthProtocol: "MIT-MAGIC-COOKIE-1",
				AuthCookie:   "0123456789abcdef0123456789abcdef",
			}),
		},
	}

	for _, tc := range refusals {
		t.Run(tc.request, func(t *testing.T) {
			ch, _ := execProbeSession(t, client)
			defer func() { _ = ch.Close() }()

			if !execProbePTY(t, ch) {
				t.Fatalf("pty-req refused")
			}
			ok, err := ch.SendRequest(tc.request, true, tc.payload)
			if err != nil {
				t.Fatalf("%s request errored: %v", tc.request, err)
			}
			if ok {
				t.Fatalf("%s was ACCEPTED; this case can no longer produce a refusal to measure", tc.name)
			}
			t.Logf("%s REFUSED, as intended", tc.name)

			// The refusal must not have taken the channel with it.
			out := execProbeRead(ch)
			ok, err = ch.SendRequest("shell", true, nil)
			if err != nil {
				t.Fatalf("after a refused %s the channel was gone: shell request errored with %v", tc.request, err)
			}
			if !ok {
				t.Fatalf("after a refused %s the server refused shell on the same channel", tc.request)
			}
			execProbeProveInteractive(t, ch, out, "after a refused "+tc.request)
		})
	}

	// One connection, one authentication, for both refusals and both shells.
	if got := fx.authCount(); got != 1 {
		t.Fatalf("server accepted %d authentications, want exactly 1 — a refusal must not "+
			"cost the user a second credential use", got)
	}
}

// ---------------------------------------------------------------------------
// Measurement 3: the other half of the question. OpenSSH's restrictions
// ACCEPT the exec request and then run something else. That outcome is not
// §6.4's refused row, and it is strictly worse — the channel is consumed,
// `shell` on it is refused because the session has already started, and a
// fresh channel on the same connection runs the forced command too, so no
// native prompt exists anywhere on that connection.
func TestExecRefusalProbe_AnAcceptedExecConsumesTheChannelAndForcedCommandsLeaveNoNativeShell(t *testing.T) {
	fx := startExecProbeSshd(t, "", "ForceCommand exit 7")
	client := fx.dial(t)

	ch, seen := execProbeSession(t, client)
	if !execProbePTY(t, ch) {
		t.Fatalf("pty-req refused")
	}
	ok, err := ch.SendRequest("exec", true, gossh.Marshal(&execProbeExecReq{Command: "nocx-probe-launch"}))
	if err != nil || !ok {
		t.Fatalf("exec request: ok=%v err=%v, want accepted", ok, err)
	}
	// The forced command ran instead of ours, and said so on the wire.
	execProbeWaitFor(t, seen, "exit-status=7", "the forced command ran behind the accepted exec")

	// `shell` on that same channel: the session has started, so it is gone.
	shellOK, shellErr := ch.SendRequest("shell", true, nil)
	t.Logf("shell on the channel whose exec was ACCEPTED: ok=%v err=%v", shellOK, shellErr)
	if shellOK {
		t.Fatalf("shell succeeded on a channel whose exec had already started a session")
	}
	_ = ch.Close()

	// A fresh session channel on the SAME connection still opens and still
	// authenticates nobody again — but it too runs the forced command, so
	// the connection has no native shell to fall back to.
	ch2, seen2 := execProbeSession(t, client)
	defer func() { _ = ch2.Close() }()
	if !execProbePTY(t, ch2) {
		t.Fatalf("pty-req refused on the second channel")
	}
	ok, err = ch2.SendRequest("shell", true, nil)
	if err != nil || !ok {
		t.Fatalf("shell on a fresh channel: ok=%v err=%v, want accepted", ok, err)
	}
	execProbeWaitFor(t, seen2, "exit-status=7", "the forced command ran behind the accepted shell too")

	if got := fx.authCount(); got != 1 {
		t.Fatalf("server accepted %d authentications, want exactly 1", got)
	}
}

package app

// The mux adapter, measured against a REAL OpenSSH master and a REAL sshd
// (design §11, assertions 18, 19, 20).
//
// internal/ssh/mux's own tests drive a hand-encoded fixture, which proves the
// client against a reading of PROTOCOL.mux and nothing more. This file is the
// paired "and on a normal machine it succeeds": a real `ssh` creates the
// master exactly as the wrapper composes it, our client proves ownership of
// that socket, and the server's own log is what counts the authentications.
//
// The numbers are the assertion. A push that rides the master adds no
// connection and no authentication; a push the master REFUSES adds no
// connection and no authentication either, because the adapter has no
// fallback to open one — which is the whole of D3 and the one thing an
// `sftp -o ControlMaster=auto` cannot promise (the spike measured it opening
// its own connection and authenticating again under MaxSessions 1).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/ssh/mux"
)

// muxLiveSSHOrSkip returns the ssh client binary, skipping where absent.
func muxLiveSSHOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh client on PATH: the real-master half of the mux measurement cannot run here")
	}
	return p
}

// muxLiveMaster starts a real `ssh` shaped exactly as the typed wrapper
// composes it, and returns the control path plus a reader on the session's
// output. The master carries the interactive session itself — there is never
// a second one — so its command is the stand-in for the user's prompt.
type muxLiveMaster struct {
	controlPath string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	out         *lockedBuffer
	errOut      *lockedBuffer
}

func startMuxLiveMaster(t *testing.T, fx *execProbeSshd) *muxLiveMaster {
	t.Helper()
	sshPath := muxLiveSSHOrSkip(t)

	// A short socket directory, created 0700 before the line is submitted:
	// an over-long ControlPath does not degrade to no-multiplexing, it kills
	// the connection outright (the spike measured it), so the path is short
	// by construction and never long enough to find out.
	sockDir, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	// #nosec G302 — a control-socket DIRECTORY is 0700 by contract: the
	// control socket is the trust boundary, and 0600 would make it
	// untraversable.
	if chmodErr := os.Chmod(sockDir, 0o700); chmodErr != nil {
		t.Fatalf("chmod socket dir: %v", chmodErr)
	}
	controlPath := filepath.Join(sockDir, "m")

	host, port, ok := strings.Cut(fx.addr, ":")
	if !ok {
		t.Fatalf("fixture address %q has no port", fx.addr)
	}

	// -F none so the developer's own ~/.ssh/config cannot decide this
	// measurement, and a disposable HOME so the run touches neither their
	// known_hosts nor anything else of theirs.
	home := t.TempDir()
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlPersist=no",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, "known_hosts"),
		"-o", "IdentitiesOnly=yes",
		"-F", "none",
		"-i", fx.keyPath,
		"-p", port,
		"-tt",
		fx.user + "@" + host,
		"printf 'NOCX_LIVE_READY\\n'; cat",
	}
	cmd := exec.Command(sshPath, args...) // #nosec G204 — sshPath is LookPath-validated; every argument is fixture-owned.
	cmd.Env = append(os.Environ(), "HOME="+home)
	// THE WHOLE GROUP GOES, not the pid — the same answer startExecProbeSshd
	// already gives above for sshd's re-exec'd listener, and for the same
	// reason. `cmd.Stdout`/`Stderr` are buffers rather than files, so exec
	// hands the child a PIPE and copies it on a goroutine that `Wait` waits
	// for; every descendant ssh leaves behind (a backgrounded multiplex
	// master is the documented one) inherits the write end. Kill only the
	// pid and the process is reaped while that copier stays parked for as
	// long as the descendant lives — which is not a slow test, it is a
	// cleanup that never returns.
	//
	// That is exactly what CI showed on 2026-08-21 (run 32474316825,
	// ci-linux/no Secret Service): this test's waitReady had already
	// FAILED, and its cleanup then held the package until Go's 10-minute
	// panic — `awaitGoroutines` parked 8 minutes, the process long gone,
	// one copier still reading. Measured here: with a grandchild holding
	// the pipe, killing the pid leaves Wait blocked past 20s; killing the
	// group returns in 0.30s.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// And the bound for the descendant that leaves the group before we
	// signal it — ssh's backgrounded master detaches, so no kill reaches
	// it: see fixtureWaitDelay.
	cmd.WaitDelay = fixtureWaitDelay
	out := &lockedBuffer{}
	cmd.Stdout = out
	errOut := &lockedBuffer{}
	cmd.Stderr = errOut
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("master stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start master: %v", err)
	}
	m := &muxLiveMaster{controlPath: controlPath, cmd: cmd, stdin: stdin, out: out, errOut: errOut}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			// #nosec G206 — the group is this test's own ssh: Setpgid above
			// made the child a group leader, so its pgid IS its pid and
			// nothing else can be in it.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	})
	return m
}

// waitReady blocks until the master's own session has produced its banner —
// an observable state change, not a duration. The deadline exists only so a
// hang reports rather than hanging the suite.
//
// STDERR IS PART OF THE REPORT. `ssh` says why it did not connect there and
// nowhere else, and this printed stdout alone — so when the runner hit this
// on 2026-08-21 the failure carried an empty buffer and named nothing at
// all. A diagnostic that omits the one stream carrying the diagnosis is how
// a failure becomes unexplainable a second time.
func (m *muxLiveMaster) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(m.out.String(), "NOCX_LIVE_READY") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the master's own session never reached its prompt; it said:\n%s\nand ssh said:\n%s",
		m.out.String(), m.errOut.String())
}

// openOwned waits for the control socket to answer OUR handshake. Ownership
// is proven by this call and by nothing earlier.
func (m *muxLiveMaster) openOwned(t *testing.T) *mux.Master {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		master, err := mux.Open(m.controlPath)
		if err == nil {
			t.Cleanup(func() { _ = master.Close() })
			return master
		}
		last = err
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the control socket never completed the handshake: %v; master said:\n%s\nand ssh said:\n%s",
		last, m.out.String(), m.errOut.String())
	return nil
}

// echoThrough proves the master's own session is a working interactive one:
// bytes typed into it come back off its pty.
func (m *muxLiveMaster) echoThrough(t *testing.T, token string) {
	t.Helper()
	if _, err := io.WriteString(m.stdin, token+"\n"); err != nil {
		t.Fatalf("type into the master's session: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(m.out.String(), token) >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the master's session never echoed %q; it said:\n%s", token, m.out.String())
}

func muxLiveConnCount(fx *execProbeSshd) int {
	return strings.Count(fx.log.String(), "Connection from")
}

// ---------------------------------------------------------------------------

// Assertion 18/19, the accepting case: the push rides the master, and the
// server records no second connection and no second authentication.
func TestTypedMux_ThePublishRidesTheUsersOwnConnection(t *testing.T) {
	fx := startExecProbeSshd(t, "", "MaxSessions 10\n")
	m := startMuxLiveMaster(t, fx)
	m.waitReady(t)

	authsBefore, connsBefore := fx.authCount(), muxLiveConnCount(fx)
	if authsBefore != 1 || connsBefore != 1 {
		t.Fatalf("before the push: auths=%d conns=%d, want 1/1", authsBefore, connsBefore)
	}

	master := m.openOwned(t)
	if master.PID() <= 0 {
		t.Fatalf("the master reported pid %d; the ownership proof must name the process", master.PID())
	}

	sess, err := master.Session(mux.SessionRequest{Subsystem: true, Command: "sftp"})
	if err != nil {
		t.Fatalf("subsystem session on the master: %v; master said:\n%s", err, m.errOut.String())
	}
	defer func() { _ = sess.Close() }()

	client, err := sftp.NewClientPipe(sess, sess)
	if err != nil {
		t.Fatalf("sftp over the mux session: %v", err)
	}
	defer func() { _ = client.Close() }()

	target := filepath.Join(fx.home, "rode-the-master")
	f, err := client.Create(target)
	if err != nil {
		t.Fatalf("create over the mux session: %v", err)
	}
	payload := strings.Repeat("nocx", 1024)
	if _, wErr := f.Write([]byte(payload)); wErr != nil {
		t.Fatalf("write over the mux session: %v", wErr)
	}
	if cErr := f.Close(); cErr != nil {
		t.Fatalf("close over the mux session: %v", cErr)
	}
	got, err := os.ReadFile(target) // #nosec G304 — fixture-owned path
	if err != nil {
		t.Fatalf("the published file is not on the far side: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("published %d bytes, read back %d", len(payload), len(got))
	}

	if auths, conns := fx.authCount(), muxLiveConnCount(fx); auths != 1 || conns != 1 {
		t.Fatalf("after a %d-byte push: auths=%d conns=%d, want 1/1 — the publish must add neither",
			len(payload), auths, conns)
	}

	// And the user's own session is still theirs.
	m.echoThrough(t, "NOCX_STILL_MINE")
}

// Assertions 19 and 20: with the server's MaxSessions at 1 the master holds
// the only session, so the mux session request is REFUSED — and the adapter
// answers that by refusing the delivery, never by opening a connection of its
// own. The user is left exactly where they were: at a working prompt on the
// connection they already authenticated.
func TestTypedMux_ARefusedSessionOpensNoConnectionAndCostsNoSecondAuthentication(t *testing.T) {
	fx := startExecProbeSshd(t, "", "MaxSessions 1\n")
	m := startMuxLiveMaster(t, fx)
	m.waitReady(t)

	master := m.openOwned(t)
	_, err := master.Session(mux.SessionRequest{Subsystem: true, Command: "sftp"})
	if err == nil {
		t.Fatal("the session request succeeded against MaxSessions 1; this fixture exists to refuse it")
	}
	if !errors.Is(err, mux.ErrSessionRefused) {
		t.Fatalf("the refusal came back as %v, want mux.ErrSessionRefused", err)
	}

	if auths, conns := fx.authCount(), muxLiveConnCount(fx); auths != 1 || conns != 1 {
		t.Fatalf("after the refused session: auths=%d conns=%d, want 1/1 — a refusal must never buy a second credential use",
			auths, conns)
	}

	// The named refusal leaves a working prompt on the user's own session.
	m.echoThrough(t, "NOCX_PROMPT_SURVIVES")

	if auths, conns := fx.authCount(), muxLiveConnCount(fx); auths != 1 || conns != 1 {
		t.Fatalf("after the surviving prompt: auths=%d conns=%d, want 1/1", auths, conns)
	}
}

// Assertion 18: nothing is published before the handshake proves ownership of
// that specific socket. The proof is negative and has to be, so it is taken
// at the seam where a publish would be visible: the server's own log records
// every subsystem session, and there is none until Open has returned.
func TestTypedMux_NothingIsPublishedBeforeOwnershipIsProven(t *testing.T) {
	fx := startExecProbeSshd(t, "", "MaxSessions 10\n")
	m := startMuxLiveMaster(t, fx)
	m.waitReady(t)

	if n := strings.Count(fx.log.String(), "subsystem 'sftp'"); n != 0 {
		t.Fatalf("%d sftp subsystem sessions before ownership was proven, want 0", n)
	}
	// A socket that is not ours is not ownership either, and asking about it
	// must publish nothing: point the client at a path that has no master.
	if _, err := mux.Open(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("Open succeeded against a socket with no master behind it")
	}
	if n := strings.Count(fx.log.String(), "subsystem 'sftp'"); n != 0 {
		t.Fatalf("%d sftp subsystem sessions after a failed handshake, want 0", n)
	}

	master := m.openOwned(t)
	sess, err := master.Session(mux.SessionRequest{Subsystem: true, Command: "sftp"})
	if err != nil {
		t.Fatalf("subsystem session after the handshake: %v", err)
	}
	defer func() { _ = sess.Close() }()
	client, err := sftp.NewClientPipe(sess, sess)
	if err != nil {
		t.Fatalf("sftp over the mux session: %v", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Getwd(); err != nil {
		t.Fatalf("the subsystem session is not usable: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(fx.log.String(), "subsystem 'sftp'") == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the server never recorded the subsystem session; log:\n%s", fmt.Sprintf("%.4000s", fx.log.String()))
}

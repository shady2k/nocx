//go:build nocx_local_ssh

package session_test

// The stand the ssh-pane tests run against: a REAL in-process ssh server that
// runs REAL commands on a REAL pty, a REAL helper host with BOTH services
// registered (session and ssh), a REAL coordinator client on the other end of a
// socket, and a scripted coordinator answering the reverse ops.
//
// Nothing here is a mock of the thing under test. What is scripted is what is
// NOT this repository's code on either end: the ssh SERVER (somebody else's
// machine) and the coordinator's own answers (a vault, known_hosts and the
// person behind them). Everything between — the framing, the handshake, the
// reverse asks, the pool, the channel, the pty request, the launcher and its
// frames, the session window, the runtime — is the shipped path.
//
// A fixture of its own rather than sshsvc's, for the reason that package gives
// about its own: those symbols live in another package's `_test.go` file, and
// Go does not export those across packages. This one serves what a PANE needs
// and a probe never reaches: session channels with a pty, a shell, an exec, a
// window-change and a real exit status.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

const sshTestHash = "ssh-test-hash"

// sshTestRef is the credential reference the scripted coordinator answers for.
// It is the coordinator's own opaque handle and the helper never interprets it,
// which is what makes an arbitrary value the right one here.
const sshTestRef = "ssh-cred-1"

// paneWait bounds one observable wait. It is a DEADLINE on a state change and
// never an assertion about duration: every wait below returns the moment the
// thing it waits for happens, and this is only what turns a hang into a failure
// with a sentence.
const paneWait = 15 * time.Second

// ── the far host ────────────────────────────────────────────────────────

// sshFixture is an in-process ssh server that serves a session channel the way
// a host does for `ssh host`: a pty, then a shell or a command on it.
type sshFixture struct {
	addr       string
	hostSigner gossh.Signer
	password   string

	// shellCommand is what a `shell` request runs. It is the test's own choice
	// because that is what a far account's login shell IS from this side: a
	// program nobody here controls.
	shellCommand string

	mu         sync.Mutex
	passwords  []string
	shells     int
	execs      []string
	terms      []string
	signals    []string
	exits      []uint32
	started    chan struct{}
	exited     chan struct{}
	sized      chan struct{}
	signalled  chan struct{}
	execSeen   chan struct{}
	lastCols   uint16
	lastRows   uint16
	lastMaster *os.File
	// farOutput is everything the far side has written on its pty, and changed
	// is woken on every chunk of it. Together they are how a test waits for a
	// far-side TOKEN without polling: the wait is on the event that bytes
	// arrived, re-checked against the buffer, and the deadline only ever turns
	// a hang into a failure.
	farOutput []byte
	// programInput is everything the CLIENT sent to the far program's stdin,
	// and programWriteErr is why a write to the pty master failed, if one did.
	programInput    []byte
	programWriteErr error
	changed         chan struct{}
	// dropExit makes the next command's end close the channel with no
	// exit-status request at all: the far side going away mid-session.
	dropExit bool
}

func newSSHFixture(t *testing.T, password, shellCommand string) *sshFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	_ = pub
	f := &sshFixture{
		hostSigner: signer, password: password, shellCommand: shellCommand,
		started: make(chan struct{}, 8), exited: make(chan struct{}, 8),
		sized: make(chan struct{}, 8), signalled: make(chan struct{}, 8),
		execSeen: make(chan struct{}, 8), changed: make(chan struct{}, 1),
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			f.mu.Lock()
			f.passwords = append(f.passwords, string(pw))
			f.mu.Unlock()
			if string(pw) == f.password {
				return nil, nil
			}
			return nil, fmt.Errorf("password refused")
		},
	}
	config.AddHostKey(signer)

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

func (f *sshFixture) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatalf("split %q: %v", f.addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func (f *sshFixture) fingerprint() string {
	return gossh.FingerprintSHA256(f.hostSigner.PublicKey())
}

func (f *sshFixture) serve(conn net.Conn, config *gossh.ServerConfig) {
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
		if newChan.ChannelType() != "session" {
			_ = ch.Close()
			continue
		}
		go f.serveSession(ch, chReqs)
	}
}

// sessionState is one session channel: its pty size, the command it is running
// and whether its end should be silent.
type sessionState struct {
	cols, rows uint16
	master     *os.File
}

func (f *sshFixture) serveSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	st := &sessionState{cols: 80, rows: 24}
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term  string
				Cols  uint32
				Rows  uint32
				W     uint32
				H     uint32
				Modes string
			}
			if gossh.Unmarshal(req.Payload, &p) == nil {
				f.mu.Lock()
				f.terms = append(f.terms, p.Term)
				f.mu.Unlock()
				st.cols, st.rows = clampU16(p.Cols), clampU16(p.Rows)
			}
			_ = req.Reply(true, nil)
		case "window-change":
			var w struct {
				Cols uint32
				Rows uint32
				W    uint32
				H    uint32
			}
			if gossh.Unmarshal(req.Payload, &w) == nil {
				st.cols, st.rows = clampU16(w.Cols), clampU16(w.Rows)
				f.mu.Lock()
				f.lastCols, f.lastRows = st.cols, st.rows
				master := st.master
				f.mu.Unlock()
				if master != nil {
					_ = pty.Setsize(master, &pty.Winsize{Rows: st.rows, Cols: st.cols})
				}
				f.signal(f.sized)
			}
			_ = req.Reply(true, nil)
		case "env":
			_ = req.Reply(true, nil)
		case "signal":
			var s struct{ Signal string }
			if gossh.Unmarshal(req.Payload, &s) == nil {
				f.mu.Lock()
				f.signals = append(f.signals, s.Signal)
				f.mu.Unlock()
				f.signal(f.signalled)
			}
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			f.mu.Lock()
			f.shells++
			f.mu.Unlock()
			f.start(ch, st, f.shellCommand)
		case "exec":
			var e struct{ Command string }
			if gossh.Unmarshal(req.Payload, &e) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			f.mu.Lock()
			f.execs = append(f.execs, e.Command)
			f.mu.Unlock()
			f.signal(f.execSeen)
			f.start(ch, st, e.Command)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// start runs one command line on a fresh pty and wires it to the channel.
//
// The command is handed to a shell (`sh -c`), exactly as sshd hands a far
// account's session to its login shell: the launcher carrier is a command LINE
// with its own quoting, and re-parsing it here is what a real host does.
func (f *sshFixture) start(ch gossh.Channel, st *sessionState, command string) {
	shell := "/bin/sh"
	if _, lookErr := os.Stat(shell); lookErr != nil {
		shell = "/bin/bash"
	}
	//nolint:gosec // the command is the test's own, and it is the point of this fixture.
	cmd := exec.Command(shell, "-c", command)
	cmd.Env = append(os.Environ(), "SHELL="+shell, "TERM=xterm-256color")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: st.rows, Cols: st.cols})
	if err != nil {
		_ = ch.Close()
		return
	}
	st.master = master
	f.mu.Lock()
	f.lastMaster = master
	drop := f.dropExit
	f.mu.Unlock()
	f.signal(f.started)

	go f.pumpToChannel(ch, master)
	go f.pumpToProgram(ch, master)
	go func() {
		werr := cmd.Wait()
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
			if code < 0 {
				// A signalled child: the wire field is unsigned and 255 is what
				// a shell reports.
				code = 255
			}
		}
		_ = werr
		f.mu.Lock()
		f.exits = append(f.exits, uint32(code)) // #nosec G115 -- clamped non-negative just above.
		f.mu.Unlock()
		if !drop {
			_, _ = ch.SendRequest("exit-status", false,
				gossh.Marshal(struct{ Status uint32 }{Status: uint32(code)})) // #nosec G115 -- clamped above.
		}
		_ = master.Close()
		_ = ch.Close()
		f.signal(f.exited)
	}()
}

// pumpToChannel moves the far command's output to the channel, and records it.
//
// It is the same io.Copy the fixture used, with one addition: the bytes are kept
// and every chunk wakes the far-output waiter, which is what lets a test assert
// a far-side TOKEN (the loader's own readiness line) without ever asking how
// long it took.
func (f *sshFixture) pumpToChannel(ch gossh.Channel, master *os.File) {
	buf := make([]byte, 32*1024)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.farOutput = append(f.farOutput, buf[:n]...)
			f.mu.Unlock()
			f.signal(f.changed)
			if _, werr := ch.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpToProgram moves the client's bytes to the far program's input, the way a
// server feeds a session's stdin, and records them: a test asserting that the
// SESSION wrote something to the far side has to be able to see it arrive.
func (f *sshFixture) pumpToProgram(ch gossh.Channel, master *os.File) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.programInput = append(f.programInput, buf[:n]...)
			f.mu.Unlock()
			f.signal(f.changed)
			if _, werr := master.Write(buf[:n]); werr != nil {
				f.mu.Lock()
				f.programWriteErr = werr
				f.mu.Unlock()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// programInputSeen is everything the far PROGRAM was sent on its stdin.
func (f *sshFixture) programInputSeen() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.programInput...)
}

// waitFarOutput waits until the far side has written want, and answers
// everything it wrote. The wait is on the far side's own bytes — an event, not a
// duration — and paneWait is only what turns a far side that never speaks into a
// failure with a sentence.
func (f *sshFixture) waitFarOutput(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(paneWait)
	for {
		f.mu.Lock()
		seen := string(f.farOutput)
		f.mu.Unlock()
		if strings.Contains(seen, want) {
			return seen
		}
		select {
		case <-f.changed:
		case <-deadline:
			t.Fatalf("the far side never wrote %q; it wrote:\n%s", want, seen)
		}
	}
}

func (f *sshFixture) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *sshFixture) waitSignal(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(paneWait):
		t.Fatalf("timed out waiting for the far host: %s", what)
	}
}

// waitStarted waits for the far side to have a command running.
func (f *sshFixture) waitStarted(t *testing.T) {
	t.Helper()
	f.waitSignal(t, f.started, "a command started")
}

// waitExec answers the command line the helper exec'd, waiting for one.
func (f *sshFixture) waitExec(t *testing.T) string {
	t.Helper()
	f.waitSignal(t, f.execSeen, "an exec request")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execs[len(f.execs)-1]
}

// waitWindowChange waits for a window-change request and answers the size it
// carried, and the size the far pty actually holds.
func (f *sshFixture) waitWindowChange(t *testing.T) (uint16, uint16, uint16, uint16) {
	t.Helper()
	f.waitSignal(t, f.sized, "a window-change request")
	f.mu.Lock()
	cols, rows, master := f.lastCols, f.lastRows, f.lastMaster
	f.mu.Unlock()
	var ptyCols, ptyRows uint16
	if master != nil {
		// GetsizeFull returns the pixel dimensions too, which this fixture does
		// not use: the assertion is about the CELL size reaching the far pty,
		// and nothing on the helper protocol carries pixels today.
		if rows64, cols64, err := pty.Getsize(master); err == nil {
			ptyCols, ptyRows = uint16(cols64), uint16(rows64) // #nosec G115 -- a pty size fits uint16.
		}
	}
	return cols, rows, ptyCols, ptyRows
}

// waitExit waits for the far command to have exited.
func (f *sshFixture) waitExit(t *testing.T) {
	t.Helper()
	f.waitSignal(t, f.exited, "the far command to exit")
}

// armsSilentEnd makes the NEXT command's end close the channel with no exit
// status — the far side disappearing mid-session.
func (f *sshFixture) armsSilentEnd() {
	f.mu.Lock()
	f.dropExit = true
	f.mu.Unlock()
}

// shellsSeen and execsSeen are the two commands this fixture was asked to run,
// in order: what the helper actually sent to the far host.
func (f *sshFixture) shellsSeen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shells
}

func (f *sshFixture) execsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

// authAttempts answers the passwords this fixture was offered, in order: a
// refusal that never dialed left this empty, which is how a test tells "refused
// before the network" from "refused by the host".
func (f *sshFixture) authAttempts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.passwords...)
}

func (f *sshFixture) signalsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.signals...)
}

func (f *sshFixture) termsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.terms...)
}

func clampU16(v uint32) uint16 {
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

// ── the coordinator on this side ────────────────────────────────────────

// sshCoordinator is this side of the reverse channel: the ops a helper asks a
// coordinator, scripted. It stands in for internal/app's handlers exactly in
// shape — same service, same ops, same result types, same refusal codes — and
// not in where the answers come from (a vault, a known_hosts file).
type sshCoordinator struct {
	password    string
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

func (c *sshCoordinator) registry() *client.ReverseRegistry {
	r := client.NewReverseRegistry()
	r.Register(proto.ServiceSSH, proto.OpSecret, func(_ context.Context, _ json.RawMessage) (any, error) {
		c.record(proto.OpSecret)
		if c.sealed {
			return nil, &proto.Refusal{Code: proto.ErrCodeVaultSealed, Message: "the vault is sealed"}
		}
		return proto.SecretResult{Secret: []byte(c.password)}, nil
	})
	r.Register(proto.ServiceSSH, proto.OpVerifyHostKey, func(_ context.Context, _ json.RawMessage) (any, error) {
		c.record(proto.OpVerifyHostKey)
		return proto.VerifyHostKeyResult{
			Verdict: c.verdict, Fingerprint: c.fingerprint, Expected: c.expected,
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

func (c *sshCoordinator) record(op string) {
	c.mu.Lock()
	c.ops = append(c.ops, op)
	c.mu.Unlock()
}

func (c *sshCoordinator) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

// ── the stand ───────────────────────────────────────────────────────────

// exitWatcher is a bound connection whose only job is to be TOLD when a session
// ends. It is how a test waits for an exit without polling the inventory: the
// helper's exit notification is the event, and the entry is read once, after it.
type exitWatcher struct {
	mu    sync.Mutex
	exits []proto.SessionExit
	woken chan struct{}
}

func newExitWatcher() *exitWatcher {
	return &exitWatcher{woken: make(chan struct{}, 8)}
}

func (w *exitWatcher) SendSessionData(proto.SessionFrame) error   { return nil }
func (w *exitWatcher) SendLifecycleData(proto.SessionFrame) error { return nil }
func (w *exitWatcher) SendNotification(n proto.Notification) error {
	if n.Service != proto.ServiceSession || n.Event != proto.EventSessionExit {
		return nil
	}
	exit, ok := n.Params.(proto.SessionExit)
	if !ok {
		return nil
	}
	w.mu.Lock()
	w.exits = append(w.exits, exit)
	w.mu.Unlock()
	select {
	case w.woken <- struct{}{}:
	default:
	}
	return nil
}

// waitExit waits for the helper to report a session ending and answers the
// status it reported.
func (w *exitWatcher) waitExit(t *testing.T, session proto.HostSessionID) proto.SessionExit {
	t.Helper()
	deadline := time.After(paneWait)
	for {
		w.mu.Lock()
		for _, e := range w.exits {
			if e.Session == session {
				w.mu.Unlock()
				return e
			}
		}
		w.mu.Unlock()
		select {
		case <-w.woken:
		case <-deadline:
			t.Fatalf("the helper never reported session %s ending", session.Session)
		}
	}
}

// sshStand is one helper process as a coordinator sees it: a host with BOTH
// services on it, a client, the bytes each side sent, and the session service
// the panes live in.
type sshStand struct {
	client    *client.Client
	helper    *host.Host
	sessions  *session.Service
	sshsvc    *sshsvc.Service
	fixture   *sshFixture
	coord     *sshCoordinator
	inspector *recordingInspector
	exits     *exitWatcher
	toHelper  *wireRecorder
	toCoord   *wireRecorder
	cancel    context.CancelFunc
	served    chan struct{}
	closeOnce sync.Once
}

// recordingInspector is the OS-evidence seam, and it RECORDS what it was asked
// about. That is the whole point of it: an ssh session must be observed by
// nobody, so a test can assert both that its entry says `null` and that the
// inspector was never handed a pid for it — which is the difference between
// "we reported nothing" and "we asked the kernel about the scheduler".
type recordingInspector struct {
	mu   sync.Mutex
	pids []int
}

func (i *recordingInspector) Observe(pid, _ int) *proto.Observation {
	i.mu.Lock()
	i.pids = append(i.pids, pid)
	i.mu.Unlock()
	return &proto.Observation{Source: "recording", Unavailable: []proto.Diagnostic{}}
}

func (i *recordingInspector) asked() []int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]int(nil), i.pids...)
}

// wireRecorder keeps every byte ITS side wrote. Reads pass through untouched:
// what a test asks about the wire is what each side SENT.
type wireRecorder struct {
	net.Conn
	mu   sync.Mutex
	sent []byte
}

func (r *wireRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.sent = append(r.sent, p...)
	r.mu.Unlock()
	return r.Conn.Write(p)
}

func (r *wireRecorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.sent...)
}

// standLogger is what the stand's services log to. Every test leaves it nil —
// these tests assert on behaviour, not on prose — and a diagnostic sets it to a
// stderr logger to read the helper's own account of what happened.
var standLogger *slog.Logger

func standLog() *slog.Logger {
	if standLogger != nil {
		return standLogger
	}
	return discardLog()
}

func newSSHStand(t *testing.T, f *sshFixture, coord *sshCoordinator) *sshStand {
	t.Helper()
	return newSSHStandWith(t, f, coord, true)
}

// newSSHStandWithoutSpawner is the same stand with the session service built the
// way an UNTAGGED helper builds it: no ssh spawner at all, which is a fact about
// the binary rather than about any request (plan §1). It exists so the refusal
// that fact produces can be checked OVER THE SOCKET — the code is what a caller
// switches on, and an in-process error value would prove only that a sentinel
// exists.
func newSSHStandWithoutSpawner(t *testing.T, f *sshFixture, coord *sshCoordinator) *sshStand {
	t.Helper()
	return newSSHStandWith(t, f, coord, false)
}

func newSSHStandWith(t *testing.T, f *sshFixture, coord *sshCoordinator, withSSHSpawner bool) *sshStand {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	s := &sshStand{
		toHelper:  &wireRecorder{Conn: helperEnd},
		toCoord:   &wireRecorder{Conn: coordEnd},
		fixture:   f,
		coord:     coord,
		inspector: &recordingInspector{},
		exits:     newExitWatcher(),
		served:    make(chan struct{}),
	}
	realClient, err := ssh.NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = realClient.Close() })

	s.sshsvc = sshsvc.New(realClient, standLog())
	var sshSpawner session.SSHSpawner
	if withSSHSpawner {
		sshSpawner = session.NewSSHSpawner(s.sshsvc, standLog())
	}
	s.sessions = session.New(session.Options{
		Generation: "gen-under-test",
		// No local spawner: these tests are about the ssh half, and a local
		// spawn that could run beside them would only make a failure here
		// ambiguous.
		Spawner:    session.NewLocalSpawner(standLog(), session.Shell{}, "", ""),
		SSHSpawner: sshSpawner,
		Inspector:  s.inspector,
		Log:        standLog(),
		Limits:     session.DefaultLimits(),
	})
	s.helper = host.New(s.toHelper, s.toHelper, sshTestHash, "instance-1", standLog())
	s.helper.Register(s.sessions)
	s.helper.Register(s.sshsvc)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.served)
		_ = s.helper.Serve(ctx)
	}()

	c2, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(s.toCoord),
		ExpectHash:  sshTestHash,
		SentinelTTL: paneWait,
		Reverse:     coord.registry(),
		Log:         standLog(),
	})
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	s.client = c2
	// The connection is BOUND to the session service, as main binds it: the
	// service outlives the connection and every attachment on it dies with it.
	release := s.sessions.Bind(s.helper)
	// A SECOND connection, bound for one reason: the exit notification. A helper
	// daemon serves several at once (D12), and a test that had to poll the
	// inventory to learn a session ended would be asserting on a clock rather
	// than on the event the helper already sends.
	releaseWatcher := s.sessions.Bind(s.exits)
	t.Cleanup(s.stop)
	t.Cleanup(release)
	t.Cleanup(releaseWatcher)
	return s
}

func (s *sshStand) stop() {
	s.closeOnce.Do(func() {
		_ = s.client.Close()
		s.cancel()
		select {
		case <-s.served:
		case <-time.After(paneWait):
		}
	})
}

// spawnParams is the ordinary request: the fixture's address, the password
// identity the scripted coordinator answers for, and the pinned fingerprint of
// the host key the fixture presents.
func (s *sshStand) spawnParams(t *testing.T, mode proto.SSHMode) proto.SSHSpawnParams {
	t.Helper()
	host, port := s.fixture.hostPort(t)
	return proto.SSHSpawnParams{
		Destination: proto.SSHDestination{
			Host: host, Port: port, User: "test",
			Identity: proto.SSHIdentity{
				Credential: proto.SSHCredential{Ref: sshTestRef},
				Auth:       proto.SSHAuthPassword,
			},
		},
		AcceptOnTrust:      true,
		HostKeyFingerprint: s.fixture.fingerprint(),
		Cols:               80,
		Rows:               24,
		DesiredMode:        mode,
	}
}

// spawn runs one spawn-ssh over the real socket.
func (s *sshStand) spawn(t *testing.T, p proto.SSHSpawnParams) (client.SessionEntry, error) {
	t.Helper()
	return s.client.SpawnSSH(context.Background(), p)
}

// mustSpawn runs one spawn-ssh and fails the test if the helper refused.
func (s *sshStand) mustSpawn(t *testing.T, p proto.SSHSpawnParams) client.SessionEntry {
	t.Helper()
	entry, err := s.spawn(t, p)
	if err != nil {
		t.Fatalf("spawn-ssh: %v", err)
	}
	return entry
}

// entry asks the helper's inventory for one session.
func (s *sshStand) entry(t *testing.T, id client.HostSessionID) client.SessionEntry {
	t.Helper()
	entries, err := s.client.Sessions(context.Background())
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	for _, e := range entries {
		if e.HostSessionID == id {
			return e
		}
	}
	t.Fatalf("the inventory holds no session %+v", id)
	return client.SessionEntry{}
}

// refusalCode answers the helper's refusal code for an error, or "" when the
// error is not a refusal at all. It reads the CODE and never the message: a
// test that matched on a sentence would keep passing while the code a caller
// switches on changed underneath it.
func refusalCode(err error) string {
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

// refusalDetails answers the structured half of a refusal, which is where the
// host-key evidence rides.
func refusalDetails(err error) []byte {
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		return refusal.Details
	}
	return nil
}

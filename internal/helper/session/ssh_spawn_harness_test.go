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
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	// t is the test this fixture is the far host of, kept so a failure that
	// happens on an accept goroutine can be reported: t.Errorf is the one
	// reporting method safe to call from another goroutine, and Fatal is not.
	t *testing.T

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
	// conns counts the TCP connections this "host" accepted. A pane's
	// listeners and its shell channel must share ONE of them (AD-4's pool is
	// keyed by the resolved destination), and this is how a test asserts that
	// rather than trusting the pool's key.
	conns int
	// tcpForwards and unixForwards are the listeners this host granted, keyed
	// by the address or path the client named them with — which is how a
	// cancel finds them, and how the client's own forward list matches an
	// incoming connection to the request that created it.
	tcpForwards  map[string]net.Listener
	unixForwards map[string]net.Listener
	// forwardGranted and forwardClosed wake on a listener the far host grants
	// and withdraws, and grantedPorts carries the ports, so a test waits on the
	// far host's own events rather than polling them.
	forwardGranted chan struct{}
	forwardClosed  chan struct{}
	grantedPorts   chan int
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
		t:          t,
		hostSigner: signer, password: password, shellCommand: shellCommand,
		started: make(chan struct{}, 8), exited: make(chan struct{}, 8),
		sized: make(chan struct{}, 8), signalled: make(chan struct{}, 8),
		execSeen: make(chan struct{}, 8), changed: make(chan struct{}, 1),
		tcpForwards:  map[string]net.Listener{},
		unixForwards: map[string]net.Listener{},
		// Eight: one lifecycle grant, one tool socket grant, and room for a
		// test that asks for both twice.
		forwardGranted: make(chan struct{}, 8),
		forwardClosed:  make(chan struct{}, 8),
		grantedPorts:   make(chan int, 8),
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
	t.Cleanup(func() {
		_ = ln.Close()
		// Every listener this host GRANTED is closed too, and a Unix one has
		// its node removed: a test that fails before the session's own Close
		// (or a helper that never got there) would otherwise leave a port
		// bound, a socket file on disk and accept goroutines of a finished
		// test still running.
		f.closeAllForwards()
	})
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
	f.mu.Lock()
	f.conns++
	f.mu.Unlock()
	go f.serveGlobalRequests(sconn, reqs)
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

// ── remote forward (-R) and streamlocal listener requests ───────────────

// serveGlobalRequests is the far host's answer to a client that asks IT to
// listen: `tcpip-forward` (the lifecycle channel's loopback port) and
// `streamlocal-forward@openssh.com` (the pane's agent tool socket).
//
// It is a real implementation of both, and it has to be, because the client
// side of them — x/crypto/ssh's forward list — looks the listener up by the
// address the server sends back on each connection: an echoed address that did
// not match the request is a connection the client refuses as spurious, which
// is the RFC's requirement rather than a strictness of this fixture.
//
// The listeners live on 127.0.0.1 of THIS process, which is what the far host
// is here: a real sshd on another machine binds the same way, and a test that
// wants to be the far side's process dials the port this grants.
func (f *sshFixture) serveGlobalRequests(sconn *gossh.ServerConn, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if gossh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port))))
			if err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			port := f.boundPort(ln)
			f.mu.Lock()
			f.tcpForwards[net.JoinHostPort(p.Addr, strconv.Itoa(port))] = ln
			f.mu.Unlock()
			var reply []byte
			if p.Port == 0 {
				// RFC 4254 §7.1: a requested port 0 is answered with the one
				// the server allocated, in a uint32 payload.
				reply = gossh.Marshal(struct{ Port uint32 }{Port: uint32(port)}) // #nosec G115 -- a bound port.
			}
			_ = req.Reply(true, reply)
			go f.acceptForwardedTCP(sconn, ln, p.Addr)
			f.signal(f.forwardGranted)
			f.signalPort(port)
		case "cancel-tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if gossh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			f.closeForward(net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port))))
			_ = req.Reply(true, nil)
		case "streamlocal-forward@openssh.com":
			var p struct {
				SocketPath string
			}
			if gossh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			ln, err := net.Listen("unix", p.SocketPath)
			if err != nil {
				// An address already in use is REFUSED here rather than
				// replaced, which is what OpenSSH does too: binding over a name
				// somebody else owns would hand them this listener's traffic.
				_ = req.Reply(false, nil)
				continue
			}
			f.mu.Lock()
			f.unixForwards[p.SocketPath] = ln
			f.mu.Unlock()
			_ = req.Reply(true, nil)
			go f.acceptForwardedUnix(sconn, ln, p.SocketPath)
			f.signal(f.forwardGranted)
		case "cancel-streamlocal-forward@openssh.com":
			var p struct {
				SocketPath string
			}
			if gossh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			f.closeForward(p.SocketPath)
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// boundPort answers the port a listener the far host just bound is on. Every
// listener this fixture grants is a TCP one, so anything else is the fixture
// disagreeing with itself: it is REPORTED and the port read as zero, because
// this runs on an accept goroutine as well, and only the test goroutine may
// stop a test (testing.T's Fatal is the one method that may not be called from
// another one).
func (f *sshFixture) boundPort(ln net.Listener) int {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		f.t.Errorf("the far host bound %v, which is not a TCP address", ln.Addr())
		return 0
	}
	return addr.Port
}

// closeAllForwards withdraws every listener this host granted, on a test's
// teardown. The socket files are removed with them: net.Listener.Close does not
// unlink a Unix address, and a fixture that left its nodes behind would make
// the next test's bind fail on a name nobody owns.
func (f *sshFixture) closeAllForwards() {
	f.mu.Lock()
	names := make([]string, 0, len(f.tcpForwards)+len(f.unixForwards))
	for name := range f.tcpForwards {
		names = append(names, name)
	}
	for name := range f.unixForwards {
		names = append(names, name)
	}
	f.mu.Unlock()
	for _, name := range names {
		f.closeForward(name)
	}
}

// closeForward ends one granted listener, by the name it was granted under.
func (f *sshFixture) closeForward(name string) {
	f.mu.Lock()
	ln := f.tcpForwards[name]
	if ln == nil {
		ln = f.unixForwards[name]
	}
	delete(f.tcpForwards, name)
	delete(f.unixForwards, name)
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
		// A Unix listener's NODE outlives Close, and the fixture is the far
		// host: removing it is what makes the path gone rather than stale.
		if strings.HasPrefix(name, "/") {
			_ = os.Remove(name)
		}
		f.signal(f.forwardClosed)
	}
}

// acceptForwardedTCP turns every connection that arrives on a granted port into
// a `forwarded-tcpip` channel back to the client, and pumps it.
func (f *sshFixture) acceptForwardedTCP(sconn *gossh.ServerConn, ln net.Listener, host string) {
	port := f.boundPort(ln)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer func() { _ = conn.Close() }()
			peerHost, peerPort := splitPeer(conn.RemoteAddr())
			payload := gossh.Marshal(struct {
				Addr       string
				Port       uint32
				OriginAddr string
				OriginPort uint32
			}{
				Addr: host, Port: uint32(port), // #nosec G115 -- a bound port.
				OriginAddr: peerHost, OriginPort: uint32(peerPort), // #nosec G115 -- a peer port.
			})
			ch, reqs, err := sconn.OpenChannel("forwarded-tcpip", payload)
			if err != nil {
				return
			}
			defer func() { _ = ch.Close() }()
			go gossh.DiscardRequests(reqs)
			f.pumpForwarded(conn, ch)
		}(conn)
	}
}

// acceptForwardedUnix is the same for a granted socket path: the connections
// arrive as `forwarded-streamlocal@openssh.com`, whose payload names the path
// the client asked for.
func (f *sshFixture) acceptForwardedUnix(sconn *gossh.ServerConn, ln net.Listener, path string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer func() { _ = conn.Close() }()
			payload := gossh.Marshal(struct {
				SocketPath string
				Reserved   string
			}{SocketPath: path})
			ch, reqs, err := sconn.OpenChannel("forwarded-streamlocal@openssh.com", payload)
			if err != nil {
				return
			}
			defer func() { _ = ch.Close() }()
			go gossh.DiscardRequests(reqs)
			f.pumpForwarded(conn, ch)
		}(conn)
	}
}

// pumpForwarded copies bytes both ways between the far side's connection and
// the channel the client holds, until either end is done.
func (f *sshFixture) pumpForwarded(conn net.Conn, ch gossh.Channel) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ch, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, ch); done <- struct{}{} }()
	<-done
}

// splitPeer reads a net.Addr into the two host/port fields a
// forwarded-tcpip payload carries. A peer that cannot be split yields an
// empty host and port 0, which the client still parses: the origin address is
// a label for the log and never an authority.
func splitPeer(addr net.Addr) (string, int) {
	if addr == nil {
		return "", 0
	}
	h, p, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", 0
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return h, 0
	}
	return h, port
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
			// Recorded BEFORE the reply, like the pty and window-change arms
			// above, and for the same reason: the reply is what a caller waits
			// on, so a test that asserts what the far host was asked for reads
			// it after the answer has travelled back — and a record written
			// after the reply is one that assertion can outrun. It did, in 82
			// of 2000 repetitions under load: the spawn answered, and this
			// fixture's own count said the far host had never been asked for a
			// shell.
			f.mu.Lock()
			f.shells++
			f.mu.Unlock()
			_ = req.Reply(true, nil)
			f.start(ch, st, f.shellCommand)
		case "exec":
			var e struct{ Command string }
			if gossh.Unmarshal(req.Payload, &e) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			// Same ordering as `shell`: the command line is on record before
			// the request that made it is answered.
			f.mu.Lock()
			f.execs = append(f.execs, e.Command)
			f.mu.Unlock()
			_ = req.Reply(true, nil)
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

	// drained is closed by the output pump when the pty has nothing left to
	// hand it — see the exit watcher below for why the close waits on it.
	drained := make(chan struct{})

	go f.pumpToChannel(ch, master, drained)
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
		// The far side's OUTPUT is drained before anything is closed, and this
		// wait is what makes that true. Closing the pty from here — which is
		// what this watcher used to do — kills whatever read the pump has
		// parked, and the bytes the kernel was still holding for it (the
		// program's last line) die with the descriptor: measured, 3 runs in
		// 2000 under load, with the far side's `od` line recorded and its final
		// `DSR-DONE` line gone. Nothing has to be closed to end that read: once
		// the command and anything it left holding the slave are gone, a read
		// on the master returns EIO after draining what is buffered, so the
		// pump finishing IS the far host having nothing left to say.
		<-drained
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
//
// It also closes drained on the way out, which is the only signal that the far
// side has no more output: the pty hands a read an error of its own once the
// command is gone, and the exit watcher in start waits on this before it closes
// anything — closing the pty out from under this read is what used to drop the
// program's last line.
func (f *sshFixture) pumpToChannel(ch gossh.Channel, master *os.File, drained chan<- struct{}) {
	defer close(drained)
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

// signalPort records a port the far host just bound. It never blocks: the
// channel is a record for a test that waits later, and a far host that owned a
// goroutine on a test's read position would be a fixture that can deadlock the
// thing it is measuring.
func (f *sshFixture) signalPort(port int) {
	select {
	case f.grantedPorts <- port:
	default:
	}
}

// waitLifecyclePort waits until the far host has granted a loopback listener
// and answers the port it bound.
//
// The wait is on the far host's own accept of the request — an event — and
// paneWait only turns a client that never asked into a failure with a sentence.
func (f *sshFixture) waitLifecyclePort(t *testing.T) int {
	t.Helper()
	select {
	case port := <-f.grantedPorts:
		return port
	case <-time.After(paneWait):
		t.Fatalf("the far host was never asked for a loopback listener")
		return 0
	}
}

// waitForwardGranted waits for ANY listener the far host has granted (a
// streamlocal socket is not a port and has no number to answer with).
func (f *sshFixture) waitForwardGranted(t *testing.T) {
	t.Helper()
	select {
	case <-f.forwardGranted:
	case <-time.After(paneWait):
		t.Fatalf("the far host granted no listener")
	}
}

// connections is how many TCP connections this host has accepted. One means
// the pane's listeners and its shell channel rode a single pooled connection.
func (f *sshFixture) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// liveForwards is how many listeners the far host still holds, by name. A
// session that ended leaves none: cancelling them is the process's Close.
// waitNoForwards waits until the far host holds no listener.
func (f *sshFixture) waitNoForwards(t *testing.T) {
	t.Helper()
	deadline := time.After(paneWait)
	for {
		if len(f.liveForwards()) == 0 {
			return
		}
		select {
		case <-f.forwardClosed:
		case <-deadline:
			t.Fatalf("the far host still holds listeners %v", f.liveForwards())
		}
	}
}

// liveForwards is how many listeners the far host still holds, by name. A
// session that ended leaves none: cancelling them is the process's Close.
func (f *sshFixture) liveForwards() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.tcpForwards)+len(f.unixForwards))
	for name := range f.tcpForwards {
		names = append(names, name)
	}
	for name := range f.unixForwards {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	// toolSocket is the coordinator this stand's client IS: the tool endpoint
	// it names on every spawn it makes, which is what that pane's tool
	// connections belong to (nocx-50w7p.18). Empty is a coordinator that runs
	// none — a real state, and the one that makes a request for a far-side tool
	// socket impossible to honour.
	toolSocket string
	toHelper   *wireRecorder
	toCoord    *wireRecorder
	cancel     context.CancelFunc
	served     chan struct{}
	closeOnce  sync.Once
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

// newSSHStandWithoutToolEndpoint is the same stand with a coordinator that runs
// NO tool endpoint: it names none on its spawn requests, which is a real state
// (cmd/nocx-server answers nil, nil when it has no tool surface) and the one
// that makes a request for a far-side tool socket impossible to honour.
func newSSHStandWithoutToolEndpoint(t *testing.T, f *sshFixture, coord *sshCoordinator) *sshStand {
	t.Helper()
	return newSSHStandFull(t, f, coord, true, "")
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
	// The coordinator's own tool endpoint, when it runs one. The path is the
	// stand's and never created: a test that wants an endpoint serves one
	// there, and a test that does not leaves the path dead — which is exactly
	// the difference between a pane whose agent can reach the tool surface and
	// one whose agent cannot.
	return newSSHStandFull(t, f, coord, withSSHSpawner, filepath.Join(t.TempDir(), "tool.sock"))
}

func newSSHStandFull(t *testing.T, f *sshFixture, coord *sshCoordinator, withSSHSpawner bool, toolSocket string) *sshStand {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	s := &sshStand{
		toHelper:   &wireRecorder{Conn: helperEnd},
		toCoord:    &wireRecorder{Conn: coordEnd},
		fixture:    f,
		coord:      coord,
		inspector:  &recordingInspector{},
		exits:      newExitWatcher(),
		toolSocket: toolSocket,
		served:     make(chan struct{}),
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
		Spawner:    session.NewLocalSpawner(standLog(), session.Shell{}, ""),
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
// identity the scripted coordinator answers for, the pinned fingerprint of the
// host key the fixture presents, and THIS coordinator's own tool endpoint —
// which is what the pane's tool connections belong to (nocx-50w7p.18) and is
// therefore named by the request rather than held by the daemon.
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
		AgentToolEndpoint:  s.toolSocket,
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

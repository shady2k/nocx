package mux

// The mux client, measured against a master that speaks the protocol back.
//
// The fake master here encodes the wire BY HAND, from OpenSSH's PROTOCOL.mux
// rather than from this package's own encoder. That is deliberate: a fixture
// built on the implementation agrees with it even when both are wrong, which
// is the failure AGENTS.md's fourth testing rule names. The independent check
// that the bytes are the REAL protocol is the live test in internal/app,
// which drives an actual `ssh` master.

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------------------
// A master that speaks PROTOCOL.mux, hand-encoded.

type fakeMaster struct {
	t    *testing.T
	ln   *net.UnixListener
	path string

	// refuseSession makes every MUX_C_NEW_SESSION answer MUX_S_FAILURE, the
	// shape a server with MaxSessions exhausted produces.
	refuseSession bool
	// denySession answers MUX_S_PERMISSION_DENIED instead.
	denySession bool
	// badVersion makes the hello answer name a protocol version we do not
	// speak, so the handshake fails and ownership is never proven.
	badVersion bool
	// silentHello closes the connection instead of answering the hello.
	silentHello bool

	mu        sync.Mutex
	hellos    int
	alives    int
	sessions  int
	terminate int
	opened    []openedSession
	pid       uint32
}

type openedSession struct {
	wantTTY   bool
	subsystem bool
	term      string
	command   string
	// in is the descriptor the master received as the session's STDIN: what
	// the client writes lands here.
	in, out, errFD *os.File
}

func newFakeMaster(t *testing.T) *fakeMaster {
	t.Helper()
	// The socket must be short: a long path is exactly the failure the
	// wrapper refuses by construction, and a test must not trip over it.
	dir, err := os.MkdirTemp("", "muxt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "m")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatalf("listen %s: %v", p, err)
	}
	m := &fakeMaster{t: t, ln: ln, path: p, pid: 4242}
	t.Cleanup(func() { _ = ln.Close() })
	go m.serve()
	return m
}

func (m *fakeMaster) counts() (hellos, alives, sessions, terminate int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hellos, m.alives, m.sessions, m.terminate
}

func (m *fakeMaster) lastOpened() (openedSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.opened) == 0 {
		return openedSession{}, false
	}
	return m.opened[len(m.opened)-1], true
}

func (m *fakeMaster) serve() {
	for {
		c, err := m.ln.AcceptUnix()
		if err != nil {
			return
		}
		go m.handle(c)
	}
}

// readPacket reads one length-prefixed message body.
func fakeReadPacket(c net.Conn) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(c, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 256*1024 {
		return nil, errors.New("implausible packet length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return nil, err
	}
	return body, nil
}

func fakeWritePacket(c net.Conn, body []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(body))) //nolint:gosec // fixture message, bounded by the fixture
	if _, err := c.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := c.Write(body)
	return err
}

// tiny hand encoder/decoder, per PROTOCOL.mux's SSH string conventions.
type enc struct{ b []byte }

func (e *enc) u32(v uint32) { e.b = binary.BigEndian.AppendUint32(e.b, v) }
func (e *enc) str(s string) {
	e.u32(uint32(len(s))) //nolint:gosec // fixture strings are short constants
	e.b = append(e.b, s...)
}

type dec struct {
	b   []byte
	err error
}

func (d *dec) u32() uint32 {
	if d.err != nil || len(d.b) < 4 {
		d.err = errors.New("short read")
		return 0
	}
	v := binary.BigEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

// flag reads one of PROTOCOL.mux's "bool" fields. They are uint32 on the
// wire: OpenSSH's master reads them with sshbuf_get_u32, and this fixture
// encodes what the real master expects, not what the word "bool" suggests.
func (d *dec) flag() bool { return d.u32() != 0 }

func (d *dec) str() string {
	n := d.u32()
	//nolint:gosec // the fixture's buffer is bounded by fakeReadPacket
	if d.err != nil || uint32(len(d.b)) < n {
		d.err = errors.New("short read")
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func (m *fakeMaster) handle(c *net.UnixConn) {
	defer func() { _ = c.Close() }()

	body, err := fakeReadPacket(c)
	if err != nil {
		return
	}
	d := &dec{b: body}
	if d.u32() != msgHello {
		return
	}
	m.mu.Lock()
	m.hellos++
	m.mu.Unlock()
	if m.silentHello {
		return
	}
	ver := uint32(protocolVersion)
	if m.badVersion {
		ver = 99
	}
	e := &enc{}
	e.u32(msgHello)
	e.u32(ver)
	if fakeWritePacket(c, e.b) != nil {
		return
	}

	for {
		body, err = fakeReadPacket(c)
		if err != nil {
			return
		}
		d = &dec{b: body}
		switch typ := d.u32(); typ {
		case cAliveCheck:
			rid := d.u32()
			m.mu.Lock()
			m.alives++
			pid := m.pid
			m.mu.Unlock()
			r := &enc{}
			r.u32(sAlive)
			r.u32(rid)
			r.u32(pid)
			if fakeWritePacket(c, r.b) != nil {
				return
			}
		case cTerminate:
			rid := d.u32()
			m.mu.Lock()
			m.terminate++
			m.mu.Unlock()
			r := &enc{}
			r.u32(sOK)
			r.u32(rid)
			if fakeWritePacket(c, r.b) != nil {
				return
			}
		case cNewSession:
			m.newSession(c, d)
			return
		default:
			return
		}
	}
}

func (m *fakeMaster) newSession(c *net.UnixConn, d *dec) {
	rid := d.u32()
	_ = d.str() // reserved
	wantTTY := d.flag()
	_ = d.flag() // x11
	_ = d.flag() // agent
	subsystem := d.flag()
	_ = d.u32() // escape char
	term := d.str()
	command := d.str()
	if d.err != nil {
		return
	}

	// Three descriptors arrive next, one sendmsg each.
	fds := make([]*os.File, 0, 3)
	for i := 0; i < 3; i++ {
		f, err := recvFD(c)
		if err != nil {
			m.t.Logf("fake master: receiving descriptor %d: %v", i, err)
			return
		}
		fds = append(fds, f)
	}

	m.mu.Lock()
	m.sessions++
	m.mu.Unlock()

	if m.refuseSession || m.denySession {
		for _, f := range fds {
			_ = f.Close()
		}
		r := &enc{}
		if m.denySession {
			r.u32(sPermissionDenied)
		} else {
			r.u32(sFailure)
		}
		r.u32(rid)
		r.str("session request failed: Session open refused by peer")
		_ = fakeWritePacket(c, r.b)
		return
	}

	m.mu.Lock()
	m.opened = append(m.opened, openedSession{
		wantTTY: wantTTY, subsystem: subsystem, term: term, command: command,
		in: fds[0], out: fds[1], errFD: fds[2],
	})
	m.mu.Unlock()

	r := &enc{}
	r.u32(sSessionOpened)
	r.u32(rid)
	r.u32(7) // session id
	_ = fakeWritePacket(c, r.b)

	// Echo whatever the client sends, so the caller can prove the pipe is
	// the master's own descriptors and not something this package invented.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := fds[0].Read(buf)
			if n > 0 {
				_, _ = fds[1].Write(append([]byte("echo:"), buf[:n]...))
			}
			if err != nil {
				_ = fds[1].Close()
				return
			}
		}
	}()
}

// recvFD reads one SCM_RIGHTS descriptor off the control socket.
func recvFD(c *net.UnixConn) (*os.File, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if len(msgs) != 1 {
		return nil, errors.New("want exactly one control message")
	}
	got, err := unix.ParseUnixRights(&msgs[0])
	if err != nil {
		return nil, err
	}
	if len(got) != 1 {
		return nil, errors.New("want exactly one descriptor")
	}
	return os.NewFile(uintptr(got[0]), "muxfd"), nil
}

// ---------------------------------------------------------------------------

func TestOpen_ProvesOwnershipWithAHandshakeAgainstThatSocket(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()
	if hellos, alives, _, _ := m.counts(); hellos != 1 || alives != 1 {
		t.Fatalf("hellos=%d alives=%d, want 1/1: ownership is proven by a handshake, not assumed", hellos, alives)
	}
	if master.PID() != 4242 {
		t.Fatalf("master pid %d, want the 4242 the socket answered", master.PID())
	}
}

func TestOpen_AnAbsentSocketIsNotOwnership(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "nothing")); err == nil {
		t.Fatal("Open succeeded against a socket that does not exist")
	}
}

func TestOpen_AMasterSpeakingAnotherVersionIsNotOwnership(t *testing.T) {
	m := newFakeMaster(t)
	m.badVersion = true
	if _, err := Open(m.path); err == nil {
		t.Fatal("Open succeeded against a master speaking a protocol we do not")
	}
}

func TestOpen_AMasterThatNeverAnswersIsNotOwnership(t *testing.T) {
	m := newFakeMaster(t)
	m.silentHello = true
	if _, err := Open(m.path); err == nil {
		t.Fatal("Open succeeded against a master that answered nothing")
	}
}

func TestSession_CarriesTheRequestAndTheMastersOwnDescriptors(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()

	s, err := master.Session(SessionRequest{Subsystem: true, Command: "sftp"})
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	defer func() { _ = s.Close() }()

	got, ok := m.lastOpened()
	if !ok {
		t.Fatal("the master recorded no opened session")
	}
	if !got.subsystem || got.command != "sftp" {
		t.Fatalf("master saw subsystem=%v command=%q, want true/%q", got.subsystem, got.command, "sftp")
	}
	if got.wantTTY {
		t.Fatal("an auxiliary channel asked for a tty; there is only ever one interactive session")
	}

	if _, wErr := s.Write([]byte("ping")); wErr != nil {
		t.Fatalf("write to the session: %v", wErr)
	}
	buf := make([]byte, 64)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("read from the session: %v", err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Fatalf("read %q, want %q — the pipe must be the descriptors the master received", buf[:n], "echo:ping")
	}
}

func TestSession_ARefusedSessionIsRefused_NeverAFallback(t *testing.T) {
	m := newFakeMaster(t)
	m.refuseSession = true
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()

	_, err = master.Session(SessionRequest{Subsystem: true, Command: "sftp"})
	if err == nil {
		t.Fatal("a refused session request returned a session")
	}
	if !errors.Is(err, ErrSessionRefused) {
		t.Fatalf("refusal reported as %v, want ErrSessionRefused — the caller decides on the class, never on the text", err)
	}
	if !strings.Contains(err.Error(), "Session open refused by peer") {
		t.Fatalf("the master's own reason was dropped: %v", err)
	}
	if _, _, sessions, _ := m.counts(); sessions != 1 {
		t.Fatalf("session requests=%d, want exactly 1: a refusal is never retried", sessions)
	}
}

func TestSession_PermissionDeniedIsAlsoARefusal(t *testing.T) {
	m := newFakeMaster(t)
	m.denySession = true
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()
	if _, err := master.Session(SessionRequest{Command: "true"}); !errors.Is(err, ErrSessionRefused) {
		t.Fatalf("permission denied reported as %v, want ErrSessionRefused", err)
	}
}

func TestExit_AsksTheMasterToGo(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := master.Exit(); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if _, _, _, terminate := m.counts(); terminate != 1 {
		t.Fatalf("terminate requests=%d, want 1", terminate)
	}
	_ = master.Close()
}

// Exit must still reach the master after this client has released the
// connection Open took.
//
// That release is not optional tidiness: an attached mux client is an open
// channel on the master's connection and `ssh` does not exit while one is
// open, so holding it for the whole delivery is what kept the user's own ssh
// — and their local prompt — alive for 20 s after their remote shell exited.
// Releasing it early is only safe if the closing half of the interval still
// works afterwards, which is this.
func TestExit_ReachesTheMasterAfterTheClientReleasedItsConnection(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := master.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// And twice, because the release and the deferred cleanup are two
	// callers that do not know about each other.
	if err := master.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := master.Exit(); err != nil {
		t.Fatalf("Exit after Close: %v", err)
	}
	if _, _, _, terminate := m.counts(); terminate != 1 {
		t.Fatalf("terminate requests=%d, want 1", terminate)
	}
}

// The bound is at the seam, not at the builder (nocx-e4ir3).
//
// The ~92 KiB command that carried the integration bundle and two bearers was
// removed by nocx-m8jwn, and the replacement declares a 1 KiB contract. That
// contract lived in ONE producer. This asserts the other half: the transport
// refuses an over-long command itself, so no producer — the one that exists,
// or the next one — can put a long command on the wire by handing it here.
//
// "Nothing was sent" is the assertion that matters. A refusal that has
// already written the packet is not a bound, it is a log line.
func TestSession_ACommandAtTheBoundIsRefusedAndNothingIsSent(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()

	_, err = master.Session(SessionRequest{Command: strings.Repeat("x", MaxCommandLen)})
	if !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("Session with a command at the bound returned %v, want ErrCommandTooLong", err)
	}
	if _, ok := m.lastOpened(); ok {
		t.Fatal("the master recorded an opened session: the command reached the wire before it was refused")
	}
	if _, _, sessions, _ := m.counts(); sessions != 0 {
		t.Fatalf("the master saw %d session requests, want 0", sessions)
	}
}

// Refusal never truncates: a bounded transport that silently shortens the
// command runs something the caller did not ask for, on somebody else's
// machine.
func TestSession_AnOverLongCommandIsNeverTruncatedIntoAShorterOne(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()

	if _, err := master.Session(SessionRequest{Command: strings.Repeat("x", MaxCommandLen*4)}); !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("Session returned %v, want ErrCommandTooLong", err)
	}
	if got, ok := m.lastOpened(); ok {
		t.Fatalf("a session was opened with a %d-byte command; the bound must refuse, never truncate", len(got.command))
	}
}

// The largest command the bound admits still goes through untouched — the
// paired "and on a normal machine it succeeds" AGENTS.md asks for next to
// every "returns an error when...".
func TestSession_TheLongestAdmissibleCommandIsCarriedWhole(t *testing.T) {
	m := newFakeMaster(t)
	master, err := Open(m.path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = master.Close() }()

	cmd := strings.Repeat("x", MaxCommandLen-1)
	s, err := master.Session(SessionRequest{Command: cmd})
	if err != nil {
		t.Fatalf("Session with a command one byte under the bound: %v", err)
	}
	defer func() { _ = s.Close() }()

	got, ok := m.lastOpened()
	if !ok {
		t.Fatal("the master recorded no opened session")
	}
	if got.command != cmd {
		t.Fatalf("the master saw a %d-byte command, want the %d bytes submitted", len(got.command), len(cmd))
	}
}

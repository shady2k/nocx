package coordinator_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// --- doubles -------------------------------------------------------------

// fakeBackend stands in for the running WS server. It reports a fixed
// address and the token the transport minted for this launch.
type fakeBackend struct {
	addr  string
	token string
}

func (b fakeBackend) WSAddress() string { return b.addr }
func (b fakeBackend) WSToken() string   { return b.token }

// fixedPeer presents a chosen uid for every connection — the seam that lets
// a test ask what happens when the process on the other end is not us,
// which no test can arrange for real without a second account.
type fixedPeer struct {
	uid uint32
	err error
}

func (p fixedPeer) PeerUID(*net.UnixConn) (uint32, error) {
	if p.err != nil {
		return 0, p.err
	}
	return p.uid, nil
}

// fixedOwner presents a chosen owning uid for every path, for the same
// reason: an unprivileged test cannot create a directory owned by somebody
// else.
type fixedOwner struct {
	uid uint32
	err error
}

func (o fixedOwner) OwnerUID(string) (uint32, error) {
	if o.err != nil {
		return 0, o.err
	}
	return o.uid, nil
}

// capturingHandler keeps every record a logger emitted so a test can assert
// on what was written rather than on what somebody read.
type capturingHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Resolve())
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, b.String())
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.records...)
}

// --- helpers -------------------------------------------------------------

const (
	testVersion = "9.9.9"
	testCommit  = "cafebabe"
	testAddr    = "127.0.0.1:54321"
	//nolint:gosec // a fixture string the test compares against, not a credential
	testToken = "test-token-Q3JyYnh1bg"
)

func newConfig(t *testing.T, dir string) coordinator.Config {
	t.Helper()
	return coordinator.Config{
		Dir:     dir,
		Build:   coordinator.Build{Version: testVersion, Commit: testCommit},
		Backend: fakeBackend{addr: testAddr, token: testToken},
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// startServer starts a server on a short-lived directory and stops it when
// the test ends. Socket paths are bounded by sun_path, so the directory is
// deliberately short rather than a nested t.TempDir.
func startServer(t *testing.T, cfg coordinator.Config) *coordinator.Server {
	t.Helper()
	s, err := coordinator.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func shortDir(t *testing.T) string {
	t.Helper()
	// os.MkdirTemp under the system temp root keeps the resulting socket
	// path well inside sun_path on macOS, where a nested t.TempDir plus the
	// profile layout can exceed 104 bytes.
	dir, err := os.MkdirTemp("", "nocxco")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "run")
}

// ask sends one request over the socket and returns the single response line.
func ask(t *testing.T, path string, req coordinator.Request) coordinator.Response {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// A refusal is written and the connection closed before the client's
	// write necessarily lands, so an EPIPE here is not the failure — an
	// unreadable response is.
	_ = json.NewEncoder(conn).Encode(req)
	var resp coordinator.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func helloRequest() coordinator.Request {
	return coordinator.Request{
		Type: coordinator.RequestHello,
		Client: &coordinator.ClientIdentity{
			Version:  "0.0.1",
			Commit:   "deadbeef",
			Protocol: coordinator.ProtocolVersion,
		},
	}
}

// --- 1. the hello carries the four facts ---------------------------------

func TestHelloCarriesBuildProtocolAddressAndToken(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	resp := ask(t, s.SocketPath(), helloRequest())

	if resp.Error != "" {
		t.Fatalf("hello returned error %q", resp.Error)
	}
	if resp.Hello == nil {
		t.Fatal("hello response carried no payload")
	}
	if resp.Hello.Build.Version != testVersion {
		t.Errorf("build version = %q, want %q", resp.Hello.Build.Version, testVersion)
	}
	if resp.Hello.Build.Commit != testCommit {
		t.Errorf("build commit = %q, want %q", resp.Hello.Build.Commit, testCommit)
	}
	if resp.Hello.Protocol != coordinator.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", resp.Hello.Protocol, coordinator.ProtocolVersion)
	}
	if resp.Hello.WSAddress != testAddr {
		t.Errorf("ws address = %q, want %q", resp.Hello.WSAddress, testAddr)
	}
	// The point of the socket: the token the transport minted, not a copy
	// of one that travelled by another route.
	if resp.Hello.WSToken != testToken {
		t.Errorf("ws token = %q, want the minted %q", resp.Hello.WSToken, testToken)
	}
}

func TestSocketAndParentCarryTheDeclaredModes(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("parent dir mode = %o, want 700", got)
	}
	si, err := os.Lstat(s.SocketPath())
	if err != nil {
		t.Fatalf("lstat socket: %v", err)
	}
	if si.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket path is not a socket: mode %v", si.Mode())
	}
	if got := si.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
}

func TestSecondHelloOnTheSameConnectionIsAnswered(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(bufio.NewReader(conn))
	for i := range 2 {
		if err := enc.Encode(helloRequest()); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		var resp coordinator.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if resp.Hello == nil {
			t.Fatalf("response %d carried no hello: %+v", i, resp)
		}
	}
}

// --- 2. peer uid ---------------------------------------------------------

func TestForeignPeerUIDIsRefused(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	cfg.Peers = fixedPeer{uid: coordinator.SelfUID() + 1}
	s := startServer(t, cfg)

	resp := ask(t, s.SocketPath(), helloRequest())

	if resp.Hello != nil {
		t.Fatalf("a foreign uid was served a hello: %+v", resp.Hello)
	}
	if resp.Error == "" {
		t.Fatal("a foreign uid was refused with no reason")
	}
	if strings.Contains(resp.Error, testToken) {
		t.Fatal("the refusal leaked the token")
	}
}

func TestOwnPeerUIDIsServed(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	cfg.Peers = fixedPeer{uid: coordinator.SelfUID()}
	s := startServer(t, cfg)

	if resp := ask(t, s.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("our own uid was refused: %+v", resp)
	}
}

func TestPeerCredentialLookupFailureRefusesTheConnection(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	cfg.Peers = fixedPeer{err: errors.New("getsockopt: no such device")}
	s := startServer(t, cfg)

	resp := ask(t, s.SocketPath(), helloRequest())

	if resp.Hello != nil {
		t.Fatalf("an unidentifiable peer was served a hello: %+v", resp.Hello)
	}
	if resp.Error == "" {
		t.Fatal("an unidentifiable peer was refused with no reason")
	}
}

// SystemPeerCredentials is the half a double cannot check: on an ordinary
// machine the real lookup must report the uid we are running as.
func TestSystemPeerCredentialsReportsThisUser(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		t.Fatalf("dialled connection is a %T, not a unix conn", conn)
	}
	uid, err := coordinator.SystemPeerCredentials{}.PeerUID(unixConn)
	if err != nil {
		t.Fatalf("PeerUID: %v", err)
	}
	if uid != coordinator.SelfUID() {
		t.Errorf("PeerUID = %d, want %d", uid, coordinator.SelfUID())
	}
}

func TestSystemPeerCredentialsErrorsOnAClosedConnection(t *testing.T) {
	left, right, err := socketPair()
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	_ = right.Close()
	_ = left.Close()

	if _, err := (coordinator.SystemPeerCredentials{}).PeerUID(left); err == nil {
		t.Fatal("PeerUID on a closed connection returned no error")
	}
}

func socketPair() (*net.UnixConn, *net.UnixConn, error) {
	dir, err := os.MkdirTemp("", "nocxsp")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = l.Close()
		_ = os.RemoveAll(dir)
	}()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, acceptErr := l.Accept()
		ch <- res{c, acceptErr}
	}()
	client, err := net.Dial("unix", path)
	if err != nil {
		return nil, nil, err
	}
	r := <-ch
	if r.err != nil {
		return nil, nil, r.err
	}
	left, ok := client.(*net.UnixConn)
	if !ok {
		return nil, nil, fmt.Errorf("dialled a %T, not a unix conn", client)
	}
	right, ok := r.c.(*net.UnixConn)
	if !ok {
		return nil, nil, fmt.Errorf("accepted a %T, not a unix conn", r.c)
	}
	return left, right, nil
}

// --- 3. a symlinked socket path ------------------------------------------

func TestSymlinkedSocketPathIsRefusedAndTheTargetIsUntouched(t *testing.T) {
	dir := shortDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(filepath.Dir(dir), "target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	cfg := newConfig(t, dir)
	s, err := coordinator.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if linkErr := os.Symlink(target, s.SocketPath()); linkErr != nil {
		t.Fatalf("symlink: %v", linkErr)
	}

	err = s.Start()

	if !errors.Is(err, coordinator.ErrSymlinkPath) {
		t.Fatalf("Start on a symlinked path = %v, want ErrSymlinkPath", err)
	}
	got, readErr := os.ReadFile(target) //nolint:gosec // test-owned path
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(got) != "original" {
		t.Errorf("the symlink target was written: %q", got)
	}
	if fi, lerr := os.Lstat(s.SocketPath()); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced rather than refused (lstat err %v)", lerr)
	}
}

func TestNonSocketAtTheSocketPathIsRefused(t *testing.T) {
	dir := shortDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, err := coordinator.NewServer(newConfig(t, dir))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := os.WriteFile(s.SocketPath(), []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := s.Start(); !errors.Is(err, coordinator.ErrOccupiedPath) {
		t.Fatalf("Start over a regular file = %v, want ErrOccupiedPath", err)
	}
}

// A dead daemon leaves its socket file behind; a live one holds the lock, so
// the file alone must not stop the next start.
func TestStaleSocketFromADeadDaemonIsReplaced(t *testing.T) {
	dir := shortDir(t)
	first := startServer(t, newConfig(t, dir))
	path := first.SocketPath()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close unlinks; put a stale socket back the way a SIGKILLed daemon
	// would have left one.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("stage stale socket: %v", err)
	}
	ul, ok := l.(*net.UnixListener)
	if !ok {
		t.Fatalf("staged listener is a %T", l)
	}
	ul.SetUnlinkOnClose(false)
	_ = l.Close()

	second := startServer(t, newConfig(t, dir))
	if resp := ask(t, second.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("the replacement socket did not serve: %+v", resp)
	}
}

// --- 4. the parent directory's owner --------------------------------------

func TestForeignOwnedParentDirectoryIsRefused(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	cfg.Owner = fixedOwner{uid: coordinator.SelfUID() + 1}
	s, err := coordinator.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if err := s.Start(); !errors.Is(err, coordinator.ErrForeignOwner) {
		t.Fatalf("Start under a foreign owner = %v, want ErrForeignOwner", err)
	}
	if _, statErr := os.Lstat(s.SocketPath()); !os.IsNotExist(statErr) {
		t.Errorf("a socket was bound under a foreign owner (lstat err %v)", statErr)
	}
}

func TestOwnerLookupFailureRefusesTheStart(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	cfg.Owner = fixedOwner{err: errors.New("stat: permission denied")}
	s, err := coordinator.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded although the owner could not be established")
	}
}

func TestSystemPathOwnerReportsThisUserForADirectoryWeMade(t *testing.T) {
	dir := t.TempDir()
	uid, err := coordinator.SystemPathOwner{}.OwnerUID(dir)
	if err != nil {
		t.Fatalf("OwnerUID: %v", err)
	}
	if uid != coordinator.SelfUID() {
		t.Errorf("OwnerUID = %d, want %d", uid, coordinator.SelfUID())
	}
}

func TestSystemPathOwnerErrorsOnAMissingPath(t *testing.T) {
	if _, err := (coordinator.SystemPathOwner{}).OwnerUID(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("OwnerUID on a missing path returned no error")
	}
}

// --- 5. exactly one server per app directory ------------------------------

func TestSecondServerAgainstTheSameDirectoryRefuses(t *testing.T) {
	dir := shortDir(t)
	first := startServer(t, newConfig(t, dir))

	second, err := coordinator.NewServer(newConfig(t, dir))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	err = second.Start()

	if !errors.Is(err, coordinator.ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}
	// The first one is still the one serving — the refusal must not have
	// unbound it on its way out.
	if resp := ask(t, first.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("the incumbent stopped serving: %+v", resp)
	}
}

func TestTheLockIsReleasedWhenTheFirstServerCloses(t *testing.T) {
	dir := shortDir(t)
	first := startServer(t, newConfig(t, dir))
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := startServer(t, newConfig(t, dir))
	if resp := ask(t, second.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("the successor did not serve: %+v", resp)
	}
}

// The in-process test above exercises flock across two open file
// descriptions, which is what the kernel arbitrates. This one crosses a real
// process boundary, because that is the thing the daemon actually faces.
func TestSecondProcessAgainstTheSameDirectoryRefuses(t *testing.T) {
	if os.Getenv("NOCX_COORD_LOCK_CHILD") == "1" {
		runLockChild()
		return
	}
	dir := shortDir(t)
	first := startServer(t, newConfig(t, dir))
	_ = first

	//nolint:gosec // os.Args[0] is this test binary; the arguments are constants
	cmd := exec.Command(os.Args[0], "-test.run=TestSecondProcessAgainstTheSameDirectoryRefuses", "-test.v=false")
	cmd.Env = append(os.Environ(), "NOCX_COORD_LOCK_CHILD=1", "NOCX_COORD_LOCK_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child process started a second daemon; output:\n%s", out)
	}
	if !strings.Contains(string(out), "ALREADY_RUNNING") {
		t.Fatalf("the child failed for some other reason:\n%s", out)
	}
}

func runLockChild() {
	dir := os.Getenv("NOCX_COORD_LOCK_DIR")
	s, err := coordinator.NewServer(coordinator.Config{
		Dir:     dir,
		Build:   coordinator.Build{Version: testVersion, Commit: testCommit},
		Backend: fakeBackend{addr: testAddr, token: testToken},
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		fmt.Println("NEW_SERVER_FAILED", err)
		os.Exit(3)
	}
	err = s.Start()
	if errors.Is(err, coordinator.ErrAlreadyRunning) {
		fmt.Println("ALREADY_RUNNING")
		os.Exit(2)
	}
	fmt.Println("UNEXPECTED", err)
	_ = s.Close()
	os.Exit(4)
}

// --- 6. the token is not written anywhere ---------------------------------

func TestTokenAppearsInNoLogLine(t *testing.T) {
	dir := shortDir(t)
	logs := &capturingHandler{}
	cfg := newConfig(t, dir)
	cfg.Logger = slog.New(logs)
	s := startServer(t, cfg)

	if resp := ask(t, s.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("hello failed: %+v", resp)
	}
	// A refusal and a malformed request are the paths most likely to echo
	// what they were given, so exercise them before reading the log.
	_ = ask(t, s.SocketPath(), coordinator.Request{Type: "nonsense"})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := logs.all()
	if len(lines) == 0 {
		t.Fatal("the server logged nothing at all during a normal start")
	}
	for _, line := range lines {
		if strings.Contains(line, testToken) {
			t.Fatalf("the token appeared in a log line: %s", line)
		}
	}
}

func TestNothingButTheSocketAndTheLockIsWritten(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))
	if resp := ask(t, s.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("hello failed: %+v", resp)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // test-owned dir
		if rerr == nil && strings.Contains(string(b), testToken) {
			t.Fatalf("the token was written to %s", e.Name())
		}
	}
	if len(names) != 2 {
		t.Errorf("runtime dir holds %v, want exactly the socket and the lock", names)
	}
}

// --- 7. the remaining failure paths ---------------------------------------

func TestStartRefusesWhenTheRuntimeDirectoryCannotBeCreated(t *testing.T) {
	base, err := os.MkdirTemp("", "nocxco")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	// A regular file where the runtime directory must go.
	blocker := filepath.Join(base, "run")
	if writeErr := os.WriteFile(blocker, []byte("in the way"), 0o600); writeErr != nil {
		t.Fatalf("write blocker: %v", writeErr)
	}
	s, err := coordinator.NewServer(newConfig(t, blocker))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded although the runtime directory could not be created")
	}
}

func TestStartRefusesAnOverlongSocketPath(t *testing.T) {
	base, err := os.MkdirTemp("", "nocxco")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	deep := filepath.Join(base, strings.Repeat("d", 60), strings.Repeat("e", 60))
	s, err := coordinator.NewServer(newConfig(t, deep))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if err := s.Start(); !errors.Is(err, coordinator.ErrPathTooLong) {
		t.Fatalf("Start on an overlong path = %v, want ErrPathTooLong", err)
	}
}

func TestMalformedRequestIsAnsweredWithAnError(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp coordinator.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("a malformed request was not refused: %+v", resp)
	}
	if resp.Hello != nil {
		t.Fatalf("a malformed request was served a hello: %+v", resp.Hello)
	}
}

func TestUnknownRequestTypeIsAnsweredWithAnError(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	resp := ask(t, s.SocketPath(), coordinator.Request{Type: "detach-everything"})

	if resp.Error == "" {
		t.Fatalf("an unknown request type was not refused: %+v", resp)
	}
	if resp.Hello != nil {
		t.Fatalf("an unknown request type was served a hello: %+v", resp.Hello)
	}
}

// A client that hangs up mid-exchange makes the server's write fail. It must
// cost that connection and nothing else.
func TestAClientThatHangsUpDoesNotStopTheServer(t *testing.T) {
	dir := shortDir(t)
	s := startServer(t, newConfig(t, dir))

	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(helloRequest()); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_ = conn.Close()

	if resp := ask(t, s.SocketPath(), helloRequest()); resp.Hello == nil {
		t.Fatalf("the server stopped serving after a hangup: %+v", resp)
	}
}

func TestCloseIsIdempotentAndUnlinksTheSocket(t *testing.T) {
	dir := shortDir(t)
	cfg := newConfig(t, dir)
	s, err := coordinator.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Lstat(s.SocketPath()); !os.IsNotExist(err) {
		t.Errorf("the socket outlived Close (lstat err %v)", err)
	}
}

func TestNewServerRefusesAnIncompleteConfiguration(t *testing.T) {
	full := newConfig(t, shortDir(t))
	for name, mangle := range map[string]func(*coordinator.Config){
		"no dir":      func(c *coordinator.Config) { c.Dir = "" },
		"no backend":  func(c *coordinator.Config) { c.Backend = nil },
		"no peers":    func(c *coordinator.Config) { c.Peers = nil },
		"no owner":    func(c *coordinator.Config) { c.Owner = nil },
		"no logger":   func(c *coordinator.Config) { c.Logger = nil },
		"no relative": func(c *coordinator.Config) { c.Dir = "relative/run" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := full
			mangle(&cfg)
			if _, err := coordinator.NewServer(cfg); err == nil {
				t.Fatalf("NewServer accepted a config with %s", name)
			}
		})
	}
}

// --- the app directory is the one the build owns --------------------------

func TestRuntimeDirLivesUnderTheProfileThisBuildOwns(t *testing.T) {
	root := storagetest.Isolate(t)
	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatalf("NewAppPaths: %v", err)
	}

	got := coordinator.RuntimeDir(paths)

	if !strings.HasPrefix(got, paths.DataDir()) {
		t.Errorf("RuntimeDir = %q, want it under the data dir %q", got, paths.DataDir())
	}
	if !strings.HasPrefix(got, root) {
		t.Errorf("RuntimeDir = %q escaped the isolated root %q", got, root)
	}
	if !strings.Contains(got, storage.AppDirName) {
		t.Errorf("RuntimeDir = %q does not name the profile %q", got, storage.AppDirName)
	}
}

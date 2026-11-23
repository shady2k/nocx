package app

// Hosted shell carrier fixtures and tests. The farhelper registry owns the
// separate client Git consumes; these tests cover shell installation, lane
// identity, inventory, reconnect and uninstall without constructing a Git binding.
// The wire meets the real helper host and services over io.Pipe; only SSH and
// the remote filesystem are doubles.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
)

func TestBridgeLifecycleCarriesOpaqueBytesAndCloses(t *testing.T) {
	peer, peerRemote := net.Pipe()
	carrier, carrierRemote := net.Pipe()
	t.Cleanup(func() {
		_ = peer.Close()
		_ = peerRemote.Close()
		_ = carrier.Close()
		_ = carrierRemote.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	for _, conn := range []net.Conn{peer, peerRemote, carrier, carrierRemote} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	bridgeLifecycle(nil, lifecycle.TransportID("tpt-test"), peer, carrier, nil, nil)

	const outbound = "opaque lifecycle bytes"
	writeDone := make(chan error, 1)
	go func() {
		_, err := peerRemote.Write([]byte(outbound))
		writeDone <- err
	}()
	got := make([]byte, len(outbound))
	if _, err := io.ReadFull(carrierRemote, got); err != nil {
		t.Fatalf("read toward carrier: %v", err)
	}
	if string(got) != outbound {
		t.Fatalf("carrier got %q, want %q", got, outbound)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write toward carrier: %v", err)
	}

	const inbound = "opaque response bytes"
	writeDone = make(chan error, 1)
	go func() {
		_, err := carrierRemote.Write([]byte(inbound))
		writeDone <- err
	}()
	got = make([]byte, len(inbound))
	if _, err := io.ReadFull(peerRemote, got); err != nil {
		t.Fatalf("read toward peer: %v", err)
	}
	if string(got) != inbound {
		t.Fatalf("peer got %q, want %q", got, inbound)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write toward peer: %v", err)
	}

	_ = carrierRemote.Close()
	buf := make([]byte, 1)
	if _, err := peerRemote.Read(buf); err == nil {
		t.Fatal("peer remained open after carrier closed")
	}
}

// fixtureRepo builds a real repository with a commit, a modified file and
// an untracked file, so a successful open is non-trivial. The recipe is
// the one internal/git/helper's test uses, inline.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	home := t.TempDir()
	cfg := "[user]\n\tname = Test User\n\temail = test@nocx.invalid\n[init]\n\tdefaultBranch = master\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + filepath.Dir(gitBin) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"LANG=C",
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...) // #nosec G204 — gitBin is LookPath-resolved; args are fixed test literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@nocx.invalid")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeLaneConn is a HelperConn backed by io.Pipe whose peer is the REAL
// helper host — the client side of the wire meets the actual
// implementation, in process, no SSH. It records Close so a test can prove
// the factory released the lane.
type fakeLaneConn struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.Reader

	exited   chan struct{}
	exitCode int

	startErr error

	mu     sync.Mutex
	closed int

	// openedBy is the test that asked this stand for a lane, NAMED by the
	// fixture that owns the stand rather than read from whoever opens it. A
	// lane is opened on the server's goroutine when a test drives the open
	// over the socket, and that stack holds no test's frames — which is why
	// the stack-walking answer was "?" for exactly the leaks this report
	// exists to name (nocx-y3kfq).
	openedBy string
}

// laneRegistry is every fake lane whose helper peer has NOT returned yet. A
// lane is forgotten when its peer ends — its pipes are gone with it — so what
// remains is exactly what an end-of-run report needs, and a run with
// -count=N does not accumulate one entry per lane it ever opened
// (nocx-y3kfq).
type laneRegistry struct {
	mu    sync.Mutex
	conns []*fakeLaneConn
}

// laneConns is the package's one registry. The reports at the end of the run
// read it to say WHICH test left a lane open instead of only which shape is
// still parked.
var laneConns laneRegistry

func (r *laneRegistry) add(f *fakeLaneConn) {
	r.mu.Lock()
	r.conns = append(r.conns, f)
	r.mu.Unlock()
}

// forget drops a lane whose peer has returned: both halves of its wire are
// gone by then, so it cannot be the leak the report looks for.
func (r *laneRegistry) forget(f *fakeLaneConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.conns {
		if c == f {
			// slices.Delete, not append: the append form leaves the removed
			// lane's pointer in the array's last slot, so the registry keeps a
			// closed lane and both its pipes alive past their end.
			r.conns = slices.Delete(r.conns, i, i+1)
			return
		}
	}
}

// liveLanes is a snapshot of the lanes still in flight.
func (r *laneRegistry) liveLanes() []*fakeLaneConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*fakeLaneConn(nil), r.conns...)
}

// newFakeLaneConn makes a lane for the test its owner names. owner is
// required: attribution read from the creating goroutine's stack works only
// where a test's own frame is in it, and a lane is opened on the server's
// goroutine whenever an open arrives over the socket (nocx-y3kfq).
func newFakeLaneConn(owner string, peer func(stdin io.Reader, stdout io.Writer) int) *fakeLaneConn {
	if owner == "" {
		panic("newFakeLaneConn: a fake lane must name the test that owns it")
	}
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeLaneConn{
		stdin:    toPeerW,
		stdout:   fromPeerR,
		stderr:   bytes.NewReader(nil),
		exited:   make(chan struct{}),
		openedBy: owner,
	}
	laneConns.add(f)
	go func() {
		code := peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		f.exitCode = code
		close(f.exited)
		laneConns.forget(f)
	}()
	return f
}

// leakLine is one line of the leak report: the lane whose helper peer is still
// running, the test that opened it, and how many times it was closed. The last
// two separate "opened and never closed" from "closed, but the peer never
// returned".
func (f *fakeLaneConn) leakLine() string {
	return fmt.Sprintf("  a fake lane opened by %s never ended: it was closed %d time(s), "+
		"and its helper peer is still running", f.openedBy, f.closeCount())
}

// laneLeakReport is the leak report's lane half, one line per fake lane whose
// helper peer is still running. The registry forgets a lane when its peer
// returns, so every line here is a lane somebody opened and did not end.
func laneLeakReport() []string {
	left := laneConns.liveLanes()
	lines := make([]string, 0, len(left))
	for _, c := range left {
		select {
		case <-c.exited:
			// Its peer returned: the registry forgets it one statement later,
			// and a lane whose peer is gone is not a leak.
			continue
		default:
		}
		lines = append(lines, c.leakLine())
	}
	return lines
}

func (f *fakeLaneConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeLaneConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeLaneConn) Stderr() io.Reader     { return f.stderr }

// Start answers the one thing a helper-opened lane can be asked: whether the
// exec it already performed failed. A COMMAND is not part of it any more —
// client.LaneConn refuses one — so the fake records nothing, and its startErr
// stands in for "the helper could not open the lane at all", which is the state
// it was always modelling.
func (f *fakeLaneConn) Start(string) error { return f.startErr }
func (f *fakeLaneConn) Wait() (int, error) { <-f.exited; return f.exitCode, nil }

// Done reports the lane's own end, and this fake IS the helper process: the
// connection dies when the process does, so Done is the same event Wait
// watches. A fresh channel that never closes models a lane nobody can lose,
// and the client's loss watcher (client.Dial's `<-c.conn.Done()` goroutine,
// one per lane) parked on it for the rest of the package's run — 179 of them
// in the package's own goroutine dump (nocx-1ebrs.66.1.29).
func (f *fakeLaneConn) Done() <-chan struct{} { return f.exited }

func (f *fakeLaneConn) LostErr() error { return nil }

// Close releases BOTH halves of the wire, which is what a real lane's Close
// does. Closing stdin alone leaves a peer parked in a write to the stdout pipe
// with nobody left to read it: an io.Pipe write blocks until somebody reads,
// so the peer never returned, Wait never returned, and the goroutine dump
// showed it "chan receive, 5 minutes" inside Wait while the test that opened
// the lane was long gone.
func (f *fakeLaneConn) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	stdinErr := f.stdin.Close()
	if outErr := f.stdout.Close(); stdinErr == nil {
		stdinErr = outErr
	}
	return stdinErr
}

// endSessions detaches every session reg holds, in the order the map gives:
// Reg.Close is the product's own word for a coordinator ending, so a stand
// that uses it models a process exit rather than leaking a write loop.
func endSessions(reg *session.Reg) {
	if reg == nil {
		return
	}
	for _, s := range reg.List() {
		_ = reg.Close(s.ID())
	}
}

// endSessionsOnCleanup detaches every session reg holds when the test ends.
// It is endSessions at the one moment a stand's process exit is: the test's
// own end (nocx-1ebrs.66.1.29).
func endSessionsOnCleanup(t *testing.T, reg *session.Reg) {
	t.Helper()
	t.Cleanup(func() { endSessions(reg) })
}

// endHelperChannels closes every helper channel reg holds. A method was not
// added to helperRegistry because this IS what a coordinator's process exit
// does and nothing in the product needs it: the registry is closed from the
// composition root's shutdown, which walks its own hosts.
func endHelperChannels(reg *helperRegistry) {
	if reg == nil {
		return
	}
	reg.mu.Lock()
	helpers := make([]*hostedShellCarrier, 0, len(reg.hosts))
	for _, h := range reg.hosts {
		helpers = append(helpers, h)
	}
	reg.mu.Unlock()
	for _, h := range helpers {
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
	}
}

// endStandOnCleanup ends a test stand the way its coordinator's process would:
// its sessions detach, the helper channels it holds close, and every lane it
// closed has its helper waited for. It is coordinator.quit in cleanup form,
// for the stands that are not built on a coordinator — without it those stands
// left a session's write loop and a lane's watchers running for the rest of
// the package's run (nocx-1ebrs.66.1.29).
func endStandOnCleanup(t *testing.T, provider *fakeLaneProvider, reg *helperRegistry) {
	t.Helper()
	t.Cleanup(func() {
		if reg != nil {
			endSessions(reg.registry)
		}
		endHelperChannels(reg)
		if provider != nil {
			provider.awaitClosedPeers(t)
		}
	})
}

func (f *fakeLaneConn) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// laneRequest is what the coordinator ASKED for, which is the whole of what
// this side names any more: the machine whose install to run and the
// generation of it. The command itself is the helper's to build (D3), and the
// app-level test asserts the two facts; internal/helper/sshsvc asserts what
// they turn into.
type laneRequest struct {
	machine    proto.Machine
	generation proto.GenerationID
	pin        string
}

// fakeLaneProvider hands out a fresh scripted lane per LaneConn call and
// records them, so a test can prove how many helpers were brought up. It
// also serves the install surface the selection needs: a scripted probe
// answer and an in-memory install lease, so the REAL deploy.Ensure runs
// against a fake transport — the wiring under test is the production
// wiring, only the SSH is fake (and, since nocx-50w7p.10, only the LANE the
// helper would have opened: this double stands in for this machine's helper,
// not for the coordinator's own dial).
type fakeLaneProvider struct {
	// owner is the test this stand answers for, named where the stand is made
	// so a lane opened on the server's goroutine is still attributed to it
	// (nocx-y3kfq).
	owner    string
	peer     func(in io.Reader, out io.Writer) int
	startErr error

	laneErr error // when set, every LaneConn fails: an unreachable host
	// laneBlock, when set, parks every LaneConn on it (or on the caller's
	// context): a host that accepts nothing and refuses nothing, which is what
	// a machine behind a black-holing firewall looks like.
	laneBlock chan struct{}
	uname     string // the probe's canned answer; default "Linux x86_64"
	// fingerprint is the host key the platform probe reports; default
	// "SHA256:fake". A test about the identity a real it dial observed sets it
	// to the fingerprint its own known_hosts entry answers.
	fingerprint string
	home        string // the install lease's home; default "/home/u"
	probeFail   error  // when set, DiscoveryConn fails
	installFail error  // when set, HelperInstallConn fails
	installPin  string
	// inflate, when set, is the install's decompression seam; nil refuses it, so
	// every test that is not about the optimisation installs the plain way
	// (nocx-evj4b.1.3).
	inflate func(path string) error

	mu      sync.Mutex
	conns   []*fakeLaneConn
	request []laneRequest

	install *fakeInstallConn
}

// awaitClosedPeers waits until every lane somebody has closed has had its
// helper end — the moment the helper's connection handler has returned and
// released what that connection held, which a real host reaches when the
// process on the other end of the lane is gone.
func (p *fakeLaneProvider) awaitClosedPeers(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	conns := append([]*fakeLaneConn(nil), p.conns...)
	p.mu.Unlock()
	for _, c := range conns {
		if c.closeCount() == 0 {
			continue
		}
		select {
		case <-c.exited:
		case <-time.After(30 * time.Second):
			// A HANG LIMIT, not a duration the assertion depends on — the
			// same 30 s bound the stands' shell wait uses, and it fires only
			// on a lane whose helper should have ended and did not
			// (nocx-1ebrs.66.1.29). t.Context() cannot be the bound here any
			// more: it is cancelled BEFORE the test's cleanups run, and a
			// stand now ends its coordinator in one.
			t.Errorf("a closed lane's helper never ended: 30 s after its lane closed, the peer is still running")
		}
	}
}

func (p *fakeLaneProvider) LaneConn(ctx context.Context, _ string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	var cfg ssh.ConnectConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if p.laneErr != nil {
		return nil, p.laneErr
	}
	if p.laneBlock != nil {
		select {
		case <-p.laneBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := newFakeLaneConn(p.owner, p.peer)
	c.startErr = p.startErr
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.request = append(p.request, laneRequest{machine: machine, generation: generation, pin: cfg.HostKeyFingerprint})
	p.mu.Unlock()
	return c, nil
}

// asked returns what the lane was asked for, for the one app-level assertion
// about it.
func (p *fakeLaneProvider) asked(i int) laneRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.request[i]
}

func (p *fakeLaneProvider) DiscoveryConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	if p.probeFail != nil {
		return nil, p.probeFail
	}
	uname := p.uname
	if uname == "" {
		uname = "Linux x86_64"
	}
	return &fakeProbeConn{uname: uname, fingerprint: p.fingerprint}, nil
}

func TestFakeLaneProviderConcurrentInstallLeasesShareOneFilesystem(t *testing.T) {
	const callers = 32
	provider := &fakeLaneProvider{owner: t.Name()}
	start := make(chan struct{})
	leases := make(chan ssh.HelperInstallConn, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			lease, err := provider.HelperInstallConn(t.Context(), "host.example", ssh.WithHostKeyPin("SHA256:approved"))
			if err != nil {
				t.Errorf("install lease: %v", err)
				return
			}
			leases <- lease
		})
	}
	close(start)
	wg.Wait()
	close(leases)
	for lease := range leases {
		if lease != provider.install {
			t.Error("concurrent install leases used different remote filesystems")
		}
	}
	if provider.installPin != "SHA256:approved" {
		t.Errorf("install pin = %q, want approved host key", provider.installPin)
	}
}

func (p *fakeLaneProvider) HelperInstallConn(_ context.Context, _ string, opts ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
	var cfg ssh.ConnectConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installPin = cfg.HostKeyFingerprint
	if p.installFail != nil {
		return nil, p.installFail
	}
	home := p.home
	if home == "" {
		home = "/home/u"
	}
	if p.install == nil {
		p.install = newFakeInstallConn(home)
	}
	return p.install, nil
}

// HelperInflate is the install's decompression seam. The double refuses it,
// which is the one behaviour every test here wants: the install falls back to
// the plain upload, so a test that is not about the optimisation measures the
// install it always measured (nocx-evj4b.1.3). A test that IS about it sets
// inflate.
func (p *fakeLaneProvider) HelperInflate(_ context.Context, _ string, path string, _ ...ssh.ConnectOption) error {
	if p.inflate != nil {
		return p.inflate(path)
	}
	return errors.New("no inflate on this double")
}

func (p *fakeLaneProvider) laneCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *fakeLaneProvider) lane(i int) *fakeLaneConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[i]
}

// fakeProbeConn is a DiscoveryConn that answers the platform probe with a
// canned uname line — the only exec the selection runs.
type fakeProbeConn struct {
	uname       string
	fingerprint string // empty means the stand's usual "SHA256:fake"
}

func (f *fakeProbeConn) Exec(_ context.Context, _ string) (*ssh.ExecResult, error) {
	return &ssh.ExecResult{Stdout: []byte(f.uname)}, nil
}
func (f *fakeProbeConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeProbeConn) LostErr() error        { return nil }
func (f *fakeProbeConn) Close() error          { return nil }
func (f *fakeProbeConn) HostKeyFingerprint() string {
	if f.fingerprint != "" {
		return f.fingerprint
	}
	return "SHA256:fake"
}

// fakeInstallConn is a HelperInstallConn whose FS is an in-memory map. The
// REAL deploy.Ensure runs against it, so the selection's install path is
// the production one; only the transport is fake. uploads counts binary
// writes so a test can assert D7's uploads-nothing-on-complete without
// timing.
type fakeInstallConn struct {
	mu      sync.Mutex
	dirs    map[string]bool
	files   map[string][]byte
	modes   map[string]iofs.FileMode
	home    string
	uploads int

	// holdWrite, when non-nil, parks the FIRST payload write until it is
	// closed, and writeStarted is closed when that write parks. Together they
	// are a link moving a binary slowly enough for a person to look at the
	// Operations row: the install is provably mid-transfer while the test
	// asserts, and nothing waits on a clock (nocx-3an1s.2.10). Both are nil
	// for an ordinary instant install.
	holdWrite    chan struct{}
	writeStarted chan struct{}
}

func newFakeInstallConn(home string) *fakeInstallConn {
	return &fakeInstallConn{
		dirs:  map[string]bool{"/": true},
		files: map[string][]byte{},
		modes: map[string]iofs.FileMode{},
		home:  home,
	}
}

// hasCompleteInstall reports whether SOME install directory under the
// helper root carries the completion marker — the marker lives inside the
// versioned install directory, whose name the test does not need to know.
func (f *fakeInstallConn) hasCompleteInstall(home string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := path.Join(home, ".nocx", "helper") + "/"
	for p := range f.files {
		if path.Base(p) == ".install-complete" && strings.HasPrefix(p, root) {
			return true
		}
	}
	return false
}

func (f *fakeInstallConn) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads
}

func (f *fakeInstallConn) Lstat(p string) (iofs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[p] {
		return fakeInstallInfo{name: path.Base(p), dir: true}, nil
	}
	if data, ok := f.files[p]; ok {
		return fakeInstallInfo{name: path.Base(p), size: int64(len(data))}, nil
	}
	return nil, iofs.ErrNotExist
}

func (f *fakeInstallConn) Mkdir(p string, mode os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[p] {
		return iofs.ErrExist
	}
	if _, ok := f.files[p]; ok {
		return errors.New("fakeinstall: not a directory")
	}
	if parent := path.Dir(p); !f.dirs[parent] {
		return iofs.ErrNotExist
	}
	f.dirs[p] = true
	return nil
}

func (f *fakeInstallConn) Create(p string, mode os.FileMode) (ssh.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirs[path.Dir(p)] {
		return nil, iofs.ErrNotExist
	}
	if path.Base(p) != ".install-complete" {
		f.uploads++
	}
	return &fakeInstallFile{fs: f, path: p}, nil
}

func (f *fakeInstallConn) SyncDir(string) error { return nil }

func (f *fakeInstallConn) Rename(src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.files[src]; ok {
		delete(f.files, src)
		f.files[dst] = data
		return nil
	}
	if f.dirs[src] {
		delete(f.dirs, src)
		f.dirs[dst] = true
		return nil
	}
	return iofs.ErrNotExist
}

func (f *fakeInstallConn) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[p]; ok {
		delete(f.files, p)
		return nil
	}
	if f.dirs[p] {
		delete(f.dirs, p)
		return nil
	}
	return iofs.ErrNotExist
}

func (f *fakeInstallConn) ReadDir(dir string) ([]iofs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirs[dir] {
		return nil, iofs.ErrNotExist
	}
	var out []iofs.FileInfo
	for p := range f.dirs {
		if path.Dir(p) == dir && p != dir {
			out = append(out, fakeInstallInfo{name: path.Base(p), dir: true})
		}
	}
	for p := range f.files {
		if path.Dir(p) == dir {
			out = append(out, fakeInstallInfo{name: path.Base(p), size: int64(len(f.files[p]))})
		}
	}
	return out, nil
}

func (f *fakeInstallConn) Sync(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[p]; !ok {
		return iofs.ErrNotExist
	}
	return nil
}

func (f *fakeInstallConn) Chmod(p string, mode iofs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modes[p] = mode
	return nil
}

func (f *fakeInstallConn) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[p]
	if !ok {
		return nil, iofs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeInstallConn) Home() (string, error) { return f.home, nil }
func (f *fakeInstallConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeInstallConn) LostErr() error        { return nil }

func (f *fakeInstallConn) Close() error {
	return nil
}

// fakeInstallFile is the lease's File handle; content becomes visible on
// Close, like the real adapter's write boundary.
type fakeInstallFile struct {
	fs   *fakeInstallConn
	path string
	buf  []byte
}

func (ff *fakeInstallFile) Write(p []byte) (int, error) {
	ff.fs.mu.Lock()
	gate := ff.fs.holdWrite
	started := ff.fs.writeStarted
	ff.fs.holdWrite = nil
	ff.fs.mu.Unlock()
	if gate != nil {
		if started != nil {
			close(started)
		}
		<-gate
	}
	ff.fs.mu.Lock()
	defer ff.fs.mu.Unlock()
	ff.buf = append(ff.buf, p...)
	return len(p), nil
}

func (ff *fakeInstallFile) Sync() error { return nil }

func (ff *fakeInstallFile) Close() error {
	ff.fs.mu.Lock()
	defer ff.fs.mu.Unlock()
	ff.fs.files[ff.path] = ff.buf
	return nil
}

type fakeInstallInfo struct {
	name string
	size int64
	dir  bool
}

func (fi fakeInstallInfo) Name() string       { return fi.name }
func (fi fakeInstallInfo) Size() int64        { return fi.size }
func (fi fakeInstallInfo) Mode() os.FileMode  { return 0o600 }
func (fi fakeInstallInfo) ModTime() time.Time { return time.Time{} }
func (fi fakeInstallInfo) IsDir() bool        { return fi.dir }
func (fi fakeInstallInfo) Sys() any           { return nil }

// realHelperPeer serves the REAL helper host with the REAL git service. The
// reported content hash is syntheticArtifact's: the in-process helper
// stands in for the installed binary, and the stub artifact source is what
// the selection installed — the client's D21 verification is the real one,
// and it must not depend on the embedded binaries existing.
func realHelperPeer(t testing.TB) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(localgit.NewFactory()))
		sessions := helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(contentHash),
		})
		// The peer IS this test's helper process, so its sessions end with the
		// connection that served them (nocx-1ebrs.66.1.29).
		defer sessions.Close()
		h.Register(sessions)
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// helperPeerWithoutSession serves the git surface but omits the session
// service. Uninstall must refuse when this daemon cannot enumerate its live
// sessions; treating the unknown service as an empty inventory would remove a
// live helper executable without first closing its process.
func helperPeerWithoutSession(t testing.TB) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(localgit.NewFactory()))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// fakeRemoteSession is a session.Session whose only interesting facts are
// the id, kind, host, fingerprint and SSH options the helper selection reads.
type fakeRemoteSession struct {
	id          session.ID
	host        string
	fingerprint string
}

func (s *fakeRemoteSession) ID() session.ID     { return s.id }
func (s *fakeRemoteSession) Kind() session.Kind { return session.KindRemote }
func (s *fakeRemoteSession) Host() string       { return s.host }
func (s *fakeRemoteSession) Cwd() string        { return "" }
func (s *fakeRemoteSession) ProfileID() string  { return "" }
func (s *fakeRemoteSession) CredentialID() string {
	return ""
}
func (s *fakeRemoteSession) Write([]byte) (int, error) { return 0, nil }
func (s *fakeRemoteSession) EnqueueWrite([]byte) bool  { return false }

// EnqueueInputIf answers "queued" and settles at once: this double has no
// queue to validate against.
func (s *fakeRemoteSession) EnqueueInputIf(p []byte, holds func() bool, settle func(written bool, err error)) bool {
	if holds != nil && !holds() {
		if settle != nil {
			settle(false, nil)
		}
		return true
	}
	if settle != nil {
		settle(true, nil)
	}
	return true
}
func (s *fakeRemoteSession) EffectiveSize() session.Size { return session.DefaultSize() }

func (s *fakeRemoteSession) Resize(context.Context, session.Size) error {
	return nil
}

// The pipe this fake never opened. A zero time is the honest answer for a
// double that has no pipe, and it is what the other session doubles in this
// package return (fs_provider_factory_test.go).
func (s *fakeRemoteSession) OpenedAt() time.Time { return time.Time{} }

func (s *fakeRemoteSession) Close() error { return nil }
func (s *fakeRemoteSession) Done() <-chan struct{} {
	return make(chan struct{})
}

func (s *fakeRemoteSession) StartOutput(context.Context, session.OutputHandler) error {
	return nil
}
func (s *fakeRemoteSession) ShellIntegrationReason() ssh.RefusalReason { return ssh.ReasonNone }

// HostKeyFingerprint defaults to a stable test identity so a granted store
// can answer for the default session; a test that wants a specific machine
// sets the field.
func (s *fakeRemoteSession) HostKeyFingerprint() string {
	if s.fingerprint == "" {
		return "SHA256:test-host"
	}
	return s.fingerprint
}

// ExitOutcome: this fake never runs a process, so there is no reported exit
// to relay — an interrupted outcome with no fabricated status is the honest
// answer (internal/session's own rule).
func (s *fakeRemoteSession) ExitOutcome() (session.ExitCause, int) {
	return session.ExitInterrupted, 0
}

// The identity, lineage and liveness surface (epic A): this fake stands for
// a remote session in the helper's git tests and asserts nothing about who
// opened it, so it answers the zero incarnation, no parent, and alive.
func (s *fakeRemoteSession) Identity() session.Identity { return session.Identity{} }

func (s *fakeRemoteSession) Parent() (session.Ref, bool) {
	return session.Ref{}, false
}

func (s *fakeRemoteSession) PaneID() string { return "" }

func (s *fakeRemoteSession) OpenBootstrapWindow() (session.BootstrapWindow, error) {
	return nil, errors.New("fakeRemoteSession has no terminal")
}

// SSHOptions answers none: a saved connection carries no integration method
// any more (ADR-727g63tr decision 1), so there is nothing here for the
// selection to read.
func (s *fakeRemoteSession) SSHOptions() []ssh.ConnectOption { return nil }

// discardLogger used to be a bare io.Discard sink shared by every caller, so
// a failure showed only the assertion that tripped and none of the debug
// lines that led to it (nocx-n14oo.10). It is now logtest.Slog(t): a buffer
// private to the test that asked for it, dumped by t.Cleanup only when that
// test fails.
func discardLogger(t testing.TB) *slog.Logger {
	return logtest.Slog(t)
}

// syntheticPayload is the stand-in for the embedded helper: real gzip
// bytes whose decompressed content hashes to syntheticHash. The selector
// tests exercise the wiring — the selection reaching install, computing
// the D7 path and hash, wiring the factory — none of which needs a genuine
// 4 MB binary, and all of which must run identically whether or not `make
// helpers` has run (CI's fresh checkout has no embedded artifacts).
var syntheticPayload = []byte("fake nocx-helper for selector wiring tests\n")

// syntheticArtifactCompressed and syntheticArtifactHash are the stand-in
// artifact: real gzip bytes whose decompressed content hashes to the hash.
// Built once at package init from syntheticPayload.
var (
	syntheticArtifactCompressed []byte
	syntheticArtifactHash       string
)

func init() {
	syntheticArtifactCompressed, syntheticArtifactHash = makeSyntheticArtifact()
}

func makeSyntheticArtifact() (compressed []byte, contentHash string) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(syntheticPayload); err != nil {
		panic("syntheticArtifact: " + err.Error())
	}
	if err := zw.Close(); err != nil {
		panic("syntheticArtifact: " + err.Error())
	}
	sum := sha256.Sum256(syntheticPayload)
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// syntheticSource is the app tests' ArtifactSource: it serves the synthetic
// bytes built above. The selection under test is the composition root, which
// receives this source explicitly rather than mutating deploy package state.
type syntheticSource struct{}

func (syntheticSource) Artifact(_ deploy.Platform) ([]byte, string, error) {
	return syntheticArtifactCompressed, syntheticArtifactHash, nil
}

// refusingSource is an ArtifactSource that answers ErrArtifactsNotBuilt —
// the state of a fresh checkout before `make helpers`.
type refusingSource struct{}

func (refusingSource) Artifact(_ deploy.Platform) ([]byte, string, error) {
	return nil, "", helperartifacts.ErrArtifactsNotBuilt
}

func stubArtifacts(t *testing.T) deploy.ArtifactSource {
	t.Helper()
	return syntheticSource{}
}

// testConsentStores builds the consent store (pre-granted for the default
// test machine) and the install-observation store the selection writes.
// The grant is seeded as the document the accept-write path (nocx-1xxa)
// persists — this bead owns no writer for it.
func testConsentStores(t *testing.T) (*consent.Store, *consent.InstallStore) {
	t.Helper()
	// BOTH IDENTITIES ARE ANSWERED, because the default stand plays two: the
	// platform probe reports "SHA256:fake" (fakeProbeConn) and the git-lane
	// session reports "SHA256:test-host" (fakeRemoteSession). Before ADR-0105
	// only the second mattered — a helper-mode open never consulted the store
	// — and now an open whose machine has no answer is asked instead of
	// opened, so a stand built for the helper has to have answered for the
	// machine its probe names.
	return testConsentStoresFor(t, "SHA256:fake", "SHA256:test-host")
}

// testConsentStoresFor is the same pair with the caller's own machine answers,
// for a stand whose identities the caller knows: a fixture over a REAL host
// reports that host's key, which no canned fingerprint can predict, so the
// caller passes what its own probe will report (ADR-0105 — a helper-mode open
// reads this store, so a stand that opens the helper has to have answered for
// the machine its probe names).
func testConsentStoresFor(t *testing.T, fingerprints ...string) (*consent.Store, *consent.InstallStore) {
	t.Helper()
	logger := log.NewSlogAdapter(discardLogger(t))
	answers := make(map[string]consent.Answer, len(fingerprints))
	for _, fingerprint := range fingerprints {
		answers[fingerprint] = consent.Granted
	}
	store := seedAnswersDocument(t, t.TempDir(), answers)
	installs := consent.NewInstallStore(logger, storage.NewDocumentStore(t.TempDir()), "installs.json")
	return store, installs
}

// registerHostedCarrier installs and opens a shell carrier without a Git binding.
func registerHostedCarrier(t *testing.T, reg *helperRegistry, sid session.ID, hostName, fingerprint string) *hostedShellCarrier {
	t.Helper()
	installed, err := installHelperAt(t.Context(), hostName, nil, reg.install, reg.source, deploy.Platform{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	f := &hostedShellBinding{reg: reg, sid: sid, host: hostName, fp: fingerprint, opts: []ssh.ConnectOption{ssh.WithHostKeyPin(fingerprint)}, install: installed}
	h := &hostedShellCarrier{f: f, lanes: reg.lanes, log: reg.log}
	reg.mu.Lock()
	reg.hosts[sid] = h
	reg.mu.Unlock()
	h.mu.Lock()
	_, outcome, err := h.connectLocked(t.Context())
	h.mu.Unlock()
	if err != nil || outcome.State != "" {
		t.Fatalf("open hosted carrier: %v %+v", err, outcome)
	}
	return h
}

// Uninstall must close the named machine's carriers before its install can be
// removed (D25), without disturbing another machine. The verified host-key
// fingerprint, not a hostname or a Git binding, identifies the affected entries.
func TestHelperCloseHelpersForClosesOnlyTheMachine(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t)}
	store, _ := testConsentStores(t)
	reg := newHostedShellRegistry(provider, stubArtifacts(t), store, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	registerHostedCarrier(t, reg, "s1", "host.example", "SHA256:machine-a")
	registerHostedCarrier(t, reg, "s2", "host.other", "SHA256:machine-b")
	if err := reg.CloseHelpersFor(t.Context(), "SHA256:machine-a"); err != nil {
		t.Fatal(err)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("machine-a lane closes = %d, want 1", got)
	}
	if got := provider.lane(1).closeCount(); got != 0 {
		t.Fatalf("machine-b lane closes = %d, want 0", got)
	}
	if _, ok := reg.hostFor("s1"); ok {
		t.Fatal("closed carrier remains registered")
	}
	reg.FinishHelpersFor("SHA256:machine-a")
	if err := reg.CloseHelpersFor(t.Context(), "SHA256:nobody"); err != nil {
		t.Fatal(err)
	}
}

// An unknown session service is not an empty inventory: uninstall cannot know
// whether deleting the executable is safe. On refusal, every affected carrier
// must remain registered and unfrozen so a repaired daemon can be tried again.
func TestHelperCloseHelpersForRefusesWhenSessionsCannotBeEnumerated(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: helperPeerWithoutSession(t)}
	store, _ := testConsentStores(t)
	reg := newHostedShellRegistry(provider, stubArtifacts(t), store, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	for _, sid := range []session.ID{"s1", "s2"} {
		registerHostedCarrier(t, reg, sid, "host.example", "SHA256:test-host")
	}
	if err := reg.CloseHelpersFor(t.Context(), "SHA256:test-host"); err == nil {
		t.Fatal("uninstall continued without a session inventory")
	}
	reg.mu.Lock()
	_, closing := reg.closing["SHA256:test-host"]
	reg.mu.Unlock()
	if closing {
		t.Fatal("failed uninstall left closing state")
	}
	for _, sid := range []session.ID{"s1", "s2"} {
		h, ok := reg.hostFor(sid)
		if !ok {
			t.Fatalf("forgot carrier %s", sid)
		}
		h.mu.Lock()
		closing := h.closing
		h.mu.Unlock()
		if closing {
			t.Fatalf("carrier %s stayed frozen", sid)
		}
	}
	for i := range 2 {
		if got := provider.lane(i).closeCount(); got != 0 {
			t.Fatalf("lane %d closed %d times", i, got)
		}
	}
}

// A carrier can end while the daemon's sessions survive on the host (D1).
// From carrier loss until the next inventory answer the registry must retain
// the hosted entry, redial its lane, and use the replacement for later teardown.
func TestHelperSessionsRedialsAfterCarrierLoss(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
	store, _ := testConsentStores(t)
	reg := newHostedShellRegistry(provider, stubArtifacts(t), store, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	h := registerHostedCarrier(t, reg, "s1", "host.example", "SHA256:test-host")
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.sessions(t.Context()); err != nil {
		t.Fatalf("inventory after loss: %v", err)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("inventory used %d lanes, want 2", got)
	}
	if err := reg.CloseHelpersFor(t.Context(), "SHA256:test-host"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.hostFor("s1"); ok {
		t.Fatal("uninstall left carrier registered")
	}
}

// Hosted selection installs the artifact (D7); a repeat open of a complete
// install uploads nothing. The lane names the install DIRECTORY and generation,
// not an argv: the helper builds the command (D3). A wrong generation reaches a
// different build's sessions; a wrong directory names a binary nobody installed.
// Compare with deploy's layout so the test does not invent another install path.
func TestHostedOpenInstallsArtifactAndNamesItsGenerationOnTheLane(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
	c := newCoordinator(t, provider)
	first := openHostedFixture(t, c, "pane-1")
	if provider.install == nil || !provider.install.hasCompleteInstall("/home/u") {
		t.Fatal("hosted open did not complete the helper install")
	}
	before := provider.install.uploadCount()
	second := openHostedFixture(t, c, "pane-2")
	if got := provider.install.uploadCount() - before; got != 0 {
		t.Fatalf("second hosted open uploaded %d files, want 0", got)
	}
	if first.Generation != syntheticArtifactHash || second.Generation != first.Generation {
		t.Fatalf("opened generations %q, %q", first.Generation, second.Generation)
	}
	wantDir := path.Dir(deploy.InstalledPath("/home/u", deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, syntheticArtifactHash))
	for i := range 2 {
		asked := provider.asked(i)
		if string(asked.generation) != syntheticArtifactHash || asked.machine.Dir != wantDir {
			t.Fatalf("lane %d names %+v generation %q, want %q/%q", i, asked.machine, asked.generation, wantDir, syntheticArtifactHash)
		}
	}
}

// A destination with no available helper artifact must decline hosted delivery
// before taking an install lease or opening a lane. The ordinary opener can
// still serve the pane; declining is not permission to deploy another binary.
func TestHostedOpenRefusesUnavailableArtifactWithoutInstalling(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name()}
	c := newCoordinator(t, provider)
	c.reg.source = refusingSource{}
	_, selected, err := c.reg.OpenHosted(t.Context(), remoteConfigFor(), "")
	if err != nil || selected {
		t.Fatalf("unavailable hosted helper selected=%v err=%v", selected, err)
	}
	if provider.install != nil || provider.laneCount() != 0 {
		t.Fatal("unavailable artifact installed or opened a helper")
	}
}

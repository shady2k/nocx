package app

// Tests for the helper-backed git factory selection (the remote-helper
// design): the env configuration that says a helper is available, the
// dialing factory that brings one helper up per session and never leaks
// it — a refusing open closes the lane, an exec the server refuses closes
// the lane, and two bindings on one session share ONE helper process (D4)
// that dies when the last binding closes and is redialed on the next open.
// The wire is met in process: the real host, the real git service, over
// io.Pipe.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	iofs "io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	helperartifacts "github.com/shady2k/nocx/internal/helper/deploy/artifacts"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
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
	bridgeLifecycle(peer, carrier)

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
	stdout io.Reader
	stderr io.Reader

	exited   chan struct{}
	exitCode int

	startErr error

	mu      sync.Mutex
	closed  int
	started string
}

func newFakeLaneConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeLaneConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeLaneConn{
		stdin:  toPeerW,
		stdout: fromPeerR,
		stderr: bytes.NewReader(nil),
		exited: make(chan struct{}),
	}
	go func() {
		code := peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		f.exitCode = code
		close(f.exited)
	}()
	return f
}

func (f *fakeLaneConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeLaneConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeLaneConn) Stderr() io.Reader     { return f.stderr }
func (f *fakeLaneConn) Start(command string) error {
	f.mu.Lock()
	f.started = command
	f.mu.Unlock()
	return f.startErr
}
func (f *fakeLaneConn) Wait() (int, error)    { <-f.exited; return f.exitCode, nil }
func (f *fakeLaneConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeLaneConn) LostErr() error        { return nil }

func (f *fakeLaneConn) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.stdin.Close()
}

// startedCommand is what the exec lane was asked to run: the whole point of
// the remote half of D11 is that it is the BRIDGE and not the helper serving
// over this channel.
func (f *fakeLaneConn) startedCommand() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeLaneConn) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// fakeLaneProvider hands out a fresh scripted lane per HelperConn call and
// records them, so a test can prove how many helpers were brought up. It
// also serves the install surface the selection needs: a scripted probe
// answer and an in-memory install lease, so the REAL deploy.Ensure runs
// against a fake transport — the wiring under test is the production
// wiring, only the SSH is fake.
type fakeLaneProvider struct {
	peer     func(in io.Reader, out io.Writer) int
	startErr error

	laneErr error // when set, every HelperConn fails: an unreachable host
	// laneBlock, when set, parks every HelperConn on it (or on the caller's
	// context): a host that accepts nothing and refuses nothing, which is what
	// a machine behind a black-holing firewall looks like.
	laneBlock   chan struct{}
	uname       string // the probe's canned answer; default "Linux x86_64"
	home        string // the install lease's home; default "/home/u"
	probeFail   error  // when set, DiscoveryConn fails
	installFail error  // when set, HelperInstallConn fails

	mu    sync.Mutex
	conns []*fakeLaneConn

	install *fakeInstallConn
}

func (p *fakeLaneProvider) HelperConn(ctx context.Context, _ string, _ ...ssh.ConnectOption) (ssh.HelperConn, error) {
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
	c := newFakeLaneConn(p.peer)
	c.startErr = p.startErr
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
	return c, nil
}

func (p *fakeLaneProvider) DiscoveryConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	if p.probeFail != nil {
		return nil, p.probeFail
	}
	uname := p.uname
	if uname == "" {
		uname = "Linux x86_64"
	}
	return &fakeProbeConn{uname: uname}, nil
}

func (p *fakeLaneProvider) HelperInstallConn(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
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
	uname string
}

func (f *fakeProbeConn) Exec(_ context.Context, _ string) (*ssh.ExecResult, error) {
	return &ssh.ExecResult{Stdout: []byte(f.uname)}, nil
}
func (f *fakeProbeConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeProbeConn) LostErr() error        { return nil }
func (f *fakeProbeConn) Close() error          { return nil }

// fakeInstallConn is a HelperInstallConn whose FS is an in-memory map. The
// REAL deploy.Ensure runs against it, so the selection's install path is
// the production one; only the transport is fake. uploads counts binary
// writes so a test can assert D7's uploads-nothing-on-complete without
// timing.
type fakeInstallConn struct {
	mu      sync.Mutex
	dirs    map[string]bool
	files   map[string][]byte
	home    string
	uploads int
}

func newFakeInstallConn(home string) *fakeInstallConn {
	return &fakeInstallConn{
		dirs:  map[string]bool{"/": true},
		files: map[string][]byte{},
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
func realHelperPeer() func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(contentHash),
		}))
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
func helperPeerWithoutSession() func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// fakeRemoteSession is a session.Session whose only interesting facts are
// the id, kind, host, fingerprint, mode and SSH options the helper
// selection reads.
type fakeRemoteSession struct {
	id          session.ID
	host        string
	fingerprint string
	mode        profile.DesiredMode
}

func (s *fakeRemoteSession) ID() session.ID     { return s.id }
func (s *fakeRemoteSession) Kind() session.Kind { return session.KindRemote }
func (s *fakeRemoteSession) Host() string       { return s.host }
func (s *fakeRemoteSession) Cwd() string        { return "" }
func (s *fakeRemoteSession) ProfileID() string  { return "" }
func (s *fakeRemoteSession) CredentialID() string {
	return ""
}
func (s *fakeRemoteSession) Write([]byte) (int, error)   { return 0, nil }
func (s *fakeRemoteSession) EnqueueWrite([]byte) bool    { return false }
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

func (s *fakeRemoteSession) Liveness() session.LivenessState {
	return session.LivenessState{Liveness: session.LivenessAlive, Epoch: 1}
}
func (s *fakeRemoteSession) PaneID() string { return "" }

func (s *fakeRemoteSession) OpenBootstrapWindow() (session.BootstrapWindow, error) {
	return nil, errors.New("fakeRemoteSession has no terminal")
}

func (s *fakeRemoteSession) SSHOptions() []ssh.ConnectOption {
	if s.mode == "" {
		return nil
	}
	return []ssh.ConnectOption{ssh.WithDesiredMode(string(s.mode))}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
	logger := log.NewSlogAdapter(discardLogger())
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:test-host")
	installs := consent.NewInstallStore(logger, storage.NewDocumentStore(t.TempDir()), "installs.json")
	return store, installs
}

func configuredSelector(t *testing.T, provider *fakeLaneProvider) transport.GitFactoryFor {
	t.Helper()
	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	factory, _ := helperGitFactory(provider, source, store, installs, discardLogger())
	return factory
}

// TestHelperSelectorInstallsTheArtifact is the deploy wiring (D7): the
// selection obtains the factory's command and hash by INSTALLING the
// artifact on the session's host — the fake install lease carries a
// complete install afterwards — and a second consultation of the same host
// uploads nothing (an already-complete directory is not reinstalled).
func TestHelperSelectorInstallsTheArtifact(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	sess := &fakeRemoteSession{id: "s1", host: "host.example"}

	selection := sel(sess)
	if selection.Factory == nil {
		t.Fatal("selection returned no factory after a successful install")
	}
	if provider.install == nil {
		t.Fatal("the selection never acquired an install lease")
	}
	if !provider.install.hasCompleteInstall("/home/u") {
		t.Fatal("the selection did not install the artifact (no complete install directory)")
	}

	before := provider.install.uploadCount()
	selection2 := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection2.Factory == nil {
		t.Fatal("second consultation returned no factory after a complete install")
	}
	if got := provider.install.uploadCount() - before; got != 0 {
		t.Fatalf("second consultation performed %d uploads, want 0 — a complete install must not be reinstalled", got)
	}
}

// TestHelperSelectorFallsBackWhenArtifactsNotBuilt is the fresh-checkout
// path CI exercises: with no artifacts built, asking to install is a
// VISIBLE ErrArtifactsNotBuilt refusal naming the missing build step —
// never an unsupported-platform confusion, which would be a silent degrade
// the UI contradicts (AGENTS.md). The selection returns nil and the
// zero-install refusal stands (D16); the log carries the distinct refusal.
// On CI (no embedded artifacts) the REAL artifact source is what produces
// the refusal; in a workspace where `make helpers` has run, the same path
// is exercised through the stub, so the test asserts identically in both
// worlds and never depends on which one it is in.
func TestHelperSelectorFallsBackWhenArtifactsNotBuilt(t *testing.T) {
	source := helperartifacts.DefaultSource
	if _, _, err := source.Artifact(deploy.Platform{GOOS: "linux", GOARCH: "amd64"}); !errors.Is(err, helperartifacts.ErrArtifactsNotBuilt) {
		source = refusingSource{}
	}

	var buf bytes.Buffer
	sel, _ := helperGitFactory(&fakeLaneProvider{}, source, nil, nil, slog.New(slog.NewTextHandler(&buf, nil)))
	if got := sel(&fakeRemoteSession{host: "host.example"}); got.Factory != nil {
		t.Fatalf("selection with no artifacts = %+v, want the empty refusal", got)
	}
	out := buf.String()
	if !strings.Contains(out, "not built") {
		t.Fatalf("the refusal was not visible: log says %q, want it to name the missing build step", out)
	}
	if strings.Contains(out, "no helper artifact for this platform") {
		t.Fatalf("no artifacts must not look like an unsupported platform: log says %q", out)
	}
}

// TestHelperSelectorFallsBackOnUnsupportedPlatform: a host we build no
// helper for gets none — darwin/amd64 is deliberately not a build target
// (D20), and the refusal stands rather than an install attempt.
func TestHelperSelectorFallsBackOnUnsupportedPlatform(t *testing.T) {
	provider := &fakeLaneProvider{uname: "Darwin x86_64"}
	sel, _ := helperGitFactory(provider, helperartifacts.DefaultSource, nil, nil, discardLogger())
	if got := sel(&fakeRemoteSession{host: "host.example"}); got.Factory != nil {
		t.Fatalf("selection on an unsupported platform = %+v, want the empty refusal", got)
	}
}

// TestHelperSharesOneProcessAcrossOpens is D4 on the wire: two bindings on
// one session share ONE helper process — one lane acquired, one dial — and
// the process dies when the last binding closes and is redialed on the
// next open.
func TestHelperSharesOneProcessAcrossOpens(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection.Factory == nil {
		t.Fatal("selection returned no factory")
	}
	factory := selection.Factory
	dir := fixtureRepo(t)

	repo1, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("first open outcome = %s, want ok", outcome.State)
	}
	repo2, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("second open outcome = %s, want ok", outcome.State)
	}
	if got := provider.laneCount(); got != 1 {
		t.Fatalf("two opens brought up %d helper processes, want 1 — the second open must share the first's process (D4)", got)
	}

	// Both bindings reference the helper; closing the first must NOT kill
	// the process the second still uses.
	if err = repo1.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if got := provider.lane(0).closeCount(); got != 0 {
		t.Fatalf("lane closed %d times after the first binding closed, want 0 — the second binding still references it", got)
	}

	// The last binding closes → the helper process dies with it.
	if err = repo2.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times after the last binding closed, want 1", got)
	}

	// The next open must NOT reuse the dead client — a fresh helper comes
	// up.
	repo3, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open after the helper died: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("third open outcome = %s, want ok", outcome.State)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("open after the helper died brought up %d helpers total, want 2 — the dead client must not be reused", got)
	}
	_ = repo3.Close()
}

// TestHelperSessionsDoNotShareAProcess: two sessions — even to the same
// host — each get their own helper. The pool key that would prove they
// share one connection is not exposed, and sharing across principals would
// be an authorization error.
func TestHelperSessionsDoNotShareAProcess(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	dir := fixtureRepo(t)

	selectionA := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	selectionB := sel(&fakeRemoteSession{id: "s2", host: "host.example"})
	if selectionA.Factory == nil || selectionB.Factory == nil {
		t.Fatal("selection returned no factory for one of the sessions")
	}
	factoryA := selectionA.Factory
	factoryB := selectionB.Factory
	repoA, _, err := factoryA.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("session A open: %v", err)
	}
	repoB, _, err := factoryB.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("session B open: %v", err)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("two sessions brought up %d helpers, want 2 — each session owns its helper", got)
	}
	_ = repoA.Close()
	_ = repoB.Close()
}

// TestHelperCloseHelpersForClosesOnlyTheMachine is D25's closer: an
// uninstall must close the exec channel of every live helper on the
// machine being uninstalled BEFORE the directory is removed, and it must
// not touch any other machine's helpers — the backend knows its own
// channels by the host-key fingerprint that keys consent.
func TestHelperCloseHelpersForClosesOnlyTheMachine(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	source := stubArtifacts(t)
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	if err := store.Grant("SHA256:machine-a"); err != nil {
		t.Fatalf("grant a: %v", err)
	}
	if err := store.Grant("SHA256:machine-b"); err != nil {
		t.Fatalf("grant b: %v", err)
	}
	installs := consent.NewInstallStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "installs.json")
	factory, reg := helperGitFactory(provider, source, store, installs, discardLogger())
	dir := fixtureRepo(t)

	selA := factory(&fakeRemoteSession{id: "s1", host: "host.example", fingerprint: "SHA256:machine-a"})
	selB := factory(&fakeRemoteSession{id: "s2", host: "host.other", fingerprint: "SHA256:machine-b"})
	repoA, outcome, err := selA.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open A: %v %+v", err, outcome)
	}
	repoB, outcome, err := selB.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open B: %v %+v", err, outcome)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("two machines brought up %d helpers, want 2", got)
	}

	if closeErr := reg.CloseHelpersFor(context.Background(), "SHA256:machine-a"); closeErr != nil {
		t.Fatalf("close machine-a helper: %v", closeErr)
	}

	// The machine named by the fingerprint lost its helper channel; the
	// other machine's helper is untouched.
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("machine-a's helper closed %d times, want 1 (its channel is closed first, D25)", got)
	}
	if got := provider.lane(1).closeCount(); got != 0 {
		t.Fatalf("machine-b's helper closed %d times, want 0 — the closer must not touch other machines", got)
	}
	// The closed helper is forgotten: the next open redials it instead of
	// reusing a dead client.
	if _, ok := reg.hosts["s1"]; ok {
		t.Fatal("the closed helper is still registered")
	}
	if _, _, openErr := selA.Factory.Open(context.Background(), dir); openErr == nil || openErr.Error() != "helper is closing for uninstall" {
		t.Fatalf("open during uninstall gate: got %v, want helper is closing for uninstall", openErr)
	}
	reg.FinishHelpersFor("SHA256:machine-a")
	retryRepo, retryOutcome, err := selA.Factory.Open(context.Background(), dir)
	if err != nil || retryOutcome.State != git.OpenOK {
		t.Fatalf("open after uninstall gate: %v %+v", err, retryOutcome)
	}
	_ = retryRepo.Close()
	_ = repoB.Close()
	_ = repoA.Close()

	// An unknown fingerprint closes nothing and is not an error.
	if err := reg.CloseHelpersFor(context.Background(), "SHA256:nobody"); err != nil {
		t.Fatalf("close unknown helper: %v", err)
	}
}

// TestHelperCloseHelpersForRefusesWhenSessionsCannotBeEnumerated protects
// uninstall's safety boundary: an unknown session service is not an empty
// inventory. Every affected helper must be unfrozen and remain registered so
// the operation can be retried after the daemon is repaired.
func TestHelperCloseHelpersForRefusesWhenSessionsCannotBeEnumerated(t *testing.T) {
	const fingerprint = "SHA256:test-host"
	provider := &fakeLaneProvider{peer: helperPeerWithoutSession()}
	source := stubArtifacts(t)
	store, installs := testConsentStores(t)
	factory, reg := helperGitFactory(provider, source, store, installs, discardLogger())
	dir := fixtureRepo(t)

	selectionA := factory(&fakeRemoteSession{id: "s1", host: "host.example"})
	selectionB := factory(&fakeRemoteSession{id: "s2", host: "host.example"})
	repoA, outcome, err := selectionA.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open A: %v %+v", err, outcome)
	}
	t.Cleanup(func() { _ = repoA.Close() })
	repoB, outcome, err := selectionB.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open B: %v %+v", err, outcome)
	}
	t.Cleanup(func() { _ = repoB.Close() })

	if got := len(reg.hosts); got != 2 {
		t.Fatalf("helper registry before uninstall = %d entries, want 2", got)
	}
	if err := reg.CloseHelpersFor(context.Background(), fingerprint); err == nil {
		t.Fatal("uninstall continued after the daemon could not list its sessions, so a live session's executable was about to be deleted")
	}
	if reg.isClosing(fingerprint) {
		t.Fatal("failed uninstall left the machine in the closing state")
	}
	for _, id := range []session.ID{"s1", "s2"} {
		h, ok := reg.hosts[id]
		if !ok {
			t.Fatalf("failed uninstall forgot helper %q, so retry could not reach it", id)
		}
		h.mu.Lock()
		closing := h.closing
		h.mu.Unlock()
		if closing {
			t.Fatalf("failed uninstall left helper %q frozen against retry", id)
		}
	}
	for i := range 2 {
		if got := provider.lane(i).closeCount(); got != 0 {
			t.Fatalf("failed uninstall closed helper lane %d %d times, want 0", i, got)
		}
	}
}

// TestHelperDialFactory_RefusingOpenClosesTheLane: an open that answers
// notARepository carries no repo, and nothing else references the helper
// process — the factory must close the client (and so the lane) rather
// than leaking it on the far host.
func TestHelperDialFactory_RefusingOpenClosesTheLane(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection.Factory == nil {
		t.Fatal("selection returned no factory")
	}
	factory := selection.Factory
	_, outcome, err := factory.Open(context.Background(), t.TempDir()) // not a repository
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if outcome.State != git.OpenNotARepository {
		t.Fatalf("outcome = %s, want notARepository", outcome.State)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times, want 1 — a refusing open must not leak the helper process", got)
	}
}

// TestHelperDialFactory_ExecForbiddenClosesTheLane: a server that refuses
// the exec is a helper that cannot start — the open answers the
// execForbidden §6 outcome (remote-helper design §6), and the lane the
// factory acquired is closed rather than leaked.
func TestHelperDialFactory_ExecForbiddenClosesTheLane(t *testing.T) {
	provider := &fakeLaneProvider{
		peer:     func(io.Reader, io.Writer) int { return 0 },
		startErr: errors.New("exec request failed"),
	}
	sel := configuredSelector(t, provider)
	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection.Factory == nil {
		t.Fatal("selection returned no factory")
	}
	factory := selection.Factory
	repo, outcome, err := factory.Open(context.Background(), "/some/cwd")
	if err != nil {
		t.Fatalf("open error = %v, want the execForbidden outcome, not an error", err)
	}
	if repo != nil {
		t.Fatal("open returned a repo for a refused exec")
	}
	if outcome.State != git.OpenExecForbidden || outcome.Message == "" {
		t.Fatalf("outcome = %+v, want execForbidden with a message naming the recovery", outcome)
	}
	if got := provider.lane(0).closeCount(); got != 1 {
		t.Fatalf("lane closed %d times, want 1", got)
	}
}

// TestHelperSelectionConsentRequiredWritesNothing is D8's zero-write
// invariant at the selection: a machine with no helper-tier answer gets the
// ask — and not a byte is written to the host. No install lease is
// acquired, no platform probe is even needed to decide that.
func TestHelperSelectionConsentRequiredWritesNothing(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	source := stubArtifacts(t)
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	installs := consent.NewInstallStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "installs.json")
	sel, _ := helperGitFactory(provider, source, store, installs, discardLogger())

	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example", fingerprint: "SHA256:never-answered"})
	if !selection.ConsentRequired {
		t.Fatalf("selection = %+v, want consentRequired for a machine with no answer", selection)
	}
	if provider.install != nil && provider.install.uploadCount() != 0 {
		t.Fatalf("consentRequired wrote %d uploads, want 0 — the ask must not leave a footprint", provider.install.uploadCount())
	}
	if got := provider.laneCount(); got != 0 {
		t.Fatalf("consentRequired brought up %d helper lanes, want 0", got)
	}
	if got := installs.All(); len(got) != 0 {
		t.Fatalf("consentRequired recorded %d installs, want 0", len(got))
	}
}

// TestHelperSelectionExplicitRawWritesNothing: a machine at explicit raw is
// never asked and never written to (consent design §4.2); the selection
// answers the resolver's Refused as a reason with no earned state, and
// git.open's not-available error carries it.
func TestHelperSelectionExplicitRawWritesNothing(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	source := stubArtifacts(t)
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	installs := consent.NewInstallStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "installs.json")
	sel, _ := helperGitFactory(provider, source, store, installs, discardLogger())

	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example", mode: profile.DesiredRaw})
	if selection.Factory != nil || selection.ConsentRequired {
		t.Fatalf("selection = %+v, want no factory and no ask for explicit raw", selection)
	}
	if selection.Refusal == nil || selection.Refusal.State != "" || selection.Refusal.Message == "" {
		t.Fatalf("selection = %+v, want the Refused reason naming what to do", selection)
	}
	if provider.install != nil && provider.install.uploadCount() != 0 {
		t.Fatalf("explicit raw wrote %d uploads, want 0", provider.install.uploadCount())
	}
}

// TestHelperSelectionRecordsTheFootprintObservation: the footprint screen's
// data is written only when the install actually succeeded — after a grant,
// the selection installs and the observation store lists the machine.
func TestHelperSelectionRecordsTheFootprintObservation(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	source := stubArtifacts(t)
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:test-host")
	installs := consent.NewInstallStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "installs.json")
	sel, _ := helperGitFactory(provider, source, store, installs, discardLogger())

	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection.Factory == nil {
		t.Fatal("selection returned no factory after a grant")
	}
	got := installs.All()
	if len(got) != 1 {
		t.Fatalf("install observations = %d, want 1 after a successful install", len(got))
	}
	if got[0].Fingerprint != "SHA256:test-host" || got[0].Identity == "" || got[0].Hash == "" {
		t.Fatalf("observation = %+v, want fingerprint, identity and hash recorded", got[0])
	}
}

// TestHelperSelectionExplicitScriptIsNotOfferedTheBinary: a machine at
// explicit script has ANSWERED — "the shell tiers, and do not offer me the
// binary" (D8: script is an answer, not a gap; ADR-0033). It is neither
// silently upgraded nor asked, and nothing is written to the host. The
// refusal names the modes that would offer it, so the user is told what to
// change rather than left with a dead panel.
//
// This assertion was the other way round until ADR-0033, and could not have
// been otherwise: while silence also resolved to script, refusing here would
// have refused every user who never opened a connection's settings.
func TestHelperSelectionExplicitScriptIsNotOfferedTheBinary(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	source := stubArtifacts(t)
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	installs := consent.NewInstallStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "installs.json")
	sel, _ := helperGitFactory(provider, source, store, installs, discardLogger())

	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example", mode: profile.DesiredScript})
	if selection.ConsentRequired {
		t.Fatalf("selection = %+v, want no ask for an explicit script — it is an answer", selection)
	}
	if selection.Factory != nil {
		t.Fatalf("selection = %+v, want no factory for an explicit script — it was never upgraded", selection)
	}
	if selection.Refusal == nil {
		t.Fatal("an explicit script produced neither an ask nor a refusal — the panel would have nothing to say")
	}
	if !strings.Contains(selection.Refusal.Message, "Helper") {
		t.Errorf("refusal = %q, want it to name a mode that DOES offer the helper — "+
			"a refusal the user cannot act on is the dead end this bead exists to remove",
			selection.Refusal.Message)
	}
	if provider.install != nil && provider.install.uploadCount() != 0 {
		t.Fatalf("explicit script wrote %d uploads, want 0", provider.install.uploadCount())
	}
}

// TestTheExecLaneRunsTheBridgeForTheGenerationInstalled is the remote half of
// the level-1 design's D11, asserted where the coordinator actually decides
// it: what goes down the pty-less exec lane is `nocx-helper bridge
// <generation>`, not the helper serving over that channel's stdin and stdout.
//
// The distinction is the whole bead. With the helper serving the channel, its
// sessions died with the channel; with the bridge, the channel reaches an
// endpoint on the host that outlives it — which is what lets a session survive
// a coordinator being replaced (D1). The generation is the content hash the
// installer wrote, so the bridge can never reach a different generation's
// sessions while two coexist on one host (D4).
func TestTheExecLaneRunsTheBridgeForTheGenerationInstalled(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	sel := configuredSelector(t, provider)
	selection := sel(&fakeRemoteSession{id: "s1", host: "host.example"})
	if selection.Factory == nil {
		t.Fatal("selection returned no factory")
	}
	repo, outcome, err := selection.Factory.Open(context.Background(), fixtureRepo(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("open outcome = %s, want ok", outcome.State)
	}
	t.Cleanup(func() { _ = repo.Close() })

	started := provider.lane(0).startedCommand()
	fields := strings.Fields(started)
	if len(fields) != 3 || fields[1] != endpoint.BridgeCommand {
		t.Fatalf("the exec lane ran %q, want <path> %s <generation>: it must run the bridge, "+
			"not a helper serving this channel", started, endpoint.BridgeCommand)
	}
	// The generation the bridge was handed is a content hash. Asserted
	// against the ERROR CLASS, and with a short directory, because
	// endpoint.Path answers two questions in one call: socketName validates
	// the generation, and Path then measures the JOINED path against the
	// platform's sun_path bound. This passed t.TempDir(), which on macOS is a
	// ~120-character /var/folders path and under `make ci-mac` a disposable
	// root — so the LENGTH answer arrived wearing the generation answer's
	// message and the test reported "not a content hash" about a temporary
	// directory it was never about (nocx-k6p18.4). The product cannot reach
	// that state: endpoint.Dir derives from $HOME and the socket lives under
	// ~/.nocx/run. Both halves of the fix are deliberate — the short
	// directory means only the generation can fail this call today, and the
	// error class means a third failure mode added to Path tomorrow still
	// cannot be read as this one.
	const genCheckDir = "/tmp"
	if _, err := endpoint.Path(genCheckDir, proto.GenerationID(fields[2])); errors.Is(err, endpoint.ErrBadGeneration) {
		t.Fatalf("the generation the bridge was given is not a content hash: %v", err)
	}
	// And the negative, in place, so the line above cannot pass by being
	// unfalsifiable: the same call must REJECT a generation that is not one.
	for _, notAHash := range []string{"", "short", "not-hex-at-all-not-hex-at-all"} {
		if _, err := endpoint.Path(genCheckDir, proto.GenerationID(notAHash)); !errors.Is(err, endpoint.ErrBadGeneration) {
			t.Fatalf("endpoint.Path(%q) = %v, want ErrBadGeneration — the assertion above "+
				"means nothing unless this check has teeth", notAHash, err)
		}
	}
	// And it is the generation that was INSTALLED, which is what the dial then
	// verifies the hello-ok's content hash against (D21).
	if !strings.HasSuffix(path.Dir(fields[0]), "-"+fields[2]) {
		t.Fatalf("the bridge names generation %q but the binary was installed at %q", fields[2], fields[0])
	}
}

// TestHelperSessionsRedialsAfterCarrierLoss proves that inventory remains
// available when the SSH carrier dies but the helper daemon still exists.
// The binding keeps the hostHelper registered; only its client is lost.
func TestHelperSessionsRedialsAfterCarrierLoss(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	daemon := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    helpersession.NewLocalSpawner(logger, helpersession.Shell{Path: "/bin/sh"}),
		Log:        logger,
	})
	t.Cleanup(daemon.Close)
	peer := func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(daemon)
		release := daemon.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
	provider := &fakeLaneProvider{peer: peer}
	factory := configuredSelector(t, provider)
	sel := factory(&fakeRemoteSession{id: "s1", host: "host.example"})
	dir := fixtureRepo(t)
	repo, outcome, err := sel.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open: %v %+v", err, outcome)
	}
	t.Cleanup(func() { _ = repo.Close() })

	selectedFactory, ok := sel.Factory.(*sessionFactory)
	if !ok {
		t.Fatal("selection factory has unexpected type")
	}
	reg := selectedFactory.reg
	h := reg.hosts["s1"]
	h.mu.Lock()
	carrier := h.client
	h.mu.Unlock()
	if carrier == nil {
		t.Fatal("open did not retain a helper client")
	}
	var spawned proto.SpawnResult
	callErr := carrier.Call(context.Background(), proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned)
	if callErr != nil {
		t.Fatalf("spawn daemon session: %v", callErr)
	}
	closeErr := carrier.Close()
	if closeErr != nil {
		t.Fatalf("close carrier: %v", closeErr)
	}
	select {
	case <-carrier.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("closed carrier did not report loss")
	}

	entries, err := reg.sessions(context.Background())
	if err != nil {
		t.Fatalf("inventory after carrier loss: %v", err)
	}
	if len(entries) != 1 || entries[0].HostSessionID.Session != spawned.Entry.Session.Session {
		t.Fatalf("inventory after carrier loss = %+v, want daemon session %q",
			entries, spawned.Entry.Session.Session)
	}
	if got := provider.laneCount(); got != 2 {
		t.Fatalf("inventory used %d helper lanes, want 2 after redial", got)
	}
	if err := reg.CloseHelpersFor(context.Background(), "SHA256:test-host"); err != nil {
		t.Fatalf("uninstall after carrier loss: %v", err)
	}
	if _, ok := reg.hosts["s1"]; ok {
		t.Fatal("uninstall left the carrier-loss helper registered")
	}
}

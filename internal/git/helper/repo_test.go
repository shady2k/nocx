// Package helper_test drives the backend's git factory over the helper
// protocol: a helper-backed git.Repo whose read methods are one Call each
// (plan Task 7). The acceptance test is the contract in one assertion —
// a status read through the helper-backed repo returns the same git.Status
// the LOCAL factory returns for the same repository, field by field —
// against an in-process helper speaking the REAL protocol (the real host,
// the real git service, over io.Pipe).
package helper_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// fixtureRepo builds a real repository with a commit, a modified file and
// an untracked file, so the status read is non-trivial on every list.
// internal/git/local's own fixture is unexported; this is the same recipe
// inline, the way hostsvc's test does it.
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

// fakeConn is a HelperConn backed by io.Pipe whose peer is the REAL helper
// host serving the REAL git service — the client side of the wire meets the
// actual implementation, in process, no SSH. The peer function's return
// value is the remote process's exit status.
type fakeConn struct {
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	exited   chan struct{}
	exitCode int
}

func newFakeConn(peer func(stdin io.Reader, stdout io.Writer) int) *fakeConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeConn{
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

func (f *fakeConn) Stdin() io.WriteCloser { return f.stdin }
func (f *fakeConn) Stdout() io.Reader     { return f.stdout }
func (f *fakeConn) Stderr() io.Reader     { return f.stderr }
func (f *fakeConn) Start(string) error    { return nil }
func (f *fakeConn) Wait() (int, error)    { <-f.exited; return f.exitCode, nil }
func (f *fakeConn) Done() <-chan struct{} { return make(chan struct{}) }
func (f *fakeConn) LostErr() error        { return nil }
func (f *fakeConn) Close() error          { return f.stdin.Close() }

// TestHelperRepoStatusMatchesLocal is the contract in one assertion: the
// panel must say the same thing on both machines, so the helper-backed repo
// is only correct if it agrees with the local implementation on the same
// repository — field by field, through the real protocol.
func TestHelperRepoStatusMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	want, err := repoLocal.Status(context.Background())
	if err != nil {
		t.Fatalf("local status: %v", err)
	}

	c := dialHelper(t, helperPeer(t))
	repoHelper := openHelper(t, c, dir)

	got, err := repoHelper.Status(context.Background())
	if err != nil {
		t.Fatalf("helper status: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("helper-backed status disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
}

// helperPeer runs the real helper host over an io.Pipe pair, serving the
// real git service. The host applies the production frame bound — the
// chunking test exercises D14 by crossing it with a large diff, not by
// lowering it.
func helperPeer(t *testing.T) func(io.Reader, io.Writer) int {
	t.Helper()
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, "testhash", "instance-1",
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		h.Register(hostsvc.New(localgit.NewFactory()))
		if serveErr := h.Serve(context.Background()); serveErr != nil {
			return 1
		}
		return 0
	}
}

// dialHelper brings up a client over the real host serving the real git
// service, in process, no SSH.
func dialHelper(t *testing.T, peer func(io.Reader, io.Writer) int) *client.Client {
	t.Helper()
	conn := newFakeConn(peer)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("helper dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// openHelper opens dir through the helper-backed factory and returns the
// repo, registering its close with the test.
func openHelper(t *testing.T, c *client.Client, dir string) git.Repo {
	t.Helper()
	factory := helpergit.NewFactory(c)
	repo, outcome, err := factory.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("helper open outcome = %s", outcome.State)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// bigDiffRepo makes the working-tree change large enough that the diff
// exceeds proto.MaxFrameBytes, so the chunking test crosses the REAL frame
// bound — the production path — rather than a lowered one.
func bigDiffRepo(t *testing.T) string {
	t.Helper()
	dir := fixtureRepo(t)
	var b strings.Builder
	for i := range 90000 {
		fmt.Fprintf(&b, "rewritten line %d\n", i)
	}
	// #nosec G306 — a repository working-tree file, not a secret
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestHelperRepoDiffMatchesLocal is the diff half of the one contract: the
// helper-backed repo's diff is only correct if it agrees with the local
// implementation on the same repository, field by field, through the real
// protocol.
func TestHelperRepoDiffMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	want, err := repoLocal.Diff(context.Background(), "f.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatalf("local diff: %v", err)
	}

	got, err := openHelper(t, dialHelper(t, helperPeer(t)), dir).Diff(context.Background(), "f.txt", git.SideUnstaged, 1<<20)
	if err != nil {
		t.Fatalf("helper diff: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("helper-backed diff disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
}

// TestHelperRepoLogMatchesLocal is the log half of the same contract.
func TestHelperRepoLogMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	want, err := repoLocal.Log(context.Background(), 20)
	if err != nil {
		t.Fatalf("local log: %v", err)
	}

	got, err := openHelper(t, dialHelper(t, helperPeer(t)), dir).Log(context.Background(), 20)
	if err != nil {
		t.Fatalf("helper log: %v", err)
	}
	if !logEqual(want, got) {
		t.Fatalf("helper-backed log disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
}

// logEqual compares two logs field by field with the one comparison a JSON
// boundary demands: AuthoredAt as an instant (time.Equal), not as Go's
// internal representation. A time parsed from git's %aI offset and the same
// time after a JSON round trip are the same instant with the same wire
// form, but reflect.DeepEqual sees different Location pointers and calls
// them unequal — which is exactly what the panel never sees.
func logEqual(a, b git.Log) bool {
	if a.Total != b.Total || a.Completeness != b.Completeness || len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		wa, wb := a.Entries[i], b.Entries[i]
		if wa.Hash != wb.Hash || wa.ShortHash != wb.ShortHash || wa.Subject != wb.Subject ||
			wa.AuthorName != wb.AuthorName || !wa.AuthoredAt.Equal(wb.AuthoredAt) ||
			!reflect.DeepEqual(wa.Refs, wb.Refs) {
			return false
		}
	}
	return true
}

// TestChunkedDiffReassemblesIdentically is D14 end to end at the REAL
// bound: a diff too large for one frame crosses as a ChunkedResult sentinel
// plus TypeChunk frames, and the client reassembles them into the identical
// git.Diff the local implementation returns. The diff is large enough to
// cross proto.MaxFrameBytes — a test at a lowered bound could prove the
// reassembly at 512 bytes and still ship a wrong production constant.
func TestChunkedDiffReassemblesIdentically(t *testing.T) {
	dir := bigDiffRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()
	want, err := repoLocal.Diff(context.Background(), "f.txt", git.SideUnstaged, 4<<20)
	if err != nil {
		t.Fatalf("local diff: %v", err)
	}
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if len(wire) <= proto.MaxFrameBytes {
		t.Fatalf("fixture diff is %d bytes, under the %d-byte frame bound — the test is vacuous", len(wire), proto.MaxFrameBytes)
	}

	got, err := openHelper(t, dialHelper(t, helperPeer(t)), dir).Diff(context.Background(), "f.txt", git.SideUnstaged, 4<<20)
	if err != nil {
		t.Fatalf("helper diff: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("chunked helper-backed diff disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
}

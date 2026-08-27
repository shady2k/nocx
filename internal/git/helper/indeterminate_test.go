// The D12 and typed-refusal half of the mutation work (plan Task 8):
// transport loss between a mutation's request and its response is
// indeterminate — never a failure (which would invite a retry that commits
// twice), never a refusal — while a READ's transport loss is an ordinary
// failure; and the git domain errors cross the wire as themselves, fields
// intact, so the transport's type-switch can tell them apart. The harness
// is the read tests': the real host serving the real git service, in
// process, over io.Pipe.
package helper_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/client"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	// #nosec G306 — a repository working-tree file or a test marker, not a secret
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitForFile polls until the file exists — the observable that the hook
// is running. Waiting on the file, never on a duration, is the repo's
// timing rule; the deadline only bounds the failure case.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// slowCommitHook writes a pre-commit hook that writes started, then blocks
// until release appears — the only way to hold a commit in flight
// deterministically, so the transport can be killed mid-mutation.
func slowCommitHook(t *testing.T, dir, started, release string) {
	t.Helper()
	script := "#!/bin/sh\ntouch '" + started + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.02; done\n"
	// #nosec G306 — a hook in a test repository, deliberately executable
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// killableConn is the fakeConn of the read tests plus a kill that closes
// the peer's write end: the client's read loop sees EOF and marks the
// transport lost — the D12 mechanism under test. After a kill no response
// can reach the client (the peer's writes fail on the closed pipe), so a
// mutation in flight deterministically fails with ErrLost rather than
// racing a late response.
type killableConn struct {
	*fakeConn
	peerWrite io.WriteCloser
}

func (k *killableConn) kill() { _ = k.peerWrite.Close() }

func newKillableConn(peer func(stdin io.Reader, stdout io.Writer) int) *killableConn {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	f := &fakeConn{
		stdin:  toPeerW,
		stdout: fromPeerR,
		stderr: strings.NewReader(""),
		exited: make(chan struct{}),
	}
	go func() {
		code := peer(toPeerR, fromPeerW)
		_ = fromPeerW.Close()
		f.exitCode = code
		close(f.exited)
	}()
	return &killableConn{fakeConn: f, peerWrite: fromPeerW}
}

func dialKillable(t *testing.T, conn *killableConn) *client.Client {
	t.Helper()
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("helper dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestCommitTransportLossIsIndeterminate is D12: the transport dies
// between the commit's request and its response, and the outcome says the
// commit may have happened — not that it failed. The pre-commit hook holds
// the commit in flight (its started file is the witness); the transport is
// killed; the outcome is indeterminate with NO error, which is what keeps
// the store from retrying and committing twice.
func TestCommitTransportLossIsIndeterminate(t *testing.T) {
	dir := fixtureRepo(t)
	started := filepath.Join(t.TempDir(), "hook-started")
	release := filepath.Join(t.TempDir(), "hook-release")
	slowCommitHook(t, dir, started, release)

	conn := newKillableConn(helperPeer(t))
	c := dialKillable(t, conn)
	repo := openHelper(t, c, dir)

	if st, err := repo.Stage(context.Background(), []string{"f.txt"}); err != nil || len(st.Staged) != 1 {
		t.Fatalf("stage: %v, %+v", err, st.Staged)
	}

	type result struct {
		out git.CommitOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := repo.Commit(context.Background(), "in flight", false)
		done <- result{out, err}
	}()
	waitForFile(t, started) // the commit is now running inside the helper

	conn.kill()
	writeFile(t, release, "") // let the helper-side commit finish

	res := <-done
	if res.err != nil {
		t.Fatalf("a lost commit must not surface as an error: %v", res.err)
	}
	if res.out.State != git.CommitIndeterminate {
		t.Fatalf("state = %s, want indeterminate", res.out.State)
	}
	if res.out.Head != "" {
		t.Fatalf("indeterminate must not claim a head, got %q", res.out.Head)
	}
}

// TestReadTransportLossIsOrdinaryFailure is D12's contrast: a READ losing
// the transport is an ordinary failure — an error wrapping ErrLost, never
// an indeterminate outcome, never a refusal. The commit's indeterminate is
// special because a commit cannot be re-run safely; a status read can.
func TestReadTransportLossIsOrdinaryFailure(t *testing.T) {
	dir := fixtureRepo(t)
	conn := newKillableConn(helperPeer(t))
	c := dialKillable(t, conn)
	repo := openHelper(t, c, dir)

	conn.kill()

	_, err := repo.Status(context.Background())
	if err == nil {
		t.Fatal("a lost read must fail")
	}
	if !errors.Is(err, client.ErrLost) {
		t.Fatalf("want an ErrLost transport error, got %v", err)
	}
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		t.Fatalf("a lost read is a transport error, not a refusal: %+v", refusal)
	}
}

// TestLostMutationIsNeverARefusalOrSilentSuccess pins the same distinction
// for the status-shaped mutations: a stage losing the transport returns
// the ErrLost transport error — never a refusal (the helper did not refuse
// anything) and never a silent success (a zero status would render a fresh
// empty panel). The status-shaped results have no state field to carry the
// third value; the indeterminate value lives on CommitOutcome, the one
// mutation result with a state, and the panel's existing "may have
// happened" handling covers the rest.
func TestLostMutationIsNeverARefusalOrSilentSuccess(t *testing.T) {
	dir := fixtureRepo(t)
	conn := newKillableConn(helperPeer(t))
	c := dialKillable(t, conn)
	repo := openHelper(t, c, dir)

	conn.kill()

	st, err := repo.Stage(context.Background(), []string{"f.txt"})
	if err == nil {
		t.Fatalf("a lost mutation must not look like a success (status %+v)", st)
	}
	if !errors.Is(err, client.ErrLost) {
		t.Fatalf("want an ErrLost transport error, got %v", err)
	}
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		t.Fatalf("a lost mutation is a transport error, not a refusal: %+v", refusal)
	}
}

// TestCommitNothingToCommitCrossesAsItself pins the typed refusals the
// brief names: ErrNothingToCommit must reach the backend as the typed
// error — not as an opaque internal refusal — so the transport's
// type-switch maps it to the request-shaped refusal the panel renders.
func TestCommitNothingToCommitCrossesAsItself(t *testing.T) {
	dir := fixtureRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	_, err := repo.Commit(context.Background(), "nothing staged", false)
	var want *git.ErrNothingToCommit
	if !errors.As(err, &want) {
		t.Fatalf("want *git.ErrNothingToCommit, got %T: %v", err, err)
	}
}

func TestAmendUnbornCrossesAsItself(t *testing.T) {
	dir := emptyRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	writeFile(t, filepath.Join(dir, "a.txt"), "x")
	if _, err := repo.Stage(context.Background(), []string{"a.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	_, err := repo.Commit(context.Background(), "amend", true)
	var want *git.ErrAmendUnborn
	if !errors.As(err, &want) {
		t.Fatalf("want *git.ErrAmendUnborn, got %T: %v", err, err)
	}
}

// TestStageAllRefusedWhileConflictedCrossesWithPath is D19 over the wire:
// the refusal arrives as *git.ErrConflicted AND its Path rides intact in
// the refusal's structured details — the panel's "refused with a reason"
// needs the path, and a bare type marker would render an empty one.
func TestStageAllRefusedWhileConflictedCrossesWithPath(t *testing.T) {
	dir := conflictedRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	_, err := repo.StageAll(context.Background())
	var want *git.ErrConflicted
	if !errors.As(err, &want) {
		t.Fatalf("want *git.ErrConflicted, got %T: %v", err, err)
	}
	if want.Path != "f.txt" {
		t.Fatalf("ErrConflicted.Path = %q, want f.txt — the path must cross intact", want.Path)
	}
}

// TestRemoteURLNoRemoteCrossesAsItself is the same mechanism for the
// "no link to draw" state: a repository with no remote answers ErrNoRemote
// as the typed error, which the transport maps to state "none".
func TestRemoteURLNoRemoteCrossesAsItself(t *testing.T) {
	dir := fixtureRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	_, err := repo.RemoteURL(context.Background())
	var want *git.ErrNoRemote
	if !errors.As(err, &want) {
		t.Fatalf("want *git.ErrNoRemote, got %T: %v", err, err)
	}
}

// TestHelperMutationsMatchLocal is the mutation half of the one contract:
// the panel must say the same thing on both machines, so a mutation
// through the helper is only correct if it agrees with the local
// implementation on the same repository — field by field, through the real
// protocol.
func TestHelperMutationsMatchLocal(t *testing.T) {
	dirHelper := fixtureRepo(t)
	dirLocal := fixtureRepo(t)

	local := localgit.NewFactory()
	repoLocal, outcome, err := local.Open(context.Background(), dirLocal)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	if outcome.State != git.OpenOK {
		t.Fatalf("local open outcome = %s", outcome.State)
	}
	defer func() { _ = repoLocal.Close() }()

	c := dialHelper(t, helperPeer(t))
	repoHelper := openHelper(t, c, dirHelper)

	want, err := repoLocal.Stage(context.Background(), []string{"f.txt"})
	if err != nil {
		t.Fatalf("local stage: %v", err)
	}
	got, err := repoHelper.Stage(context.Background(), []string{"f.txt"})
	if err != nil {
		t.Fatalf("helper stage: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("helper stage disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}

	wantOut, err := repoLocal.Commit(context.Background(), "same subject\n\nsame body", false)
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}
	gotOut, err := repoHelper.Commit(context.Background(), "same subject\n\nsame body", false)
	if err != nil {
		t.Fatalf("helper commit: %v", err)
	}
	// The two commits are made moments apart, so the head hashes may
	// differ; the state, the staleness flag and the fresh status are the
	// panel's truth and must agree.
	if wantOut.State != gotOut.State || wantOut.StatusStale != gotOut.StatusStale || !reflect.DeepEqual(wantOut.Status, gotOut.Status) {
		t.Fatalf("helper commit disagrees with local:\nwant: %+v\ngot:  %+v", wantOut, gotOut)
	}
	if wantOut.Head == "" || gotOut.Head == "" {
		t.Fatalf("a successful commit must carry a head: helper %q, local %q", gotOut.Head, wantOut.Head)
	}
}

// TestHostilePathThroughTheWire is D8's full round trip: a path with a
// space, a quote, a leading dash and a newline crosses the protocol and
// stages exactly itself on the remote host.
func TestHostilePathThroughTheWire(t *testing.T) {
	dir := fixtureRepo(t)
	hostile := "a file -with 'quotes' and\na newline.txt"
	writeFile(t, filepath.Join(dir, hostile), "x")

	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	st, err := repo.Stage(context.Background(), []string{hostile})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(st.Staged) != 1 || st.Staged[0].Path != hostile {
		t.Fatalf("want exactly %q staged, got %+v", hostile, st.Staged)
	}
}

// TestCommitMessageRoundTripThroughTheWire is D8's commit half end to end:
// a multi-line message with quotes and non-ASCII crosses the protocol and
// becomes HEAD's message on the remote host.
func TestCommitMessageRoundTripThroughTheWire(t *testing.T) {
	dir := fixtureRepo(t)
	msg := "subject with 'quotes' and \"double\"\n\nbody line\nnéwlines ✓"

	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)

	if _, err := repo.Stage(context.Background(), []string{"f.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	out, err := repo.Commit(context.Background(), msg, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if out.State != git.CommitOK {
		t.Fatalf("commit state = %s, want ok", out.State)
	}
	if got := headMessageOf(t, dir); got != msg+"\n\n" {
		// git appends one trailing newline to stored messages; log -B adds its own.
		t.Fatalf("HEAD message is not the one sent:\nwant %q\ngot  %q", msg, got)
	}
}

// ── fixtures ────────────────────────────────────────────────────────────

// emptyRepo is a repository with no commits at all — the unborn state an
// amend must refuse.
func emptyRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	dir := t.TempDir()
	cmd := exec.Command(gitBin, "init", "-q", dir) // #nosec G204 — gitBin is LookPath-resolved; args are fixed literals
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// conflictedRepo builds a repository in a conflicted merge state, the
// recipe internal/git/local's own tests use, inline because the fixture is
// unexported.
func conflictedRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	home := t.TempDir()
	cfg := "[user]\n\tname = Test User\n\temail = test@nocx.invalid\n[init]\n\tdefaultBranch = master\n[commit]\n\tgpgsign = false\n"
	// #nosec G306 — a disposable gitconfig for tests, not a secret
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
		cmd := exec.Command(gitBin, args...) // #nosec G204 — gitBin is LookPath-resolved; args are test literals
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@nocx.invalid")
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "topic")
	writeFile(t, filepath.Join(dir, "f.txt"), "topic\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "topic")
	run("checkout", "-q", "master")
	writeFile(t, filepath.Join(dir, "f.txt"), "master\n")
	run("add", "f.txt")
	run("commit", "-q", "-m", "master")
	merge := exec.Command(gitBin, "merge", "topic") // #nosec G204 — gitBin is LookPath-resolved; args are test literals
	merge.Dir = dir
	merge.Env = env
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", out)
	}
	return dir
}

// headMessageOf reads HEAD's message straight from the repository — the
// ground truth a commit message must match.
func headMessageOf(t *testing.T, dir string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	cmd := exec.Command(gitBin, "log", "-1", "--format=%B") // #nosec G204 — gitBin is LookPath-resolved; args are fixed literals
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return string(out)
}

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
//
// The wait is bounded twice, and both bounds are load-bearing (nocx-hgtt0).
// The release file lives in a directory the test owns and Go deletes on
// cleanup, so there is a window one poll wide in which the release is not
// late but impossible — the file and the directory that would hold it are
// already gone. A hook that only asks "is it there yet" spins on that
// forever: 62 of them accumulated here over five days, forking 2364
// processes a second and holding the load average near 50 on six cores while
// using a third of one. So the hook exits the moment the directory goes,
// and caps the wait outright in case it is ever pointed at a path whose
// parent outlives the test.
//
// Losing the race now fails the commit instead of leaking a process, which is
// the right trade: a failed commit is visible in the test that caused it.
func slowCommitHook(t *testing.T, dir, started, release string) {
	t.Helper()
	releaseDir := filepath.Dir(release)
	script := "#!/bin/sh\n" +
		"touch '" + started + "'\n" +
		"i=0\n" +
		"while [ ! -f '" + release + "' ]; do\n" +
		"  [ -d '" + releaseDir + "' ] || exit 1\n" +
		"  i=$((i + 1))\n" +
		"  [ \"$i\" -gt 3000 ] && exit 1\n" +
		"  sleep 0.02\n" +
		"done\n"
	// #nosec G306 — a hook in a test repository, deliberately executable
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSlowCommitHookCannotOutliveItsTest is the guard for nocx-hgtt0.
//
// The hook slowCommitHook installs waits for a release file that lives in a
// t.TempDir(). Go deletes that directory when the test ends, so there is a
// window — one poll interval wide — in which the release never arrives and
// never can: the file the hook is waiting for has been deleted along with the
// directory that would hold it. A hook that only asks "does the file exist
// yet" then spins until the machine is rebooted.
//
// That is not hypothetical. On 2026-08-29 this machine carried 62 of them, the
// oldest 5.3 days, together forking 2364 processes a second and holding the
// load average between 35 and 74 on six cores while using barely a third of one.
// The cost is the forking, not the arithmetic, which is why it hid behind an
// unremarkable %CPU column.
//
// So the contract is not "the hook usually gets released". It is that the hook
// cannot wait for something that can no longer happen. This test takes the
// release directory away — exactly what t.TempDir() cleanup does — and requires
// the hook to notice and exit on its own.
func TestSlowCommitHookCannotOutliveItsTest(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT t.TempDir(): this test removes the directory itself, at
	// the moment of its choosing, to make the race deterministic rather than
	// waiting to lose it by chance.
	sig, err := os.MkdirTemp("", "hook-signals")
	if err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(sig, "hook-started")
	release := filepath.Join(sig, "hook-release")
	slowCommitHook(t, repo, started, release)

	cmd := exec.Command(filepath.Join(repo, ".git", "hooks", "pre-commit")) // #nosec G204 — a path this test just wrote
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Kill only; do NOT read done here. The success path below already took
	// the one value the goroutine sends, and a second receive would block
	// this cleanup forever — which is how the first draft of this test hung
	// to the package timeout instead of reporting anything.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(sig)
	})

	waitForFile(t, started)

	// The release can now never arrive: this is t.TempDir() cleanup, early.
	if err := os.RemoveAll(sig); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the hook is still waiting for a release that can no longer arrive — " +
			"this is the leak in nocx-hgtt0: one such process per test run, forever")
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
	// Closing the client is not enough: it ends the CLIENT's half, and these
	// tests exist because the helper's half is still working when it does.
	// `Commit` returns the moment the transport dies, while the git the peer
	// spawned is mid-write inside the fixture repo's .git — so the test
	// returned, t.TempDir's RemoveAll walked that directory, and files
	// appeared under it between the readdir and the rmdir:
	//
	//     TempDir RemoveAll cleanup: unlinkat .../002/.git: directory not empty
	//
	// which is a FAILING test whose every assertion passed. It only shows up
	// where cleanup outruns the peer, so it was green here and red on CI.
	//
	// `exited` is the observable that closes the gap — never a sleep, the
	// repo's rule. Closing the client closes the peer's stdin, the host's
	// read loop sees EOF, Serve returns and `exited` closes.
	//
	// What makes that mean "nothing is writing into the repository any more"
	// is host.Serve waiting for its in-flight handlers before it returns.
	// It does NOT serve frames on the read loop: `frame` dispatches every
	// request on its own goroutine (D13), so for a while this comment was
	// wrong about the very thing it was justifying, and the wait it
	// describes was a coincidence of scheduling. TestLostMutation... then
	// failed in RemoveAll on CI with every assertion passing (nocx-t76b9),
	// which is the same red-on-green shape this cleanup was written for.
	// The closing end now lives in the host, where the interval is opened:
	// see TestServeWaitsForItsHandlersBeforeReturning.
	//
	// It is registered here rather than in the test bodies so no future test
	// on a killable conn can forget it, and it runs BEFORE every t.TempDir
	// cleanup in these tests: those directories are made in fixtureRepo and
	// the test body, both earlier, and cleanups run last-registered-first.
	t.Cleanup(func() {
		_ = c.Close()
		select {
		case <-conn.exited:
		case <-time.After(30 * time.Second):
			// The deadline bounds the failure case only; the wait itself is
			// on the observable.
			t.Error("the helper peer never exited — a commit may still be writing into the fixture repo")
		}
	})
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

// sameStatus compares two panels' status as the contract actually defines it:
// every field except Head.
//
// Head is the repository's OWN commit hash, and a helper/local comparison runs
// against two DIFFERENT repositories. Their fixtures are byte-identical, so
// the hashes agree only while both initial commits land inside the same second
// — git's timestamp granularity — and diverge otherwise: about one run in
// fifteen here (nocx-n0n6b). Asserting it asserts a coincidence, not that the
// panel says the same thing on both machines.
//
// Both call sites assert separately that a head is PRESENT. What it is belongs
// to the repository; that there is one belongs to the contract.
func sameStatus(a, b git.Status) bool {
	a.Head, b.Head = "", ""
	return reflect.DeepEqual(a, b)
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
	if !sameStatus(want, got) {
		t.Fatalf("helper stage disagrees with local:\nwant: %+v\ngot:  %+v", want, got)
	}
	if want.Head == "" || got.Head == "" {
		t.Fatalf("a staged status must carry a head: helper %q, local %q", got.Head, want.Head)
	}

	wantOut, err := repoLocal.Commit(context.Background(), "same subject\n\nsame body", false)
	if err != nil {
		t.Fatalf("local commit: %v", err)
	}
	gotOut, err := repoHelper.Commit(context.Background(), "same subject\n\nsame body", false)
	if err != nil {
		t.Fatalf("helper commit: %v", err)
	}
	// The state, the staleness flag and the fresh status are the panel's
	// truth and must agree. The head is excluded on both levels — the outer
	// one AND the one inside Status, which this check used to compare
	// through reflect.DeepEqual while excluding only its neighbour.
	if wantOut.State != gotOut.State || wantOut.StatusStale != gotOut.StatusStale ||
		!sameStatus(wantOut.Status, gotOut.Status) {
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

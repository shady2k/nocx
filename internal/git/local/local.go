// Package local is the local implementation of the git seam: a Repo over a
// spawned git, and a RepoFactory that resolves capability, environment and
// repository identity before a Repo can exist.
//
// Everything about spawning a child lives here and nowhere else — argv,
// the isolated process group, the INT → TERM → KILL escalation, the pipes,
// the bounded stderr, the early-cut protocol. None of it is part of the
// seam: what crosses git.Repo are domain states (Completeness: cut, Diff:
// tooLarge), never the fact that a child was killed. The cut is recorded in
// the private execResult and mapped before anything is returned (spec D16).
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/spawn"
	"github.com/shady2k/nocx/internal/proc"
)

// errEnough is the sentinel a stdout sink returns to stop the traversal
// deliberately — the work ceiling (status) or the byte bound (diff). It is
// not a failure: the child is cut and reaped, and the broken pipe that
// follows is expected.
var errEnough = errors.New("git: enough output received")

// Repo is a git.Repo implementation over one local repository. It holds only
// facts about how to invoke git in this repository; the interface methods
// each spawn a fresh child, so Repo itself owns no process.
type Repo struct {
	gitPath   string
	pinnedEnv []string  // WithEnv's pinned environment; nil when resolving from the shell
	resolver  *envCache // the shared resolution; nil with a pinned environment
	toplevel  string    // the worktree root; every invocation runs here
	gitDir    string
	ceilings  ceilings
}

type ceilings struct {
	statusBytes   int64
	statusWall    time.Duration
	statusEntries int
	logBytes      int64
	logWall       time.Duration
}

// Close releases nothing: the local Repo owns no persistent resource — every
// invocation is a fresh child, and the binding drains in-flight calls before
// Close — so closing is a no-op. The method exists so the relay
// implementation has a teardown point at the seam.
func (r *Repo) Close() error { return nil }

// envSettled is the environment every non-commit invocation runs under: the
// pinned one, or the current settled resolution — the fallback while the
// resolution is pending or its failure is being remembered. It never
// resolves: status, diff and log need a PATH that finds git, which the
// capability probe established at open, not the shell environment, which
// only the commit path needs (D6, nocx-6pz0).
func (r *Repo) envSettled() []string {
	if r.resolver != nil {
		env, _, _ := r.resolver.known()
		return env
	}
	return r.pinnedEnv
}

// envResolved is the environment the commit path runs under: the pinned one,
// or the shared resolution — joining an in-flight attempt, or retrying a
// remembered failure once its cooldown has passed. It is the only place that
// blocks on resolution: the commit is where D6's guarantee matters.
func (r *Repo) envResolved(ctx context.Context) []string {
	if r.resolver == nil {
		return r.pinnedEnv
	}
	env, _, _ := r.resolver.resolve(ctx)
	return env
}

// EnvState is the current environment state, read without waiting: the
// pinned environment is always resolved; the shared resolution answers
// what known() holds — resolved, a remembered failure, or the conservative
// degraded before the background attempt settles. The status poll carries
// it so the panel can withdraw a warning Open showed for the pre-settle
// window (nocx-69ey); it never resolves, so the poll cannot be held by a
// hung rc file, and the commit path is unchanged — it still waits (D6).
func (r *Repo) EnvState() (git.EnvState, string) {
	if r.resolver == nil {
		return git.EnvResolved, ""
	}
	_, state, reason := r.resolver.known()
	return state, reason
}

// spec is one invocation of git. sink receives stdout; returning errEnough
// from it stops the child deliberately. deadline is the wall-clock half of
// the work ceiling (zero: none). stderrMax bounds captured stderr (zero:
// git.MaxStderrBytes); past the bound output is discarded, never an error.
type spec struct {
	argv      []string
	dir       string
	env       []string
	stdin     io.Reader
	sink      io.Writer
	deadline  time.Time
	stderrMax int64
}

// execResult is local's private execution record. Cut is exactly the fact
// that a child was stopped by us before it finished; it maps to the domain
// states (Completeness: cut, Diff: tooLarge) before anything crosses Repo.
// Cancelled means the caller's context did it — the caller gets its context
// error back, never a cut-shaped result. A non-zero exit is data, not an
// error: git says ordinary things with exit status (1 from diff --no-index
// means "there are differences").
type execResult struct {
	err       error  // the invocation could not be made or completed
	exitCode  int    // meaningful when err == nil
	cut       bool   // stopped deliberately at a work ceiling
	cancelled bool   // stopped because ctx was cancelled
	stderr    string // bounded; see spec.stderrMax
	stderrCut bool   // the stderr bound was reached
}

// run executes one git invocation: the child runs in its own process group,
// cancellation escalates INT → TERM → KILL against the GROUP — not the direct
// child alone, because git diff can spawn a textconv filter or an external
// diff driver that would keep the inherited pipe open so the read never sees
// EOF (ADR-0020 decided the escalation for this repo) — then the direct child
// is reaped.
//
// stdout is exec-owned: exec reads the pipe in its own goroutine and closes
// the parent's end only after that goroutine has finished, so no close can
// race a read. The stopSink turns the sink's "enough" — the deliberate
// stop — into the kill signal.
func run(ctx context.Context, spec spec) execResult {
	var res execResult

	if spec.sink == nil {
		// Invocations with no stdout interest (mutations) still drain the
		// pipe: the child must be able to write without blocking.
		spec.sink = io.Discard
	}

	// #nosec G204 — argv[0] is git, resolved by resolveGit as an executable
	// file literally named "git" on the environment's PATH (never a shell);
	// the tail is fixed literals built by spawn.*Args, and the only variable
	// input is a diff path, which DiffArgs places after "--" — mutation
	// pathspecs ride on stdin, never argv. exec.Command without a shell
	// cannot inject.
	cmd := exec.Command(spec.argv[0], spec.argv[1:]...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	proc.InOwnGroup(cmd)

	stdinW, err := cmd.StdinPipe()
	if err != nil {
		res.err = fmt.Errorf("git: stdin pipe: %w", err)
		return res
	}
	stderr := newBoundedBuffer(spec.stderrMax)
	cmd.Stderr = stderr // discards past the bound, never errors

	stopped := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopped) }) }

	sink := &stopSink{Writer: spec.sink, onStop: stop}
	cmd.Stdout = sink

	if err := cmd.Start(); err != nil {
		res.err = fmt.Errorf("git: start %s: %w", spec.argv[0], err)
		return res
	}

	// done closes when the child is reaped.
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	// stopped closes when we decide the child must not run any longer; the
	// escalator then kills the group.
	go func() {
		select {
		case <-done:
			// the child finished on its own; nothing to kill
		case <-stopped:
			proc.KillGroup(cmd, done, signalGrace)
		}
	}()

	go func() {
		select {
		case <-done:
		case <-ctx.Done():
			stop()
		}
	}()

	// The wall-clock half of the work ceiling. A separate timer, not a check
	// in the sink: a traversal that produces no output (a stuck filesystem)
	// would never reach the sink to be checked. deadlineHit records that the
	// ceiling, not the sink or the context, stopped the child — without it a
	// wall-clock cut would surface as an ordinary "exit -1" failure instead
	// of Completeness: cut. The done check keeps the one race honest: a
	// child that finishes in the same instant as the ceiling is complete,
	// not cut.
	deadlineHit := make(chan struct{})
	if !spec.deadline.IsZero() {
		time.AfterFunc(time.Until(spec.deadline), func() {
			select {
			case <-done:
				return // the child finished before the ceiling; nothing to cut
			default:
			}
			close(deadlineHit)
			stop()
		})
	}

	if spec.stdin != nil {
		go func() {
			// A copy error is expected when the child exits before consuming
			// stdin (EPIPE) or Wait closes the pipe (os.ErrClosed); the
			// sources are always in-memory, so no other failure is possible.
			_, _ = io.Copy(stdinW, spec.stdin)
			// The close can fail only with os.ErrClosed — Wait already
			// closed the pipe because the child exited — and that outcome is
			// what Wait records and run() judges below; the close itself has
			// no other error path and nothing to report.
			_ = stdinW.Close()
		}()
	} else {
		// No stdin content: closing the write end is the EOF signal to the
		// child. It can fail only with os.ErrClosed — the child exited
		// before we closed — and that outcome is already recorded by Wait
		// and judged below.
		_ = stdinW.Close()
	}

	// EOF on stdout — the copy goroutine finishing — arrives only when the
	// child AND every descendant holding the write end are gone, which is
	// exactly what killing the group guarantees. The sink's "enough" fires
	// the kill through stopSink.
	<-done
	if waitErr != nil && res.err == nil {
		// A copy goroutine that aborted because the sink said enough
		// surfaces here as errEnough when the child exited 0 — that is the
		// deliberate cut, handled by attribution below, never a failure.
		if errors.Is(waitErr, errEnough) {
			res.cut = true
		}
	}
	// cmd.ProcessState is written by Wait inside the goroutine and read here
	// after <-done; closing done orders the two.
	res.exitCode = cmd.ProcessState.ExitCode()

	// Attribute the stop. The cause is decided here, in the goroutine that
	// owns res, after everything has settled: the sink's errEnough is a
	// deliberate cut, a fired deadline is a deliberate cut, and the
	// caller's context cancelling is the one way the child is stopped that
	// must surface as an error rather than a result.
	if res.err == nil && !res.cut {
		switch {
		case ctx.Err() != nil:
			res.cancelled = true
		case sink.cut():
			res.cut = true
		default:
			select {
			case <-deadlineHit:
				res.cut = true
			default:
			}
		}
	}

	// Wait's error is only a failure when the child ended on its own in a
	// way that was not an ordinary exit. Go 1.26's Wait reports BOTH a
	// non-zero exit and a signal death as errors: a non-zero exit is data
	// (git says ordinary things with exit status), and a signal death is the
	// expected end of a deliberate stop — the child we cut dies by
	// INT/TERM/KILL or by SIGPIPE once the pipe it was writing to closes,
	// and that is not "the invocation could not be completed".
	if waitErr != nil && res.err == nil && !res.cut && !res.cancelled {
		if ps := cmd.ProcessState; !ps.Exited() {
			res.err = fmt.Errorf("git: wait: %w", waitErr)
		}
	}
	res.stderr = stderr.String()
	res.stderrCut = stderr.cut()

	return res
}

// signalGrace is the pause between escalation steps proc.KillGroup takes.
// git handles INT promptly, so the whole escalation completes in ~2×grace
// even against a child that ignores everything. It is this adapter's value
// and not proc's: the escalation is shared, how long a caller can afford to
// ask politely is not.
const signalGrace = 200 * time.Millisecond

// stopSink wraps the caller's stdout sink and fires the deliberate-stop
// signal the moment the sink says "enough" — the work ceiling was reached
// while exec's own reader goroutine was copying. The child is then cut and
// reaped; the sink records the fact so run can attribute the stop.
type stopSink struct {
	io.Writer
	onStop func()

	mu     sync.Mutex
	enough bool
	once   sync.Once
}

func (s *stopSink) Write(p []byte) (int, error) {
	n, err := s.Writer.Write(p)
	if errors.Is(err, errEnough) {
		s.mu.Lock()
		s.enough = true
		s.mu.Unlock()
		s.once.Do(s.onStop)
	}
	return n, err
}

func (s *stopSink) cut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enough
}

// boundedBuffer retains up to max bytes of one stream and discards the rest,
// always returning success: a stderr writer that errors stops the reader
// while the child is still writing, and that deadlocks the invocation (spec
// §5.1). The bound is reported, never hidden.

type boundedBuffer struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	full bool
}

func newBoundedBuffer(max int64) *boundedBuffer {
	if max <= 0 {
		max = git.MaxStderrBytes
	}
	return &boundedBuffer{max: int(max)}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.full {
		room := b.max - len(b.buf)
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
		if room < len(p) {
			b.full = true
		}
	} else {
		b.full = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *boundedBuffer) cut() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.full
}

// byteSink is the bounded stdout sink: it retains up to max bytes and then
// returns errEnough — "I have all I need" — after which the execution is cut
// (spec §5.1: diff terminates deliberately; the retained text is a prefix
// and says so).
type byteSink struct {
	buf []byte
	max int64
}

func (s *byteSink) Write(p []byte) (int, error) {
	room := s.max - int64(len(s.buf))
	if room <= 0 {
		return 0, errEnough
	}
	if int64(len(p)) > room {
		p = p[:room]
		s.buf = append(s.buf, p...)
		return len(p), errEnough
	}
	s.buf = append(s.buf, p...)
	return len(p), nil
}

// streamParser is the sink contract shared by the two streaming parsers
// (porcelain status and numstat): the sink feeds bytes and stops the
// traversal at the byte half of the work ceiling (design D9). The
// wall-clock half lives in run's deadline: it must fire even when git
// produces no output.
type streamParser interface {
	Write(b []byte) error
}

// statusSink feeds a status or numstat stream into its parser and applies
// the byte half of the work ceiling.
type statusSink struct {
	p        streamParser
	maxBytes int64
	bytes    int64
}

func (s *statusSink) Write(b []byte) (int, error) {
	s.bytes += int64(len(b))
	if s.bytes > s.maxBytes {
		return 0, errEnough
	}
	if err := s.p.Write(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Status answers "what changed in this repository" from one invocation (D7):
// the parser consumes every record, retains the first MaxStatusEntries, and
// keeps counting the rest, so Total is exact unless the traversal hit a work
// ceiling and was cut. A non-zero exit is a failure here — unlike diff,
// status has no data-carrying exit codes.
//
// The entries then gain their line counts (brief nocx-i4ki) from two more
// bounded reads — the index side and the worktree side — merged by
// attachCounts. The counts are ALL-OR-NOTHING: a count read that was bounded
// out or failed attaches no counts anywhere and demotes Completeness to cut,
// the panel's one visible "the answer is incomplete" state. A status
// traversal that was itself cut skips the count reads entirely: two more
// full-repository diffs are exactly the work the ceiling exists to bound,
// and counts on a lower-bound prefix would look authoritative.
func (r *Repo) Status(ctx context.Context) (git.Status, error) {
	return r.statusWithEnv(ctx, r.envSettled())
}

// statusWithEnv is Status with an explicit environment. The commit path runs
// its preflight and post-commit reads with the SAME environment the commit
// runs under: the state a commit checks and the state its hook sees cannot
// disagree (D6, nocx-6pz0).
func (r *Repo) statusWithEnv(ctx context.Context, env []string) (git.Status, error) {
	p := spawn.NewParser(r.ceilings.statusEntries)
	res := run(ctx, spec{
		argv:     append([]string{r.gitPath}, spawn.StatusArgs()...),
		dir:      r.toplevel,
		env:      env,
		sink:     &statusSink{p: p, maxBytes: r.ceilings.statusBytes},
		deadline: time.Now().Add(r.ceilings.statusWall),
	})
	if res.cancelled {
		return git.Status{}, ctx.Err()
	}
	if res.err != nil {
		return git.Status{}, res.err
	}
	parsed, err := p.Finish()
	if err != nil {
		return git.Status{}, fmt.Errorf("git: parse status: %w", err)
	}
	st := git.Status{
		Branch:     parsed.Branch,
		Detached:   parsed.Detached,
		Unborn:     parsed.Unborn,
		Head:       parsed.Head,
		Upstream:   parsed.Upstream,
		Ahead:      parsed.Ahead,
		Behind:     parsed.Behind,
		Staged:     parsed.Staged,
		Unstaged:   parsed.Unstaged,
		Conflicted: parsed.Conflicted,
		Total:      parsed.Total,
	}
	if res.cut {
		st.Completeness = git.CompletenessCut
		return st, nil
	}
	if res.exitCode != 0 {
		return git.Status{}, fmt.Errorf("git status: exit %d: %s", res.exitCode, res.stderr)
	}
	st.Completeness = git.CompletenessComplete
	if st.Total > r.ceilings.statusEntries {
		st.Completeness = git.CompletenessCapped
	}
	if err := r.attachCounts(ctx, &st); err != nil {
		return git.Status{}, err
	}
	return st, nil
}

// attachCounts enriches the status entries with their line counts (brief
// nocx-i4ki): the index side from `git diff --cached --numstat -z`, the
// worktree side from `git diff --numstat -z`. Both reads complete before any
// entry carries a count, because a partial count set makes the rows past the
// cut look like rows with nothing to count — the D9 lie. When either read is
// bounded out or failed, no entry carries counts and Completeness becomes
// cut, which is the panel's one visible "the answer is incomplete" state.
//
// Untracked files get no counts by design: a numstat per untracked file is
// one git process per file, and the 761-file case is exactly what the work
// ceiling exists for. They are absent from the numstat stream, which is the
// same "no count exists" state a binary file occupies.
//
// Conflicted entries get no counts either: during a merge git diff reports
// several diff pairs for one unmerged path (measured: two records for one
// conflicted file), none of which is THE line count of the row the panel
// names. The merge keys on the lists, so conflicted entries are untouched.
func (r *Repo) attachCounts(ctx context.Context, st *git.Status) error {
	cached, cachedDegraded, err := r.numstat(ctx, true)
	if err != nil {
		return err
	}
	worktree, worktreeDegraded, err := r.numstat(ctx, false)
	if err != nil {
		return err
	}
	if cachedDegraded || worktreeDegraded {
		st.Completeness = git.CompletenessCut
		return nil
	}
	apply := func(entries []git.Entry, counts map[string]spawn.NumstatCount) {
		for i := range entries {
			if c, ok := counts[entries[i].Path]; ok {
				entries[i].Added = &c.Added
				entries[i].Deleted = &c.Deleted
			}
		}
	}
	apply(st.Staged, cached)
	apply(st.Unstaged, worktree)
	return nil
}

// numstat runs one line-count read, bounded by the same budget as the status
// read (design D9). degraded is true when the read could not produce a
// complete answer — the work ceiling cut the stream, or git exited non-zero
// — in which case the map is empty and the caller must attach no counts
// (all-or-nothing). A non-zero exit here is not an error the caller should
// fail the whole status on: the primary read succeeded, and degrading the
// whole panel over an enrichment read is the worse lie. It IS logged, so
// the degrade is never structural only.
func (r *Repo) numstat(ctx context.Context, cached bool) (map[string]spawn.NumstatCount, bool, error) {
	p := spawn.NewNumstatParser()
	res := run(ctx, spec{
		// GIT_OPTIONAL_LOCKS=0 carries StatusArgs' --no-optional-locks
		// decision onto a command that rejects the flag: git diff refuses
		// --no-optional-locks (measured), so the env var — the knob git
		// documents for "take no optional locks" — is the honest form. The
		// panel's reads must never rewrite .git/index under an agent
		// working in the same repository.
		argv:     append([]string{r.gitPath}, spawn.NumstatArgs(cached)...),
		dir:      r.toplevel,
		env:      append(append([]string{}, r.envSettled()...), "GIT_OPTIONAL_LOCKS=0"),
		sink:     &statusSink{p: p, maxBytes: r.ceilings.statusBytes},
		deadline: time.Now().Add(r.ceilings.statusWall),
	})
	if res.cancelled {
		return nil, false, ctx.Err()
	}
	if res.err != nil {
		return nil, false, res.err
	}
	if res.cut {
		return nil, true, nil
	}
	parsed, err := p.Finish()
	if err != nil {
		return nil, false, fmt.Errorf("git: parse numstat: %w", err)
	}
	if res.exitCode != 0 {
		slog.Warn("git: numstat read failed; no rows will carry counts",
			"cached", cached, "exit", res.exitCode, "stderr", res.stderr)
		return nil, true, nil
	}
	return parsed.Counts, false, nil
}

// Stage stages exactly the given paths — never "all": an empty slice is a
// no-op error here, and the wire's "all" is StageAll (D19). Paths ride on
// stdin as NUL-separated literal pathspecs (D8), so no path is ever
// interpolated into argv or read as a glob.
func (r *Repo) Stage(ctx context.Context, paths []string) (git.Status, error) {
	return r.mutate(ctx, spawn.AddArgs(), paths)
}

// Unstage unstages exactly the given paths. On an unborn branch this fails
// with git's own error — individual unstaging resolves HEAD, which unborn
// branches lack; unstage-ALL is the operation that works there (D19).
func (r *Repo) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	return r.mutate(ctx, spawn.ResetArgs(), paths)
}

func (r *Repo) mutate(ctx context.Context, args []string, paths []string) (git.Status, error) {
	if len(paths) == 0 {
		return git.Status{}, errors.New("git: stage/unstage with no paths")
	}
	var stdin strings.Builder
	for _, p := range paths {
		stdin.WriteString(spawn.LiteralPathspec(p))
		stdin.WriteByte(0)
	}
	res := run(ctx, spec{
		argv:  append([]string{r.gitPath}, args...),
		dir:   r.toplevel,
		env:   r.envSettled(),
		stdin: strings.NewReader(stdin.String()),
	})
	if res.cancelled {
		return git.Status{}, ctx.Err()
	}
	if res.err != nil {
		return git.Status{}, res.err
	}
	if res.exitCode != 0 {
		return git.Status{}, fmt.Errorf("git %s: exit %d: %s", args[0], res.exitCode, res.stderr)
	}
	// D12: the mutation returns the fresh status. If the status read fails
	// the mutation still happened — the caller says the view is stale and
	// re-polls; there is nothing to revert.
	return r.Status(ctx)
}

// StageAll stages everything (git add -A). It is refused while any entry is
// conflicted: measured on git 2.55, git add -A marks the conflict resolved
// using a worktree file that still contains conflict markers (D19).
func (r *Repo) StageAll(ctx context.Context) (git.Status, error) {
	if err := r.refuseWhileConflicted(ctx); err != nil {
		return git.Status{}, err
	}
	res := run(ctx, spec{
		argv: append([]string{r.gitPath}, spawn.AddAllArgs()...),
		dir:  r.toplevel,
		env:  r.envSettled(),
	})
	if res.cancelled {
		return git.Status{}, ctx.Err()
	}
	if res.err != nil {
		return git.Status{}, res.err
	}
	if res.exitCode != 0 {
		return git.Status{}, fmt.Errorf("git add -A: exit %d: %s", res.exitCode, res.stderr)
	}
	return r.Status(ctx)
}

// UnstageAll unstages everything — bare git reset, no HEAD, no pathspec —
// which is what makes it work on an unborn branch, where git restore --staged
// fails on an unresolvable HEAD. No special unborn path is needed (D19,
// measured). It is refused while any entry is conflicted: bare git reset
// during a conflicted merge deletes .git/MERGE_HEAD, silently aborting the
// merge.
func (r *Repo) UnstageAll(ctx context.Context) (git.Status, error) {
	if err := r.refuseWhileConflicted(ctx); err != nil {
		return git.Status{}, err
	}
	res := run(ctx, spec{
		argv: append([]string{r.gitPath}, spawn.ResetAllArgs()...),
		dir:  r.toplevel,
		env:  r.envSettled(),
	})
	if res.cancelled {
		return git.Status{}, ctx.Err()
	}
	if res.err != nil {
		return git.Status{}, res.err
	}
	if res.exitCode != 0 {
		return git.Status{}, fmt.Errorf("git reset: exit %d: %s", res.exitCode, res.stderr)
	}
	return r.Status(ctx)
}

// refuseWhileConflicted implements D19's one rule: while a merge is
// unresolved, the panel does not touch the index. The check is a fresh
// status — the panel's last-known status could be stale by a millisecond.
func (r *Repo) refuseWhileConflicted(ctx context.Context) error {
	st, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if len(st.Conflicted) > 0 {
		return &git.ErrConflicted{Path: st.Conflicted[0].Path}
	}
	return nil
}

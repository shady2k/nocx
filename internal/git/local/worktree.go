package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/spawn"
)

// branchCleanupTimeout bounds the one cleanup this file runs on a context the
// caller's cancellation cannot reach: the removal of a branch git created for
// an add that then failed (nocx-tlfoj). It is a bound on a HUNG git, not a
// budget for the work — the work is three short reads and one guarded delete
// against a repository the caller was already using — so it is generous, and
// its only job is to stop a cleanup that will never finish from holding the
// call that is already returning an error.
const branchCleanupTimeout = 30 * time.Second

// The linked-worktree operations (brief nocx-xn63t.1.1). Everything about
// spawning a child stays in this package (spec D16) and the argv comes from
// internal/git/spawn; what is here is the ORDER of the invocations and the
// refusals that have to happen before one is made.
//
// The ordering is the substance. Measured on git 2.55:
//
//   - `worktree add -b <branch> <path> <base>` validates the BASE before it
//     creates the branch, but creates the branch BEFORE it prepares the
//     worktree. A refused base therefore leaves nothing behind, and an
//     occupied path leaves the new ref behind.
//   - `worktree remove` refuses a worktree holding a modified OR untracked
//     file, and an ignored file alone does not make one dirty.
//   - `worktree list --porcelain` prints the path verbatim, unquoted, main
//     worktree first.
//
// So Add resolves the base and checks the path itself, before anything can be
// written, and each refusal that the caller may have to act on is a typed
// error naming the fact (the other worktree's path, the count of commits) —
// never git's prose (D11).

// revParseNotFound is git's "I cannot resolve that" exit status for
// `rev-parse --verify --quiet`: 0 answers, 1 refuses with no output, and any
// other status is an invocation that could not be made. It is a STATUS and
// not a message on purpose — the same machine-checked shape the symbolic-ref
// read already relies on (D11).
const revParseNotFound = 1

// AddWorktree places a linked worktree at path, on branch, creating the
// branch at base when it does not exist yet.
func (r *Repo) AddWorktree(ctx context.Context, branch, base, path string) (git.WorktreeAdded, error) {
	if branch == "" || path == "" {
		return git.WorktreeAdded{}, errors.New("git: worktree add needs a branch and a path")
	}
	env := r.envSettled()
	path = r.resolvePath(path)

	records, err := r.worktreeRecords(ctx, env)
	if err != nil {
		return git.WorktreeAdded{}, err
	}
	for _, rec := range records {
		if rec.Branch == branch {
			return git.WorktreeAdded{}, &git.ErrBranchCheckedOut{Branch: branch, Path: rec.Path}
		}
	}
	// The path is checked here rather than left to git: git creates the
	// branch before it prepares the worktree, so an occupied path reaches
	// git as a new ref and a failure (measured). This is what keeps the
	// interval true — from the call until it returns an error, nothing it
	// made survives.
	if pathErr := r.refuseOccupiedPath(path); pathErr != nil {
		return git.WorktreeAdded{}, pathErr
	}

	_, exists, err := r.branchOID(ctx, env, branch)
	if err != nil {
		return git.WorktreeAdded{}, err
	}
	if exists {
		// The branch is checked out nowhere (the listing above) and exists:
		// check it out as it is. base is deliberately NOT resolved here —
		// it is not applied on this path, and refusing a base git would
		// never have looked at would refuse a call git would have carried
		// out. Created says so to the caller.
		if checkoutErr := r.mutateArgv(ctx, env, spawn.WorktreeCheckoutArgs(path, branch)); checkoutErr != nil {
			return git.WorktreeAdded{}, checkoutErr
		}
		return git.WorktreeAdded{Created: false}, nil
	}

	baseOID, ok, err := r.commitOID(ctx, env, base)
	if err != nil {
		return git.WorktreeAdded{}, err
	}
	if !ok {
		return git.WorktreeAdded{}, &git.ErrBaseUnresolved{Base: base}
	}
	if err := r.mutateArgv(ctx, env, spawn.WorktreeCreateArgs(path, branch, base)); err != nil {
		// git may have created the branch before failing to prepare the
		// worktree, so the branch is removed here — by the same narrow rule,
		// and in the same code, the caller's compensation drives. Leaving it
		// would put a ref in the repository that only this call knows about,
		// and the call is returning an error.
		//
		// ON A CONTEXT THE CALLER'S CANCELLATION CANNOT REACH (nocx-tlfoj).
		// The commonest reason the add above failed is that ctx ran out: the
		// registrar gives a spawn and its enrolment ONE budget together
		// (internal/workers/registrar.go, "covers spawn and enrolment"), so on
		// a machine where `git worktree add` does not fit inside it, git makes
		// the branch and is then killed. Every invocation discardBranch makes
		// is refused by that same spent context, and the branch this call is
		// returning an error ABOUT stays in the repository — which is a ref in
		// a person's repo that only a dead call ever knew of. The same shape
		// is already settled one layer up, where a spawn's undo runs on
		// killContext (internal/app/workers.go, nocx-4gj5w): a cleanup may not
		// be bounded by the clock whose expiry is what it cleans up after.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), branchCleanupTimeout)
		cleanupErr := r.discardBranch(cleanupCtx, env, branch, base, baseOID)
		cancelCleanup()
		if cleanupErr != nil {
			return git.WorktreeAdded{}, fmt.Errorf("%w; and the branch %q it created could not be removed: %w", err, branch, cleanupErr)
		}
		return git.WorktreeAdded{}, err
	}
	return git.WorktreeAdded{Created: true}, nil
}

// Worktrees lists every working tree of this repository with the state the
// caller decides on. git's own order is kept, and git's first record is the
// main checkout (git-worktree(1): "The main worktree is listed first"), which
// is what Main reports — derived from position rather than from a comparison
// against this Repo's toplevel, because a Repo may itself be bound to a
// linked worktree.
func (r *Repo) Worktrees(ctx context.Context, base string) ([]git.Worktree, error) {
	env := r.envSettled()
	baseOID, ok, err := r.commitOID(ctx, env, base)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &git.ErrBaseUnresolved{Base: base}
	}
	records, err := r.worktreeRecords(ctx, env)
	if err != nil {
		return nil, err
	}
	list := make([]git.Worktree, 0, len(records))
	for i, rec := range records {
		wt, err := r.readWorktree(ctx, env, rec, i == 0, baseOID)
		if err != nil {
			return nil, err
		}
		list = append(list, wt)
	}
	return list, nil
}

// RemoveWorktree removes a clean linked worktree, leaving its branch.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	if path == "" {
		return errors.New("git: worktree remove needs a path")
	}
	env := r.envSettled()
	path = r.resolvePath(path)

	records, err := r.worktreeRecords(ctx, env)
	if err != nil {
		return err
	}
	idx := -1
	for i, rec := range records {
		if samePath(rec.Path, path) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &git.ErrNotAWorktree{Path: path}
	}
	if idx == 0 {
		return &git.ErrMainWorktree{Path: records[0].Path}
	}
	rec := records[idx]

	changes, err := r.worktreeState(ctx, env, rec.Path)
	if err != nil {
		if cancelled(err) {
			return err
		}
		return &git.ErrWorktreeUnreadable{Path: rec.Path, Cause: err}
	}
	if changes > 0 {
		return &git.ErrWorktreeUncommitted{Path: rec.Path, Changes: changes}
	}
	// git's own refusal is still behind this one, and deliberately: the
	// listing and the state were read moments ago, and a file written in
	// between reaches git as "contains modified or untracked files". Its
	// error is the honest account of that race.
	return r.mutateArgv(ctx, env, spawn.WorktreeRemoveArgs(rec.Path))
}

// DeleteWorktreeBranch deletes a branch that has no commits beyond base — the
// narrow compensation half of AddWorktree.
func (r *Repo) DeleteWorktreeBranch(ctx context.Context, branch, base string) error {
	if branch == "" {
		return errors.New("git: branch delete needs a branch")
	}
	env := r.envSettled()
	baseOID, ok, err := r.commitOID(ctx, env, base)
	if err != nil {
		return err
	}
	if !ok {
		return &git.ErrBaseUnresolved{Base: base}
	}
	return r.discardBranch(ctx, env, branch, base, baseOID)
}

// discardBranch is the one owner of "delete a branch that is safe to delete":
// not checked out anywhere, and reaching no commit beyond baseOID. It is
// driven by the compensation's public call and by AddWorktree's own failure
// path, so both act on the same rule. A branch that is not there is not an
// error — the caller's goal state holds, and a compensation that ran twice
// must not fail on the second run.
func (r *Repo) discardBranch(ctx context.Context, env []string, branch, base, baseOID string) error {
	records, err := r.worktreeRecords(ctx, env)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.Branch == branch {
			return &git.ErrBranchCheckedOut{Branch: branch, Path: rec.Path}
		}
	}
	tip, exists, err := r.branchOID(ctx, env, branch)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	ahead, err := r.countCommits(ctx, env, r.toplevel, baseOID, tip)
	if err != nil {
		return err
	}
	if ahead > 0 {
		return &git.ErrBranchHasCommits{Branch: branch, Base: base, Ahead: ahead}
	}
	// The delete is guarded by the oid the count was made about: a ref that
	// moved between the two invocations is refused by git, so a commit that
	// arrived in the meantime cannot be deleted by a count that predates it.
	return r.mutateArgv(ctx, env, spawn.DeleteRefArgs("refs/heads/"+branch, tip))
}

// ── the reads ──────────────────────────────────────────────────────────

// readWorktree is one worktree's state: what git reports about it, or the
// domain state saying it could not be read. An error return is the CALL's —
// the caller's context stopped it — and nothing else.
func (r *Repo) readWorktree(ctx context.Context, env []string, rec spawn.WorktreeRecord, main bool, baseOID string) (git.Worktree, error) {
	wt := git.Worktree{
		Path:     rec.Path,
		Branch:   rec.Branch,
		Detached: rec.Detached,
		Main:     main,
		State:    git.WorktreeReadable,
	}
	changes, err := r.worktreeState(ctx, env, rec.Path)
	if err == nil {
		var ahead int
		if ahead, err = r.countCommits(ctx, env, rec.Path, baseOID, "HEAD"); err == nil {
			wt.Uncommitted = changes > 0
			wt.Ahead = ahead
			return wt, nil
		}
	}
	if cancelled(err) {
		return git.Worktree{}, err
	}
	// Neither read is retried and neither fact is guessed: the state says it
	// was not read, and Uncommitted and Ahead stay zero as the absence of an
	// answer rather than an answer of zero. The reason is the failure's own
	// account, FORMATTED here rather than taken from the error's Error
	// method — the log-context ratchet reads that method name as a logger
	// call, and its baseline may only shrink (nocx-n14oo.9). Same text, one
	// fewer false positive.
	wt.State = git.WorktreeUnreadable
	wt.Reason = fmt.Sprint(err)
	return wt, nil
}

// worktreeState reads one worktree's working state and answers how many
// status records git reported; zero means clean. It runs the same status read
// the panel runs, in that worktree, WITHOUT the line counts: the counts are
// the panel's rows' enrichment and cost two more invocations per worktree,
// and no decision here is taken on them.
func (r *Repo) worktreeState(ctx context.Context, env []string, path string) (int, error) {
	st, err := r.statusIn(ctx, path, env)
	if err != nil {
		return 0, err
	}
	if st.Completeness == git.CompletenessCut {
		// A status traversal stopped at the work ceiling holds a prefix: what
		// it did not reach is exactly what could be a worker's uncommitted
		// work, so "clean" is not available and the state is unreadable.
		return 0, errors.New("the status read was stopped at the work ceiling")
	}
	return st.Total, nil
}

// countCommits counts the commits reachable from `to` and not from `from`.
func (r *Repo) countCommits(ctx context.Context, env []string, dir, from, to string) (int, error) {
	argv := spawn.RevListCountArgs(from, to)
	out, err := r.gitReadOut(ctx, env, dir, argv, git.MaxWorktreeAnswerBytes)
	if err != nil {
		return 0, err
	}
	if out.exit != 0 {
		return 0, fmt.Errorf("git %s: exit %d: %s", strings.Join(argv, " "), out.exit, out.stderr)
	}
	answer := strings.TrimSpace(string(out.out))
	n, err := strconv.Atoi(answer)
	if err != nil {
		return 0, fmt.Errorf("git %s answered %q, which is not a commit count", strings.Join(argv, " "), answer)
	}
	return n, nil
}

// worktreeRecords reads the repository's worktree listing. It is the one
// owner of that read: Add consults it before writing anything, Remove and
// discardBranch before deleting anything, and Worktrees for the list itself.
// The encoding follows the git that is running (worktreeNUL): the NUL form
// where it exists, because it can represent every path, and the line form
// below 2.36, because that is what git had.
func (r *Repo) worktreeRecords(ctx context.Context, env []string) ([]spawn.WorktreeRecord, error) {
	argv, parse := spawn.WorktreeListArgs(), spawn.ParseWorktreeList
	if !r.worktreeNUL {
		argv, parse = spawn.WorktreeListLinesArgs(), spawn.ParseWorktreeListLines
	}
	out, err := r.gitReadOut(ctx, env, r.toplevel, argv, git.MaxWorktreeListBytes)
	if err != nil {
		return nil, err
	}
	if out.exit != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", strings.Join(argv, " "), out.exit, out.stderr)
	}
	records, err := parse(out.out)
	if err != nil {
		return nil, err
	}
	return records, nil
}

// branchOID resolves a branch NAME to the oid of its tip: ("", false, nil)
// when no such branch exists, which is a value and not a failure.
func (r *Repo) branchOID(ctx context.Context, env []string, branch string) (string, bool, error) {
	return r.resolveOID(ctx, env, spawn.RefTipArgs("refs/heads/"+branch), branch)
}

// commitOID resolves a revision to the oid of the commit it names: ("",
// false, nil) when it names none, which the caller turns into
// ErrBaseUnresolved.
func (r *Repo) commitOID(ctx context.Context, env []string, rev string) (string, bool, error) {
	return r.resolveOID(ctx, env, spawn.VerifyCommitArgs(rev), rev)
}

func (r *Repo) resolveOID(ctx context.Context, env []string, argv []string, what string) (string, bool, error) {
	out, err := r.gitReadOut(ctx, env, r.toplevel, argv, git.MaxWorktreeAnswerBytes)
	if err != nil {
		return "", false, err
	}
	switch out.exit {
	case 0:
	case revParseNotFound:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("git %s: exit %d: %s", strings.Join(argv, " "), out.exit, out.stderr)
	}
	oid := strings.TrimSpace(string(out.out))
	if oid == "" {
		return "", false, fmt.Errorf("git: %s resolved to an empty oid", what)
	}
	return oid, true, nil
}

// ── the one spawn these operations share ───────────────────────────────

// readOutcome is one bounded read's answer: what git wrote to stdout, what it
// wrote to stderr, and the status it exited with. The two ways a read can
// fail to BE an answer — an invocation that could not be made or completed,
// and a read stopped at its byte ceiling — are the error return; an ordinary
// non-zero exit is data, because for rev-parse it is the answer.
type readOutcome struct {
	out    []byte
	exit   int
	stderr string
}

func (r *Repo) gitReadOut(ctx context.Context, env []string, dir string, argv []string, maxBytes int64) (readOutcome, error) {
	sink := &byteSink{max: maxBytes}
	res := run(ctx, spec{
		argv:     append([]string{r.gitPath}, argv...),
		dir:      dir,
		env:      env,
		sink:     sink,
		deadline: time.Now().Add(git.MaxWorktreeWallClock),
	})
	if res.cancelled {
		return readOutcome{}, ctx.Err()
	}
	if res.err != nil {
		return readOutcome{}, res.err
	}
	if res.cut {
		return readOutcome{}, fmt.Errorf("git %s: the read was stopped at the work ceiling", strings.Join(argv, " "))
	}
	return readOutcome{out: sink.buf, exit: res.exitCode, stderr: res.stderr}, nil
}

// mutateArgv runs one mutation whose argv is complete — no pathspec on stdin.
// Mutations carry no work ceiling, the same shape the panel's other mutations
// have (Stage, Commit): the caller's context is the cancellation contract,
// and git's non-zero exit is the failure, with its own stderr as the account.
func (r *Repo) mutateArgv(ctx context.Context, env []string, argv []string) error {
	res := run(ctx, spec{
		argv: append([]string{r.gitPath}, argv...),
		dir:  r.toplevel,
		env:  env,
	})
	if res.cancelled {
		return ctx.Err()
	}
	if res.err != nil {
		return res.err
	}
	if res.exitCode != 0 {
		return fmt.Errorf("git %s: exit %d: %s", strings.Join(argv, " "), res.exitCode, res.stderr)
	}
	return nil
}

// ── paths ──────────────────────────────────────────────────────────────

// resolvePath resolves a caller's path the way git resolves it: an absolute
// path is used as it stands, and a relative one is joined to the working tree
// every invocation here runs in. Without this the same relative path means
// two different directories to the caller and to git — one created, the other
// looked for.
func (r *Repo) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(r.toplevel, path)
}

// refuseOccupiedPath refuses a path a worktree cannot be created at, without
// git being involved — see AddWorktree for why the ordering matters. An
// existing EMPTY directory is allowed because git fills one (measured on
// 2.55); anything else would be written over, and nothing here removes what
// it found.
func (r *Repo) refuseOccupiedPath(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("git: worktree path %q: %w", path, err)
	case !info.IsDir():
		return &git.ErrWorktreePathNotEmpty{Path: path}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("git: worktree path %q: %w", path, err)
	}
	if len(entries) > 0 {
		return &git.ErrWorktreePathNotEmpty{Path: path}
	}
	return nil
}

// samePath reports whether two paths name the same directory. Symlinks are
// resolved on both sides because git reports the path it RECORDED, which on
// macOS is /private/var/... while a test (or a caller) may hold /var/... —
// the same difference canonicalTempDir exists for. A path that cannot be
// resolved (it is not there) falls back to the literal comparison, so a
// worktree whose directory was deleted is still found by its own path.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return resolvedA == resolvedB
}

// cancelled reports whether the caller's context is what stopped a read. It
// is the ONE failure that belongs to the call rather than to the worktree:
// every other failure is the worktree's state being unreadable, and the two
// must not be reported as each other.
func cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

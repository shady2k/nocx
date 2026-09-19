package app

// The composition root's answer about the checkouts nocx's own spawns leave
// behind (nocx-xn63t.1.4).
//
// THE ANSWER IS KEYED TO THE REPOSITORY, and that is the whole reason this
// type exists where it does. A worker's record answers per coordinator
// SESSION and dies with the backend by design; a checkout outlives both. git
// is the durable list — `git worktree list`, read through the seam at the
// moment the question is asked — and the location under nocx's worktrees
// directory (internal/storage's build-tagged profile) is what marks a
// checkout as nocx's. What git does not hold — the worker the spawn was for,
// and when nocx last had a pane open in the checkout — comes from the
// durable record beside it (content's worker_checkouts), and the join
// happens here, where the seam, the record and the session walk already
// meet. Nothing here keeps a second list: a row whose checkout has left
// git's answer is DROPPED by the read that noticed, and a checkout with no
// row is still listed.
//
// THE LAST-USED STAMP IS A JUDGEMENT INPUT, not a display value — a later
// sweep (task 1.6) will age checkouts by it — so it is written at creation,
// moved forward only, and reached through the smallest surface that can
// carry it: Touch, plus the pane-open note the composition root hands the
// transport. The one call a worker's CLOSE must make to keep the stamp
// honest is reported at the wiring site (app.go), because the close path is
// another worker's file.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// heldWorktreeSource is the record's answer to "which checkouts are held",
// across every coordinator session at once. It is named here rather than
// taken as *workers.Registrar so a test can stand a record in without the
// registrar's whole machinery — and so this type cannot reach anything else
// the registrar can do.
type heldWorktreeSource interface {
	HeldWorktrees(ctx context.Context) ([]workers.Worktree, error)
}

// workerCheckouts joins the seam's list, the record's annotations and the
// session walk into the one answer holdings and spawn give. Nil fields are
// the absence case every seam here follows: an answer is still made, and it
// says what it could not see (Complete false) rather than guessing.
type workerCheckouts struct {
	// repos opens the repository the coordinator's pane stands in. The ONE
	// local factory the composition root already owns — the same instance
	// the spawner's worktree plan resolves with.
	repos git.RepoFactory
	// worktreeRoot is where nocx-made checkouts live; the location is the
	// marker, and the filter is literal: a checkout of this repository
	// whose path is not under this root is not nocx's to report.
	worktreeRoot string
	// rows is the durable record's repository (content.WorkerCheckouts).
	// A stub answers no rows and silent touches, which is the honest shape
	// of a store that never opened: rows missing, but nothing invented.
	rows content.WorkerCheckoutRepository
	// sessions and layout are the walk from a coordinator session to the
	// directory it stands in — the SAME walk the spawner resolves a
	// participant's pane with, asked through the same two package functions
	// rather than a second derivation of it.
	sessions sessionCloser
	layout   paneMinter
	// held answers which checkouts live workers hold.
	held heldWorktreeSource
	// now is the wall clock the record stamps with. A field rather than a
	// call site so the test that asserts a stamp can pin the clock that
	// wrote it; production leaves it alone, and a nil field is the same as
	// time.Now.
	now func() time.Time
}

func (c *workerCheckouts) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// nocxCheckoutRepoKey is the location formula's <repo key>: the main
// checkout's basename plus "-" and the first 8 hex chars of sha256 over the
// common git dir (the main checkout's .git), so two repositories that happen
// to share a basename do not collide. It is THE one derivation — the spawn
// path computes the same key through this function, and a second spelling of
// it would be two answers that agree until the day they don't.
func nocxCheckoutRepoKey(mainPath string) string {
	digest := sha256.Sum256([]byte(filepath.Join(mainPath, ".git")))
	return filepath.Base(mainPath) + "-" + hex.EncodeToString(digest[:4])
}

// underWorktreeRoot reports whether p is inside the nocx worktrees root —
// the marker test. filepath.Clean both sides first: the seam's paths and a
// pane's recorded directory are absolute already, but a trailing separator
// or a "." component would turn the prefix test into a wrong answer.
func (c *workerCheckouts) underWorktreeRoot(p string) bool {
	if c.worktreeRoot == "" || p == "" {
		return false
	}
	root, err := filepath.Abs(c.worktreeRoot)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return rel != "."
}

// Leftovers answers which of the repository's nocx-made checkouts no live
// worker holds. The walk is the coordinator's own: its session → its pane →
// its pane's recorded directory → the repository that directory belongs to.
// Every failure of a READ marks the answer incomplete — the one dishonest
// answer would be a short list standing as the whole truth — while a
// coordinator standing nowhere (or nowhere that is a repository) answers
// empty and complete, because nothing about any repository was claimed.
//
// The logger comes from the context, and deliberately not from a stored
// field: a line that asks log.From(ctx) carries the module, request id,
// trace and span of the reading that raised it (workerEscalation's rule,
// kept for the same reason).
func (c *workerCheckouts) Leftovers(ctx context.Context, coordinatorSession string) workers.CheckoutSurvey {
	lg := log.From(ctx)
	empty := workers.CheckoutSurvey{Leftovers: []workers.LeftoverCheckout{}, Complete: true}
	// A MISSING INPUT IS AN INCOMPLETE ANSWER, before anything else: without
	// the record's held answer, held and abandoned cannot be told apart —
	// and offering a live worker's checkout up as abandoned is the one
	// mistake this answer must never make — and without the durable record
	// (rows, which the composition root leaves nil when the store is the
	// stub, which is what a store that failed to open IS) the annotations
	// and the drop-on-read are both out of reach. Either way the answer says
	// incomplete, never shorter-and-silent. The same holds for an unwired
	// seam: nil here is a composition that cannot answer, not one that
	// answered nothing.
	if c.repos == nil || c.sessions == nil || c.layout == nil || c.held == nil || c.rows == nil {
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}

	paneID := coordinatorPaneFor(c.sessions, coordinatorSession, lg)
	if paneID == "" {
		return empty
	}
	cwd := coordinatorCwdFor(ctx, c.layout, paneID, lg)
	if cwd == "" {
		return empty
	}

	repo, outcome, err := c.repos.Open(ctx, cwd)
	if err != nil {
		lg.Warn("worker checkouts: open the coordinator's repository", "error", err)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	if outcome.State != git.OpenOK {
		// Not standing in a repository nocx can open: an empty answer about
		// no repository, and nothing is being hidden.
		lg.Debug("worker checkouts: the coordinator does not stand in a repository", "cwd", cwd, "state", string(outcome.State))
		return empty
	}
	defer func() { _ = repo.Close() }()

	// THE BASE, read the way the spawn path reads it: this checkout's HEAD,
	// as a hash — the one spelling of "now" that cannot drift before the
	// count runs. Ahead is each checkout against it.
	head, err := repo.Log(ctx, 1)
	if err != nil || len(head.Entries) == 0 {
		lg.Warn("worker checkouts: read the coordinator's HEAD", "error", err)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	base := head.Entries[0].Hash

	trees, err := repo.Worktrees(ctx, base)
	if err != nil {
		lg.Warn("worker checkouts: list the repository's worktrees", "error", err)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	if len(trees) == 0 || !trees[0].Main {
		// git never lists a repository with no main checkout first; if the
		// answer says otherwise, no key can be derived honestly.
		lg.Warn("worker checkouts: the repository reports no main checkout", "cwd", cwd)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	repoKey := nocxCheckoutRepoKey(trees[0].Path)

	held, err := c.held.HeldWorktrees(ctx)
	if err != nil {
		lg.Warn("worker checkouts: read the record's held checkouts", "error", err)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	heldPaths := make(map[string]bool, len(held))
	for _, wt := range held {
		heldPaths[wt.Path] = true
	}

	var rows []content.WorkerCheckout
	if rows, err = c.rows.List(ctx, repoKey); err != nil {
		lg.Warn("worker checkouts: read the durable record", "error", err)
		return workers.CheckoutSurvey{Leftovers: empty.Leftovers, Complete: false}
	}
	rowByPath := make(map[string]content.WorkerCheckout, len(rows))
	for _, row := range rows {
		rowByPath[row.Path] = row
	}

	// THE JOIN, in the direction that cannot overstate: git's list is the
	// list, the record annotates it, and a row whose checkout has left the
	// list is dropped by this very read — the brief's "a row with no
	// checkout behind it is dropped on read", which is also what makes a
	// checkout removed by hand stop costing a read the moment one happens.
	seen := make(map[string]bool, len(trees))
	out := make([]workers.LeftoverCheckout, 0, len(rows))
	for _, tree := range trees {
		seen[tree.Path] = true
		if tree.Main || !c.underWorktreeRoot(tree.Path) {
			continue
		}
		if heldPaths[tree.Path] {
			continue
		}
		row := rowByPath[tree.Path]
		leftover := workers.LeftoverCheckout{
			Path:   tree.Path,
			Branch: tree.Branch,
			// "Could not read" is not "clean": Uncommitted and Ahead mean
			// something only when the seam says it read them.
			Readable: tree.State == git.WorktreeReadable,
		}
		if leftover.Readable {
			leftover.Uncommitted = tree.Uncommitted
			leftover.Ahead = tree.Ahead
		}
		leftover.Name, leftover.Task = row.Name, row.Task
		if row.LastUsedAt != 0 {
			leftover.LastUsed = time.UnixMilli(row.LastUsedAt).UTC()
		}
		out = append(out, leftover)
	}
	var stale []string
	for _, row := range rows {
		if !seen[row.Path] {
			stale = append(stale, row.Path)
		}
	}
	if len(stale) > 0 && c.rows != nil {
		if err := c.rows.Delete(ctx, repoKey, stale); err != nil {
			// The drop is a convenience that keeps the record from silt; a
			// failed one leaves invisible rows, never a wrong answer.
			lg.Warn("worker checkouts: drop the rows whose checkouts are gone", "error", err)
		}
	}
	return workers.CheckoutSurvey{Leftovers: out, Complete: true}
}

// recordCreated writes the row the moment a spawn's checkout exists — the
// task 1.2 path's first line that wrote anything was AddWorktree, and this
// is its record half. The spawn does NOT fail when the write does: the
// checkout is real whether or not the annotation landed, and git's list
// answers for it regardless. What is lost is the name and task a later
// coordinator would have read, so the failure is a warning worth having.
func (c *workerCheckouts) recordCreated(ctx context.Context, lg log.Logger, undo *worktreeUndo, name workers.ID, task string) {
	if c.rows == nil || undo == nil || undo.repoKey == "" {
		return
	}
	now := c.clock().UnixMilli()
	err := c.rows.Put(ctx, content.WorkerCheckout{
		RepoKey:    undo.repoKey,
		Path:       undo.path,
		Branch:     undo.branch,
		Base:       undo.base,
		Name:       string(name),
		Task:       task,
		CreatedAt:  now,
		LastUsedAt: now,
	})
	if err != nil {
		lg.Warn("worker spawn: could not record the checkout it created",
			"path", undo.path, "error", err)
	}
}

// Touch moves a checkout's last-used stamp forward to at — THE ONE CALL the
// worker close path must make for a participant whose Worktree.Path is set
// (the wiring is the composition root's to state, the close path's file is
// another worker's). A pane opened in a directory that is not a recorded
// checkout changes nothing, by the store's own WHERE clause.
func (c *workerCheckouts) Touch(ctx context.Context, path string, at time.Time) error {
	if c.rows == nil || path == "" {
		return nil
	}
	return c.rows.Touch(ctx, path, at.UnixMilli())
}

// notePaneOpened is the pane-open half of the stamp: every pane nocx opens
// through the one open path is noted with the spec it was asked for, and a
// pane standing inside a recorded checkout (at its root or below it) moves
// that checkout's last-used forward. It is a NOTE — it never fails the
// open, never blocks on anything but the store's own short write, and a
// read failure is a warning a person can grep, not an error the open
// answers.
func (c *workerCheckouts) notePaneOpened(spec transport.OpenSpec, _ session.ID) {
	if c.rows == nil || spec.Cwd == "" || spec.Kind == "ssh" {
		// Kind "" is local — the zero value every plain open has always
		// carried — and an ssh pane's cwd is a far machine's directory,
		// which no local checkout prefix can honestly claim.
		return
	}
	ctx := context.Background()
	rows, err := c.rows.All(ctx)
	if err != nil {
		log.From(ctx).Warn("worker checkouts: list rows for a pane-open note", "error", err)
		return
	}
	cwd := filepath.Clean(spec.Cwd)
	for _, row := range rows {
		root := filepath.Clean(row.Path)
		if cwd != root && !strings.HasPrefix(cwd, root+string(os.PathSeparator)) {
			continue
		}
		if touchErr := c.rows.Touch(ctx, row.Path, c.clock().UnixMilli()); touchErr != nil {
			log.From(ctx).Warn("worker checkouts: move last-used for a pane opened inside it",
				"path", row.Path, "error", touchErr)
		}
		return
	}
}

// workerRecordWithCheckouts is what the composition root hands the
// assistant's tool surface: the registrar it already held, plus the one
// answer the registrar cannot give — the repository's leftovers, which need
// the git seam and the durable record the record deliberately never holds.
type workerRecordWithCheckouts struct {
	*workers.Registrar
	checkouts *workerCheckouts
}

// LeftoverCheckouts answers through the service. The session is the one the
// coordinator capability already carries; nothing here can be asked about
// another.
func (w *workerRecordWithCheckouts) LeftoverCheckouts(ctx context.Context, coordinatorSession string) workers.CheckoutSurvey {
	return w.checkouts.Leftovers(ctx, coordinatorSession)
}

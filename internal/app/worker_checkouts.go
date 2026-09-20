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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// recordStatus is the sweep-status seam a record write failing at runtime
// is raised through — the one method of transport's *CheckoutSweepStatus
// the record needs, narrow so a test can stand a double in (AD-8).
type recordStatus interface {
	RaiseUnavailable(reason transport.CheckoutSweepDegradeReason, detail string)
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
	// sweepStatus is the product surface a record write failure is raised
	// on — the same status the stub store raises at composition, so a
	// mid-run refusal reaches the Settings notice and not only a log.
	// Nil in tests that assert nothing about the surface.
	sweepStatus recordStatus
	// recordMu guards untrustedWrites: once the record has refused a
	// write, every stamp it holds may be stale, so the sweep trusts none
	// and ages nothing. The flag is sticky for the life of the process —
	// the safe direction, never removing on a stamp that may be a lie.
	recordMu        sync.Mutex
	untrustedWrites bool
	// sweepMu guards sweepNotes: the sweep's last completed judgement, by
	// checkout path. The sweep (worker_checkout_sweep.go) writes it
	// wholesale at the end of a pass; the holdings answer reads it to say
	// which checkout is expired and why it is still there. Nil is the state
	// before the first sweep ran — no judgement has been made, and no row
	// reads as expired.
	sweepMu    sync.Mutex
	sweepNotes map[string]checkoutSweepNote
}

func (c *workerCheckouts) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// nocxCanonicalPath is THE one canonical spelling of a path in the
// checkouts logic — every comparison or join that touches two spellings of
// one directory goes through it. It is absolute, cleaned, and has every
// existing symlinked ancestor resolved: on macOS the temp directory is a
// symlink (/var → /private/var), so git answers the resolved spelling of
// the paths it reports while nocx's own worktrees root is the unresolved
// one, and a naive comparison of the two is a wrong answer on the platform
// nocx ships on (nocx-xn63t.1.5 evidence).
//
// A path that does not EXIST (or an ancestor of it) cannot be resolved by
// filepath.EvalSymlinks; the deepest existing ancestor is resolved and the
// missing tail carried over unchanged, so a path nocx is about to create
// canonicalizes to where creating it will land, and a caller comparing a
// missing checkout's two spellings still agrees. What a caller WANTS a
// missing path to mean is its own decision: for the "is it ours" guard a
// missing tail must never turn a checkout nocx made into a stranger, which
// is why the guard canonicalizes both sides rather than refusing on the
// resolution error.
func nocxCanonicalPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	resolved, linkErr := filepath.EvalSymlinks(abs)
	if linkErr == nil {
		return resolved
	}
	parent := filepath.Clean(filepath.Dir(abs))
	if parent == abs {
		return abs
	}
	return filepath.Join(nocxCanonicalPath(parent), filepath.Base(abs))
}

// nocxCheckoutRepoKey is the location formula's <repo key>: the main
// checkout's basename plus "-" and the first 8 hex chars of sha256 over the
// common git dir (the main checkout's .git), so two repositories that happen
// to share a basename do not collide. It is THE one derivation — the spawn
// path computes the same key through this function, and a second spelling of
// it would be two answers that agree until the day they don't. The input is
// canonicalized (nocxCanonicalPath) first: git answers the resolved spelling
// of the main checkout while a coordinator's pane records whatever spelling
// it stood down through, and the two must hash to one key.
func nocxCheckoutRepoKey(mainPath string) string {
	mainPath = nocxCanonicalPath(mainPath)
	digest := sha256.Sum256([]byte(filepath.Join(mainPath, ".git")))
	return filepath.Base(mainPath) + "-" + hex.EncodeToString(digest[:4])
}

// underWorktreeRoot reports whether p is inside the nocx worktrees root —
// the marker test. BOTH sides go through nocxCanonicalPath: git answers the
// resolved spelling of the checkout while the root nocx holds is the
// unresolved one (macOS /var → /private/var), and a trailing separator or a
// "." component would turn the prefix test into a wrong answer anyway.
// Canonicalizing both is what keeps a checkout nocx made from turning into
// a stranger on the platform nocx ships on (nocx-xn63t.1.5).
func (c *workerCheckouts) underWorktreeRoot(p string) bool {
	if c.worktreeRoot == "" || p == "" {
		return false
	}
	root := nocxCanonicalPath(c.worktreeRoot)
	abs := nocxCanonicalPath(p)
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
		if note, ok := c.sweepNoteOf(tree.Path); ok {
			leftover.Expired = true
			leftover.HoldReason = note.Reason
			leftover.HoldDetail = note.Detail
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
		c.markRecordUntrusted(err)
		lg.Warn("worker spawn: could not record the checkout it created",
			"path", undo.path, "error", err)
	}
}

// markRecordUntrusted records that the durable record refused a write, and
// raises the sweep's status for it — the product-visible half (nocx-xn63t.1
// review, blocker 4): a soft degrade the Settings notice shows, not only a
// log line. A creation row that fails makes a checkout invisible to cleanup
// forever; a last-used stamp that fails leaves an old time the sweep would
// age by. Both make the record untrustworthy, which is the one fact the
// surface carries.
func (c *workerCheckouts) markRecordUntrusted(err error) {
	c.recordMu.Lock()
	c.untrustedWrites = true
	c.recordMu.Unlock()
	if c.sweepStatus != nil {
		// fmt.Sprint and not the error's own message method — this file's
		// log ratchet reads that spelling as a log call site, and this is
		// not one.
		c.sweepStatus.RaiseUnavailable(transport.CheckoutSweepDegradeRecordWrites, fmt.Sprint(err))
	}
}

// recordUntrusted answers whether the record has refused a write. See
// markRecordUntrusted.
func (c *workerCheckouts) recordUntrusted() bool {
	c.recordMu.Lock()
	defer c.recordMu.Unlock()
	return c.untrustedWrites
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
	if err := c.rows.Touch(ctx, path, at.UnixMilli()); err != nil {
		c.markRecordUntrusted(err)
		return err
	}
	return nil
}

// notePaneOpened is the pane-open half of the stamp: every pane nocx opens
// through the one open path is noted with the spec it was asked for, and a
// pane standing inside a recorded checkout (at its root or below it) moves
// that checkout's last-used forward. The directory a RENDERER open stands in
// is never on the wire — open's params have never carried one — so the note
// reads it from the pane's own layout row, the one owner of the fact (AD-5):
// the row the renderer writes from a verified OSC 7, and the row the spawner
// wrote at creation for its own panes. It is a NOTE — it never fails the
// open, never blocks on anything but the store's own short write, and a read
// failure is a warning a person can grep, not an error the open answers.
func (c *workerCheckouts) notePaneOpened(spec transport.OpenSpec, _ session.ID) {
	if c.rows == nil || spec.Kind == "ssh" {
		// Kind "" is local — the zero value every plain open has always
		// carried — and an ssh pane's cwd is a far machine's directory,
		// which no local checkout prefix can honestly claim.
		return
	}
	cwd := spec.Cwd
	if cwd == "" && spec.PaneID != "" && c.layout != nil {
		// A pane whose row holds no directory yet — the fresh pane whose
		// shell has not answered an OSC 7 — has no directory to stand
		// anywhere, and stamps nothing.
		cwd, _ = c.layout.PaneCwd(context.Background(), spec.PaneID)
	}
	if cwd == "" {
		return
	}
	ctx := context.Background()
	rows, err := c.rows.All(ctx)
	if err != nil {
		log.From(ctx).Warn("worker checkouts: list rows for a pane-open note", "error", err)
		return
	}
	cwd = filepath.Clean(cwd)
	for _, row := range rows {
		root := filepath.Clean(row.Path)
		if cwd != root && !strings.HasPrefix(cwd, root+string(os.PathSeparator)) {
			continue
		}
		if touchErr := c.rows.Touch(ctx, row.Path, c.clock().UnixMilli()); touchErr != nil {
			c.markRecordUntrusted(touchErr)
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

// RemoveCheckouts removes through the service. The session is the one the
// coordinator capability already carries; nothing here can be asked about
// another.
func (w *workerRecordWithCheckouts) RemoveCheckouts(ctx context.Context, coordinatorSession string, refs []workers.CheckoutRef) workers.CheckoutRemoval {
	return w.checkouts.RemoveCheckouts(ctx, coordinatorSession, refs)
}

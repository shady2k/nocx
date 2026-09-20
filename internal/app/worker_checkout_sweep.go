package app

// The sweep half of the checkouts service (nocx-xn63t.1.6): checkouts
// nobody has used for a period are removed, at backend start and daily,
// and the period is the person's setting (worktrees.idleDays, zero is
// never).
//
// THE REMOVAL IS NOT A SECOND PATH. The sweep holds the same
// checkoutRemover seam the explicit removal of nocx-xn63t.1.5 exposes, and
// every refusal that guard names — uncommitted work, a live worker's hold,
// not-ours, unresolved — is this sweep's refusal verbatim, because both
// callers run the same removeOne under the same listing reads
// (readRemovalGround). What the sweep adds is only the two things an
// explicit caller carries and a schedule does not: WHICH checkouts are
// expired — judged from the durable record's last-used stamp, the one the
// worker close and the pane-open note keep honest — and WHY anything it
// could not remove is still there, recorded as a note the holdings answer
// relays. A checkout a live pane of nocx stands in is the one refusal the
// removal's closed set does not name — the explicit ask is a coordinator's
// deliberate act about a repository it is looking at, where the sweep is
// nobody looking — so the sweep never even asks the removal for such a
// checkout, and its note says so.
//
// THE CLOCK IS INJECTED. `now` is a field, nil meaning time.Now, the same
// shape workerCheckouts.now keeps; the daily cadence is a plain ticker, and
// no test waits on it — every test drives RunOnce with the clock pinned.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/workers"
)

// checkoutSweepInterval is the daily cadence. A mechanism parameter, not a
// user knob: the period that decides what is removed is the setting, and
// how often the judgement runs is not.
const checkoutSweepInterval = 24 * time.Hour

// checkoutSweepHoldPaneOpen is the sweep's own hold reason — the one fact
// the removal's closed refusal set does not carry, because only the sweep
// acts with nobody looking.
const checkoutSweepHoldPaneOpen = "pane-open"

// checkoutSweepNote is the sweep's judgement of one checkout it could not
// remove: when it judged, and why the checkout is still there. Reason is
// the closed vocabulary — a removal refusal's name, or pane-open — and
// Detail is what is true on disk, verbatim from the removal answer.
type checkoutSweepNote struct {
	At     time.Time
	Reason string
	Detail string
}

// sessionLister is the sweep's narrow view of the session registry (AD-8):
// the live sessions, and nothing else a registry can do. A pane's row in
// the layout outlives its session, so "which panes are LIVE" is this
// registry's fact alone — reading it from the layout would delete a
// checkout under a running shell.
type sessionLister interface {
	List() []session.Session
}

// checkoutIdlePeriod is the period closure the composition root hands the
// sweep: the registry is read fresh on every call, an unreadable setting
// degrades to the DECLARED default (never to zero — zero is "never", and a
// read failure must not quietly disable the cleanup), and a fractional day
// is a fraction of a day. The conversion multiplies in float BEFORE the
// Duration cast: converting days to a Duration first truncated 0.5 to zero
// — the "never" setting — so a person who asked for half a day of retention
// got none (nocx-xn63t.1 review, finding 5).
func checkoutIdlePeriod(registry *settings.Registry) func() time.Duration {
	return func() time.Duration {
		days, err := registry.GetNumber(settings.WorktreeIdleDays)
		if err != nil {
			log.From(context.Background()).Warn("checkout sweep: the idle period is unreadable; falling back to the declared default",
				"key", settings.WorktreeIdleDays.Key(), "error", err)
			days = settings.WorktreeIdleDays.DefaultValue()
		}
		return time.Duration(days * float64(24*time.Hour))
	}
}

// checkoutSweeper removes the checkouts the record says nobody has used
// past the idle period. checkouts is the one service the removal and the
// holdings answer share; period is read fresh for every run, so a person's
// change in Settings governs the next sweep and no restart.
type checkoutSweeper struct {
	checkouts *workerCheckouts
	// sessions is the live-pane inventory. Nil means no pane can hold a
	// checkout, which is a wiring hole rather than a safe answer — the
	// composition root always wires the registry it already holds.
	sessions sessionLister
	// period answers the idle period the person set; zero means never.
	period func() time.Duration
	// now is the wall clock the expiry judgement runs on. Nil is time.Now,
	// and a test pins it — never a duration.
	now func() time.Time
}

func (s *checkoutSweeper) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// RunOnce judges every recorded checkout once and removes the expired ones
// it honestly can. A read that fails ends the pass with nothing removed —
// the removal rule, held and abandoned untold apart, applies to a sweep
// more than to any explicit ask, because nobody is standing here watching.
func (s *checkoutSweeper) RunOnce(ctx context.Context) {
	lg := log.From(ctx)
	c := s.checkouts
	// THE MISSING INPUTS, the removal walk's own rule, applied to the whole
	// pass: without the record, the record's held answer, the layout that
	// owns a pane's directory, or the live-session inventory, held and
	// abandoned — and pane-held and abandoned — cannot be told apart. The
	// struct's nil fields are the honest absence this branch answers, never
	// an inventory that happens to read as empty.
	if c == nil || c.rows == nil || c.held == nil || c.layout == nil || s.sessions == nil {
		lg.Warn("checkout sweep: a read the sweep needs is not wired, so nothing can be judged or removed")
		return
	}
	if s.period == nil {
		lg.Warn("checkout sweep: no period is wired, so the sweep removes nothing")
		return
	}
	period := s.period()
	if period <= 0 {
		lg.Debug("checkout sweep: the idle period is zero, so nothing is ever removed")
		return
	}
	// THE RECORD HAS REFUSED A WRITE (nocx-xn63t.1 review, blocker 4): a
	// creation row that failed makes a checkout invisible to cleanup
	// forever, and a last-used stamp that failed leaves an old time — so
	// with a write outstanding, no stamp in the record can be trusted to
	// age by. Nothing is judged this pass and the last notes stand; the
	// status surface carries the degrade, and the hold is sticky for the
	// life of the process, because the safe direction is never to remove
	// on a stamp that may be stale.
	if c.recordUntrusted() {
		lg.Warn("checkout sweep: the checkout record has refused a write, so last-used stamps cannot be trusted and nothing is aged out")
		return
	}
	all, err := c.rows.All(ctx)
	if err != nil {
		lg.Warn("checkout sweep: read the durable record", "error", err)
		return
	}
	// The record's rows are read CANONICAL (nocxCanonicalPath): a row
	// written in another spelling of the same directory must still meet
	// the holds, the pane inventory and the listing it is judged against
	// (nocx-xn63t.1.6).
	for i := range all {
		all[i].Path = nocxCanonicalPath(all[i].Path)
	}
	// The record's held answer: while it cannot be read, held and abandoned
	// cannot be told apart, and nothing is judged — the last notes stand,
	// which is the last honest judgement anybody made.
	held, err := c.held.HeldWorktrees(ctx)
	if err != nil {
		lg.Warn("checkout sweep: read the record's held checkouts; a live worker's checkout cannot be told from a left-over one",
			"error", err)
		return
	}
	heldPaths := make(map[string]bool, len(held))
	for _, wt := range held {
		heldPaths[wt.Path] = true
	}

	now := s.clock()
	cutoff := now.Add(-period).UnixMilli()
	paneCwds, panesKnown := s.livePaneCwds(ctx, lg)
	if !panesKnown {
		// One pane whose directory cannot be read makes a pane-held
		// checkout indistinguishable from an abandoned one, and that is the
		// one mistake a sweep must never make. Nothing is judged this pass;
		// the last completed pass's notes stand.
		lg.Warn("checkout sweep: the live-pane inventory could not be read, so nothing is judged this pass")
		return
	}

	notes := make(map[string]checkoutSweepNote)
	judged := false
	defer func() {
		if judged {
			c.recordSweepNotes(notes)
		}
	}()

	byRepo := make(map[string][]content.WorkerCheckout)
	for _, row := range all {
		// A row with no stamp cannot be aged honestly: the stamp is written
		// at creation and moved only forward, so zero means a row nobody
		// wrote through the record's own paths, and the sweep leaves it.
		if row.LastUsedAt == 0 || row.LastUsedAt >= cutoff {
			continue
		}
		byRepo[row.RepoKey] = append(byRepo[row.RepoKey], row)
	}
	judged = true

	// THE LAST LOOK (nocx-xn63t.1 review, blocker 3): paneCwds above is a
	// snapshot, and a pane can open in an expired checkout after it was
	// taken and before the removal runs — the checkout would come down
	// under a running shell. The removal step therefore re-reads the live
	// panes per checkout, immediately before taking that checkout away,
	// and unreadable still answers held — the same rule the snapshot
	// applies, because unreadable is never an answer of safe.
	//
	// WHAT REMAINS RACY, and why that is acceptable: the window is now one
	// git invocation wide — a pane opening between the re-read and git's
	// remove still loses its ground — and closing it entirely would need
	// one exclusion both this sweep and the pane-open path take, which no
	// seam between the session registry and the layout offers. The residue
	// is the pane's shell standing in a directory that is gone, visible in
	// the product and named by holdings, never a silent deletion of a
	// checkout a worker still held; the old window was the whole pass.
	paneGuard := func(lg log.Logger, path string) *checkoutSweepNote {
		cwds, known := s.livePaneCwds(ctx, lg)
		if !known {
			return &checkoutSweepNote{
				At: now, Reason: checkoutSweepHoldPaneOpen,
				Detail: "the live-pane inventory could not be re-read at the removal",
			}
		}
		if paneHoldsPath(cwds, path) {
			return &checkoutSweepNote{
				At: now, Reason: checkoutSweepHoldPaneOpen,
				Detail: "a pane of nocx is open in it",
			}
		}
		return nil
	}

	removed := 0
	for repoKey, rows := range byRepo {
		var refs []workers.CheckoutRef
		for _, row := range rows {
			if heldPaths[row.Path] {
				notes[row.Path] = checkoutSweepNote{
					At: now, Reason: string(workers.CheckoutRefusalHeldByWorker),
					Detail: "a live worker holds it; close the worker first",
				}
				continue
			}
			if paneHoldsPath(paneCwds, row.Path) {
				notes[row.Path] = checkoutSweepNote{
					At: now, Reason: checkoutSweepHoldPaneOpen,
					Detail: "a pane of nocx is open in it",
				}
				continue
			}
			refs = append(refs, workers.CheckoutRef{Path: row.Path, Branch: row.Branch})
		}
		if len(refs) == 0 {
			continue
		}
		repo, ok := openRepoForSweep(ctx, lg, c.repos, rows)
		if !ok {
			for _, ref := range refs {
				notes[ref.Path] = checkoutSweepNote{
					At: now, Reason: string(workers.CheckoutRefusalUnresolved),
					Detail: "the repository could not be opened, so nothing could be verified",
				}
			}
			continue
		}
		ground, fail := readRemovalGround(ctx, lg, repo)
		if fail == nil && ground.repoKey != repoKey {
			lg.Warn("checkout sweep: the record's repository key does not match the repository the checkout names",
				"recorded_key", repoKey, "resolved_key", ground.repoKey)
			fail = &workers.RemovedCheckout{
				Refusal: workers.CheckoutRefusalUnresolved,
				Detail:  "the repository the record names could not be re-established, so nothing was removed",
			}
		}
		if fail != nil {
			for _, ref := range refs {
				notes[ref.Path] = checkoutSweepNote{At: now, Reason: string(fail.Refusal), Detail: fail.Detail}
			}
			_ = repo.Close()
			continue
		}
		treeByPath := make(map[string]git.Worktree, len(ground.trees))
		treeByBranch := make(map[string]git.Worktree, len(ground.trees))
		for _, tree := range ground.trees {
			treeByPath[tree.Path] = tree
			if !tree.Main {
				treeByBranch[tree.Branch] = tree
			}
		}
		removal, paneNotes := c.removeResolved(ctx, lg, ground.repoKey, repo, heldPaths, treeByPath, treeByBranch, refs, paneGuard)
		_ = repo.Close()
		for path, note := range paneNotes {
			notes[path] = note
		}
		for _, item := range removal.Items {
			if item.Removed {
				removed++
				continue
			}
			notes[item.Path] = checkoutSweepNote{At: now, Reason: string(item.Refusal), Detail: item.Detail}
		}
	}
	lg.Info("checkout sweep: the pass is done",
		"expired", len(notes)+removed, "removed", removed, "kept", len(notes))
}

// start runs the sweep once — the pass at backend start, synchronously,
// the way the startup reconcile pass runs: bounded work, done before the
// caller moves on — and then daily until the returned stop is called.
// Stopping cancels the pass context, which ends the cadence; calling stop
// twice is nothing. No test waits on the ticker: a test drives RunOnce,
// or calls start and finds the first pass already done.
func (s *checkoutSweeper) start(logger log.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(log.WithLogger(context.Background(), logger))
	var once sync.Once
	stop = func() { once.Do(cancel) }
	s.RunOnce(ctx)
	go func() {
		ticker := time.NewTicker(checkoutSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx)
			}
		}
	}()
	return stop
}

// livePaneCwds is the sweep's pane inventory: the recorded directory of
// every LIVE session's pane, cleaned. A session with no pane and a pane
// with no recorded directory answer nothing; a read that FAILS answers
// not-ok — one unreadable pane makes pane-held indistinguishable from
// abandoned, and unreadable is never an answer of safe.
func (s *checkoutSweeper) livePaneCwds(ctx context.Context, lg log.Logger) ([]string, bool) {
	var cwds []string
	for _, sess := range s.sessions.List() {
		paneID := sess.PaneID()
		if paneID == "" {
			continue
		}
		cwd, err := s.checkouts.layout.PaneCwd(ctx, paneID)
		if err != nil {
			lg.Warn("checkout sweep: a live pane's directory could not be read",
				"pane_id", paneID, "error", err)
			return nil, false
		}
		if cwd == "" {
			continue
		}
		cwds = append(cwds, filepath.Clean(cwd))
	}
	return cwds, true
}

// paneHoldsPath reports whether any live pane's recorded directory is the
// checkout itself or below it — somebody is standing there, and a sweep
// that removed the directory would pull the ground out from under their
// shell.
func paneHoldsPath(cwds []string, path string) bool {
	if len(cwds) == 0 || path == "" {
		return false
	}
	// BOTH sides canonical (nocxCanonicalPath): a pane's recorded
	// directory carries whatever spelling its shell answered an OSC 7
	// with — on macOS a symlinked ancestor makes that the unresolved one —
	// and a hold that missed on a spelling removes a directory somebody
	// is standing in (nocx-xn63t.1.6).
	root := nocxCanonicalPath(path)
	for _, cwd := range cwds {
		if dir := nocxCanonicalPath(cwd); dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// openRepoForSweep opens the rows' repository — any one of the group's
// checkout paths resolves the same repository, so the first that opens
// answers for the group. None opening means every checkout the group names
// has left the disk; the pass answers not-ok and the rows wait for the
// holdings read's drop-on-read.
func openRepoForSweep(ctx context.Context, lg log.Logger, repos git.RepoFactory, rows []content.WorkerCheckout) (git.Repo, bool) {
	for _, row := range rows {
		repo, outcome, err := repos.Open(ctx, row.Path)
		if err == nil && outcome.State == git.OpenOK {
			return repo, true
		}
		if err != nil {
			lg.Debug("checkout sweep: a checkout's repository would not open from the checkout",
				"path", row.Path, "error", err)
		} else {
			lg.Debug("checkout sweep: a checkout's repository would not open from the checkout",
				"path", row.Path, "state", string(outcome.State))
		}
	}
	return nil, false
}

// recordSweepNotes replaces the sweep's judgement wholesale: the notes are
// what the LAST COMPLETED PASS said, never an accumulation of passes — a
// checkout this pass did not judge carries no note, because the sweep has
// nothing current to say about it.
func (c *workerCheckouts) recordSweepNotes(notes map[string]checkoutSweepNote) {
	c.sweepMu.Lock()
	defer c.sweepMu.Unlock()
	c.sweepNotes = notes
}

// sweepNoteOf answers the sweep's last judgement of one checkout, if it
// made one.
func (c *workerCheckouts) sweepNoteOf(path string) (checkoutSweepNote, bool) {
	c.sweepMu.Lock()
	defer c.sweepMu.Unlock()
	note, ok := c.sweepNotes[path]
	return note, ok
}

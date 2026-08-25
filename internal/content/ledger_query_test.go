package content_test

// The ledger's read path (nocx-rtg0.20): QueryEntries, the ONE ordering
// implementation (design §6.2). These tests are written from the bead's
// acceptance criteria rather than from the query that answers them.
//
// Two rules shape every case here:
//
//   - Every filter is proved by what it EXCLUDES. A filter that is silently
//     ignored returns a superset and looks like it works, and a test that
//     only asserts "rows came back" cannot see the difference.
//   - HasRows is read in the same answer as the page, because "the store
//     answered and had nothing" and "the store has nothing to answer from"
//     are different facts. A read that cannot tell them apart ships a UI
//     saying "no history" when it means "history is off".

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// envReadyHost records an environment with an endpoint plus its first
// observation — envReady's remote counterpart, so a rung can be told from
// another rung.
func envReadyHost(t *testing.T, led content.LedgerRepository, id, endpoint string) {
	t.Helper()
	ctx := context.Background()
	env := content.Environment{ID: id, Kind: content.EnvSSH, Endpoint: &endpoint}
	if err := led.EnsureEnvironment(ctx, env); err != nil {
		t.Fatalf("EnsureEnvironment(%q): %v", id, err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: id, Criticality: content.CriticalityRoutine, Payload: "{}",
	}); err != nil {
		t.Fatalf("RecordObservation(%q): %v", id, err)
	}
}

// submitAt records one entry with the coordinates a rung filters on.
func submitAt(t *testing.T, led content.LedgerRepository, id, envID, cwd string, kind content.EntryKind, intent string) content.SubmitResult {
	t.Helper()
	res, err := led.Submit(context.Background(), content.SubmitEntry{
		ID: id, Client: "test-client", EnvironmentID: envID, Cwd: cwd,
		Kind: kind, Intent: intent,
		// The column's own default: the close merges its kind arm into
		// whatever the row holds, and json_patch has nothing to merge into
		// when the open stored an empty string.
		Payload: "{}",
	})
	if err != nil {
		t.Fatalf("Submit(%q): %v", id, err)
	}
	return res
}

// closeEntry walks an entry to closed through the real lifecycle, which is
// the only thing that gives it a status, an ended_at and an exit code.
func closeEntry(t *testing.T, led content.LedgerRepository, id string, status content.EntryStatus, exit *int) {
	t.Helper()
	ctx := context.Background()
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: id})
	if err != nil {
		t.Fatalf("StartExecution(%q): %v", id, err)
	}
	payload := content.ShellPayloadJSON(exit)
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt:           time.Now().UnixMilli(),
		TerminationReason: content.TermCompleted,
		Status:            status,
		Payload:           &payload,
	}); err != nil {
		t.Fatalf("FinishExecution(%q): %v", id, err)
	}
}

// queryOK runs one query and fails the test if it errors.
func queryOK(t *testing.T, led content.LedgerRepository, q content.LedgerQuery) content.LedgerPage {
	t.Helper()
	page, err := led.QueryEntries(context.Background(), q)
	if err != nil {
		t.Fatalf("QueryEntries(%+v): %v", q, err)
	}
	return page
}

// pageIDs is the page's entry ids in page order.
func pageIDs(page content.LedgerPage) []string {
	out := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		out = append(out, e.ID)
	}
	return out
}

// wantOnly asserts the page holds exactly these ids, in this order. It is
// the assertion the EXCLUDES rule needs: naming the whole set is what makes
// a silently-ignored filter fail.
func wantOnly(t *testing.T, page content.LedgerPage, ids ...string) {
	t.Helper()
	got := pageIDs(page)
	if len(got) != len(ids) {
		t.Fatalf("page = %v, want exactly %v", got, ids)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("page = %v, want exactly %v", got, ids)
		}
	}
}

func entryID(n int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
}

// ── ordering ─────────────────────────────────────────────────────────────

// seq is the total order (§6.3). Two entries written inside the SAME
// millisecond still have an order, and it is the counter's — which is the
// case a wall-clock ordering gets wrong. The stamps are forced equal so the
// test cannot pass by accident on a machine whose clock ticked between the
// two submissions.
func TestQueryEntriesOrdersBySeqDescInsideOneMillisecond(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")

	first := submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "first")
	second := submitAt(t, led, entryID(2), "local", "/repo", content.EntryShell, "second")
	if first.IngestSeq >= second.IngestSeq {
		t.Fatalf("ingest_seq is not submission order: %d then %d", first.IngestSeq, second.IngestSeq)
	}

	if first.SubmittedAt != second.SubmittedAt {
		// The clock ticked between the two submissions; flatten it, so what
		// remains to order by is the counter and nothing else.
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := rawLedger(t, path, hex.EncodeToString(testKey()),
			`UPDATE entries SET submitted_at = 1750000000000`); err != nil {
			t.Fatalf("flatten submitted_at: %v", err)
		}
		again, err := content.Open(ctx, content.Config{Path: path, Key: testKey(), Budget: testBudget})
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer func() { _ = again.Close() }()
		led = again.Ledger()
	}

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	wantOnly(t, page, entryID(2), entryID(1))
}

// ── the filters, each proved by what it excludes ─────────────────────────

// The recall ladder's rungs (§10.6). The server answers from the rung it was
// asked for and never silently widens: directory is the exact (environment,
// cwd) pair, host is the environment, everywhere is no rung at all.
func TestQueryEntriesAnswersFromTheRungItWasAskedFor(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	envReadyHost(t, led, "remote", "deploy@prod.example.com")

	here := entryID(1)
	elsewhere := entryID(2)
	remote := entryID(3)
	submitAt(t, led, here, "local", "/repo", content.EntryShell, "make test")
	submitAt(t, led, elsewhere, "local", "/other", content.EntryShell, "make lint")
	submitAt(t, led, remote, "remote", "/repo", content.EntryShell, "make deploy")

	t.Run("directory excludes another directory and another environment", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeDirectory, EnvironmentID: "local", Cwd: "/repo", Limit: 10,
		})
		wantOnly(t, page, here)
	})
	t.Run("host excludes another environment but keeps its directories", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeHost, EnvironmentID: "local", Limit: 10,
		})
		wantOnly(t, page, elsewhere, here)
	})
	t.Run("everywhere excludes nothing", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
		wantOnly(t, page, remote, elsewhere, here)
	})
}

// Each row says which host it ran on, resolved through the join the page
// already carries (nocx-rtg0.25) — never a lookup per row.
func TestQueryEntriesRowsCarryTheirResolvedEnvironment(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	envReadyHost(t, led, "remote", "deploy@prod.example.com")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make test")
	submitAt(t, led, entryID(2), "remote", "/repo", content.EntryShell, "make deploy")

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	want := map[string]string{entryID(1): "", entryID(2): "deploy@prod.example.com"}
	for _, row := range page.Entries {
		if row.Environment == nil {
			t.Fatalf("row %q resolved no environment", row.ID)
		}
		if got := row.Environment.Host(); got != want[row.ID] {
			t.Fatalf("row %q host = %q, want %q", row.ID, got, want[row.ID])
		}
	}
}

// kind is a closed enum mirroring the CHECK constraint, and it excludes.
func TestQueryEntriesKindExcludesEveryOtherKind(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	shell := entryID(1)
	agent := entryID(2)
	submitAt(t, led, shell, "local", "/repo", content.EntryShell, "make test")
	submitAt(t, led, agent, "local", "/repo", content.EntryAsk, "why did it fail")

	page := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Kind: content.EntryShell, Limit: 10,
	})
	wantOnly(t, page, shell)
}

// status likewise, and it is the filter the recall flow leans on hardest:
// "what failed here" is the question §10.4 exists for.
func TestQueryEntriesStatusExcludesEveryOtherStatus(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	ok := entryID(1)
	bad := entryID(2)
	pending := entryID(3)
	submitAt(t, led, ok, "local", "/repo", content.EntryShell, "make test")
	submitAt(t, led, bad, "local", "/repo", content.EntryShell, "make deploy")
	submitAt(t, led, pending, "local", "/repo", content.EntryShell, "make watch")
	zero := 0
	one := 1
	closeEntry(t, led, ok, content.EntrySuccess, &zero)
	closeEntry(t, led, bad, content.EntryFailure, &one)

	page := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Status: content.EntryFailure, Limit: 10,
	})
	wantOnly(t, page, bad)
}

// text is the recall overlay's search box, and it is the SAME predicate
// history.query has answered from command_history since nocx-ms7v: a
// case-insensitive substring over the recorded intent, applied WITHIN the
// rung. It is instr(), not LIKE, so "%" and "_" in the needle are literal
// characters and there is no wildcard grammar to learn — a search box that
// meant one thing before the cutover and another after it is a regression
// the user would meet as "my search stopped working" (nocx-rtg0.26).
func TestQueryEntriesTextExcludesEveryOtherIntent(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	envReadyHost(t, led, "remote", "deploy@prod.example.com")

	here := entryID(1)
	upper := entryID(2)
	unrelated := entryID(3)
	elsewhere := entryID(4)
	remote := entryID(5)
	decoy := entryID(6)
	literal := entryID(7)
	submitAt(t, led, here, "local", "/repo", content.EntryShell, "make deploy")
	submitAt(t, led, upper, "local", "/repo", content.EntryShell, "Make Deploy PROD")
	submitAt(t, led, unrelated, "local", "/repo", content.EntryShell, "rm -rf build")
	submitAt(t, led, elsewhere, "local", "/other", content.EntryShell, "make deploy")
	submitAt(t, led, remote, "remote", "/repo", content.EntryShell, "make deploy")
	// A LIKE pattern of "100%_done" matches this one — % any run, _ any one
	// character — and instr() does not. It is here so the literal case is
	// proved by exclusion rather than by the survivor alone.
	submitAt(t, led, decoy, "local", "/repo", content.EntryShell, "1000-and-done")
	submitAt(t, led, literal, "local", "/repo", content.EntryShell, "grep '100%_done'")

	t.Run("case-insensitive, and every intent that does not contain it is out", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeEverywhere, Text: "DEPLOY", Limit: 10,
		})
		wantOnly(t, page, remote, elsewhere, upper, here)
	})
	t.Run("the filter is applied within the rung, never instead of it", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeDirectory, EnvironmentID: "local", Cwd: "/repo",
			Text: "deploy", Limit: 10,
		})
		wantOnly(t, page, upper, here)
	})
	t.Run("percent and underscore are literal characters, not wildcards", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeEverywhere, Text: "100%_done", Limit: 10,
		})
		wantOnly(t, page, literal)
	})
	t.Run("the empty needle is no filter at all", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeEverywhere, Text: "", Limit: 10,
		})
		wantOnly(t, page, literal, decoy, remote, elsewhere, unrelated, upper, here)
	})
	t.Run("a needle nothing matches is an empty page from a store that has rows", func(t *testing.T) {
		page := queryOK(t, led, content.LedgerQuery{
			Scope: content.ScopeEverywhere, Text: "zzz-no-such-command", Limit: 10,
		})
		wantOnly(t, page)
		// The distinction the overlay renders as source=store rather than
		// source=session: the search found nothing, the ledger is not empty.
		if !page.HasRows {
			t.Fatal("hasRows is false while the ledger holds every row the needle missed")
		}
	})
}

// before is the PAGING cursor and it reads ingest_seq — the design's total
// order — never a wall clock and never the old path's rowid.
func TestQueryEntriesBeforePagesOnSeqAndExcludesTheRowsAlreadySeen(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	first := submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "one")
	submitAt(t, led, entryID(2), "local", "/repo", content.EntryShell, "two")
	third := submitAt(t, led, entryID(3), "local", "/repo", content.EntryShell, "three")

	before := third.IngestSeq
	page := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Before: &before, Limit: 10,
	})
	wantOnly(t, page, entryID(2), entryID(1))

	before = first.IngestSeq
	page = queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Before: &before, Limit: 10,
	})
	wantOnly(t, page)
}

// since is a wall-clock FLOOR and it reads submitted_at — the store's own
// stamp, present on every row. It is deliberately not ended_at, which is
// null while a command runs and would silently drop the running ones.
func TestQueryEntriesSinceExcludesWhatCameBeforeIt(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	older := submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "older")
	// Wait for the clock to leave the millisecond the first submission
	// landed in — an observable state change, never a duration.
	for time.Now().UnixMilli() <= older.SubmittedAt {
		time.Sleep(time.Millisecond)
	}
	newer := submitAt(t, led, entryID(2), "local", "/repo", content.EntryShell, "newer")

	since := newer.SubmittedAt
	page := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Since: &since, Limit: 10,
	})
	wantOnly(t, page, entryID(2))
}

// limit bounds the page, and the extra row it does not return is what
// Exhausted reports.
func TestQueryEntriesLimitBoundsThePageAndReportsExhaustion(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	for i := 1; i <= 3; i++ {
		submitAt(t, led, entryID(i), "local", "/repo", content.EntryShell, "cmd")
	}

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 2})
	wantOnly(t, page, entryID(3), entryID(2))
	if page.Exhausted {
		t.Fatal("Exhausted on a page with a further entry behind it")
	}

	page = queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 3})
	if !page.Exhausted {
		t.Fatal("not Exhausted on a page holding every entry there is")
	}
}

// ── HasRows: an empty answer and an unanswerable question ────────────────

// The subtle one. A store with rows that match nothing answers HasRows=true
// with an empty page; a store with no rows at all answers HasRows=false. The
// wire turns the first into source=store and the second into source=session,
// so collapsing them ships "no history" where it means "history is off".
func TestQueryEntriesHasRowsSeparatesEmptyAnswerFromEmptyStore(t *testing.T) {
	_, led := newLedger(t)

	empty := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if empty.HasRows {
		t.Fatal("HasRows on a store that holds nothing")
	}
	if empty.Entries == nil {
		t.Fatal("Entries is nil on an empty store — no matches is [], never null")
	}

	envReady(t, led, "local")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make test")

	answered := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeDirectory, EnvironmentID: "local", Cwd: "/nowhere", Limit: 10,
	})
	if len(answered.Entries) != 0 {
		t.Fatalf("the rung /nowhere answered %v", pageIDs(answered))
	}
	if !answered.HasRows {
		t.Fatal("HasRows is false while the store holds a row the rung did not match")
	}
	if answered.Entries == nil {
		t.Fatal("Entries is nil for a rung with no matches — no matches is [], never null")
	}
}

// ── coverage: the store-wide horizon ─────────────────────────────────────

// Coverage is the oldest retained entry's ended_at, store-wide — independent
// of the rung and of every filter, because retention is store-wide. It
// exists so a search under retention does not present a partial history as
// the whole one (§5.4).
func TestQueryEntriesCoverageIsStoreWideAndIndependentOfEveryFilter(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")
	envReadyHost(t, led, "remote", "deploy@prod.example.com")

	oldest := entryID(1)
	submitAt(t, led, oldest, "local", "/repo", content.EntryShell, "make test")
	submitAt(t, led, entryID(2), "remote", "/elsewhere", content.EntryShell, "make deploy")
	zero := 0
	closeEntry(t, led, oldest, content.EntrySuccess, &zero)
	closeEntry(t, led, entryID(2), content.EntrySuccess, &zero)

	row, err := led.Entry(ctx, oldest)
	if err != nil || row == nil || row.EndedAt == nil {
		t.Fatalf("Entry(%q) = %+v, %v — want a closed row with an end", oldest, row, err)
	}

	// A rung that excludes the oldest row still reports the oldest row's
	// horizon: the answer's coverage is the store's, not the page's.
	page := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeHost, EnvironmentID: "remote", Limit: 10,
	})
	wantOnly(t, page, entryID(2))
	if page.Coverage == nil {
		t.Fatal("Coverage is nil while the store holds completed rows")
	}
	if *page.Coverage != *row.EndedAt {
		t.Fatalf("Coverage = %d, want the store-wide oldest ended_at %d", *page.Coverage, *row.EndedAt)
	}

	// And the text filter is not an exception to it. A search that excludes
	// the oldest row still reports the oldest row's horizon: coverage says how
	// far back the STORE can see, so the overlay can tell the user their
	// search only looked at what retention left — a horizon that shrank to
	// the matches would say the opposite, and would say it most loudly when
	// the search found nothing.
	searched := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Text: "deploy", Limit: 10,
	})
	wantOnly(t, searched, entryID(2))
	if searched.Coverage == nil || *searched.Coverage != *row.EndedAt {
		t.Fatalf("Coverage under a text filter = %v, want the store-wide oldest ended_at %d",
			searched.Coverage, *row.EndedAt)
	}
	// The same for a needle nothing matches: an empty page still states the
	// store's horizon.
	missed := queryOK(t, led, content.LedgerQuery{
		Scope: content.ScopeEverywhere, Text: "zzz-no-such-command", Limit: 10,
	})
	wantOnly(t, missed)
	if missed.Coverage == nil || *missed.Coverage != *row.EndedAt {
		t.Fatalf("Coverage under a needle that matched nothing = %v, want the store-wide oldest ended_at %d",
			missed.Coverage, *row.EndedAt)
	}
}

// Nothing completed means no horizon to state — nil, never zero, which the
// overlay would render as 1970.
func TestQueryEntriesCoverageIsNilWhileNothingHasCompleted(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make watch")

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if page.Coverage != nil {
		t.Fatalf("Coverage = %d for a store where nothing has ended", *page.Coverage)
	}
}

// ── the request is refused rather than answered wrongly ──────────────────

// A value the closed enums do not name is a rejected request, never an empty
// result set: an empty page for a misspelled status reads as "nothing ever
// failed here", which is the answer most likely to be believed.
func TestQueryEntriesRefusesWhatItCannotAnswer(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make test")

	bad := map[string]content.LedgerQuery{
		"unknown scope":            {Scope: content.Scope("recent"), Limit: 10},
		"unknown kind":             {Scope: content.ScopeEverywhere, Kind: content.EntryKind("script"), Limit: 10},
		"unknown status":           {Scope: content.ScopeEverywhere, Status: content.EntryStatus("ok"), Limit: 10},
		"limit below one":          {Scope: content.ScopeEverywhere, Limit: 0},
		"limit above the ceiling":  {Scope: content.ScopeEverywhere, Limit: content.MaxLedgerPageLimit + 1},
		"directory with no rung":   {Scope: content.ScopeDirectory, Cwd: "/repo", Limit: 10},
		"host with no environment": {Scope: content.ScopeHost, Limit: 10},
	}
	for name, q := range bad {
		t.Run(name, func(t *testing.T) {
			page, err := led.QueryEntries(context.Background(), q)
			if err == nil {
				t.Fatalf("QueryEntries(%+v) answered %v rather than refusing", q, pageIDs(page))
			}
			if len(page.Entries) != 0 {
				t.Fatalf("QueryEntries returned %d rows alongside its refusal", len(page.Entries))
			}
		})
	}
}

// The one external call this read makes is the query itself, and a closed
// store is how it fails. It reports the failure rather than answering with
// an empty page, which cannot be told from "no history".
func TestQueryEntriesAfterCloseReportsTheFailure(t *testing.T) {
	db, led := newLedger(t)
	envReady(t, led, "local")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make test")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	page, err := led.QueryEntries(context.Background(), content.LedgerQuery{
		Scope: content.ScopeEverywhere, Limit: 10,
	})
	if err == nil {
		t.Fatalf("QueryEntries after Close = (%v, nil), want an error", pageIDs(page))
	}
	if len(page.Entries) != 0 {
		t.Fatalf("QueryEntries after Close returned %d rows alongside its error", len(page.Entries))
	}
	if page.HasRows {
		t.Fatal("QueryEntries after Close claimed the store has rows")
	}
}

// ── the terminal facts the recall read renders ───────────────────────────

// A page row carries what history.query's contract declares on every entry:
// the start, the end, the measured duration and the kind payload the exit
// code and the redaction receipt live in. Without them the page is a list of
// command lines and the cutover has nothing to render.
func TestQueryEntriesRowsCarryTheirTerminalFacts(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	id := entryID(1)
	submitAt(t, led, id, "local", "/repo", content.EntryShell, "make test")
	seven := 7
	closeEntry(t, led, id, content.EntryFailure, &seven)

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	wantOnly(t, page, id)
	row := page.Entries[0]
	if row.Status != content.EntryFailure {
		t.Fatalf("status = %q, want failure", row.Status)
	}
	if row.EndedAt == nil {
		t.Fatal("a closed row carries no ended_at — the overlay renders the relative time from it")
	}
	exit, err := content.ShellExitCodeOf(row.Payload)
	if err != nil {
		t.Fatalf("ShellExitCodeOf: %v", err)
	}
	if exit == nil || *exit != 7 {
		t.Fatalf("exit code = %v, want 7", exit)
	}
}

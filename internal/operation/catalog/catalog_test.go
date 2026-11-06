package catalog

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// copyDeclarations is a DEEP copy: Declarations() copies the slice header but
// the phase, resource, dependency and preemption slices inside it are shared.
// A planted test that appends to one of them with spare capacity wrote into
// the real table, and the next -count run saw ack with a nameless phase
// (run 42139974476).
func copyDeclarations() []Declaration {
	table := Declarations()
	out := make([]Declaration, len(table))
	for i, d := range table {
		phases := make([]Phase, len(d.Phases))
		for j, p := range d.Phases {
			p.Resources = append([]Resource(nil), p.Resources...)
			p.Dependencies = append([]Domain(nil), p.Dependencies...)
			phases[j] = p
		}
		d.Phases = phases
		d.Preempts = append([]ID(nil), d.Preempts...)
		out[i] = d
	}
	return out
}

func declarationByID(t *testing.T, id ID) Declaration {
	t.Helper()
	for _, d := range Declarations() {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no declaration for %s", id)
	return Declaration{}
}

// beginStrictForTest is the test-built scope: a second settlement panics.
// It lives here rather than in catalog.go because nothing in production wants
// it, and the dead-code ratchet is right about that.
func beginStrictForTest(ctx context.Context, op Method, inv Invocation) *Scope {
	s := Begin(ctx, op, inv)
	s.strict = true
	return s
}

func TestEveryDeclarationIsCompleteAndFinite(t *testing.T) {
	if errs := Validate(Declarations()); len(errs) != 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}
	for _, d := range Declarations() {
		if d.Turn.Millis <= 0 || d.Max.Millis <= 0 {
			t.Errorf("%s: turn/max must be finite, got turn=%d max=%d", d.ID, d.Turn.Millis, d.Max.Millis)
		}
		if d.Max.Basis != BasisRead && d.Max.Basis != BasisMeasured {
			t.Errorf("%s: max basis %q does not say where the deadline came from", d.ID, d.Max.Basis)
		}
		if len(d.Phases) == 0 {
			t.Errorf("%s: no phases", d.ID)
		}
	}
}

func TestAPlantedZeroLifetimeIsRefused(t *testing.T) {
	table := copyDeclarations()
	table[0].Max = Deadline{}
	errs := Validate(table)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "max is not a finite") {
		t.Fatalf("a zero max must be refused, got %v", errs)
	}

	table = copyDeclarations()
	table[0].Turn = Deadline{Millis: 0, Basis: BasisRead, Note: "planted"}
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("a zero turn must be refused")
	}

	table = copyDeclarations()
	table[0].Silence = Bound{Kind: BoundDeadline, Millis: 0, Basis: BasisRead, Note: "planted"}
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("a zero silence deadline must be refused")
	}
}

func TestAPlantedUnlimitedSilenceIsRefused(t *testing.T) {
	table := copyDeclarations()
	table[0].Silence = Bound{Kind: "unlimited"}
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("an unlimited silence kind must be refused")
	}
}

func TestAPlantedMergeOnAnEffectfulMutationIsRefused(t *testing.T) {
	table := copyDeclarations()
	for i := range table {
		if table[i].Expired == ExpiredOutcomeUnknown && table[i].Merge == MergeNone {
			table[i].Merge = MergeLatestValue
			break
		}
	}
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("an effectful mutation with merge latest-value must be refused")
	}
}

func TestAPlantedUnknownPreemptionIsRefused(t *testing.T) {
	table := copyDeclarations()
	for i := range table {
		if table[i].ID == "close" {
			table[i].Preempts = []ID{"no.such.operation"}
			break
		}
	}
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("preempts of an undeclared operation must be refused")
	}
}

func TestAPlantedUnreferencedHandleIsRefused(t *testing.T) {
	decls := []Declaration{
		{ID: "declared.andReferenced"},
		{ID: "declared.butUnreferenced"},
	}
	sources := map[string]string{
		"internal/x/x.go": "var _ = catalog.OpDeclaredAndReferenced\n",
	}
	missing := UnreferencedHandles(decls, sources)
	if len(missing) != 1 || missing[0] != "declared.butUnreferenced" {
		t.Fatalf("unreferenced handles = %v, want [declared.butUnreferenced]", missing)
	}
}

func TestEveryDeclaredHandleIsReferencedInProduction(t *testing.T) {
	missing, err := CheckHandleReferences(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("declarations with no production reference: %v", missing)
	}
}

func TestGeneratedHandlesMatchTheTable(t *testing.T) {
	path := filepath.Join("handles_gen.go")
	committed, err := os.ReadFile(path) //nolint:gosec // repo-relative generated file
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, HandleSource()) {
		t.Fatal("handles_gen.go is stale; run go run ./internal/operation/catalog/gen")
	}
	seen := map[string]ID{}
	for _, d := range Declarations() {
		name := HandleName(d.ID)
		if other, dup := seen[name]; dup {
			t.Fatalf("handle %s names both %s and %s", name, other, d.ID)
		}
		seen[name] = d.ID
	}
}

func TestManifestMatchesTheTable(t *testing.T) {
	path := filepath.Join(repoRoot(t), "contracts/operations.json")
	committed, err := os.ReadFile(path) //nolint:gosec // repo-relative generated file
	if err != nil {
		t.Fatal(err)
	}
	want, err := ManifestJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, want) {
		t.Fatal("contracts/operations.json is stale; run go run ./internal/operation/catalog/gen")
	}
}

func testContextAndSlog(t *testing.T) (context.Context, *slog.Logger) {
	t.Helper()
	sl := logtest.Slog(t)
	return log.WithLogger(context.Background(), log.NewSlogAdapter(sl)), sl
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, lg := logtest.New(t)
	return log.WithLogger(ctx, lg)
}

func TestSettlementIsExclusiveAndStrictScopesPanicOnASecondSettle(t *testing.T) {
	s := beginStrictForTest(testContext(t), OpTransportPing, Invocation{})
	if !s.Settle(OutcomeCompleted) {
		t.Fatal("first settlement must win")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a second settlement in a strict (test-built) scope must panic")
		}
	}()
	s.Settle(OutcomeCancelled)
}

func TestCompletionAndCancellationRaceSettlesExactlyOnce(t *testing.T) {
	const attempts = 200
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		s := Begin(testContext(t), OpTransportPing, Invocation{})
		_ = ctx
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); s.Settle(OutcomeCompleted) }()
		go func() { defer wg.Done(); s.Settle(OutcomeCancelled) }()
		go func() { defer wg.Done(); s.Settle(OutcomeExpired) }()
		wg.Wait()
		if _, ok := s.Outcome(); !ok {
			t.Fatal("the scope has no outcome after a three-way race")
		}
		cancel()
	}
}

func TestAnExpiredScopeRecordsTheExpiry(t *testing.T) {
	d := declarationByID(t, "transport.ping")
	if d.Max.Duration() > time.Second {
		t.Fatalf("transport.ping max is %s; the test needs a short one", d.Max.Duration())
	}
	s := beginStrictForTest(testContext(t), OpTransportPing, Invocation{})
	deadline := time.Now().Add(d.Max.Duration() + 2*time.Second)
	for time.Now().Before(deadline) {
		if outcome, ok := s.Outcome(); ok {
			if outcome != OutcomeExpired {
				t.Fatalf("outcome = %q, want expired", outcome)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the max deadline did not settle the scope")
}

func TestAPlantedUnknownFailureDomainIsRefused(t *testing.T) {
	table := copyDeclarations()
	table[0].Phases[0].Dependencies = append(table[0].Phases[0].Dependencies, Domain("nowhere"))
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("an unknown failure domain must be refused")
	}
}

func TestAPlantedInvalidResourceIsRefused(t *testing.T) {
	table := copyDeclarations()
	table[0].Phases[0].Resources = append(table[0].Phases[0].Resources, Resource{Kind: ResourceStore, Mode: ModeKey})
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("a store resource with no name must be refused")
	}

	table = copyDeclarations()
	table[0].Phases[0].Resources = append(table[0].Phases[0].Resources, Resource{Kind: ResourceBarrier, Name: "x", Mode: ModeKey})
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("a barrier held as a per-instance key must be refused")
	}
}

func TestAPlantedNamelessPhaseIsRefused(t *testing.T) {
	table := copyDeclarations()
	table[0].Phases[0].Name = ""
	if errs := Validate(table); len(errs) == 0 {
		t.Fatal("a phase with no name must be refused")
	}
}

func TestEveryLoopNamesAnInternalOperationAndABoundedStop(t *testing.T) {
	if errs := ValidateLoops(loopTable, Declarations()); len(errs) != 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}
}

func TestAPlantedUnboundedLoopStopIsRefused(t *testing.T) {
	loops := append([]Loop(nil), loopTable...)
	loops[0].Stop = Deadline{}
	if errs := ValidateLoops(loops, Declarations()); len(errs) == 0 {
		t.Fatal("a loop with no finite stop must be refused")
	}
}

func TestAPlantedLoopWithNoIterationIsRefused(t *testing.T) {
	loops := append([]Loop(nil), loopTable...)
	loops[0].Iteration = "no.such.operation"
	if errs := ValidateLoops(loops, Declarations()); len(errs) == 0 {
		t.Fatal("a loop whose iteration has no declaration must be refused")
	}
}

// A routine success is not worth a record. The log is read by the e2e suite's
// transcript test and has a size ceiling; one line per completed operation
// pushed the readiness line out of it (run 42139974476).
func TestASettledOperationWritesNoRecord(t *testing.T) {
	ctx, sl := testContextAndSlog(t)
	s := beginStrictForTest(ctx, OpTransportPing, Invocation{})
	if !s.Complete() {
		t.Fatal("the first settlement must win")
	}
	if got := logtest.RecordsSlog(sl); len(got) != 0 {
		t.Fatalf("a routine completion wrote %d record(s): %v", len(got), got)
	}
}

// An expiry is the record that matters: it is the operation nobody is
// watching any more. Exactly one, and it says so.
func TestAnExpiredOperationWritesExactlyOneRecord(t *testing.T) {
	ctx, sl := testContextAndSlog(t)
	s := beginStrictForTest(ctx, OpTransportPing, Invocation{})
	if !logtest.WaitForSlog(sl, 3*time.Second, func(r logtest.Record) bool {
		return r.Message == "operation deadline expired"
	}) {
		t.Fatal("the max deadline wrote no expiry record")
	}
	records := logtest.RecordsSlog(sl)
	if len(records) != 1 {
		t.Fatalf("expiry wrote %d records, want exactly 1: %v", len(records), records)
	}
	if outcome, ok := s.Outcome(); !ok || outcome != OutcomeExpired {
		t.Fatalf("outcome = %q (settled=%v), want expired", outcome, ok)
	}
}

// A mutation of the copy a planted test works on must never reach the
// catalogue another test (or the generator) reads.
func TestDeclarationsReturnsAnIndependentCopy(t *testing.T) {
	before := Declarations()[0]
	table := copyDeclarations()
	table[0].Phases[0].Name = "planted"
	table[0].Phases[0].Resources = append(table[0].Phases[0].Resources, Resource{Kind: ResourceStore, Mode: ModeKey})
	table[0].Phases[0].Dependencies = append(table[0].Phases[0].Dependencies, Domain("nowhere"))
	table[0].Preempts = append(table[0].Preempts, ID("no.such.operation"))

	after := Declarations()[0]
	if after.Phases[0].Name != before.Phases[0].Name {
		t.Fatalf("a planted mutation changed the real phase name: %q -> %q", before.Phases[0].Name, after.Phases[0].Name)
	}
	if len(after.Phases[0].Resources) != len(before.Phases[0].Resources) {
		t.Fatalf("a planted mutation changed the real resources: %d -> %d", len(before.Phases[0].Resources), len(after.Phases[0].Resources))
	}
	if len(after.Phases[0].Dependencies) != len(before.Phases[0].Dependencies) {
		t.Fatalf("a planted mutation changed the real dependencies: %d -> %d", len(before.Phases[0].Dependencies), len(after.Phases[0].Dependencies))
	}
	if len(after.Preempts) != len(before.Preempts) {
		t.Fatalf("a planted mutation changed the real preemptions: %d -> %d", len(before.Preempts), len(after.Preempts))
	}
}

// A child operation's max is the minimum of its own and its parent's
// remainder (ADR-8eej3e66 §3). Stage 2 records that bound; it does not cancel.
func TestAChildScopeStaysWithinItsParentsRemainder(t *testing.T) {
	ctx, _ := testContextAndSlog(t)
	parent := Begin(ctx, OpTransportPing, Invocation{})
	child := Begin(WithScope(ctx, parent), OpAck, Invocation{})
	if child.maxDeadline.After(parent.maxDeadline) {
		t.Fatalf("child max %s is later than parent max %s", child.maxDeadline, parent.maxDeadline)
	}

	shortParent := Begin(ctx, OpAck, Invocation{})
	shortChild := Begin(WithScope(ctx, shortParent), OpTransportPing, Invocation{})
	if !shortChild.maxDeadline.Before(shortParent.maxDeadline) {
		t.Fatalf("a child with its own shorter max was stretched: child %s parent %s", shortChild.maxDeadline, shortParent.maxDeadline)
	}
}

// A cancellation after the scope settled must write nothing, and must not do
// it from the AfterFunc goroutine while the caller reads the log. The root
// package's release-diagnostics test owns its own unsynchronised buffer; this
// is the same shape (run 42139974476's follow-up, CI fdfc8d519).
func TestALateCancellationWritesNothingAfterSettlement(t *testing.T) {
	var buf bytes.Buffer
	lg := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil)))
	for i := 0; i < 200; i++ {
		ctx := log.WithLogger(context.Background(), lg)
		ctx, cancel := context.WithCancel(ctx)
		cancel() // the callback is already due when Begin registers it
		s := Begin(ctx, OpTransportPing, Invocation{})
		s.Complete()
		// The callback runs on its own goroutine and cannot be joined. Yield
		// so it reaches its settlement while this goroutine is about to read
		// the buffer the caller owns.
		runtime.Gosched()
		_ = buf.String()
	}
	if got := buf.String(); got != "" {
		t.Fatalf("a cancelled invocation wrote after it settled: %s", got)
	}
}

// blockingExpiryHandler parks inside Warn until released, so a test can see
// whether a caller that settles second waits for the timer's record.
type blockingExpiryHandler struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	buf     *bytes.Buffer
}

func (h *blockingExpiryHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockingExpiryHandler) Handle(_ context.Context, r slog.Record) error {
	h.once.Do(func() { close(h.started) })
	<-h.release
	h.buf.WriteString(r.Message)
	return nil
}
func (h *blockingExpiryHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockingExpiryHandler) WithGroup(string) slog.Handler      { return h }

// The max timer may win the settlement and still be writing its expiry line
// when the caller's Complete arrives. Complete must join that write before
// returning; otherwise the caller reads its own log while the timer writes
// (CI fdfc8d519's follow-up).
func TestACompletedScopeWaitsForTheExpiryWriterItLostTo(t *testing.T) {
	var buf bytes.Buffer
	h := &blockingExpiryHandler{started: make(chan struct{}), release: make(chan struct{}), buf: &buf}
	ctx := log.WithLogger(context.Background(), log.NewSlogAdapter(slog.New(h)))

	s := Begin(ctx, OpTransportPing, Invocation{})
	select {
	case <-h.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the max timer never expired")
	}
	completed := make(chan bool, 1)
	go func() { completed <- s.Complete() }()
	select {
	case <-completed:
		t.Fatal("Complete returned while the expiry writer was still writing")
	case <-time.After(100 * time.Millisecond):
	}
	close(h.release)
	select {
	case won := <-completed:
		if won {
			t.Fatal("Complete must lose to the expiry that already settled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete never returned after the writer finished")
	}
	if got := buf.String(); got != "operation deadline expired" {
		t.Fatalf("the expiry record was not completed before Complete returned: %q", got)
	}
}

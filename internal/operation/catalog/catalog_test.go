package catalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func copyDeclarations() []Declaration {
	table := Declarations()
	out := make([]Declaration, len(table))
	copy(out, table)
	return out
}

func declarationByID(t *testing.T, id ID) Declaration {
	t.Helper()
	d, ok := DeclarationOf(id)
	if !ok {
		t.Fatalf("no declaration for %s", id)
	}
	return d
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
	committed, err := os.ReadFile(path)
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
	committed, err := os.ReadFile(path)
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

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, lg := logtest.New(t)
	return log.WithLogger(ctx, lg)
}

func TestSettlementIsExclusiveAndStrictScopesPanicOnASecondSettle(t *testing.T) {
	s := BeginStrict(testContext(t), OpTransportPing, Invocation{})
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
	s := BeginStrict(testContext(t), OpTransportPing, Invocation{})
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
	if errs := ValidateLoops(Loops(), Declarations()); len(errs) != 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}
}

func TestAPlantedUnboundedLoopStopIsRefused(t *testing.T) {
	loops := Loops()
	loops[0].Stop = Deadline{}
	if errs := ValidateLoops(loops, Declarations()); len(errs) == 0 {
		t.Fatal("a loop with no finite stop must be refused")
	}
}

func TestAPlantedLoopWithNoIterationIsRefused(t *testing.T) {
	loops := Loops()
	loops[0].Iteration = "no.such.operation"
	if errs := ValidateLoops(loops, Declarations()); len(errs) == 0 {
		t.Fatal("a loop whose iteration has no declaration must be refused")
	}
}

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/sandbox"
)

func diagnosticFixture(t *testing.T, path string) (*nativeInbox, sandbox.DiagnosticRecord) {
	t.Helper()
	inbox := newNativeInbox(sandbox.Policy{}, "diagnostic-session")
	t.Cleanup(inbox.stopWorkers)
	inbox.record(sandbox.DiagnosticObservation{Path: path, PathKnown: true, Executable: "/bin/consumer", Operation: "openat", Access: sandbox.DiagnosticRead, Source: sandbox.DiagnosticLinuxSeccomp, Precision: sandbox.PrecisionAttempted})
	return inbox, inbox.page(0, 1).Records[0]
}

func TestNativeInboxRetargetRequiresNewConfirmation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "target")
	canonicalDirectory, canonicalErr := filepath.EvalSymlinks(directory)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	inbox, captured := diagnosticFixture(t, path)
	if captured.Proposal == nil || captured.Proposal.Directory != canonicalDirectory || captured.Proposal.Basis != "parent" {
		t.Fatalf("incorrect file-parent proposal: %+v", captured.Proposal)
	}
	if err := os.Rename(path, path+"-retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.reserve(captured.ID, captured.Revision, sandbox.DecisionAllowRO); !errors.Is(err, errDiagnosticRetarget) {
		t.Fatalf("retarget was not refused: %v", err)
	}
	fresh := inbox.page(0, 1).Records[0]
	if fresh.Revision == captured.Revision || fresh.State != sandbox.DiagnosticUnresolved {
		t.Fatalf("retarget did not invalidate confirmation: %+v", fresh)
	}
	if _, _, err := inbox.reserve(captured.ID, captured.Revision, sandbox.DecisionAllowRO); !errors.Is(err, errDiagnosticConflict) {
		t.Fatalf("old confirmation became usable: %v", err)
	}
	token, _, err := inbox.reserve(fresh.ID, fresh.Revision, sandbox.DecisionAllowRO)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.finish(fresh.ID, token, true, 7); err != nil {
		t.Fatal(err)
	}
}

func TestNativeInboxMissingTargetCreationInvalidatesParentProposal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-yet-created")
	inbox, row := diagnosticFixture(t, path)
	if row.Proposal == nil || !row.Proposal.MissingTarget {
		t.Fatalf("missing target not identified: %+v", row.Proposal)
	}
	if err := os.WriteFile(path, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionAllowRW); !errors.Is(err, errDiagnosticRetarget) {
		t.Fatalf("new target reused missing-target confirmation: %v", err)
	}
}

func TestNativeInboxOnlyOneResolverCanCommitAndReceiptIsIdempotent(t *testing.T) {
	inbox, row := diagnosticFixture(t, t.TempDir())
	type answer struct {
		token string
		err   error
	}
	answers := make(chan answer, 2)
	var started sync.WaitGroup
	started.Add(2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			started.Done()
			<-start
			token, _, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionAllowRO)
			answers <- answer{token, err}
		}()
	}
	started.Wait()
	close(start)
	first, second := <-answers, <-answers
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil || first.token == "" || second.err == nil || second.token != "" {
		t.Fatalf("double resolver admitted: %+v %+v", first, second)
	}
	committed, err := inbox.finish(row.ID, first.token, true, 9)
	if err != nil {
		t.Fatal(err)
	}
	again, err := inbox.finish(row.ID, first.token, true, 9)
	if err != nil || again.Revision != committed.Revision || again.FutureRevision != 9 || again.State != sandbox.DiagnosticFuturePolicy {
		t.Fatalf("commit replay changed receipt: %+v %v", again, err)
	}
	if _, _, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionAllowRW); !errors.Is(err, errDiagnosticPending) {
		t.Fatalf("resolved event allowed a different write: %v", err)
	}
	if inbox.resolving != 0 {
		t.Fatal("committed reservation leaked a slot")
	}
}

func TestNativeInboxUnknownAccessCannotBecomeFutureAuthority(t *testing.T) {
	inbox := newNativeInbox(sandbox.Policy{}, "unknown-session")
	t.Cleanup(inbox.stopWorkers)
	inbox.record(sandbox.DiagnosticObservation{Path: t.TempDir(), PathKnown: true, Operation: "unknown", Access: sandbox.DiagnosticUnknown, Source: sandbox.DiagnosticLinuxSeccomp, Precision: sandbox.PrecisionUnknown})
	row := inbox.page(0, 1).Records[0]
	if row.Proposal != nil || row.Prediction != sandbox.PredictionUnknown {
		t.Fatalf("unknown event gained proposal: %+v", row)
	}
	if _, _, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionAllowRO); !errors.Is(err, errDiagnosticUnknown) {
		t.Fatalf("unknown allowed: %v", err)
	}
	_, dismissed, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionDismiss)
	if err != nil || dismissed.State != sandbox.DiagnosticDismissed {
		t.Fatalf("unknown event could not be dismissed: %+v %v", dismissed, err)
	}
}

func TestNativeInboxCapacityAndPaginationRetainBoundedHistory(t *testing.T) {
	inbox := newNativeInbox(sandbox.Policy{}, "bounded-session")
	t.Cleanup(inbox.stopWorkers)
	for index := range nativeInboxCapacity + 1 {
		inbox.record(sandbox.DiagnosticObservation{Path: "/unknown/" + strconv.Itoa(index), Operation: "openat", Access: sandbox.DiagnosticUnknown, Source: sandbox.DiagnosticLinuxSeccomp, Precision: sandbox.PrecisionAttempted})
	}
	first := inbox.page(0, 200)
	second := inbox.page(first.NextCursor, 200)
	last := inbox.page(second.NextCursor, 200)
	if first.Total != 500 || first.Dropped != 1 || first.NextCursor != 200 || second.NextCursor != 400 || last.NextCursor != 0 {
		t.Fatalf("incorrect bounded page counters: %+v %+v %+v", first, second, last)
	}
	if first.Records[0].ID != "bounded-session:1" || second.Records[0].ID != "bounded-session:201" || last.Records[99].ID != "bounded-session:500" {
		t.Fatal("pagination skipped or repeated the boundary records")
	}
}

func TestNativeInboxSinkOverflowDoesNotWaitForWorkers(t *testing.T) {
	inbox := &nativeInbox{queue: make(chan sandbox.DiagnosticObservation, nativeInboxQueueCapacity)}
	for range nativeInboxQueueCapacity + 1 {
		inbox.Observe(sandbox.DiagnosticObservation{})
	}
	if inbox.dropped.Load() != 1 || len(inbox.queue) != nativeInboxQueueCapacity {
		t.Fatal("queue overflow was not dropped at the declared boundary")
	}
}

func TestNativeInboxUncertainWriteCannotBeRetriedAsFreshCAS(t *testing.T) {
	inbox, row := diagnosticFixture(t, t.TempDir())
	token, _, err := inbox.reserve(row.ID, row.Revision, sandbox.DecisionAllowRO)
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := inbox.markUncertain(row.ID, token)
	if err != nil || uncertain.State != sandbox.DiagnosticUncertain {
		t.Fatalf("ambiguous write was not quarantined: %+v %v", uncertain, err)
	}
	if _, _, err := inbox.reserve(row.ID, uncertain.Revision, sandbox.DecisionAllowRO); !errors.Is(err, errDiagnosticPending) {
		t.Fatalf("ambiguous write admitted another CAS: %v", err)
	}
	if _, err := inbox.finish(row.ID, token, false, 0); !errors.Is(err, errDiagnosticConflict) {
		t.Fatalf("unknown write was reported as rolled back: %v", err)
	}
}

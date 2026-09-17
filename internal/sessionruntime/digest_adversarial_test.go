package sessionruntime

// An independent adversarial test for nocx-6q1uh.13 ("the owner and token"
// part), isolating one promise of spec §5.5 at the sessionruntime level
// rather than through the helper's owner (internal/helper/session's own
// TestReplyStormWithBlockedWriterOverflowsReserveAndDrainContinues drives the
// integration half of the same scenario, but stops short of this assertion —
// see that test's own doc for why).
//
// Spec §5.5: once the reply reserve overflows, "this incarnation's
// completeness becomes lost ... every later intent on that incarnation is
// refused completeness_unknown by Commit, visibly, rather than the pane
// silently falling behind." harness_test.go's own directReplySink carries a
// comment saying exactly this reserve-overflow schedule belongs to
// owner_test.go, not to this package — but the REFUSAL half of the promise
// (Commit's own gate) is this package's to keep, and no schedule in
// contract_test.go drives it: TestReportHoleNamesTheLoss only checks that
// Completeness() reports CompletenessLostIngest after ReportHole, never that
// a write is refused afterward, and "Execute while completeness is unknown"
// (contract_test.go) is built over newUnestablishedModel — the ZERO value,
// CompletenessUnknown — which is a different state from the one a reserve
// overflow or a reported hole actually produces.

import (
	"errors"
	"testing"
)

// TestCommitRefusesOnceCompletenessBecomesLostIngestNotOnlyWhenUnknown
// drives completeness to CompletenessLostIngest through the same public API
// TestReportHoleNamesTheLoss already uses (ReportHole), then attempts to
// Commit a plain, otherwise-valid key intent.
//
// Reading runtime.go's own Commit: its ONLY completeness gate is
//
//	if s.completeness == CompletenessUnknown { return nil, ErrCompletenessUnknown }
//
// which never mentions CompletenessLostIngest — the state deliverReplyLocked
// (the reserve overflow's own producer) and ReportHole both set. If that
// remains true, this test fails with the write succeeding (IntentStateExecuted)
// instead of being refused, which is the exact gap this task's report names:
// spec §5.5's "every later intent ... refused completeness_unknown" holds
// only for an incarnation that never established anything, never for one
// that lost output it once had.
func TestCommitRefusesOnceCompletenessBecomesLostIngestNotOnlyWhenUnknown(t *testing.T) {
	s, _, _ := realRuntime(t)

	ctrl, err := s.GrantControl(agent())
	if err != nil {
		t.Fatalf("grant control: %v", err)
	}

	if err = s.ReportHole(3); err != nil {
		t.Fatalf("report a hole: %v", err)
	}
	if got := s.Completeness(); got != CompletenessLostIngest {
		t.Fatalf("completeness after ReportHole is %v, want CompletenessLostIngest", got)
	}

	id, err := s.Admit(Intent{At: s.Incarnation(), Under: ctrl.Epoch, By: ctrl.Holder, Kind: IntentKindKey, Payload: []byte("l")})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	if _, err := s.Commit(Intent{ID: id}, nil); !errors.Is(err, ErrCompletenessUnknown) {
		t.Fatalf("commit while completeness is lost-ingest: got %v, want %v (spec %s)",
			err, ErrCompletenessUnknown, "§5.5")
	}
}

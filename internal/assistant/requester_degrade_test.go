package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestLeaseResultExpiredSubmissionDoesNotClaimTermination(t *testing.T) {
	got := leaseResult("run", &RunLeaseError{
		Reason:            content.TermTimeout,
		Err:               context.Canceled,
		SubmissionExpired: true,
	})
	lower := strings.ToLower(got)
	if strings.Contains(lower, "terminalized") || strings.Contains(got, "TERMINATED") {
		t.Fatalf("expired submission result = %q, must not claim termination", got)
	}
	if !strings.Contains(got, "run submission expired before execution started") {
		t.Fatalf("expired submission result = %q, want the submission-expired sentence", got)
	}
}

func TestRunLeaseSentenceAndUnavailableSentenceNameTheSameBound(t *testing.T) {
	terminated := RunLeaseSentence(content.TermInactivity, false)
	unavailable := RunLeaseUnavailableSentence(RunLeaseBoundInactivity)

	if !strings.Contains(terminated, "inactivity") || !strings.Contains(terminated, "terminalized") {
		t.Fatalf("terminalized wording = %q, want inactivity and terminalized", terminated)
	}
	if !strings.Contains(unavailable, "inactivity") || !strings.Contains(unavailable, "not active") {
		t.Fatalf("unavailable wording = %q, want inactivity and not active", unavailable)
	}
}

func TestRunLeaseUnavailableBoundsNamesEnabledBounds(t *testing.T) {
	got := RunLeaseUnavailableBounds(true, true)
	want := []RunLeaseBound{RunLeaseBoundInactivity, RunLeaseBoundOutput}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unavailable bounds = %v, want %v", got, want)
	}
	if got := RunLeaseUnavailableBounds(false, false); got != nil {
		t.Fatalf("unavailable bounds with no dependent bounds = %v, want nil", got)
	}
}

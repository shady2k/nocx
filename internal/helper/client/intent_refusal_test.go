package client_test

// The two target-mint refusals a coordinator can act on (nocx-xn63t.4.1,
// spec §6.1, §6.2): `capacity` — the session's token book is full and by spec
// nothing is ever evicted, so the next step is to wait — and `snapshot_gone`
// — the retained snapshot the target would have named is gone, so the next
// step is a fresh read.
//
// The codes are the HELPER's vocabulary and the sentinels are this client's,
// which is a pair that can drift: rename the code in
// internal/helper/session.Service.Refusal and the classifier quietly stops
// matching, costing a caller the sentence that tells it what to do. So the
// first test asks the REAL service what it writes, rather than restating the
// string a second time.

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// TestTheClassifiedCodesAreTheOnesTheHelperWrites drives the shipped session
// service's own refusal mapping (session.Refusal, the one function the host
// asks what code an error has) and requires this client's sentinels to be
// reachable from exactly those codes.
func TestTheClassifiedCodesAreTheOnesTheHelperWrites(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{Generation: "testhash", Log: log})
	t.Cleanup(svc.Close)

	for _, tc := range []struct {
		name string
		err  error
		code string
		want error
	}{
		{"capacity", session.ErrCapacity, proto.ErrCodeCapacity, client.ErrTargetCapacity},
		{"snapshot_gone", session.ErrSnapshotGone, proto.ErrCodeSnapshotGone, client.ErrSnapshotGone},
	} {
		code, _ := svc.Refusal(tc.err)
		if code != tc.code {
			t.Fatalf("%s: the helper writes code %q, this client classifies %q — one of the two moved", tc.name, code, tc.code)
		}
		classified := client.ClassifyTargetRefusal(&client.RefusalError{Code: code, Message: "session: " + code})
		if !errors.Is(classified, tc.want) {
			t.Errorf("%s: ClassifyTargetRefusal(%q) = %v, want it to carry %v", tc.name, code, classified, tc.want)
		}
		// The original refusal stays in the chain: everything that logged or
		// reported the helper's own message keeps working.
		var refusal *client.RefusalError
		if !errors.As(classified, &refusal) {
			t.Errorf("%s: the refusal itself was dropped from the chain: %v", tc.name, classified)
		}
	}
}

// TestAnUnclassifiedRefusalStaysOpaque: a code this build does not know is
// not guessed at, and a caller-passed nil is answered with nil rather than
// with a sentinel invented for it.
func TestAnUnclassifiedRefusalStaysOpaque(t *testing.T) {
	unknown := &client.RefusalError{Code: "some_future_code", Message: "session: some_future_code"}
	classified := client.ClassifyTargetRefusal(unknown)
	if classified != error(unknown) {
		t.Fatalf("an unknown code was rewritten as %v, want the refusal it arrived as", classified)
	}
	if errors.Is(classified, client.ErrTargetCapacity) || errors.Is(classified, client.ErrSnapshotGone) {
		t.Fatalf("an unknown code matched a target refusal: %v", classified)
	}
	if client.ClassifyTargetRefusal(nil) != nil {
		t.Fatal("ClassifyTargetRefusal(nil) invented an error")
	}
	plain := errors.New("helper: connection lost")
	if client.ClassifyTargetRefusal(plain) != plain {
		t.Fatalf("a non-refusal error was rewritten: %v", client.ClassifyTargetRefusal(plain))
	}
}

// TestTheTwoTargetSentinelsAreDistinct guards the split itself: one sentence
// and one sentinel for both would tell a caller to wait when what it needs is
// a fresh read, which is the failure this pair exists to prevent (the same
// reason ErrNotHeld and ErrNotDelegated are separate on the workers side).
func TestTheTwoTargetSentinelsAreDistinct(t *testing.T) {
	if errors.Is(client.ErrTargetCapacity, client.ErrSnapshotGone) || errors.Is(client.ErrSnapshotGone, client.ErrTargetCapacity) {
		t.Fatal("the two target refusals share a sentinel")
	}
	if client.ErrTargetCapacity.Error() == client.ErrSnapshotGone.Error() {
		t.Fatal("the two target refusals read the same")
	}
}

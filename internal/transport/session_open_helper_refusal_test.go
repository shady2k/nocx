package transport

import (
	"errors"
	"strings"
	"testing"
)

// THE THREE PARTS OF A LOCAL HELPER REFUSAL, AT THE SEAM THAT PRODUCES THEM
// (nocx-ie23r.4, ADR-0057, design L4).
//
// The socket-level tests live in internal/app, where the classification has
// real errors to classify and a real WebSocket to cross. What is asserted here
// is the invariant those tests cannot see: the closed sets are CLOSED, and a
// refusal cannot be built whose "what to do" is missing, because a refusal
// that ends at "why" is a defect in the refusal rather than a fact about the
// failure.

// Every reason names an action, every action has words, and every reason has
// words. This is the test L4 asks for by name: adding a failure boundary
// without an action fails HERE rather than shipping a sentence that stops at
// the second part.
func TestEveryHelperRefusalReasonNamesAnAction(t *testing.T) {
	if len(helperRefusalReasons) == 0 || len(helperRefusalActions) == 0 {
		t.Fatal("the closed sets are empty: nothing can be checked")
	}
	inSet := func(a HelperRefusalAction) bool {
		for _, known := range helperRefusalActions {
			if known == a {
				return true
			}
		}
		return false
	}
	for _, reason := range helperRefusalReasons {
		if reason == "" {
			t.Fatalf("a reason in the closed set is empty: %#v", helperRefusalReasons)
		}
		if strings.TrimSpace(helperReasonWords[reason]) == "" {
			t.Errorf("reason %q has no sentence: a surface cannot say what failed", reason)
		}
		action := helperReasonActions[reason]
		if action == "" {
			t.Errorf("reason %q names no action: a refusal that cannot name an action is a defect in the "+
				"refusal (ADR-0057)", reason)
			continue
		}
		if !inSet(action) {
			t.Errorf("reason %q names %q, which is not in the action set: %#v", reason, action, helperRefusalActions)
		}
	}
	for _, action := range helperRefusalActions {
		if strings.TrimSpace(helperActionWords[action]) == "" {
			t.Errorf("action %q has no words: a person is told to do something with no way to read it", action)
		}
	}
}

// The three parts are the three the bead names, and the sentence carries each
// of them — including the action, which is the part a refusal is most likely
// to lose. The cause is quoted verbatim: the second part IS the concrete error,
// and paraphrasing it is how a refusal stops naming what broke.
func TestAHelperRefusalRendersWhatFailedWhyAndWhatToDo(t *testing.T) {
	cause := errors.New("fork/exec /home/dev/.nocx/helper/nocx-helper: permission denied")

	for _, tc := range []struct {
		refusal HelperRefusal
		want    []string
	}{
		{
			refusal: HelperRefusal{Reason: HelperInstallFailed, Cause: cause},
			// The reason's own action, which is what a caller that knows only
			// the boundary has to offer.
			want: []string{"could not install the helper", cause.Error(), "Reinstall or update nocx"},
		},
		{
			refusal: HelperRefusal{Reason: HelperInstallFailed, Action: HelperActionFreeSpace, Cause: cause},
			want:    []string{"could not install the helper", cause.Error(), "Free space"},
		},
		{
			refusal: HelperRefusal{Reason: HelperStartFailed, Cause: cause},
			want:    []string{"did not start", cause.Error(), "Reinstall or update nocx"},
		},
		{
			refusal: HelperRefusal{Reason: HelperHandshakeFailed, Cause: cause},
			want:    []string{"did not answer as the build that installed it", cause.Error(), "Open the pane again"},
		},
		{
			refusal: HelperRefusal{Reason: HelperHandshakeFailed, Action: HelperActionQuitOtherNocx, Cause: cause},
			want:    []string{"did not answer as the build that installed it", cause.Error(), "Quit every other nocx window"},
		},
	} {
		message := tc.refusal.message()
		for _, part := range tc.want {
			if !strings.Contains(message, part) {
				t.Errorf("%s: message %q does not carry %q", tc.refusal.Reason, message, part)
			}
		}
	}
}

// The refusal reaches the open path as a refusal — the carrier that keeps its
// sentence instead of letting the ssh taxonomy flatten it — and it carries the
// two closed-set values as data, so a surface keys on them rather than reading
// prose.
func TestRefuseLocalHelperCarriesItsSentenceAndItsValues(t *testing.T) {
	cause := errors.New("helper: sentinel timeout")
	err := RefuseLocalHelper(HelperRefusal{
		Reason: HelperHandshakeFailed,
		Action: HelperActionRetryOpen,
		Cause:  cause,
	})

	var refusal *openRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want the openRefusal carrier: the open path's own answers keep their sentence", err)
	}
	if refusal.code != -32603 {
		t.Fatalf("code = %d, want -32603", refusal.code)
	}
	if !strings.Contains(refusal.message, cause.Error()) {
		t.Fatalf("message = %q, want the concrete error in it", refusal.message)
	}
	data, ok := refusal.data.(helperRefusalData)
	if !ok {
		t.Fatalf("data = %#v, want the refusal's reason and action", refusal.data)
	}
	if data.Reason != HelperHandshakeFailed || data.Action != HelperActionRetryOpen {
		t.Fatalf("data = %+v, want %q/%q", data, HelperHandshakeFailed, HelperActionRetryOpen)
	}
}

// A refusal whose action was left empty is filled from its reason, and the
// value that reaches the wire is never empty: the failure mode the bead names
// — a person told what broke and nothing about what to do — cannot be
// represented, only misspelled.
func TestARefusalNeverReachesTheWireWithoutAnAction(t *testing.T) {
	err := RefuseLocalHelper(HelperRefusal{Reason: HelperStartFailed, Cause: errors.New("exit status 1")})

	var refusal *openRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want an openRefusal", err)
	}
	data, ok := refusal.data.(helperRefusalData)
	if !ok || data.Action == "" {
		t.Fatalf("data = %#v: a refusal with no action reached the wire", refusal.data)
	}
	if data.Action != helperReasonActions[HelperStartFailed] {
		t.Fatalf("action = %q, want the reason's own %q", data.Action, helperReasonActions[HelperStartFailed])
	}
}

// A refusal with NO CAUSE cannot reach the wire as a placeholder, and that is
// production behaviour with a test behind it.
//
// The third part — WHY — is enforced like the other two: words() refuses a
// reason or an action outside the closed sets, and message() refuses a nil
// Cause rather than rendering "nocx recorded no error for this failure". The
// assertion is made by RECOVERING at the seam because the point is not the
// sentence a missing cause would produce but that the constructor produces
// NONE: a refusal whose "why" is a placeholder is exactly the defect ADR-0057
// names, so the renderer fails loudly instead of shipping one. Reverting the
// panic to the old placeholder leaves this test as the only thing that
// notices.
func TestARefusalWithoutACauseNeverReachesTheWire(t *testing.T) {
	// Inline, because the invariant is about the CALL: the helper fails the
	// test unless RefuseLocalHelper refused to render.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("a refusal with no cause reached the wire: a placeholder is not a \"why\", and " +
					"the renderer must refuse rather than invent one")
			}
			says, ok := r.(string)
			if !ok {
				t.Fatalf("the refusal panicked with %#v, want a message naming the missing part", r)
			}
			if !strings.Contains(says, "has no cause") {
				t.Fatalf("the panic said %q, want it to name the missing cause", says)
			}
		}()
		_ = RefuseLocalHelper(HelperRefusal{Reason: HelperInstallFailed})
	}()
}

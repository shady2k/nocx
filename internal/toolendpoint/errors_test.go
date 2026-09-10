package toolendpoint

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/workers"
)

// WHAT AN AGENT READS WHEN A CALL IS REFUSED.
//
// The reason is the sentence the MCP adapter shows the model (mcpstdio's
// handleCall -> toolError), and it is an instruction rather than a label: what
// happened, why, and what to do next. That is the standard internal/assistant's
// refusalResult already holds this product's OWN model to; the tool endpoint
// was answering an external coordinator with fragments like "method is not
// assembled".
//
// A caller that cannot tell "you may never do this" from "try again" does the
// wrong one, and the wrong one is usually the retry.
func TestEveryRefusalTellsTheCallerWhatToDoNext(t *testing.T) {
	cases := []error{
		assistant.ErrUnknownMethod,
		assistant.ErrInvalidParams,
		assistant.ErrUnreachableMethod,
		assistant.ErrInvalidResult,
		workers.ErrNotHeld,
		workers.ErrNotDelegated,
		workers.ErrTerminal,
		workers.ErrPaneNeverTypable,
		workers.ErrTaskSubmitRefused,
		ErrSessionCallerActive,
		ErrNotEnrolled,
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("something nobody has classified"),
	}
	seen := map[string]error{}
	for _, err := range cases {
		_, _, reason := rpcErrorFor(err)
		if reason == "" {
			t.Fatalf("%v answered with no reason at all", err)
		}
		// An instruction is a sentence, not a fragment. The fragments this
		// replaced were all under 50 characters and none of them contained a
		// verb addressed to the caller.
		if len(reason) < 60 {
			t.Errorf("%v answers %q — too short to say what happened and what to do", err, reason)
		}
		if !strings.Contains(reason, ".") {
			t.Errorf("%v answers %q — not a sentence", err, reason)
		}
		if previous, clash := seen[reason]; clash {
			t.Errorf("%v and %v share one sentence %q; a caller cannot tell them apart", err, previous, reason)
		}
		seen[reason] = err
	}
}

// The regression this split was made for (nocx-e5e8q): a coordinator refused
// because its delegation had ended used to be told the participant belonged to
// somebody else — a statement about OWNERSHIP for a fact about STATE. It will
// not question that, and workers.holdings contradicts it on the next call.
func TestOwnershipAndDelegationStateDoNotShareASentence(t *testing.T) {
	_, _, notHeld := rpcErrorFor(workers.ErrNotHeld)
	_, _, notDelegated := rpcErrorFor(workers.ErrNotDelegated)

	if notHeld == notDelegated {
		t.Fatal("ownership and delegation state answer with one sentence")
	}
	if !strings.Contains(notHeld, "another session") {
		t.Errorf("the ownership refusal does not say whose it is: %q", notHeld)
	}
	if !strings.Contains(notDelegated, "yours") {
		t.Errorf("the state refusal does not say the participant is still the caller's: %q", notDelegated)
	}
	// And it must not tell a caller its own participant is somebody else's.
	if strings.Contains(notDelegated, "another session") {
		t.Errorf("the state refusal claims the participant belongs elsewhere: %q", notDelegated)
	}
}

// A cancellation is not an internal error (nocx-uhii1): the caller stopped
// waiting, nothing inside nocx failed, and — the opposite of the unclassified
// arm below — the honest instruction is that calling it again is legitimate.
func TestCancellationIsAbandonmentNotAnInternalError(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(err.Error(), func(t *testing.T) {
			code, message, reason := rpcErrorFor(err)
			if code == rpcInternalError {
				t.Fatalf("code = %d, a cancellation must not be classified as an internal error", code)
			}
			if strings.Contains(reason, "Do not repeat") {
				t.Errorf("reason tells the agent not to retry a call that is safe to retry: %q", reason)
			}
			if strings.Contains(reason, "failed inside nocx") || strings.Contains(reason, "a reason inside nocx") {
				t.Errorf("reason blames nocx for a call the caller itself abandoned: %q", reason)
			}
			if !strings.Contains(reason, "again") {
				t.Errorf("reason does not say that calling it again is legitimate: %q", reason)
			}
			if message == "" || reason == "" {
				t.Fatalf("message/reason = %q/%q, want both set", message, reason)
			}
		})
	}
}

// An unclassified error stays generic on the wire — an agent must not be told
// a backend's internals — and it still must stop the agent retrying a call
// that failed for a reason it cannot affect.
func TestTheUnclassifiedErrorSaysNotToRepeatTheCall(t *testing.T) {
	code, message, reason := rpcErrorFor(errors.New("a pipe closed somewhere deep"))
	if code != rpcInternalError {
		t.Fatalf("code = %d, want the internal-error code", code)
	}
	if strings.Contains(reason, "pipe") || strings.Contains(message, "pipe") {
		t.Fatalf("the error's own words reached the agent: %q / %q", message, reason)
	}
	if !strings.Contains(reason, "Do not repeat") {
		t.Errorf("the reason does not stop a retry: %q", reason)
	}
}

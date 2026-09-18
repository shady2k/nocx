package toolendpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
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
		// THE PANE ARM'S TWO (nocx-50w7p.16). They are here for the same reason
		// the pair above is: a bearer that does not match and an interval that
		// has ended are different facts, and a caller that cannot tell them
		// apart goes looking for the wrong repair.
		ErrNoLiveInterval,
		ErrBearerRefused,
		errUnpublishedAdmission,
		// THE TARGET-MINT REFUSALS (nocx-xn63t.4.1). A full token book and a
		// snapshot the pane redrew under are refusals of the CALLER's own
		// request with a known next step — wait, and ask again — so they are
		// named here rather than falling into the default arm, whose sentence
		// calls them a backend fault and tells the agent to stop. An agent
		// told to stop is exactly what left the owner's worker stuck: the
		// answer it needed was on that pane's menu.
		helperclient.ErrTargetCapacity,
		helperclient.ErrSnapshotGone,
		// THE REPORT'S FOUR (nocx-luqz9.4). Each is a different fact with a
		// different next step — not a worker at all, a worker nothing
		// coordinates, a write that failed, and a shape the tool could not
		// have sent — and a caller that cannot tell them apart retries the one
		// that cannot work or gives up on the one that can.
		agenttools.ErrNoParticipant,
		workers.ErrNoCoordinator,
		workers.ErrReportNotRecorded,
		workers.ErrNotAReport,
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

// A target-mint refusal is not an internal error (nocx-xn63t.4.1). The owner's
// coordinator was refused every targeted session.read with a bare
// `internal error`, whose sentence tells the agent to stop and carry on
// without the tool — while the one thing that would have freed the pane was
// the menu answer that needed a target. These assert the two refusals reach
// the agent named, with the next step each one actually has, through the
// pipeline the production error arrives by: the classifier at app's crossing,
// then the wrapping session.read puts around it.
func TestATargetRefusalNamesTheNextStepTheCallerHas(t *testing.T) {
	throughRead := func(err error) error {
		return fmt.Errorf("session.read: mint target: %w", helperclient.ClassifyTargetRefusal(err))
	}

	_, capacityMessage, capacity := rpcErrorFor(throughRead(&helperclient.RefusalError{Code: "capacity"}))
	_, goneMessage, gone := rpcErrorFor(throughRead(&helperclient.RefusalError{Code: "snapshot_gone"}))

	if capacity == gone {
		t.Fatalf("the two mint refusals share one sentence: %q", capacity)
	}
	if strings.Contains(capacity, "inside nocx") || strings.Contains(gone, "inside nocx") {
		t.Errorf("a mint refusal is still reported as a fault inside nocx: %q / %q", capacity, gone)
	}
	if !strings.Contains(capacity, "wait") {
		t.Errorf("the capacity refusal does not say to wait for a slot: %q", capacity)
	}
	if !strings.Contains(strings.ToLower(capacity), "without a target") {
		t.Errorf("the capacity refusal does not name the read that still works (the one the caller needs while it waits): %q", capacity)
	}
	if !strings.Contains(gone, "again") {
		t.Errorf("the snapshot refusal does not say that asking again is the way through: %q", gone)
	}
	for _, message := range []string{capacityMessage, goneMessage} {
		if strings.Contains(message, "capacity") || strings.Contains(message, "snapshot") {
			t.Errorf("the wire message spells the helper's own code to the agent: %q", message)
		}
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

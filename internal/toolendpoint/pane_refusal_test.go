package toolendpoint

// The coordinator's sentence follows what the pane held (nocx-f545a.3).
//
// The spawn's own refusal already distinguished a pane nocx could not read
// from a pane that was busy, and this endpoint threw that away: every
// ErrPaneNeverTypable was answered "spawning again may succeed if this was
// transient", including the case the error one layer down had just called
// retrying a bug. These assert the split survives to the wire.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/workers"
)

func TestAPaneNocxCouldNotReadIsNotAnsweredWithRetryAdvice(t *testing.T) {
	wrap := func(e *workers.PaneNeverTypable) error { return fmt.Errorf("workers.spawn: worker: spawn: %w", e) }

	_, _, unknown := rpcErrorFor(wrap(&workers.PaneNeverTypable{State: "unknown", Detail: "not recognised"}))
	_, _, neverSeen := rpcErrorFor(wrap(&workers.PaneNeverTypable{Detail: "never observed"}))
	_, _, busy := rpcErrorFor(wrap(&workers.PaneNeverTypable{State: "working", Detail: "held working"}))

	for name, reason := range map[string]string{"unknown": unknown, "never observed": neverSeen} {
		if strings.Contains(reason, "may succeed") {
			t.Errorf("%s: a pane nocx could not read was answered with retry advice: %q", name, reason)
		}
	}
	if !strings.Contains(busy, "may succeed") {
		t.Errorf("a pane that was merely busy lost its retry advice: %q", busy)
	}
	if unknown == busy {
		t.Fatalf("the two refusals are one sentence again: %q", unknown)
	}
}

// And the bare sentinel, which some callers still produce, keeps the sentence
// it always had rather than being read as unreadable.
func TestTheBarePaneSentinelKeepsItsRetrySentence(t *testing.T) {
	_, _, reason := rpcErrorFor(fmt.Errorf("spawn: %w", workers.ErrPaneNeverTypable))
	if !strings.Contains(reason, "may succeed") {
		t.Fatalf("the bare sentinel = %q, want the retry sentence", reason)
	}
}

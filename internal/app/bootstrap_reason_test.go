package app

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// The ratchet that keeps three tables from drifting: the bootstrap's closed
// outcome set, the ssh refusal vocabulary, and the closed enum on the wire.
//
// It exists because the failure it guards is silent. Before P5 every bootstrap
// outcome reached the product as ssh.ReasonUnknown — "integration did not
// happen and the backend cannot say why" — while the backend knew perfectly
// well which of twenty-one things had happened, and the precise answer went to
// a log the user cannot read. A soft degrade the UI contradicts is how a
// feature that does not exist survives a release (AGENTS.md), and the way this
// one would come back is a member added to one table and forgotten in the
// others.
//
// The schema is read from disk rather than restated here, for AGENTS.md rule
// 5's reason: a check that validates against a copy the test itself wrote
// proves the copy is self-consistent, not that the wire carries the value.

// contractPath is the wire declaration, read as the authority it is.
const contractPath = "../../contracts/session.integrationChanged.schema.json"

func wireReasons(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var doc struct {
		Properties struct {
			Reason struct {
				Enum []string `json:"enum"`
			} `json:"reason"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if len(doc.Properties.Reason.Enum) == 0 {
		t.Fatal("the contract declares no reason enum; every assertion below would be vacuous")
	}
	out := map[string]bool{}
	for _, r := range doc.Properties.Reason.Enum {
		out[r] = true
	}
	return out
}

// Assertion 23's backbone: every member of the bootstrap's closed outcome set
// produces a NAMED reason that the wire is allowed to carry — never
// ssh.ReasonUnknown, which is the answer for an outcome nobody has mapped.
func TestBootstrapOutcomes_EachHasAProductReason(t *testing.T) {
	wire := wireReasons(t)
	a := &remoteLauncherAdapter{logger: log.NewSlogAdapter(nil)}

	for _, o := range shellintegration.AllOutcomes() {
		reason := a.mapBootstrapOutcome(o)
		if o == shellintegration.OutcomeBootstrapAccepted {
			if reason != ssh.ReasonNone {
				t.Errorf("the accepted outcome maps to %q, want no refusal at all", reason)
			}
			continue
		}
		if reason == ssh.ReasonUnknown {
			t.Errorf("outcome %q maps to ssh.ReasonUnknown — the backend knows which it is "+
				"and the product would say it cannot say", o)
			continue
		}
		if reason == ssh.ReasonNone {
			t.Errorf("outcome %q maps to no refusal at all; the product would render it as success", o)
			continue
		}
		if !wire[string(reason)] {
			t.Errorf("outcome %q maps to reason %q, which the contract's closed enum does not "+
				"declare — the notification would be refused at the schema instead of reaching a user",
				o, reason)
		}
	}
}

// The identical spelling is the property that makes the mapping auditable by
// eye, so it is asserted rather than trusted. A member renamed on one side and
// not the other is a test failure here, one line long, instead of a reason
// nobody can trace back to its outcome.
func TestBootstrapOutcomes_AreSpelledIdenticallyOnBothSides(t *testing.T) {
	a := &remoteLauncherAdapter{logger: log.NewSlogAdapter(nil)}
	for _, o := range shellintegration.AllOutcomes() {
		if o == shellintegration.OutcomeBootstrapAccepted {
			continue
		}
		if got := string(a.mapBootstrapOutcome(o)); got != string(o) {
			t.Errorf("outcome %q maps to reason %q; the two vocabularies are spelled identically "+
				"so that this mapping can be checked by reading it", o, got)
		}
	}
}

// The §6.4 matrix, by real SSH channel type. Every row has a name the wire can
// carry, including the three the typed-`ssh` wrapper produces and does not own
// the vocabulary for.
func TestSelectiveRefusalMatrix_EveryRowHasAWireReason(t *testing.T) {
	wire := wireReasons(t)
	rows := map[string]ssh.RefusalReason{
		"the primary session":               ssh.ReasonSessionUnavailable,
		"pty-req after session":             ssh.ReasonPTYUnavailable,
		"exec refused, channel alive":       ssh.ReasonExecRefused,
		"exec accepted and substituted":     ssh.ReasonExecSubstituted,
		"subsystem (SFTP)":                  ssh.ReasonPublishUnavailable,
		"the lifecycle forward or channel":  ssh.ReasonChannelUnavailable,
		"an open channel severed mid-frame": ssh.ReasonBootstrapInterrupted,
	}
	seen := map[ssh.RefusalReason]string{}
	for row, reason := range rows {
		if !wire[string(reason)] {
			t.Errorf("§6.4 row %q names reason %q, which the contract does not declare", row, reason)
		}
		if other, dup := seen[reason]; dup {
			t.Errorf("rows %q and %q share reason %q; the matrix distinguishes them and the "+
				"product must too", row, other, reason)
		}
		seen[reason] = row
	}
}

// The tripwire itself. An outcome that is not in the closed set — which is
// what a new member looks like before somebody maps it — degrades to the
// distinct visible failure and never to "integration succeeded".
func TestBootstrapOutcomes_AnUnmappedOutcomeFailsOpenVisibly(t *testing.T) {
	a := &remoteLauncherAdapter{logger: log.NewSlogAdapter(nil)}
	if got := a.mapBootstrapOutcome(shellintegration.Outcome("brand-new-outcome")); got != ssh.ReasonUnknown {
		t.Errorf("an unmapped outcome mapped to %q, want %q — never ReasonNone, which the "+
			"product renders as success", got, ssh.ReasonUnknown)
	}
}

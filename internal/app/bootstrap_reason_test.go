package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
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
	lg := log.NewSlogAdapter(nil)

	for _, o := range shellintegration.AllOutcomes() {
		reason := mapBootstrapOutcome(lg, o)
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
	lg := log.NewSlogAdapter(nil)
	for _, o := range shellintegration.AllOutcomes() {
		if o == shellintegration.OutcomeBootstrapAccepted {
			continue
		}
		if got := string(mapBootstrapOutcome(lg, o)); got != string(o) {
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
	lg := log.NewSlogAdapter(nil)
	if got := mapBootstrapOutcome(lg, shellintegration.Outcome("brand-new-outcome")); got != ssh.ReasonUnknown {
		t.Errorf("an unmapped outcome mapped to %q, want %q — never ReasonNone, which the "+
			"product renders as success", got, ssh.ReasonUnknown)
	}
}

// EVERY member of the ssh refusal vocabulary is on the wire, not only the
// members some other list happens to enumerate (nocx-e4ir3).
//
// The three tests above walk the bootstrap outcome set and the selective
// refusal matrix, which is what those two lists were: a way to catch a reason
// added there and forgotten here. But `ssh.RefusalReason` is the vocabulary,
// and a member added straight to it — from a guard, a new seam, anything that
// is not a bootstrap outcome or a matrix row — was caught by none of them. The
// product then renders nothing, or the notification is refused at the schema,
// which is the "soft degrade the UI contradicts" the schema's own description
// warns about.
//
// This reads the constant block itself, so the vocabulary cannot be extended
// without the wire learning about it. Go has no reflection over a package's
// constants, so the declaration is parsed — the same "read the authority, do
// not restate it" the schema loader above is built on.
func TestEveryRefusalReasonIsDeclaredOnTheWire(t *testing.T) {
	wire := wireReasons(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../ssh/ssh.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse the refusal vocabulary: %v", err)
	}

	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
			return true
		}
		id, ok := vs.Type.(*ast.Ident)
		if !ok || id.Name != "RefusalReason" {
			return true
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value := strings.Trim(lit.Value, `"`)
		found++
		// ReasonNone is the empty string — "integration succeeded" — and is
		// deliberately not a wire value: the field is absent, not empty.
		if value == "" {
			return true
		}
		if !wire[value] {
			t.Errorf("ssh.%s = %q is not in the contract's closed reason enum. "+
				"A session refused for this reason would have its notification rejected at the "+
				"schema, so the product would show nothing at all — add it to %s",
				vs.Names[0].Name, value, contractPath)
		}
		return true
	})

	if found < 20 {
		t.Fatalf("only %d RefusalReason constants were parsed; the vocabulary is larger than that, "+
			"so this test is not reading what it thinks it is reading", found)
	}
}

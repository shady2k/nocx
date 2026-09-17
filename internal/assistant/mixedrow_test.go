package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// runAskGrant is the matrix a person writes when every row asks and the run is
// bound to one session — the shape session.run is actually offered under, since
// the declaration filter needs a session-kind scope to offer the tool at all.
func runAskGrant() content.Grant {
	row := content.EffectRow{
		Decision: content.DecisionAsk,
		Scopes:   []content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}},
	}
	policy := content.EffectPolicy{
		Observe: row, MutateReversible: row, MutateDestructive: row,
		PrivilegeChange: row, Disclose: row, CrossBoundary: row, Delegate: row,
	}
	return policy.AsGrant(nil)
}

// TestAsk_MixedResourceCommandAsksInTheRowItBelongsIn is nocx-jxq97 at the
// seam a person actually reaches: the model proposes `curl -o /tmp/x https://y`
// through session.run, and the ask that suspends the run names the row the
// command belongs in.
//
// CHANGED BEHAVIOUR. Before this, the same proposal produced an ask headed
// `delegate` — "hand work to another agent" — because session.run declares
// delegate and effectOrder ranks it highest, so a report mixing a network
// resource with a written file took the declaration set's worst member. The
// command hands work to nobody. It reaches a host, so the ask says so.
//
// The weakening is real and deliberate: a person whose matrix refuses delegate
// and permits cross-boundary was refused this command and is now asked (or, if
// they permitted the row, permitted). See
// TestResourceReportEffectMixedNetworkAndWriteNoLongerLandsInDelegate.
func TestAsk_MixedResourceCommandAsksInTheRowItBelongsIn(t *testing.T) {
	grant := runAskGrant()

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "session.run",
		args: `{"command":"curl -o /tmp/x https://y"}`,
	}))
	defer srv.Close()

	cl, _, err := NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(AskEvent) error {
		return nil
	})
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want an approval request", err)
	}
	if asked.Request.Effect != content.EffectCrossBoundary {
		t.Fatalf("the ask is headed %q, want %q — the row this command belongs in",
			asked.Request.Effect, content.EffectCrossBoundary)
	}
	if asked.Request.Effect == content.EffectDelegate {
		t.Fatal("the ask still says work was handed to another agent")
	}
}

// TestAsk_MixedResourceCommandCarriesEveryDerivedEffect is the second half:
// one row governs the decision (ADR-0020 §7, not reopened), and the request the
// approval surface renders carries BOTH things the call does, so the surface
// can say the command reached a host AND wrote a file. Without it a person
// answers a question about the network and a file is written on that answer.
func TestAsk_MixedResourceCommandCarriesEveryDerivedEffect(t *testing.T) {
	grant := runAskGrant()

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "session.run",
		args: `{"command":"curl -o /tmp/x https://y"}`,
	}))
	defer srv.Close()

	cl, _, err := NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(AskEvent) error {
		return nil
	})
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want an approval request", err)
	}
	if len(asked.Request.DerivedEffects) != 2 {
		t.Fatalf("derived effects = %v, want exactly the two the command derived",
			asked.Request.DerivedEffects)
	}
	for _, want := range []content.Effect{content.EffectCrossBoundary, content.EffectMutateReversible} {
		var found bool
		for _, candidate := range asked.Request.DerivedEffects {
			if candidate == want {
				found = true
			}
		}
		if !found {
			t.Errorf("derived effects = %v, want %q among them — the person is not told what the call does",
				asked.Request.DerivedEffects, want)
		}
	}
}

// TestAsk_SingleResourceCommandCarriesItsOneDerivedEffect: the plain form is
// unchanged, and its single candidate is still carried — a surface reads one
// field, not one field plus a rule about when it is populated.
func TestAsk_SingleResourceCommandCarriesItsOneDerivedEffect(t *testing.T) {
	grant := runAskGrant()

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "session.run",
		args: `{"command":"curl https://y"}`,
	}))
	defer srv.Close()

	cl, _, err := NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(AskEvent) error {
		return nil
	})
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want an approval request", err)
	}
	if asked.Request.Effect != content.EffectCrossBoundary {
		t.Fatalf("effect = %q, want %q", asked.Request.Effect, content.EffectCrossBoundary)
	}
	if len(asked.Request.DerivedEffects) != 1 ||
		asked.Request.DerivedEffects[0] != content.EffectCrossBoundary {
		t.Fatalf("derived effects = %v, want exactly [%q]",
			asked.Request.DerivedEffects, content.EffectCrossBoundary)
	}
}

// TestAsk_UnresolvedCommandCarriesNoDerivedEffect: a command the parser could
// not resolve still takes the declaration set's worst member, and it names no
// candidate at all. A partial list read as a complete one would be a new way of
// telling a person something untrue about the call they are approving; the
// unresolved part is carried, with its reason, in the invocation's report.
func TestAsk_UnresolvedCommandCarriesNoDerivedEffect(t *testing.T) {
	grant := runAskGrant()

	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "session.run",
		args: `{"command":"curl -o \"$OUT\" https://y"}`,
	}))
	defer srv.Close()

	cl, _, err := NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	err = cl.Ask(context.Background(), askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore()), func(AskEvent) error {
		return nil
	})
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("Ask error = %v, want an approval request", err)
	}
	if want := content.WorstEffect(runEffects); asked.Request.Effect != want {
		t.Fatalf("effect = %q, want the declared worst %q — uncertainty still tightens",
			asked.Request.Effect, want)
	}
	if len(asked.Request.DerivedEffects) != 0 {
		t.Fatalf("derived effects = %v, want none for a command that did not fully resolve",
			asked.Request.DerivedEffects)
	}
	if len(asked.Request.Invocation.Resources.Unresolved) == 0 {
		t.Fatal("the unresolved part is not carried either, so the ask says nothing about it")
	}
}

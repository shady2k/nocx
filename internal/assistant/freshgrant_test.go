package assistant

// The fresh grant of ADR-anntnxdj §3: "The grant is minted at admission ... and
// refreshed after an answer."
//
// A person's answer is not the last word. The guards and the capability run
// AFTER it, on the grant as it stands then, because the wait can be ten minutes
// long: a file changed meanwhile fails its evidence, and an authority narrowed
// meanwhile refuses the call whatever the person said. These tests hold both
// ends of that: a narrowed fence refuses through an approval, and an expired
// grant is replaced rather than refusing a call that waited.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// fencedGrant mints the state the narrowed-fence test is about: a real file
// outside the fence the resumed call runs under, inside the wide one.
func fencedGrant(t *testing.T) (wide content.Grant, inside string, path string) {
	t.Helper()
	root := t.TempDir()
	inside = filepath.Join(root, "inside")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	path = filepath.Join(outside, "b.txt")
	writeFile(t, path, "beyond the fence")
	wide = askEveryTimeMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: root},
	})
	return wide, inside, path
}

// TestFreshGrant_ANarrowedFenceRefusesThroughAnApproval: the call carries the
// strongest answer a person can give — the exact yes to this very proposal,
// under a matrix that now PERMITS observe — and an authority that no longer
// covers the resource. The yes does not reach past the fence, and the refusal
// names the fence: a refusal for the right reason and one for the wrong reason
// are the same string to an assertion that reads only the decision.
//
// The invariant is a span, and both ends are asserted: the resource lies
// outside the fence from before the approval is presented until after the call
// has returned, and nothing was attempted anywhere in between.
func TestFreshGrant_ANarrowedFenceRefusesThroughAnApproval(t *testing.T) {
	wide, inside, path := fencedGrant(t)
	args := fmt.Sprintf(`{"path":%q}`, path)

	// The authority the transport mints after the answer: the session's new
	// overlay resolved in (observe PERMITS) and a fence that no longer covers
	// the resource.
	narrowed := content.ResolvePolicy(askEveryTimeMatrix(), nil, content.SessionOverrides{
		Decisions: map[content.Effect]content.Decision{content.EffectObserve: content.DecisionPermit},
	}).AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: inside},
	})
	if narrowed.Policy.DecisionFor(content.EffectObserve) != content.DecisionPermit {
		t.Fatalf("the answered matrix decides %q for observe, want permit — the test must be about the fence and not about a missing yes",
			narrowed.Policy.DecisionFor(content.EffectObserve))
	}

	ledger := &fakeLedger{}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, wide, ledger, q)
	k.approvals = approvals
	k.runSeams.freshGrant = func() *content.Grant { return &narrowed }

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("the fenced call returned %v, want the out-of-scope refusal as its answer", err)
	}
	if len(q.asked) != 1 {
		t.Fatalf("the person was asked %d times, want exactly the one question the wide authority raised", len(q.asked))
	}
	if want := refusalResult("files.read", RefusedOutOfScope, ""); out != want {
		t.Fatalf("the fenced call answered %q, want the out-of-scope refusal %q — a refusal for the wrong reason reads identically to one for the right reason", out, want)
	}
	if strings.Contains(out, "beyond the fence") {
		t.Fatal("the refusal carried the file's contents: the call reached past the fence and the refusal was cosmetic")
	}
	// The approval is still on record — the fence is not an answer to the
	// person's yes, it is a bound the yes cannot move.
	if !approvals.IsApproved(Approval{RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: canonicalArgHash(args)}) {
		t.Fatal("the fenced refusal consumed the approval")
	}
}

// TestFreshGrant_AnExpiredGrantIsReplacedAfterTheAnswer is the DONE WHEN's
// third assertion. The grant minted at admission has expired by the time the
// answer arrives — which is what a wait longer than the grant's hour produces,
// and what the transport's own clock test advances ten minutes to exercise. The
// answer mints the authority again, and the call runs under it. Without a mint
// the same call is refused and says why, which is the other end of the interval.
func TestFreshGrant_AnExpiredGrantIsReplacedAfterTheAnswer(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "the bytes that waited")
	args := fmt.Sprintf(`{"path":%q}`, path)
	// The admission grant closed before the answer.
	grant.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()

	fresh := grant
	fresh.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()

	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	k := kernelForQuestioner(t, grant, &fakeLedger{}, q)
	k.approvals = approvals
	k.runSeams.freshGrant = func() *content.Grant { return &fresh }

	out, err := k.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "the bytes that waited") {
		t.Fatalf("result = %q, want the file read under the grant the answer minted", out)
	}

	// The other end: the same expired grant with no mint refuses the call
	// after the answer, and says why.
	approvals2 := NewApprovalStore()
	q2 := &recordingQuestioner{store: approvals2, answer: Approved{}}
	k2 := kernelForQuestioner(t, grant, &fakeLedger{}, q2)
	k2.approvals = approvals2
	out2, err := k2.Invoke(context.Background(), "files.read", "call-1", args)
	if err != nil {
		t.Fatalf("Invoke without a mint: %v", err)
	}
	if want := refusalResult("files.read", RefusedExpired, ""); out2 != want {
		t.Fatalf("without a fresh grant the call answered %q, want the expired refusal %q", out2, want)
	}
	if len(q2.asked) != 1 {
		t.Fatalf("the person was asked %d times, want the one question that preceded the refusal", len(q2.asked))
	}
}

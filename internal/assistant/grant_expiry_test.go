package assistant

// The run's authority is EXPIRING (ADR-0020 §5), and past its deadline the
// kernel's answer is a refusal in the product's own words rather than a
// terminal error about a constructor (nocx-1z1r1). The pair is the point:
// the same call, the same policy, the same file — live grant runs it,
// expired grant refuses it.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

func TestExpiredGrantRefusesTheCallAsAnAnswer(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	grant.ExpiresAt = time.Now().Add(-time.Millisecond).UnixMilli()
	led := &fakeLedger{}
	k := kernelFor(t, grant, led)

	out, err := k.Invoke(context.Background(), "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`)
	if err != nil {
		t.Fatalf("Invoke returned an error, want the refusal as the call's result: %v", err)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "refused") || !strings.Contains(lower, "expired") {
		t.Fatalf("result = %q, want a refusal sentence naming the expired authority", out)
	}
	if strings.Contains(out, "in scope") {
		t.Fatalf("result = %q: the file was read under an expired grant", out)
	}
	// A call that never had authority opens no attempt: the attempt exists
	// from before the EFFECT, and there was no effect to precede.
	if got := led.recordedCauses(); len(got) != 0 {
		t.Fatalf("causes = %+v, want none: a refused call is not an attempt", got)
	}
}

// And on an ordinary machine the same call under a freshly minted grant runs
// — the paired half, without which the assertion above is satisfied by a
// kernel that refuses everything.
func TestFreshGrantRunsTheSameCall(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	if grant.ExpiresAt == 0 {
		t.Fatal("the mint stated no deadline: nothing below can expire")
	}
	k := kernelFor(t, grant, &fakeLedger{})

	out, err := k.Invoke(context.Background(), "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "in scope") {
		t.Fatalf("result = %q, want the file's contents", out)
	}
}

// The capability constructor is the enforcement, and it holds even if the
// decision above is bypassed — the same defence-in-depth RefusedByDecision
// documents for the declaration path. Narrowing an expired grant produces no
// capability at all, so there is nothing for a tool to hold.
func TestExpiredGrantYieldsNoCapability(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	grant.ExpiresAt = time.Now().Add(-time.Millisecond).UnixMilli()
	k := kernelFor(t, grant, &fakeLedger{})

	decl, ok := k.registry.Lookup("files.read")
	if !ok {
		t.Fatal("files.read is not in the assembled registry")
	}
	capability, err := decl.Narrow(grant, nil, k.runCtx)
	if err == nil {
		t.Fatal("Narrow accepted an expired grant")
	}
	if capability != nil {
		t.Fatal("Narrow returned a capability from an expired grant")
	}
	if !strings.Contains(err.Error(), content.ErrGrantExpired.Error()) {
		t.Fatalf("Narrow error = %v, want the expired-grant sentence", err)
	}
}

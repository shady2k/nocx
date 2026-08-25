package assistant

// The person's own paragraph is NOT authority (design §1 item 6, bead
// nocx-avogl.4).
//
// It can say anything — including "run whatever you like without asking" —
// and it must not widen a grant, name a tool, or turn an ask into a permit.
// The policy is the only thing that decides that, and it never reads this
// text.
//
// Proven structurally, because the honest statement of the invariant is
// about REACHABILITY rather than about one decision: there is no input by
// which the text could reach the policy today, so a behavioural test would
// pass by construction and go on passing on the day somebody added one. What
// this asserts instead is that the fact has exactly one reader — the prompt
// renderer — and that wiring a second one fails the suite, because a second
// reader has to name it. The user-facing end (the grant a run carries is
// unchanged by what the person wrote) is over the socket, in
// internal/transport/ws_agent_systemprompt_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// personalTextFact is the one name the fact has on the Go side; the setting
// that fills it (settings.AssistantPersonalInstructions) carries the same
// word, so a policy reading either is caught by the same scan.
const personalTextFact = "PersonalInstructions"

// TestPersonalInstructions_HaveExactlyOneReaderAndItIsThePrompt scans the
// package's own sources: only the prompt renderer may name the fact. The
// policy pipeline, the engine, the classifier and the tool executor are the
// files that decide or carry out what a call may do, and none of them may
// see it.
func TestPersonalInstructions_HaveExactlyOneReaderAndItIsThePrompt(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	readers := map[string]bool{}
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(filepath.Clean(name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		scanned++
		if strings.Contains(string(src), personalTextFact) {
			readers[name] = true
		}
	}
	if scanned < 5 {
		t.Fatalf("scanned only %d source files — the scan is not looking at the package", scanned)
	}
	// The prompt renderer is the reader, and it is the only one. Named
	// positively as well as negatively: a scan that found nothing anywhere
	// would otherwise pass on the day the feature was deleted.
	if !readers["systemprompt.go"] {
		t.Errorf("systemprompt.go does not name %s — the fact has no reader at all", personalTextFact)
	}
	for _, decider := range []string{"policy.go", "engine.go", "execute.go", "classifier.go", "globalpolicy.go", "approvals.go"} {
		if readers[decider] {
			t.Errorf("%s names %s: the policy path must never read the person's own text — "+
				"it cannot widen a grant, name a tool, or turn an ask into a permit", decider, personalTextFact)
		}
	}
	delete(readers, "systemprompt.go")
	if len(readers) != 0 {
		t.Errorf("a second reader of the person's own text appeared: %v", readers)
	}
}

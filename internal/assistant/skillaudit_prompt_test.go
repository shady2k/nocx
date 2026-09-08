package assistant

// nocx-cdmjj, the prompt half. The auditing model is NOT shown nocx's static
// scan, and the prompt must therefore never refer to one.
//
// The prompt once directed the model to judge "a static scan's match" while
// internal/skill computed the findings alongside the composed document and
// never wrote them into it — a reference to something the model did not have.
// Removing the clause (nocx-c0v5k) made the two agree by asking less; this
// test is what stops them drifting apart again, in either direction. Adding
// the clause back without also giving the model the findings fails here;
// giving it the findings fails TestAuditDocumentIsTheFilesAndNothingElse.

import (
	"strings"
	"testing"
)

func TestTheAuditPromptNeverReferstoAScanTheModelWasNotGiven(t *testing.T) {
	prompt := strings.ToLower(skillAuditSystemPrompt + auditUserPreamble)
	for _, forbidden := range []string{"scan", "finding", "matched", "pattern"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("the audit prompt says %q: the model is given the files and NOT nocx's findings, "+
				"so a prompt that names them asks about something it cannot see", forbidden)
		}
	}
	// And what it DOES ask for is the model's own reading of the same bytes,
	// which is the answer the scan cannot give: the file and the line behind
	// the verdict, in the model's words.
	if !strings.Contains(prompt, "naming the file and the line") {
		t.Error("the audit prompt no longer asks the model to name the file and line behind its verdict, " +
			"which is the whole of what it offers over the scan")
	}
}

package agenttools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A path nobody can find is a path nobody has.
//
// skills.create keeps the person's own bytes when the proposed body is text
// they typed (internal/assistant/skilldraft.go, verbatim). Nothing else in the
// pipeline can put a caller on that path: the check is a byte comparison, so
// the model reaches it by QUOTING and by nothing else, and a model that does
// not know quoting is preserved will paraphrase — which lands every person who
// pasted a procedure back on the summarizer that rewrites it, the defect this
// was built for.
//
// So the field has to say so, and this is a wording test for the same reason
// TestSkillsInstallUrlFieldDoesNotWithholdTheCall is one: the whole mechanism
// is what the caller was told.
func TestSkillsCreateBodyFieldOffersTheQuotation(t *testing.T) {
	body := skillsCreateBodyDescription(t)

	for _, want := range []string{"exactly", "kept"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body field = %q\nwant it to say that text quoted %q as the person typed it is %q", body, "exactly", "kept")
		}
	}

	// And the other half of the interval, or the field reads as an invitation
	// to claim a quotation rather than to make one: the caller must be told
	// what happens when it does NOT quote, because that is the ordinary case
	// and it is not a failure.
	if !strings.Contains(body, "conversation") {
		t.Errorf("the body field = %q\nwant it to say a body that is not the person's own text is written from the conversation instead", body)
	}
}

func skillsCreateBodyDescription(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/skills.create.schema.json")
	if err != nil {
		t.Fatalf("read the skills.create contract: %v", err)
	}
	var contract struct {
		Properties struct {
			Body struct {
				Description string `json:"description"`
			} `json:"body"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse the skills.create contract: %v", err)
	}
	if contract.Properties.Body.Description == "" {
		t.Fatal("the skills.create contract's body field carries no description: the caller is then told nothing about the field that decides whether the person's own words survive")
	}
	return contract.Properties.Body.Description
}

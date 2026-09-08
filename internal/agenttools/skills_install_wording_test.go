package agenttools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A tool nobody calls refuses nothing, and this row talked a model out of
// calling it.
//
// The `url` field used to say the address "must be the address that answers
// with the document itself, not a page that links to it". Asked to install a
// skill from a documentation page, a model quoted that sentence back in its
// reasoning, concluded a /docs/ page is a page that links to the skill, and
// called fetch.url instead — then had to invent what to do with what came
// back. It obeyed us exactly (nocx-j83u4).
//
// The sentence was wrong twice. It contradicted the row's own description one
// layer up ("Reach for this when the person points you at a skill to add"),
// and the parameter wins that argument because it is the more specific
// instruction and it is attached to the field being filled. And it stated a
// precondition the caller cannot satisfy: you only have the SKILL.md address
// if you already know where the skill lives, which is the thing being asked
// of the assistant. What a person has is the page they were reading.
//
// It also protected nothing. There is no URL-shape check in
// internal/skill/preview.go — the tool fetches whatever answers and refuses a
// document with no frontmatter in words a person can act on. Calling it was
// always the better answer.
//
// So this asserts the field does not withhold the call. It is a wording test
// because the defect was wording: nothing else in the pipeline changed, and
// nothing else could have caught it.
func TestSkillsInstallUrlFieldDoesNotWithholdTheCall(t *testing.T) {
	url := skillsInstallURLDescription(t)

	// Each of these told the caller "the address you have is not for this
	// tool". They are listed rather than pattern-matched because a
	// prohibition is written in words somebody chose, and the next one will
	// be too — a reviewer reading this list learns what shape to refuse.
	for _, withheld := range []string{
		"not a page that links to it",
		"must be the address that answers with the document itself",
	} {
		if strings.Contains(url, withheld) {
			t.Errorf("the url field says %q.\n"+
				"That tells the caller not to call when the person pasted a page — which is what a person pastes. "+
				"The tool has to be what discovers the address is wrong; it cannot be a precondition on reaching it.", withheld)
		}
	}

	// And the other half: silence would pass the loop above. The field has to
	// say the call is right anyway, or a model that has learned caution from
	// somewhere else lands in the same place.
	if !strings.Contains(url, "exactly as they gave it") {
		t.Errorf("the url field = %q\nwant it to tell the caller to pass the address the person gave, unchanged", url)
	}
	if !strings.Contains(url, "refusal") {
		t.Errorf("the url field = %q\nwant it to say a wrong address earns a refusal that names the problem — "+
			"that is why calling beats withholding", url)
	}
}

// The redirect rule is the one real fact the old sentence carried, and it
// survives the rewrite: every fact nocx keeps about an installed skill is
// derived from the address the person approved, so an address that forwards
// somewhere else describes a skill that came from neither.
func TestSkillsInstallUrlFieldKeepsTheRedirectRule(t *testing.T) {
	url := skillsInstallURLDescription(t)
	if !strings.Contains(url, "redirect") {
		t.Errorf("the url field = %q\nwant the redirect rule kept: it is a fact about what nocx records, not a precondition on the caller", url)
	}
}

// It reads the repository's contract rather than an embedded copy, for the
// reason TestApprovalRequestedToolEnumMatchesTheTable gives: the file the
// model is shown is the file that must be right.
func skillsInstallURLDescription(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/skills.install.schema.json")
	if err != nil {
		t.Fatalf("read the skills.install contract: %v", err)
	}
	var contract struct {
		Properties struct {
			URL struct {
				Description string `json:"description"`
			} `json:"url"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse the skills.install contract: %v", err)
	}
	if contract.Properties.URL.Description == "" {
		t.Fatal("the skills.install contract's url field carries no description: the model is then told nothing about the one field it must fill")
	}
	return contract.Properties.URL.Description
}

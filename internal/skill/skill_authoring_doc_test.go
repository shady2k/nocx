package skill

import (
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/skill/builtin"
)

// THE SKILL NAMES NUMBERS THIS PACKAGE ENFORCES, SO THE NUMBERS ARE TESTED
// (nocx-ye2mq). The builtin skill tells the assistant what a write will be
// refused for — 64 characters of name, 2048 of description, 64 KiB of file —
// because a limit that only appears as a refusal after the fact is a limit
// the writer discovers by tripping over it. Prose that quotes a constant is
// prose that goes stale the first time the constant moves, and nothing else
// in the tree would notice: the skill is data, it compiles no matter what it
// says, and the only reader who would catch the drift is a model that has no
// way to tell us.
//
// So this test is in package `skill` rather than `skill_test`: it reads the
// live constants and asks the shipped text for them. Change a limit and this
// fails, naming the sentence to fix.
func TestTheBuiltinSkillQuotesTheLimitsThisPackageEnforces(t *testing.T) {
	doc := builtinSkillText(t)

	// The name cap is the pattern's own upper bound: one leading character
	// plus the repetition, so it is derived here rather than typed twice.
	const nameCap = 64
	if !skillNamePattern.MatchString(strings.Repeat("a", nameCap)) {
		t.Fatalf("a %d-character name is refused; the skill says that length is allowed", nameCap)
	}
	if skillNamePattern.MatchString(strings.Repeat("a", nameCap+1)) {
		t.Fatalf("a %d-character name is accepted; the skill says %d is the ceiling", nameCap+1, nameCap)
	}

	for _, want := range []struct {
		what   string
		phrase string
	}{
		{"the name cap", strconv.Itoa(nameCap) + " characters at most"},
		{"the description cap", strconv.Itoa(maxDescriptionRunes) + " characters at most"},
		{"the file cap", strconv.Itoa(maxSkillFileBytes>>10) + " KiB at most"},
	} {
		if !strings.Contains(doc, want.phrase) {
			t.Errorf("the builtin skill does not state %s: no %q in its text", want.what, want.phrase)
		}
	}
}

// AND IT OBEYS ITS OWN RULES. The skill tells its reader that a description
// over the cap costs them the whole skill; shipping one that would itself be
// dropped is the cheapest possible way to be wrong, and it would be invisible
// — a builtin arrives through discovery, which drops it with a log line
// (discover.go) and no error anybody sees.
func TestTheBuiltinSkillPassesTheRulesItDescribes(t *testing.T) {
	doc := builtinSkillText(t)

	fm, offset, ok := parseFrontmatter([]byte(doc))
	if !ok {
		t.Fatal("the builtin skill's frontmatter does not parse; discovery would drop it silently")
	}
	if !skillNamePattern.MatchString(fm.Name) {
		t.Errorf("name = %q, which discovery refuses", fm.Name)
	}
	if length, over := descriptionOverCap(strings.TrimSpace(fm.Description)); over {
		t.Errorf("description is %d runes, over the %d the skill itself quotes", length, maxDescriptionRunes)
	}
	if strings.TrimSpace(doc[offset:]) == "" {
		t.Error("the builtin skill has an empty body")
	}
	if len(doc) > maxSkillFileBytes {
		t.Errorf("the builtin skill is %d bytes, over the %d it quotes", len(doc), maxSkillFileBytes)
	}
}

func builtinSkillText(t *testing.T) string {
	t.Helper()
	data, err := builtin.FS.ReadFile("skill-authoring/SKILL.md")
	if err != nil {
		t.Fatalf("read the builtin skill: %v", err)
	}
	return string(data)
}

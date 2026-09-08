package skill

import "testing"

// The origin is a fact nocx establishes about a document it fetched itself
// (nocx-89mi5's decision, nocx-b6stz). These two helpers are the whole of what
// the assistant is allowed to ask about a repository reference: where the
// repository lives, and whether a document it holds actually names it.

func TestRepositoryURLIsTheCanonicalForgeAddress(t *testing.T) {
	if got := RepositoryURL("agentmail-to/agentmail-skills"); got != "https://github.com/agentmail-to/agentmail-skills" {
		t.Fatalf("RepositoryURL = %q", got)
	}
	if got := RepositoryURL(""); got != "" {
		t.Fatalf("RepositoryURL(\"\") = %q, want empty: an unnamed repository has no address", got)
	}
}

func TestDocumentNamesRepository(t *testing.T) {
	const repository = "agentmail-to/agentmail-skills"
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"the address in full", "AgentMail skills live in https://github.com/agentmail-to/agentmail-skills.\n", true},
		{"the bare slug", "Clone agentmail-to/agentmail-skills to get started.", true},
		{"case does not matter", "See GitHub.com/AgentMail-To/AgentMail-Skills", true},
		{"the .git suffix still names it", "git clone https://github.com/agentmail-to/agentmail-skills.git", true},
		{"a different repository", "See https://github.com/agentmail-to/agentmail-docs", false},
		{"a longer owner is not this owner", "https://github.com/not-agentmail-to/agentmail-skills", false},
		{"a longer repository is not this repository", "agentmail-to/agentmail-skills-archive", false},
		{"a document that names nothing", "Install the skill with our CLI.", false},
		{"an empty document", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DocumentNamesRepository(tc.text, repository); got != tc.want {
				t.Fatalf("DocumentNamesRepository(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
	if DocumentNamesRepository("anything at all", "") {
		t.Fatal("an empty repository is named by every document; it must be named by none")
	}
}

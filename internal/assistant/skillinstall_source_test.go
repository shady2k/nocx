package assistant

import (
	"testing"

	"github.com/shady2k/nocx/internal/skill"
)

// nocx-b6stz: the origin is established by nocx, not asserted by the model.
// The source of a resolved install route is the address of a document THIS RUN
// fetched whose bytes name the repository the resolution pinned. No such
// document, no source, no originsDiffer, no note.

const sourceTestRepository = "agentmail-to/agentmail-skills"

func heldRun(t *testing.T, docs ...runSnapshot) (*runSnapshots, string) {
	t.Helper()
	snapshots := newRunSnapshots()
	const runID = "run-under-test"
	for i, doc := range docs {
		snapshots.store(runID, "revision-"+string(rune('a'+i)), doc)
	}
	return snapshots, runID
}

func TestRouteSourceIsTheHeldDocumentThatNamesTheRepository(t *testing.T) {
	snapshots, runID := heldRun(t,
		runSnapshot{URL: "https://www.agentmail.to/docs/integrations/skills", Text: "AgentMail skills live in https://github.com/" + sourceTestRepository + ".\n"},
	)
	if got := routeSourceFromRun(snapshots, runID, sourceTestRepository); got != "https://www.agentmail.to/docs/integrations/skills" {
		t.Fatalf("routeSourceFromRun = %q", got)
	}
}

func TestRouteSourceIsTheFirstSuchDocument(t *testing.T) {
	// Two pages name it. The route STARTED at the first one fetched; picking
	// the most recent would be an inference about intent from adjacency, which
	// nocx-89mi5 rejected by name.
	snapshots, runID := heldRun(t,
		runSnapshot{URL: "https://www.agentmail.to/docs/integrations/skills", Text: "See " + sourceTestRepository + "."},
		runSnapshot{URL: "https://example.test/mirror", Text: "Also " + sourceTestRepository + "."},
	)
	if got := routeSourceFromRun(snapshots, runID, sourceTestRepository); got != "https://www.agentmail.to/docs/integrations/skills" {
		t.Fatalf("routeSourceFromRun = %q, want the first document fetched", got)
	}
}

func TestRouteSourceIsAbsentWhenNothingNamesTheRepository(t *testing.T) {
	t.Run("a run that fetched nothing", func(t *testing.T) {
		snapshots, runID := heldRun(t)
		if got := routeSourceFromRun(snapshots, runID, sourceTestRepository); got != "" {
			t.Fatalf("routeSourceFromRun = %q, want empty", got)
		}
	})
	t.Run("a run whose pages name a different repository", func(t *testing.T) {
		snapshots, runID := heldRun(t,
			runSnapshot{URL: "https://www.agentmail.to/docs/integrations/skills", Text: "See https://github.com/agentmail-to/agentmail-docs."},
		)
		if got := routeSourceFromRun(snapshots, runID, sourceTestRepository); got != "" {
			t.Fatalf("routeSourceFromRun = %q, want empty", got)
		}
	})
	t.Run("another run's document is not this run's", func(t *testing.T) {
		snapshots, runID := heldRun(t)
		snapshots.store("some-other-run", "revision-a", runSnapshot{
			URL:  "https://www.agentmail.to/docs/integrations/skills",
			Text: "See " + sourceTestRepository + ".",
		})
		if got := routeSourceFromRun(snapshots, runID, sourceTestRepository); got != "" {
			t.Fatalf("routeSourceFromRun = %q, want empty", got)
		}
	})
	t.Run("no snapshot store at all", func(t *testing.T) {
		if got := routeSourceFromRun(nil, "run-under-test", sourceTestRepository); got != "" {
			t.Fatalf("routeSourceFromRun = %q, want empty", got)
		}
	})
}

func resolvedFactsWithSource(t *testing.T, source string) *ApprovalInstall {
	t.Helper()
	resolution := &skill.Resolution{
		Handle:     "resolution-1",
		Repository: sourceTestRepository,
		Ref:        "main",
		Commit:     "commit-123",
	}
	previews := []skill.PreviewResult{{
		URL:         "https://raw.githubusercontent.com/" + sourceTestRepository + "/commit-123/agentmail/SKILL.md",
		Name:        "agentmail",
		Description: "AgentMail integrations.",
		Digest:      "digest-1",
	}}
	facts := InstallFactsForResolved(resolution, source, []string{"agentmail/SKILL.md"}, previews)
	if facts == nil {
		t.Fatal("InstallFactsForResolved returned nothing")
	}
	return facts
}

func TestOriginsDifferIsTrueWhenTheSourcePageIsOnAnotherHost(t *testing.T) {
	facts := resolvedFactsWithSource(t, "https://www.agentmail.to/docs/integrations/skills")
	if facts.Source != "https://www.agentmail.to/docs/integrations/skills" {
		t.Fatalf("source = %q", facts.Source)
	}
	if facts.OriginsDiffer == nil || !*facts.OriginsDiffer {
		t.Fatalf("originsDiffer = %v, want true: the page is on www.agentmail.to and the repository is on github.com", facts.OriginsDiffer)
	}
}

func TestOriginsDifferIsFalseWhenTheSourcePageIsOnTheRepositoryHost(t *testing.T) {
	facts := resolvedFactsWithSource(t, "https://github.com/"+sourceTestRepository+"/blob/main/README.md")
	if facts.Source == "" {
		t.Fatal("source is absent: a page on the repository's own host is still where the route started")
	}
	if facts.OriginsDiffer == nil || *facts.OriginsDiffer {
		t.Fatalf("originsDiffer = %v, want false: both the page and the repository are on github.com", facts.OriginsDiffer)
	}
}

func TestNoSourceMeansNoRouteNote(t *testing.T) {
	facts := resolvedFactsWithSource(t, "")
	if facts.Source != "" {
		t.Fatalf("source = %q, want empty", facts.Source)
	}
	if facts.OriginsDiffer != nil {
		t.Fatalf("originsDiffer = %v, want absent: a field that is never true is worse than an absent one", *facts.OriginsDiffer)
	}
	if facts.Destination != sourceTestRepository || facts.Ref != "main" || facts.Commit != "commit-123" {
		t.Fatalf("the facts that ARE real were dropped with the note: %+v", facts)
	}
}

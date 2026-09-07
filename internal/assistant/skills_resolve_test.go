package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

type resolveTestLibrary struct {
	resolution *skill.Resolution
}

func (r *resolveTestLibrary) Index() []skill.Skill                       { return nil }
func (r *resolveTestLibrary) Read(string, string) (skill.Content, error) { return skill.Content{}, nil }
func (r *resolveTestLibrary) RecordUse(string)                           {}
func (r *resolveTestLibrary) Create(string, string, string) error        { return nil }
func (r *resolveTestLibrary) Update(string, string, string) error        { return nil }
func (r *resolveTestLibrary) Delete(string) error                        { return nil }
func (r *resolveTestLibrary) Preview(context.Context, string) (skill.PreviewResult, error) {
	return skill.PreviewResult{}, nil
}

func (r *resolveTestLibrary) Install(context.Context, string) (skill.InstallResult, error) {
	return skill.InstallResult{}, nil
}

func (r *resolveTestLibrary) Resolve(context.Context, string) (*skill.Resolution, error) {
	return r.resolution, nil
}

func TestExecuteSkillsResolveReturnsThePinnedPlan(t *testing.T) {
	library := &resolveTestLibrary{resolution: &skill.Resolution{
		Handle: "resolution-1", Repository: "github.com/acme/skills", Ref: "main", Commit: "abc123",
		Candidates: []skill.ResolutionCandidate{{Path: "deploy/SKILL.md", Name: "deploy", Description: "Deploy"}},
	}}
	capability := &agenttools.URLScope{Endpoints: []content.GrantScope{{Kind: content.ResourceDestination, ID: "https://github.com"}}}
	fn, ok := executors["skills.resolve"]
	if !ok {
		t.Fatal("skills.resolve has no executor")
	}
	got, err := fn(withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: 8 << 10, Truncation: agenttools.TruncationDropTail}), capability, json.RawMessage(`{"url":"https://github.com/acme/skills"}`), toolSeams{skills: library})
	if err != nil {
		t.Fatalf("execute skills.resolve: %v", err)
	}
	var plan skill.Resolution
	if err := json.Unmarshal([]byte(got), &plan); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if plan.Repository != "github.com/acme/skills" || plan.Commit != "abc123" || len(plan.Candidates) != 1 {
		t.Fatalf("plan = %+v, want the pinned repository and candidate", plan)
	}
}

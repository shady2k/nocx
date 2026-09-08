package assistant

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

// failingResolveLibrary answers every Resolve with one upstream failure and
// records nothing, which is what the real Store does on this path
// (internal/skill/resolution.go: "a failure simply records nothing and can
// leave nothing behind").
type failingResolveLibrary struct {
	resolveErr error
	deleteErr  error
}

func (l *failingResolveLibrary) Index() []skill.Skill { return nil }
func (l *failingResolveLibrary) Read(string, string) (skill.Content, error) {
	return skill.Content{}, nil
}
func (l *failingResolveLibrary) RecordUse(string)                    {}
func (l *failingResolveLibrary) Create(string, string, string) error { return nil }
func (l *failingResolveLibrary) Update(string, string, string) error { return nil }
func (l *failingResolveLibrary) Delete(string) error                 { return l.deleteErr }

func (l *failingResolveLibrary) Preview(context.Context, string) (skill.PreviewResult, error) {
	return skill.PreviewResult{}, nil
}

func (l *failingResolveLibrary) Install(context.Context, string) (skill.InstallResult, error) {
	return skill.InstallResult{}, nil
}

func (l *failingResolveLibrary) Resolve(context.Context, string) (*skill.Resolution, error) {
	return nil, l.resolveErr
}

// invokeApproved drives one call the way the transport does when a person
// says yes: the first pass suspends (every skill-family tool asks —
// isSkillMutationTool, because a skill outlives the run that authorised it),
// the exact proposal is approved, and the second pass runs the tool. What
// these tests are about is what happens AFTER it runs and fails.
func invokeApproved(t *testing.T, library SkillLibrary, tool, callID, args string) (string, error) {
	t.Helper()
	grant := autonomousMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceContent, ID: "skill"},
		{Kind: content.ResourceDestination, ID: "*"},
	})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	approvals := NewApprovalStore()
	newKernel := func() *effectKernel {
		k, kErr := newEffectKernel(nil, grant, reg, &fakeLedger{}, approvals, &fakeKnownMaterial{},
			"run-tool-failure", "session-tool-failure", 1, "", nil, Attachments{}, nil, nil,
			toolSeams{skills: library})
		if kErr != nil {
			t.Fatalf("newEffectKernel: %v", kErr)
		}
		return k
	}

	_, err = newKernel().Invoke(context.Background(), tool, callID, args)
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("first pass error = %v, want the approval suspension %s asks for", err, tool)
	}
	if !approvals.Approve(Approval{
		RunID:   asked.Request.RunID,
		Attempt: asked.Request.Attempt,
		Tool:    asked.Request.Tool,
		CallID:  asked.Request.CallID,
		ArgHash: asked.Request.ArgHash,
	}) {
		t.Fatal("the exact proposal the kernel asked about was not pending")
	}
	return newKernel().Invoke(context.Background(), tool, callID, args)
}

// A forge that answers 504 is the case skills.resolve's own declaration
// tells the model to survive: "call this tool and let it refuse unsupported
// addresses, then fall back to skills.install". That fallback is only
// reachable if the failure comes back as the CALL'S RESULT — a failed run
// ends the turn and there is nobody left to fall back.
func TestResolveFailureIsAResultTheModelReadsRatherThanAFailedRun(t *testing.T) {
	upstream := errors.New("skills.resolve: GitHub repository metadata: GitHub API request failed: " +
		"apifetch: fetching https://api.github.com/repos/acme/skills: the server answered 504 Gateway Timeout")
	out, err := invokeApproved(t, &failingResolveLibrary{resolveErr: upstream},
		"skills.resolve", "call-resolve-504", `{"url":"https://github.com/acme/skills"}`)
	if err != nil {
		t.Fatalf("Invoke error = %v, want the failure returned as a tool result so the run continues", err)
	}
	if !strings.Contains(out, "504 Gateway Timeout") {
		t.Fatalf("result = %q, want it to carry what actually went wrong", out)
	}
	if !strings.Contains(out, "skills.resolve") {
		t.Fatalf("result = %q, want it to name the tool that failed", out)
	}
	if !strings.Contains(out, "Nothing was changed") {
		t.Fatalf("result = %q, want it to say the call changed nothing, so the model knows it may try another way", out)
	}
}

// The other half of the same rule, in the same file so neither can be
// weakened without the other being read: a tool whose failure may have left
// a partial change still ends the run. skills.delete declares
// mutate-destructive, and a delete that fails partway has already removed
// something.
func TestAMutatingToolsFailureStillEndsTheRun(t *testing.T) {
	library := &failingResolveLibrary{
		deleteErr: errors.New("skill \"deploy\": remove directory: permission denied"),
	}
	_, err := invokeApproved(t, library, "skills.delete", "call-delete-denied", `{"name":"deploy"}`)
	var toolErr *ToolFailedError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Invoke error = %v, want a ToolFailedError: a failed write may have left something behind", err)
	}
	if toolErr.Tool != "skills.delete" {
		t.Fatalf("failed tool = %q, want skills.delete", toolErr.Tool)
	}
}

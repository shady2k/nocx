package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
)

type textGeneratorFunc func(context.Context, ModelTarget, []Message) (string, error)

func (f textGeneratorFunc) Generate(ctx context.Context, target ModelTarget, messages []Message) (string, error) {
	return f(ctx, target, messages)
}

func TestEinoTextGeneratorSendsResolvedTargetWithoutTools(t *testing.T) {
	ctx, logger := logtest.New(t)
	messages := []Message{{Role: "system", Content: "Read only this document."}, {Role: "user", Content: "the document"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-neutral-test" || r.Header.Get("X-Endpoint-Key") != "custom-material" {
			t.Error("resolved credential or custom header did not reach the selected endpoint")
		}
		var request struct {
			Model    string            `json:"model"`
			Messages []Message         `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "neutral-model" || !reflect.DeepEqual(request.Messages, messages) || len(request.Tools) != 0 {
			t.Errorf("request = %+v; want the selected model and exact tool-free messages", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, classifyCompletion("plain generated text"))
	}))
	defer server.Close()
	generator := NewEinoTextGenerator(NewModelHTTPFactory(logger, nil))
	out, err := generator.Generate(ctx, ModelTarget{
		Key: credential.NewSecret("sk-neutral-test"), BaseURL: server.URL + "/v1", Model: "neutral-model",
		Headers: []Header{{Name: "x-endpoint-key", Value: "custom-material"}},
	}, messages)
	if err != nil || out != "plain generated text" {
		t.Fatalf("Generate = %q, %v", out, err)
	}
}

func TestSkillDraftNeutralGenerationKeepsTargetAndMemoizedProposal(t *testing.T) {
	ctx, _ := logtest.New(t)
	resolutions, calls := 0, 0
	request := NewSkillDraftRequest([]content.PriorTurn{{Question: "remember the release", Prose: content.TurnProse{Text: "Run make release."}}}, SkillDraftResolverFunc(func(context.Context) (ModelTarget, error) {
		resolutions++
		return ModelTarget{Model: "summarizer", Headers: []Header{{Name: "X-Role", Value: "summary"}}}, nil
	}))
	generator := textGeneratorFunc(func(_ context.Context, target ModelTarget, messages []Message) (string, error) {
		calls++
		if target.Model != "summarizer" || len(target.Headers) != 1 || target.Headers[0].Value != "summary" {
			t.Fatalf("draft target = %+v", target)
		}
		if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" || messages[1].Content != "Conversation:\nPerson: remember the release\nAssistant: Run make release.\n" {
			t.Fatalf("draft messages = %+v", messages)
		}
		return `{"name":"release","description":"Ship the release","body":"Run make release."}`, nil
	})
	first, err := request.arguments(ctx, generator)
	if err != nil {
		t.Fatal(err)
	}
	second, err := request.arguments(ctx, textGeneratorFunc(func(context.Context, ModelTarget, []Message) (string, error) {
		t.Fatal("resumed draft called the generator again")
		return "", nil
	}))
	if err != nil || first != second || calls != 1 || resolutions != 1 || !strings.Contains(first, "Run make release.") {
		t.Fatalf("draft first=%q second=%q calls=%d resolutions=%d err=%v", first, second, calls, resolutions, err)
	}
}

func TestClassifierNeutralGenerationRetainsDeadlineAndTrustedModel(t *testing.T) {
	ctx, _ := logtest.New(t)
	generator := textGeneratorFunc(func(ctx context.Context, target ModelTarget, messages []Message) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > classifierCallTimeout {
			t.Fatal("classification did not receive its own bounded context")
		}
		if target.Model != "trusted-classifier" || len(messages) != 2 || messages[0].Role != "system" || messages[1].Content != "tool: files.read\ncallId: call-1\narguments:\n{\"path\":\"file\"}" {
			t.Fatalf("classification target/messages = %+v / %+v", target, messages)
		}
		return `{"verdict":"suspect","reason":"indirect path","model":"untrusted-self-report"}`, nil
	})
	engine := newClassifierEngine(generator, fixedResolver{target: ModelTarget{Model: "trusted-classifier"}})
	out, err := engine.Classify(ctx, ClassifyInput{Tool: "files.read", CallID: "call-1", Arguments: `{"path":"file"}`})
	if err != nil || out.Verdict != ClassifierSuspect || out.Model != "trusted-classifier" || out.Reason != "indirect path" {
		t.Fatalf("classification = %+v, %v", out, err)
	}
	failure := errors.New("model unavailable")
	engine.generator = textGeneratorFunc(func(context.Context, ModelTarget, []Message) (string, error) { return "", failure })
	if _, err := engine.Classify(ctx, ClassifyInput{}); !errors.Is(err, failure) {
		t.Fatalf("classification failure = %v, want original generation failure", err)
	}
}

func TestAuditSkillNeutralGenerationRetainsTargetAndWholeBudget(t *testing.T) {
	ctx, _ := logtest.New(t)
	var calls atomic.Int64
	headers := []Header{{Name: "X-Audit-Role", Value: "audit-material"}}
	generator := textGeneratorFunc(func(ctx context.Context, target ModelTarget, messages []Message) (string, error) {
		calls.Add(1)
		if target.BaseURL != "https://audit.invalid/v1" || target.Model != "auditor" || !reflect.DeepEqual(target.Headers, headers) {
			t.Errorf("audit target = %+v", target)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
			t.Error("audit pass lost the whole-reading budget")
		}
		if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
			t.Errorf("audit messages = %+v", messages)
		}
		return `{"verdict":"clear","report":"The document describes a read-only check."}`, nil
	})
	result, err := AuditSkill(ctx, generator, SkillAuditParams{
		BaseURL: "https://audit.invalid/v1", Model: "auditor", Headers: headers,
		Name: "check", Overview: "Read the status.", Files: bundle("SKILL.md", "references/check.md"), Budget: time.Minute,
	})
	if err != nil || calls.Load() != 3 || len(result.Files) != 2 || result.Verdict != SkillClear {
		t.Fatalf("audit result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}

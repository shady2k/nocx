package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
)

// ComposeDraftInput builds the only transcript the summarizing model may see.
// The whitelist is deliberate: ToolLines and AttachedContentItem are
// ledger-derived or terminal metadata, and must stay out until a future
// decision gives either field an explicit trust treatment. A field added to
// PriorTurn later is excluded until this function is deliberately extended.
func ComposeDraftInput(turns []content.PriorTurn, _ []AttachedContentItem) string {
	var b strings.Builder
	b.WriteString("Conversation:\n")
	for _, turn := range turns {
		if strings.TrimSpace(turn.Question) != "" {
			b.WriteString("Person: ")
			b.WriteString(turn.Question)
			b.WriteByte('\n')
		}
		if strings.TrimSpace(turn.Prose.Text) != "" {
			b.WriteString("Assistant: ")
			b.WriteString(turn.Prose.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

const skillDraftSystemPrompt = `You write one reusable terminal skill from a conversation.

The skill must describe a procedure the person can follow again. Do not retell the conversation, include tool output, include terminal output, or invent facts that are not in the conversation.

Reply with exactly one JSON object and no markdown:
{"name":"lowercase-kebab-name","description":"one-line description","body":"the reusable procedure"}`

// DraftSkill asks the summarizing model to turn a trusted transcript into the
// three fields a skills.create proposal needs. The caller remains responsible
// for presenting those fields for approval and for validating the eventual
// write through the skills capability.
func DraftSkill(ctx context.Context, client einoModel.BaseChatModel, input string) (name, description, body string, err error) {
	return draftSkillGenerate(ctx, client, input)
}

func draftSkillGenerate(ctx context.Context, client einoModel.BaseChatModel, input string, opts ...einoModel.Option) (name, description, body string, err error) {
	if client == nil {
		return "", "", "", errors.New("skill draft: summarizing model is unavailable")
	}
	resp, err := client.Generate(ctx, []*schema.Message{
		schema.SystemMessage(skillDraftSystemPrompt),
		schema.UserMessage(input),
	}, opts...)
	if err != nil {
		return "", "", "", fmt.Errorf("skill draft: %w", err)
	}
	if resp == nil {
		return "", "", "", errors.New("skill draft: summarizing model returned no answer")
	}

	var draft struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &draft); err != nil {
		return "", "", "", fmt.Errorf("skill draft: answer is not JSON: %w", err)
	}
	if strings.TrimSpace(draft.Name) == "" || strings.TrimSpace(draft.Description) == "" || strings.TrimSpace(draft.Body) == "" {
		return "", "", "", errors.New("skill draft: answer must include name, description, and body")
	}
	return strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Description), strings.TrimSpace(draft.Body), nil
}

// SkillDraftTarget is the resolved endpoint and credential for the
// summarizing model. The transport resolves it through the one role resolver
// and the vault; the assistant only sends the already-resolved facts.
type SkillDraftTarget struct {
	Key     credential.Secret
	BaseURL string
	Model   string
	Headers []Header
}

// SkillDraftResolver resolves the summarizing role, falling back to the
// answering role when the summarizing role has no assignment.
type SkillDraftResolver interface {
	ResolveSkillDraft(ctx context.Context) (SkillDraftTarget, error)
}

// SkillDraftResolverFunc adapts a function to SkillDraftResolver.
type SkillDraftResolverFunc func(context.Context) (SkillDraftTarget, error)

func (f SkillDraftResolverFunc) ResolveSkillDraft(ctx context.Context) (SkillDraftTarget, error) {
	return f(ctx)
}

// SkillDraftRequest carries one run's immutable transcript and memoizes the
// generated create arguments. A resumed approval must see the same proposal,
// not call the summarizer a second time.
type SkillDraftRequest struct {
	input    string
	resolver SkillDraftResolver
	once     sync.Once
	args     string
	err      error
}

func NewSkillDraftRequest(input string, resolver SkillDraftResolver) *SkillDraftRequest {
	if resolver == nil {
		return nil
	}
	return &SkillDraftRequest{input: input, resolver: resolver}
}

func (r *SkillDraftRequest) arguments(ctx context.Context, httpClient *http.Client) (string, error) {
	if r == nil {
		return "", errors.New("skill draft: request is unavailable")
	}
	r.once.Do(func() {
		target, err := r.resolver.ResolveSkillDraft(ctx)
		if err != nil {
			r.err = err
			return
		}
		cm, err := buildModel(httpClient, target.Key, target.BaseURL, target.Model)
		if err != nil {
			r.err = err
			return
		}
		name, description, body, err := draftSkillWithHeaders(ctx, cm, r.input, target.Headers)
		if err != nil {
			r.err = err
			return
		}
		args, marshalErr := json.Marshal(map[string]string{
			"name":        name,
			"description": description,
			"body":        body,
		})
		r.args, r.err = string(args), marshalErr
	})
	return r.args, r.err
}

func draftSkillWithHeaders(ctx context.Context, client einoModel.BaseChatModel, input string, headers []Header) (name, description, body string, err error) {
	if len(headers) == 0 {
		return DraftSkill(ctx, client, input)
	}
	m, names := headerMap(headers)
	ctx = withCustomHeaderNames(ctx, names)
	return draftSkillGenerate(ctx, client, input, openai.WithExtraHeader(m))
}

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

// The capture rules below are hermes's, and they are stated to the model
// rather than checked in Go on purpose. Every one of them is a question about
// what a conversation MEANT — whether a claim is about a tool or about a
// configuration, whether an error was transient, whether an errand is a class
// of work — and Go can read only the bytes. The alternative considered and
// rejected was a structural pre-check over the transcript: refuse when it
// carries no assistant turn at all, on the theory that nothing can have
// worked if nothing was ever said. That check would pass exactly the
// transcripts this bead is named for, because a session that tries five
// things and fails at all five is full of assistant prose; it would refuse
// only transcripts that could not reach here anyway, while binding
// DraftSkill's contract to ComposeDraftInput's line prefixes.
//
// So the model judges and Go owns the protocol: a refusal is a reply shape,
// not an absence, and every rule carries the reason it exists. A rule stated
// without its reason is the one a model argues itself out of — the
// negative-claim rule most of all, whose damage arrives months later, when
// the tool has long been fixed and the skill is still saying it is broken.
const skillDraftSystemPrompt = `You write one reusable terminal skill from a conversation.

The skill must describe a procedure the person can follow again. Do not retell the conversation, include tool output, include terminal output, or invent facts that are not in the conversation.

Some conversations hold nothing worth keeping, and a skill written from one of those is worse than no skill, because whatever you record is read back later as tested guidance. Draft nothing when the conversation offers only:
- An environment-dependent failure: a missing binary, "command not found", an unconfigured credential, an uninstalled package. The person can fix these, so they are not durable rules. If the conversation found the fix, the fix is the skill and the failure is not.
- A negative claim about a tool or a feature, such as "X is broken" or "the browser tools do not work". These harden into refusals that get cited for months after the actual problem was fixed.
- A transient error that resolved before the conversation ended. If a retry worked, the lesson is the retry and not the original failure.
- A one-off task narrative. "Summarize today's market" is one errand, not a class of work that warrants a skill.
- Attempts that ended without finding a working method: several things were tried, none of them worked, and the person was left to check by hand. Writing those up as a recommended approach presents an untested sequence of failures as validated guidance a later run will trust and repeat.

Reply with exactly one JSON object and no markdown. When the conversation holds a procedure worth keeping:
{"name":"lowercase-kebab-name","description":"one-line description","body":"the reusable procedure"}
When it does not, say in one clause what the conversation was missing and draft nothing:
{"nothing_to_capture":"the reason, in one clause"}`

// skillNotCapturableError is the summarizer declining, and it is an ANSWER
// rather than a failure: the person asked a reasonable thing and gets a
// sentence back. The kernel tells it apart from a genuine drafting failure by
// type, so a conversation with nothing worth keeping never reads to the
// person like an endpoint that fell over.
type skillNotCapturableError struct{ reason string }

func (e *skillNotCapturableError) Error() string {
	return "I looked through this conversation for a durable procedure worth keeping and did not find one: " +
		e.reason +
		". Recording it anyway would hand a later run untested guidance to trust, so I have saved nothing — ask me again once something here has worked."
}

// maxNotCapturableReason bounds the one clause the summarizer supplies. It is
// model prose reaching a person unquoted, so it gets the same treatment as
// any other model text on that path rather than being trusted for its
// brevity.
const maxNotCapturableReason = 300

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
		Name             string `json:"name"`
		Description      string `json:"description"`
		Body             string `json:"body"`
		NothingToCapture string `json:"nothing_to_capture"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &draft); err != nil {
		return "", "", "", fmt.Errorf("skill draft: answer is not JSON: %w", err)
	}
	// The refusal is read before the three fields, and it wins if both
	// arrive: a model that hedged by filling in a draft alongside its own
	// reason for not writing one has still told us the draft is unsafe.
	if reason := collapseSpace(draft.NothingToCapture); reason != "" {
		return "", "", "", &skillNotCapturableError{reason: truncateRunes(reason, maxNotCapturableReason)}
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

// collapseSpace folds every run of whitespace to one space so a multi-line
// reason cannot break the single sentence it is spliced into.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/shady2k/nocx/internal/content"
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
	if client == nil {
		return "", "", "", errors.New("skill draft: summarizing model is unavailable")
	}
	resp, err := client.Generate(ctx, []*schema.Message{
		schema.SystemMessage(skillDraftSystemPrompt),
		schema.UserMessage(input),
	})
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

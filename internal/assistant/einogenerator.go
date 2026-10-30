package assistant

import (
	"context"
	"net/http"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type einoTextGenerator struct{ http *http.Client }

// NewEinoTextGenerator retains Eino as the live tool-free generation adapter.
func NewEinoTextGenerator(httpFactory ModelHTTPFactory) TextGenerator {
	return &einoTextGenerator{http: httpFactory.NewModelHTTPClient()}
}

func (g *einoTextGenerator) Generate(ctx context.Context, target ModelTarget, messages []Message) (string, error) {
	cm, err := buildModel(g.http, target.Key, target.BaseURL, target.Model)
	if err != nil {
		return "", err
	}
	var opts []einoModel.Option
	if headers, names := headerMap(target.Headers); headers != nil {
		opts = append(opts, openai.WithExtraHeader(headers))
		ctx = withCustomHeaderNames(ctx, names)
	}
	response, err := cm.Generate(ctx, einoMessages(messages), opts...)
	if err != nil || response == nil {
		return "", err
	}
	return response.Content, nil
}

func einoMessages(messages []Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "user":
			out = append(out, schema.UserMessage(message.Content))
		case "assistant":
			out = append(out, schema.AssistantMessage(message.Content, nil))
		default:
			out = append(out, schema.SystemMessage(message.Content))
		}
	}
	return out
}

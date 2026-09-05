package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func boundResult(serverID, tool string, response *sdk.CallToolResult, maxBytes int, sensitive []string) Result {
	result := Result{
		ServerID:  serverID,
		Tool:      tool,
		Text:      []string{},
		Resources: []Resource{},
		Omitted:   []Omitted{},
	}
	if response == nil {
		result.IsError = true
		result.Text = append(result.Text, "MCP server returned no result")
		return fitResult(result, maxBytes)
	}
	result.IsError = response.IsError
	remaining := max(maxBytes-1024, 0)
	for _, content := range response.Content {
		if len(result.Text)+len(result.Resources)+len(result.Omitted) >= maxResultEntries {
			appendOmitted(&result, Omitted{Type: "content", Reason: "entry limit"})
			break
		}
		switch value := content.(type) {
		case *sdk.TextContent:
			text := redactKnown(value.Text, sensitive)
			keep := min(len(text), remaining)
			text = clipUTF8(text, keep)
			result.Text = append(result.Text, text)
			remaining -= len(text)
			if keep < len(value.Text) {
				appendOmitted(&result, Omitted{Type: "text", Bytes: int64(len(value.Text) - keep), Reason: "result byte limit"})
			}
		case *sdk.ResourceLink:
			resource := Resource{
				URI:      redactKnown(clipUTF8(value.URI, 2048), sensitive),
				Name:     redactKnown(clipUTF8(value.Name, 256), sensitive),
				Title:    redactKnown(clipUTF8(value.Title, 256), sensitive),
				MIMEType: clipUTF8(value.MIMEType, 256),
			}
			result.Resources = append(result.Resources, resource)
		case *sdk.EmbeddedResource:
			if value.Resource == nil {
				appendOmitted(&result, Omitted{Type: "resource", Reason: "missing resource"})
				continue
			}
			resource := value.Resource
			if len(resource.Blob) > 0 {
				appendOmitted(&result, Omitted{Type: "resource", MIMEType: clipUTF8(resource.MIMEType, 256), Bytes: int64(len(resource.Blob)), Reason: "binary media is not included"})
				continue
			}
			text := redactKnown(resource.Text, sensitive)
			keep := min(len(text), remaining)
			text = clipUTF8(text, keep)
			result.Resources = append(result.Resources, Resource{
				URI:      redactKnown(clipUTF8(resource.URI, 2048), sensitive),
				MIMEType: clipUTF8(resource.MIMEType, 256),
				Text:     text,
			})
			remaining -= len(text)
			if keep < len(resource.Text) {
				appendOmitted(&result, Omitted{Type: "resource-text", MIMEType: clipUTF8(resource.MIMEType, 256), Bytes: int64(len(resource.Text) - keep), Reason: "result byte limit"})
			}
		case *sdk.ImageContent:
			appendOmitted(&result, Omitted{Type: "image", MIMEType: clipUTF8(value.MIMEType, 256), Bytes: int64(len(value.Data)), Reason: "binary media is not included"})
		case *sdk.AudioContent:
			appendOmitted(&result, Omitted{Type: "audio", MIMEType: clipUTF8(value.MIMEType, 256), Bytes: int64(len(value.Data)), Reason: "binary media is not included"})
		default:
			appendOmitted(&result, Omitted{Type: "unsupported", Reason: "unsupported MCP content type"})
		}
	}
	if response.StructuredContent != nil {
		clean := redactStructured(response.StructuredContent, sensitive, 0)
		if encoded, err := json.Marshal(clean); err == nil && len(encoded) <= remaining {
			result.StructuredContent = encoded
		} else {
			appendOmitted(&result, Omitted{Type: "structured-content", Reason: "result byte limit or invalid JSON"})
		}
	}
	if len(result.Text) == 0 && len(result.Resources) == 0 && len(result.Omitted) > 0 {
		result.Text = append(result.Text, "The MCP result contained only content that nocx could not include.")
	}
	return fitResult(result, maxBytes)
}

func appendOmitted(result *Result, omitted Omitted) {
	if len(result.Omitted) >= maxResultEntries {
		return
	}
	result.Omitted = append(result.Omitted, omitted)
}

func fitResult(result Result, maxBytes int) Result {
	for {
		encoded, err := json.Marshal(result)
		if err == nil && len(encoded) <= maxBytes {
			return result
		}
		switch {
		case len(result.StructuredContent) > 0:
			result.StructuredContent = nil
			appendOmitted(&result, Omitted{Type: "structured-content", Reason: "result byte limit"})
		case len(result.Resources) > 0:
			result.Resources = result.Resources[:len(result.Resources)-1]
		case len(result.Text) > 0:
			last := len(result.Text) - 1
			if len(result.Text[last]) > 64 {
				result.Text[last] = clipUTF8(result.Text[last], len(result.Text[last])/2)
			} else {
				result.Text = result.Text[:last]
			}
		case len(result.Omitted) > 0:
			result.Omitted = result.Omitted[:len(result.Omitted)-1]
		default:
			return Result{ServerID: clipUTF8(result.ServerID, 128), Tool: clipUTF8(result.Tool, 256), IsError: result.IsError, Text: []string{}, Resources: []Resource{}, Omitted: []Omitted{}}
		}
	}
}

func redactKnown(value string, sensitive []string) string {
	for _, secret := range sensitive {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func redactStructured(value any, sensitive []string, depth int) any {
	if depth > 32 {
		return nil
	}
	switch value := value.(type) {
	case string:
		return redactKnown(value, sensitive)
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = redactStructured(value[i], sensitive, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[redactKnown(key, sensitive)] = redactStructured(child, sensitive, depth+1)
		}
		return out
	default:
		return value
	}
}

func validateStructuredContent(raw json.RawMessage, schemaRaw json.RawMessage) error {
	if len(raw) == 0 || len(schemaRaw) == 0 || string(schemaRaw) == "null" {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return errors.New("structured content is invalid JSON")
	}
	const resource = "https://nocx.local/mcp/output-schema.json"
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		return errors.New("MCP output schema is invalid")
	}
	compiler := jsonschema.NewCompiler()
	if addErr := compiler.AddResource(resource, doc); addErr != nil {
		return errors.New("MCP output schema cannot be compiled")
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return errors.New("MCP output schema cannot be compiled")
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("structured content does not match MCP output schema: %w", err)
	}
	return nil
}

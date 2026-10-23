package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
				URI:      clipUTF8(redactKnown(value.URI, sensitive), 2048),
				Name:     clipUTF8(redactKnown(value.Name, sensitive), 256),
				Title:    clipUTF8(redactKnown(value.Title, sensitive), 256),
				MIMEType: clipUTF8(redactKnown(value.MIMEType, sensitive), 256),
			}
			result.Resources = append(result.Resources, resource)
		case *sdk.EmbeddedResource:
			if value.Resource == nil {
				appendOmitted(&result, Omitted{Type: "resource", Reason: "missing resource"})
				continue
			}
			resource := value.Resource
			if len(resource.Blob) > 0 {
				appendOmitted(&result, Omitted{Type: "resource", MIMEType: clipUTF8(redactKnown(resource.MIMEType, sensitive), 256), Bytes: int64(len(resource.Blob)), Reason: "binary media is not included"})
				continue
			}
			text := redactKnown(resource.Text, sensitive)
			keep := min(len(text), remaining)
			text = clipUTF8(text, keep)
			result.Resources = append(result.Resources, Resource{
				URI:      clipUTF8(redactKnown(resource.URI, sensitive), 2048),
				MIMEType: clipUTF8(redactKnown(resource.MIMEType, sensitive), 256),
				Text:     text,
			})
			remaining -= len(text)
			if keep < len(resource.Text) {
				appendOmitted(&result, Omitted{Type: "resource-text", MIMEType: clipUTF8(redactKnown(resource.MIMEType, sensitive), 256), Bytes: int64(len(resource.Text) - keep), Reason: "result byte limit"})
			}
		case *sdk.ImageContent:
			appendOmitted(&result, Omitted{Type: "image", MIMEType: clipUTF8(redactKnown(value.MIMEType, sensitive), 256), Bytes: int64(len(value.Data)), Reason: "binary media is not included"})
		case *sdk.AudioContent:
			appendOmitted(&result, Omitted{Type: "audio", MIMEType: clipUTF8(redactKnown(value.MIMEType, sensitive), 256), Bytes: int64(len(value.Data)), Reason: "binary media is not included"})
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
			appendOmitted(&result, Omitted{Type: "resource", Reason: "result byte limit"})
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
			return Result{ServerID: result.ServerID, Tool: result.Tool, IsError: result.IsError, Text: []string{}, Resources: []Resource{}, Omitted: []Omitted{}}
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
	case json.Number:
		if clean := redactKnown(value.String(), sensitive); clean != value.String() {
			return clean
		}
		return value
	case float64:
		raw, _ := json.Marshal(value)
		if clean := redactKnown(string(raw), sensitive); clean != string(raw) {
			return clean
		}
		return value
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
	return validateAgainstSchema(raw, schemaRaw, "MCP output schema", "structured content", "does not match")
}

func validateToolArguments(raw json.RawMessage, schemaRaw json.RawMessage) error {
	if len(schemaRaw) == 0 || string(schemaRaw) == "null" {
		return errors.New("MCP tool input schema is missing")
	}
	return validateAgainstSchema(raw, schemaRaw, "MCP tool input schema", "tool arguments", "do not match")
}

func validateAgainstSchema(raw json.RawMessage, schemaRaw json.RawMessage, schemaName, valueName, mismatchVerb string) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%s is invalid JSON", valueName)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrSchema
	}
	const resource = "https://nocx.local/mcp/value-schema.json"
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		return fmt.Errorf("%s is invalid", schemaName)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	if addErr := compiler.AddResource(resource, doc); addErr != nil {
		return fmt.Errorf("%s cannot be compiled", schemaName)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return fmt.Errorf("%s cannot be compiled", schemaName)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("%s %s %s", valueName, mismatchVerb, schemaName)
	}
	return nil
}

func catalogContainsSecret(catalog Catalog, sensitive []string) bool {
	raw, err := json.Marshal(catalog)
	if err != nil {
		return true
	}
	for _, secret := range sensitive {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		if bytes.Contains(raw, []byte(secret)) || len(encoded) > 2 && bytes.Contains(raw, encoded[1:len(encoded)-1]) {
			return true
		}
	}
	return false
}

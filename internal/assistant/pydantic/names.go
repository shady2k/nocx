package pydantic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
)

var (
	providerName   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	referenceToken = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_-]*(?:\.[a-zA-Z0-9_-]+)*`)
)

type toolNames struct {
	toProvider  map[string]string
	toCanonical map[string]string
}

func newToolNames(reg agenttools.Registry) (toolNames, error) {
	n := toolNames{make(map[string]string), make(map[string]string)}
	for _, t := range reg.All() {
		if err := n.add(t.Name); err != nil {
			return toolNames{}, err
		}
	}
	// Lazy discovery is a host-owned declaration, not a registry capability.
	if err := n.add("tools.search"); err != nil {
		return toolNames{}, err
	}
	return n, nil
}

func (n toolNames) add(name string) error {
	encoded := strings.ReplaceAll(name, ".", "_")
	if !providerName.MatchString(encoded) {
		return fmt.Errorf("pydantic: tool %q has no legal provider name", name)
	}
	if previous, ok := n.toCanonical[encoded]; ok && previous != name {
		return fmt.Errorf("pydantic: provider tool name collision for %q", name)
	}
	n.toProvider[name], n.toCanonical[encoded] = encoded, name
	return nil
}

// references is only for host-owned templates. It must never receive model
// arguments, tool output, user messages, pane facts or restored history.
func (n toolNames) references(template string) string {
	return referenceToken.ReplaceAllStringFunc(template, func(token string) string {
		if mapped, ok := n.toProvider[token]; ok {
			return mapped
		}
		return token
	})
}

func (n toolNames) catalog(host assistant.RunHost) (catalogPayload, error) {
	out := catalogPayload{Tools: []toolDefinition{}, VisibleTools: []string{}}
	admitted := make(map[string]bool)
	for _, t := range host.Tools() {
		name, ok := n.toProvider[t.Name]
		if !ok {
			return out, fmt.Errorf("pydantic: unregistered host tool %q", t.Name)
		}
		if admitted[name] || !json.Valid(t.ParamsSchema) {
			return out, errProtocol
		}
		admitted[name] = true
		out.Tools = append(out.Tools, toolDefinition{name, n.references(t.Description), t.ParamsSchema})
	}
	for _, t := range host.ModelTools() {
		name, ok := n.toProvider[t.Name]
		if !ok || !admitted[name] {
			return out, errProtocol
		}
		out.VisibleTools = append(out.VisibleTools, name)
	}
	return out, nil
}

func (n toolNames) messages(input []assistant.Message) []message {
	out := make([]message, 0, len(input))
	for _, m := range input {
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "system"
		}
		out = append(out, message{role, m.Content})
	}
	return out
}

// ModelToolName renders only a trusted canonical name, never arbitrary text.
func (c *Client) ModelToolName(canonical string) string {
	if name, ok := c.names.toProvider[canonical]; ok {
		return name
	}
	return canonical
}

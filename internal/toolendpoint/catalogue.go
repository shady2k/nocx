package toolendpoint

import (
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

var errCatalogueUnavailable = errors.New("toolendpoint: dispatcher does not expose a catalogue")

type catalogueTool struct {
	Name    string          `json:"name"`
	Summary string          `json:"summary"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
}

type catalogueResult struct {
	Tools []catalogueTool `json:"tools"`
}

func buildCatalogue(dispatch assistant.ToolDispatcher, grant content.Grant) ([]byte, error) {
	cataloguer, ok := dispatch.(assistant.ToolCatalogue)
	if !ok {
		return nil, errCatalogueUnavailable
	}
	tools := cataloguer.Catalogue(grant)
	result := catalogueResult{Tools: make([]catalogueTool, 0, len(tools))}
	for _, tool := range tools {
		result.Tools = append(result.Tools, catalogueTool{
			Name:    tool.Name,
			Summary: tool.Description,
			Params:  append(json.RawMessage(nil), tool.ParamsSchema...),
			Result:  append(json.RawMessage(nil), tool.ResultSchema...),
		})
	}
	return json.Marshal(result)
}

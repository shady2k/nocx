package transport

import (
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/apicoll"
)

type apiFolderParams struct {
	Handle  string `json:"handle"`
	RelPath string `json:"relPath"`
}

type apiFolderWriteParams struct {
	Handle    string         `json:"handle"`
	RelPath   string         `json:"relPath"`
	Variables []apiParamWire `json:"variables"`
}

type apiFolderReadResponse struct {
	Variables []apiParamWire `json:"variables"`
}

type apiFolderWriteResponse struct {
	Variables []apiParamWire `json:"variables"`
}

func storedParams(ps []apiParamWire) []apicoll.Param {
	out := make([]apicoll.Param, 0, len(ps))
	for _, p := range ps {
		out = append(out, apicoll.Param{Name: p.Name, Value: p.Value, Enabled: p.Enabled})
	}
	return out
}

func validateAPIFolderReadRaw(raw json.RawMessage) string {
	var p apiFolderParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	return validateAPIFolderParams(p.Handle, p.RelPath)
}

func validateAPIFolderWriteRaw(raw json.RawMessage) string {
	var p apiFolderWriteParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIFolderParams(p.Handle, p.RelPath); msg != "" {
		return msg
	}
	if len(p.Variables) > maxAPIRequestRows {
		return fmt.Sprintf("variables exceeds %d rows", maxAPIRequestRows)
	}
	for i, variable := range p.Variables {
		if msg := boundedRunes(fmt.Sprintf("variables[%d].name", i), variable.Name, maxHeaderNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes(fmt.Sprintf("variables[%d].value", i), variable.Value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	return ""
}

func validateAPIFolderParams(handle, relPath string) string {
	if msg := validateAPIHandle(handle); msg != "" {
		return msg
	}
	return boundedRunes("relPath", relPath, maxPathRunes)
}

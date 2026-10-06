package sandbox

import (
	"bytes"
	"encoding/json"
	"io"
)

// The largest valid 64-path profile fits even when every byte needs a JSON
// escape. Root counts are checked while reading, before allocating another root.
const maxProfileJSONBytes = 2 << 20

type boundedProfileRoots []string

type boundedProfilePath string

func (p *boundedProfilePath) UnmarshalJSON(raw []byte) error {
	if len(raw) > MaxPathBytes*6+2 {
		return &ProfileError{Code: "path_too_long", Field: "roots"}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return &ProfileError{Code: "invalid_path", Field: "roots"}
	}
	if len(value) > MaxPathBytes {
		return &ProfileError{Code: "path_too_long", Field: "roots"}
	}
	*p = boundedProfilePath(value)
	return nil
}

func (r *boundedProfileRoots) UnmarshalJSON(raw []byte) error {
	if len(raw) > maxProfileJSONBytes {
		return &ProfileError{Code: "document_too_large", Field: "roots"}
	}
	if bytes.Equal(raw, []byte("null")) {
		*r = nil
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return &ProfileError{Code: "invalid_roots", Field: "roots"}
	}
	roots := make([]string, 0, 4)
	for decoder.More() {
		if len(roots) == MaxProfileRoots {
			return &ProfileError{Code: "too_many_roots", Field: "roots", Index: MaxProfileRoots}
		}
		var path boundedProfilePath
		if err := decoder.Decode(&path); err != nil {
			return err
		}
		roots = append(roots, string(path))
	}
	if _, err := decoder.Token(); err != nil {
		return &ProfileError{Code: "invalid_roots", Field: "roots"}
	}
	*r = roots
	return nil
}

func decodeProfileJSON(raw []byte, into any) error {
	if len(raw) > maxProfileJSONBytes {
		return &ProfileError{Code: "document_too_large", Field: "profile"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return &ProfileError{Code: "invalid_document", Field: "profile", cause: err}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &ProfileError{Code: "invalid_document", Field: "profile"}
	}
	return nil
}

func (r *ProfileRoots) UnmarshalJSON(raw []byte) error {
	var wire struct {
		ReadOnlyDirs  boundedProfileRoots `json:"readOnlyDirs"`
		ReadWriteDirs boundedProfileRoots `json:"readWriteDirs"`
	}
	if err := decodeProfileJSON(raw, &wire); err != nil {
		return err
	}
	out := ProfileRoots{ReadOnlyDirs: wire.ReadOnlyDirs, ReadWriteDirs: wire.ReadWriteDirs}
	if err := validateRoots(out); err != nil {
		return err
	}
	*r = out
	return nil
}

// Explicit fields avoid promoting ProfileRoots.UnmarshalJSON through the
// embedded standard roots and accidentally discarding schema/revision/enabled.
func (d *StandardDocument) UnmarshalJSON(raw []byte) error {
	var wire struct {
		SchemaVersion int                 `json:"schemaVersion"`
		Revision      uint64              `json:"revision"`
		Enabled       bool                `json:"enabled"`
		ReadOnlyDirs  boundedProfileRoots `json:"readOnlyDirs"`
		ReadWriteDirs boundedProfileRoots `json:"readWriteDirs"`
	}
	if err := decodeProfileJSON(raw, &wire); err != nil {
		return err
	}
	out := StandardDocument{
		SchemaVersion: wire.SchemaVersion, Revision: wire.Revision, Enabled: wire.Enabled,
		ProfileRoots: ProfileRoots{ReadOnlyDirs: wire.ReadOnlyDirs, ReadWriteDirs: wire.ReadWriteDirs},
	}
	if err := validateStandard(out); err != nil {
		return err
	}
	*d = out
	return nil
}

package transport

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSecretsDetect_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.detect.schema.json")
	cases := map[string]secretsDetectResponse{
		"no findings": {Revision: 7, Findings: []secretsDetectFinding{}},
		"one finding": {
			Revision: 3,
			Findings: []secretsDetectFinding{
				{Kind: "openai", Start: 10, End: 30, ValueStart: 10, ValueEnd: 30, SuggestedName: "openrouter.ai"},
			},
		},
		"structural value bounds": {
			Revision: 3,
			Findings: []secretsDetectFinding{
				{Kind: "env-assignment", Start: 0, End: 40, ValueStart: 16, ValueEnd: 40, SuggestedName: "openai-key"},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "secrets.detect DTO")
		})
	}
}

func TestSecretsDetect_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.detect.schema.json")
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	line := `echo "🔥" && curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api`
	resp := vaultCall(t, conn, "secrets.detect", map[string]any{"line": line, "revision": 42}, 1)
	if resp.Error != nil {
		t.Fatalf("detect error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "secrets.detect result (real socket)")

	var got struct {
		Revision int64 `json:"revision"`
		Findings []struct {
			Kind          string `json:"kind"`
			Start         int    `json:"start"`
			End           int    `json:"end"`
			ValueStart    int    `json:"valueStart"`
			ValueEnd      int    `json:"valueEnd"`
			SuggestedName string `json:"suggestedName"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Revision != 42 {
		t.Errorf("revision = %d, want the echo 42", got.Revision)
	}
	if len(got.Findings) != 1 || got.Findings[0].Kind != "openai" {
		t.Fatalf("findings = %+v, want one openai", got.Findings)
	}
	if got.Findings[0].SuggestedName == "" {
		t.Errorf("suggestedName = %q, want the backend's SuggestName", got.Findings[0].SuggestedName)
	}
	if got.Findings[0].ValueStart != got.Findings[0].Start || got.Findings[0].ValueEnd != got.Findings[0].End {
		t.Errorf("value bounds = [%d,%d), want the whole-match span [%d,%d)", got.Findings[0].ValueStart, got.Findings[0].ValueEnd, got.Findings[0].Start, got.Findings[0].End)
	}
	wantStart := strings.Index(line, "sk-proj") - 2
	if got.Findings[0].Start != wantStart {
		t.Errorf("start = %d, want %d (UTF-16, not bytes)", got.Findings[0].Start, wantStart)
	}
	units := utf16.Encode([]rune(line))
	if slice := string(utf16.Decode(units[got.Findings[0].Start:got.Findings[0].End])); !strings.HasPrefix(slice, "sk-proj") {
		t.Errorf("finding does not slice the line as JS would: %q", slice)
	}
}

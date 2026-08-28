package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestScopedParamsContractsAgreeWithRegisteredValidators(t *testing.T) {
	server := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	registered := make(map[string]methodSpec)
	for method, spec := range server.methods {
		if isParamsContractScope(method) {
			registered[method] = spec
		}
	}

	entries, entriesErr := os.ReadDir(contractDir)
	if entriesErr != nil {
		t.Fatalf("read contracts dir: %v", entriesErr)
	}
	contracts := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".params.schema.json") || !isParamsContractScope(strings.TrimSuffix(name, ".params.schema.json")) {
			continue
		}
		method := strings.TrimSuffix(name, ".params.schema.json")
		contracts[method] = struct{}{}
		if _, ok := registered[method]; !ok {
			t.Errorf("params contract %s has no registered method", name)
		}
	}

	invalid := [][]byte{
		[]byte(`[]`),
		[]byte(`"scalar"`),
		[]byte(`true`),
		[]byte(`{"unknown":true}`),
		[]byte(`{"id":null,"body":null,"title":null,"query":null,"ids":null}`),
		[]byte(`{"id":123}`),
		[]byte(`{"body":123}`),
		[]byte(`{"title":123}`),
		[]byte(`{"query":123}`),
		[]byte(`{"ids":"not-an-array"}`),
	}
	for _, probe := range []struct {
		field string
		size  int
	}{
		{field: "id", size: 129},
		{field: "body", size: 200001},
		{field: "title", size: 201},
		{field: "query", size: 1001},
	} {
		raw, marshalErr := json.Marshal(map[string]string{probe.field: strings.Repeat("x", probe.size)})
		if marshalErr != nil {
			t.Fatalf("marshal %s probe: %v", probe.field, marshalErr)
		}
		invalid = append(invalid, raw)
	}
	idsProbe, idsErr := json.Marshal(map[string][]string{"ids": {strings.Repeat("x", 129)}})
	if idsErr != nil {
		t.Fatalf("marshal ids probe: %v", idsErr)
	}
	invalid = append(invalid, idsProbe)
	valid := map[string][][]byte{
		"notes.create": {
			[]byte(`{}`),
			[]byte(`{"body":"body"}`),
		},
		"notes.delete": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.get": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.list": {
			[]byte(`{}`),
		},
		"notes.search": {
			[]byte(`{}`),
			[]byte(`{"query":"term"}`),
		},
		"notes.update": {
			[]byte(`{"id":"note-1"}`),
			[]byte(`{"id":"note-1","body":"updated"}`),
		},
		"snippets.create": {
			[]byte(`{}`),
			[]byte(`{"title":"title","body":"body"}`),
		},
		"snippets.delete": {
			[]byte(`{"id":"snippet-1"}`),
		},
		"snippets.list": {
			[]byte(`{}`),
		},
		"snippets.reorder": {
			[]byte(`{}`),
			[]byte(`{"ids":[]}`),
		},
		"snippets.update": {
			[]byte(`{"id":"snippet-1"}`),
			[]byte(`{"id":"snippet-1","title":"title","body":"body"}`),
		},
		"app.about": {
			[]byte(`{}`),
		},
		"backup.create": {
			[]byte(`{}`),
		},
		"backup.preview": {
			[]byte(`{"contents":"{}","strategy":"merge"}`),
		},
		"backup.restore": {
			[]byte(`{"contents":"{}","strategy":"replace","previewToken":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`),
		},
		"backup.saveToFile": {
			[]byte(`{"fileName":"backup.json","contents":"{}"}`),
		},
		"connections.passwordResolved": {
			[]byte(`{"requestId":"request-1","outcome":"cancelled"}`),
		},
		"connections.test": {
			[]byte(`{"profileId":"ssh:test:1"}`),
		},
		"connections.trustHostKey": {
			[]byte(`{"host":"host.example.com:22","key":"b2ZmZXJlZC1rZXktYmxvYg=="}`),
		},
		"dialog.openDirectory": {
			[]byte(`{}`),
		},
		"dialog.openFile": {
			[]byte(`{}`),
		},
		"dialog.openFileForUpload": {
			[]byte(`{}`),
		},
		"endpoints.create": {
			[]byte(`{"name":"Local","baseUrl":"https://example.com/v1","schema":"openai-compatible","models":[{"name":"model"}]}`),
		},
		"endpoints.delete": {
			[]byte(`{"id":"endpoint:test:1"}`),
		},
		"endpoints.list": {
			[]byte(`{}`),
		},
		"endpoints.probe": {
			[]byte(`{"baseUrl":"https://example.com/v1","model":"model"}`),
		},
		"endpoints.update": {
			[]byte(`{"id":"endpoint:test:1","name":"Local","baseUrl":"https://example.com/v1","schema":"openai-compatible","models":[{"name":"model"}]}`),
		},
		"fs.complete": {
			[]byte(`{"text":"/tmp","cwd":"/"}`),
		},
		"groups.apply": {
			[]byte(`[{"id":"group:test:1","name":"group"}]`),
		},
		"groups.create": {
			[]byte(`{"name":"group"}`),
		},
		"groups.delete": {
			[]byte(`{"id":"group:test:1"}`),
		},
		"groups.impact": {
			[]byte(`{"deleteGroupId":"group:test:1"}`),
		},
		"groups.list": {
			[]byte(`{}`),
		},
		"groups.update": {
			[]byte(`{"id":"group:test:1","name":"group"}`),
		},
		"history.query": {
			[]byte(`{"scope":"everywhere"}`),
			[]byte(`{"scope":"directory","cwd":"/tmp"}`),
			[]byte(`{"scope":"host","host":""}`),
		},
		"history.record": {
			[]byte(`{"command":"echo hi","source":"user","status":"success","paneId":"pane-1"}`),
		},
		"history.status": {
			[]byte(`{}`),
		},
		"ledger.artifact": {
			[]byte(`{"id":"artifact-1"}`),
		},
		"ledger.bind": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal"}}`),
		},
		"ledger.capture": {
			[]byte(`{"entryId":"entry-1","artifactId":"0198f2b0-0000-7000-8000-00000000c001","mediaType":"application/vt","captureVersion":1,"seq":1,"body":"body"}`),
		},
		"ledger.close": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal"},"status":"success","facts":{"terminationReason":"completed"}}`),
		},
		"ledger.get": {
			[]byte(`{"id":"entry-1"}`),
		},
		"ledger.open": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal"}}`),
		},
		"ledger.query": {
			[]byte(`{"scope":"everywhere"}`),
		},
		"lifecycle.establishAck": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","lane":"lane-1","domain":"domain-1","epoch":1,"generation":"generation-1"}`),
		},
		"lifecycle.recoverAck": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","generation":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`),
		},
		"lifecycle.submitAttempt": {
			[]byte(`{"domain":"domain-1","command":"echo hi","source":"user"}`),
		},
		"notify.bell": {
			[]byte(`{"sessionId":"session-1"}`),
		},
		"notify.feed.markRead": {
			[]byte(`{}`),
		},
		"notify.feed.read": {
			[]byte(`{}`),
		},
		"notify.paneWorkFinished": {
			[]byte(`{"sessionId":"session-1"}`),
		},
		"notify.raise": {
			[]byte(`{"sessionId":"session-1","title":"title","body":"body"}`),
		},
		"policy.get": {
			[]byte(`{}`),
		},
		"policy.set": {
			[]byte(`{"policy":{}}`),
		},
		"ports.pause": {
			[]byte(`{"profileId":"profile-1","paused":false,"visible":true}`),
		},
		"ports.sample": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"ports.status": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"ports.visible": {
			[]byte(`{"profileId":"profile-1","paused":false,"visible":true}`),
		},
		"profiles.create": {
			[]byte(`{"name":"profile","type":"ssh","options":{"host":"host.example.com"}}`),
		},
		"profiles.delete": {
			[]byte(`{"id":"profile:test:1"}`),
		},
		"profiles.effective": {
			[]byte(`{}`),
			[]byte(`{"ids":[]}`),
		},
		"profiles.importTabby": {
			[]byte(`{"config":"profiles: []"}`),
		},
		"profiles.list": {
			[]byte(`{}`),
		},
		"profiles.moveImpact": {
			[]byte(`{"profileIds":["profile:test:1"]}`),
		},
		"profiles.patch": {
			[]byte(`{"id":"profile:test:1"}`),
		},
		"profiles.tabbyExecute": {
			[]byte(`{"planToken":"0000000000000000000000000000000000000000000000000000000000000000"}`),
		},
		"profiles.tabbyPreview": {
			[]byte(`{"config":"profiles: []"}`),
		},
		"profiles.update": {
			[]byte(`{"id":"profile:test:1","name":"profile","type":"ssh","options":{"host":"host.example.com"}}`),
		},
		"roles.assign": {
			[]byte(`{"role":"answering"}`),
		},
		"roles.list": {
			[]byte(`{}`),
		},
		"roles.setDefault": {
			[]byte(`{"endpointId":"","model":""}`),
		},
		"session.signal": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","signal":"interrupt"}`),
		},
		"sessions.status": {
			[]byte(`{"profileIds":[]}`),
		},
		"settings.describe": {
			[]byte(`{}`),
		},
		"settings.getSnapshot": {
			[]byte(`{}`),
		},
		"settings.reset": {
			[]byte(`{"key":"clipboard.osc52Suppressed"}`),
		},
		"settings.secretDelete": {
			[]byte(`{"key":"secret.key"}`),
		},
		"settings.secretExists": {
			[]byte(`{"key":"secret.key"}`),
		},
		"settings.secretSet": {
			[]byte(`{"key":"secret.key","value":"secret"}`),
		},
		"settings.set": {
			[]byte(`{"key":"clipboard.osc52Suppressed","value":true}`),
		},
		"shell.commandNames": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.complete": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","cwd":"/tmp","line":"echo","pos":4}`),
		},
		"shell.footprint.consent": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.footprint.helperUninstall": {
			[]byte(`{"profileId":"profile-1","fingerprint":"SHA256:abc","path":"~/.nocx/helper"}`),
		},
		"shell.footprint.status": {
			[]byte(`{}`),
		},
		"shell.footprint.uninstall": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"shell.integrate": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.openUrl": {
			[]byte(`{"url":"https://example.com"}`),
		},
		"tunnel.open": {
			[]byte(`{"profileId":"profile-1","destination":"example.com:22"}`),
		},
		"tunnel.stop": {
			[]byte(`{"id":"0123456789abcdef0123456789abcdef"}`),
		},
		"uistate.get": {
			[]byte(`{}`),
		},
		"uistate.set": {
			[]byte(`{"sidebar":{"activeViewId":"home","width":240},"activeTab":"tab-1"}`),
		},
	}
	for method := range registered {
		if _, ok := valid[method]; !ok {
			t.Errorf("registered method %s has no valid params probes", method)
		}
	}
	for method := range valid {
		if _, ok := registered[method]; !ok {
			t.Errorf("valid params probes have no registered method %s", method)
		}
	}

	for method, spec := range registered {
		name := method + ".params.schema.json"
		if _, ok := contracts[method]; !ok {
			t.Errorf("registered method %s has no params contract", method)
			continue
		}
		schema := loadSchema(t, name)
		for _, raw := range invalid {
			raw := raw
			t.Run(method+"/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err == nil {
					t.Fatalf("schema accepted invalid probe %s", raw)
				}
				if msg := spec.validate(raw); msg == "" {
					t.Fatalf("registered validator accepted schema-rejected probe %s", raw)
				}
			})
		}
		for _, raw := range valid[method] {
			raw := raw
			t.Run(method+"/accept/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err != nil {
					t.Fatalf("schema rejected valid probe %s: %v", raw, err)
				}
				if msg := spec.validate(raw); msg != "" {
					t.Fatalf("registered validator rejected schema-accepted probe %s: %s", raw, msg)
				}
			})
		}
	}

	for method := range registered {
		if _, ok := contracts[method]; !ok {
			continue
		}
		path := filepath.Join(contractDir, method+".params.schema.json")
		raw, readErr := os.ReadFile(path) //nolint:gosec // path is registration-derived under contracts/
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		var metadata struct {
			AdditionalProperties *bool    `json:"additionalProperties"`
			Required             []string `json:"required"`
		}
		if parseErr := json.Unmarshal(raw, &metadata); parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		if metadata.AdditionalProperties == nil || *metadata.AdditionalProperties {
			t.Errorf("%s must set additionalProperties to false", path)
		}
		if metadata.Required == nil {
			t.Errorf("%s must declare required, including when it is empty", path)
		}
	}
}

func TestDecodeObjectRejectsUnknownTrailingAndNamedNull(t *testing.T) {
	type params struct {
		Required string `json:"required"`
	}
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "malformed object", raw: json.RawMessage(`{`), want: "params must be a JSON object"},
		{name: "top-level null", raw: json.RawMessage(`null`)},
		{name: "empty object", raw: json.RawMessage(`{}`)},
		{name: "named value", raw: json.RawMessage(`{"required":"ok"}`)},
		{name: "named null", raw: json.RawMessage(`{"required":null}`), want: "required must not be null"},
		{name: "unknown field", raw: json.RawMessage(`{"unknown":true}`), want: `unknown field "unknown"`},
		{name: "trailing value", raw: json.RawMessage(`{} {}`), want: "trailing content after the params object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got params
			if msg := decodeObject(test.raw, &got, "required"); msg != test.want {
				t.Fatalf("decodeObject(%s) = %q, want %q", test.raw, msg, test.want)
			}
		})
	}
}

func isParamsContractScope(method string) bool {
	for _, prefix := range []string{
		"app.", "backup.", "connections.", "dialog.", "endpoints.", "fs.",
		"groups.", "history.", "ledger.", "lifecycle.", "notify.", "notes.",
		"policy.", "ports.", "profiles.", "roles.", "session.",
		"sessions.", "settings.", "shell.", "snippets.", "tunnel.", "uistate.",
	} {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return false
}

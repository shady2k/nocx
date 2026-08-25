package transport

import (
	"encoding/json"
	"testing"
)

func TestAPIRequestScope_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.scope.schema.json")
	raw, err := json.Marshal(apiRequestScopeResponse{
		Variables: []apiRequestScopeVariableWire{
			{Name: "id", Value: "request", Scope: "request", From: "", Overridden: false, Refused: ""},
			{Name: "id", Value: "folder", Scope: "folder", From: "users", Overridden: true, Refused: ""},
			{Name: "token", Value: "draft", Scope: "request", From: "", Overridden: false, Refused: `apicoll: a request variable would shadow a name this environment declares secret: "token"`},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "api.request.scope DTO")
}

func TestAPIRequestScope_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.scope.schema.json")
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")

	opened := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 1)
	if opened.Error != nil {
		t.Fatalf("api.collections.open: %+v", opened.Error)
	}
	var open apiOpenResponse
	if err := json.Unmarshal(opened.Result, &open); err != nil {
		t.Fatalf("unmarshal open: %v", err)
	}

	result := vaultCall(t, conn, "api.request.scope", map[string]any{
		"handle": open.Handle, "relPath": "ping.json", "envRelPath": "",
		"variables": []map[string]any{{"name": "draft", "value": "value", "enabled": true}},
	}, 2)
	if result.Error != nil {
		t.Fatalf("api.request.scope: %+v", result.Error)
	}
	validateJSON(t, schema, result.Result, "api.request.scope result")
	var scope apiRequestScopeResponse
	if err := json.Unmarshal(result.Result, &scope); err != nil {
		t.Fatalf("unmarshal scope: %v", err)
	}
	if scope.Variables == nil {
		t.Fatal("api.request.scope returned null variables")
	}
}

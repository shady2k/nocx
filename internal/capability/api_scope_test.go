package capability_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/capability"
)

func TestAPICollectionService_RequestScopeReturnsTheResolvedChain(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(apicoll.ManifestName, `{"schemaVersion":1,"name":"scope"}`)
	write(".variables.json", `{"variables":[{"name":"shared","value":"root","enabled":true},{"name":"disabled","value":"off","enabled":false}]}`)
	write("users/.variables.json", `{"variables":[{"name":"shared","value":"parent","enabled":true},{"name":"parentOnly","value":"parent-value","enabled":true}]}`)
	write("users/private/.variables.json", `{"variables":[{"name":"shared","value":"nearest","enabled":true},{"name":"nearestOnly","value":"nearest-value","enabled":true}]}`)
	write("users/private/get.json", `{"id":"r1","name":"get","method":"GET","url":"https://example.test/{{shared}}","variables":[{"name":"shared","value":"request","enabled":true},{"name":"requestOnly","value":"request-value","enabled":true}],"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","values":{"environmentOnly":"environment-value","shared":"environment-shared"},"secretVars":["token"],"route":{"kind":"direct"}}`)

	// The constructor owns the collection root, so it is given the path the
	// fixture files were written under.
	op := capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(apiPaths{root: root}),
		scopeSecrets{},
	)

	opened, err := openScopeCollection(op, root)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	var got capability.RequestScopeResult
	draft := []apicoll.Param{
		{Name: "shared", Value: "draft", Enabled: true},
		{Name: "draftOnly", Value: "draft-value", Enabled: true},
	}
	if runErr := op.Run(context.Background(), func(ctx context.Context, svc capability.APICollectionService) error {
		got, err = svc.RequestScope(ctx, opened, "users/private/get.json", "environments/dev.json", draft)
		return err
	}); runErr != nil {
		t.Fatalf("RequestScope: %v", runErr)
	}

	want := []capability.ScopeVariable{
		{Name: "shared", Value: "draft", Scope: "request", From: "", Overridden: false, Refused: ""},
		{Name: "draftOnly", Value: "draft-value", Scope: "request", From: "", Overridden: false, Refused: ""},
		{Name: "shared", Value: "nearest", Scope: "folder", From: "users/private", Overridden: true, Refused: ""},
		{Name: "nearestOnly", Value: "nearest-value", Scope: "folder", From: "users/private", Overridden: false, Refused: ""},
		{Name: "shared", Value: "parent", Scope: "folder", From: "users", Overridden: true, Refused: ""},
		{Name: "parentOnly", Value: "parent-value", Scope: "folder", From: "users", Overridden: false, Refused: ""},
		{Name: "shared", Value: "root", Scope: "folder", From: "", Overridden: true, Refused: ""},
		{Name: "environmentOnly", Value: "environment-value", Scope: "environment", From: "environments/dev.json", Overridden: false, Refused: ""},
		{Name: "shared", Value: "environment-shared", Scope: "environment", From: "environments/dev.json", Overridden: true, Refused: ""},
		{Name: "token", Value: "", Scope: "vault", From: "", Overridden: false, Refused: ""},
	}
	if len(got.Variables) != len(want) {
		t.Fatalf("variables = %+v, want %d rows", got.Variables, len(want))
	}
	for i := range want {
		if got.Variables[i] != want[i] {
			t.Errorf("variable %d = %+v, want %+v", i, got.Variables[i], want[i])
		}
	}
}

func TestAPICollectionService_RequestScopeMarksDraftSecretShadow(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(apicoll.ManifestName, `{"schemaVersion":1,"name":"scope"}`)
	write("users/get.json", `{"id":"r1","name":"get","method":"GET","url":"https://example.test/{{token}}","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","secretVars":["token"],"route":{"kind":"direct"}}`)

	op := capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(apiPaths{root: root}),
		nil,
	)
	opened, err := openScopeCollection(op, root)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	var got capability.RequestScopeResult
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APICollectionService) error {
		var requestErr error
		got, requestErr = svc.RequestScope(ctx, opened, "users/get.json", "environments/dev.json", []apicoll.Param{
			{Name: "token", Value: "draft-secret", Enabled: true},
			{Name: "visible", Value: "draft-visible", Enabled: true},
		})
		return requestErr
	}); err != nil {
		t.Fatalf("RequestScope: %v", err)
	}
	if len(got.Variables) != 3 {
		t.Fatalf("variables = %+v, want three rows", got.Variables)
	}
	if got.Variables[0].Refused != `apicoll: a request variable would shadow a name this environment declares secret: "token"` {
		t.Fatalf("refused row = %+v", got.Variables[0])
	}
	if got.Variables[1].Refused != "" || got.Variables[2].Refused != "" {
		t.Fatalf("refusal leaked to other rows: %+v", got.Variables)
	}
}

func TestAPICollectionService_RequestScopeReportsVaultLookupFailure(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(apicoll.ManifestName, `{"schemaVersion":1,"name":"scope"}`)
	write("get.json", `{"id":"r1","name":"get","method":"GET","url":"https://example.test","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","secretVars":["token"],"route":{"kind":"direct"}}`)

	op := capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(apiPaths{root: root}),
		failingScopeSecrets{},
	)
	opened, err := openScopeCollection(op, root)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	var gotErr error
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APICollectionService) error {
		_, gotErr = svc.RequestScope(ctx, opened, "get.json", "environments/dev.json", nil)
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotErr == nil || gotErr.Error() != "vault sealed" {
		t.Fatalf("RequestScope error = %v, want vault sealed", gotErr)
	}
}

type scopeSecrets struct{}

func (scopeSecrets) Variables(context.Context, string, string) apicoll.Lookup {
	return func(name string) (string, bool, error) {
		return "secret-value", name == "token", nil
	}
}

type failingScopeSecrets struct{}

func (failingScopeSecrets) Variables(context.Context, string, string) apicoll.Lookup {
	return func(string) (string, bool, error) {
		return "", false, errors.New("vault sealed")
	}
}

func openScopeCollection(op capability.APICollectionOperation, root string) (apicoll.HandleID, error) {
	var handle apicoll.HandleID
	err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		opened, err := svc.Open(root)
		if err != nil {
			return err
		}
		handle = opened.Handle
		return nil
	})
	return handle, err
}

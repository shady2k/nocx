package transport

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

type openRPCSchemaRef struct {
	Ref string `json:"$ref"`
}

type openRPCParam struct {
	Schema openRPCSchemaRef `json:"schema"`
}

type openRPCResult struct {
	Schema openRPCSchemaRef `json:"schema"`
}

type openRPCError struct {
	Code   int              `json:"code"`
	Schema openRPCSchemaRef `json:"x-nocx-errorSchema"`
}

type openRPCMethod struct {
	Name           string         `json:"name"`
	Params         []openRPCParam `json:"params"`
	Result         *openRPCResult `json:"result"`
	Errors         []openRPCError `json:"errors"`
	NoResultSchema bool           `json:"x-nocx-noResultSchema"`
}

type openRPCManifest struct {
	Methods []openRPCMethod `json:"methods"`
}

func TestOpenRPCManifestMatchesRegisteredMethods(t *testing.T) {
	manifestBytes, err := os.ReadFile(filepath.Join(contractDir, "openrpc.json"))
	if err != nil {
		t.Fatalf("read OpenRPC manifest: %v", err)
	}
	var manifest openRPCManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode OpenRPC manifest: %v", err)
	}

	logger := log.NewSlogAdapter(nil)
	server := NewWSServer(logger, newRegWithStub(logger))
	registered := server.methods
	if len(manifest.Methods) != len(registered) {
		t.Fatalf("OpenRPC method count = %d, registered method count = %d", len(manifest.Methods), len(registered))
	}

	manifestMethods := make(map[string]openRPCMethod, len(manifest.Methods))
	for _, method := range manifest.Methods {
		if method.Name == "" {
			t.Fatal("OpenRPC manifest contains a method with no name")
		}
		if _, duplicate := manifestMethods[method.Name]; duplicate {
			t.Fatalf("OpenRPC manifest contains duplicate method %q", method.Name)
		}
		manifestMethods[method.Name] = method

		paramsSchema := method.Name + ".params.schema.json"
		if len(method.Params) != 1 {
			t.Fatalf("%s has %d params entries, want one", method.Name, len(method.Params))
		}
		if got := manifestRefFile(t, method.Params[0].Schema.Ref); got != paramsSchema {
			t.Errorf("%s params ref = %q, want %q", method.Name, got, paramsSchema)
		}
		assertContractFile(t, paramsSchema)

		if method.Result == nil && !method.NoResultSchema {
			t.Errorf("%s has no result ref without x-nocx-noResultSchema", method.Name)
		}
		if method.Result != nil {
			assertContractFile(t, manifestRefFile(t, method.Result.Schema.Ref))
		}
		if len(method.Errors) == 0 {
			t.Errorf("%s has no error references", method.Name)
		}
		for _, rpcErr := range method.Errors {
			if rpcErr.Code == 0 {
				t.Errorf("%s has an error without a code", method.Name)
			}
			if got := manifestRefFile(t, rpcErr.Schema.Ref); got == "" {
				t.Errorf("%s error %d has no shared error schema", method.Name, rpcErr.Code)
			} else {
				assertContractFile(t, got)
			}
		}
	}

	for method := range registered {
		if _, ok := manifestMethods[method]; !ok {
			t.Errorf("registered method %q is absent from contracts/openrpc.json", method)
		}
	}
}

func manifestRefFile(t *testing.T, raw string) string {
	t.Helper()
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest ref %q: %v", raw, err)
	}
	if !strings.HasPrefix(u.Scheme, "https") {
		t.Fatalf("manifest ref %q is not an HTTPS contract ref", raw)
	}
	return filepath.Base(u.Path)
}

func assertContractFile(t *testing.T, name string) {
	t.Helper()
	if name == "." || name == string(filepath.Separator) || filepath.Base(name) != name {
		t.Fatalf("manifest ref escapes contracts directory: %q", name)
	}
	if _, err := os.Stat(filepath.Join(contractDir, name)); err != nil {
		t.Errorf("manifest ref %q does not name a contract: %v", name, err)
	}
}

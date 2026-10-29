package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func manifestContract(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	s, err := c.Compile("../../contracts/update/manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func contractDocument() []byte {
	return []byte(fmt.Sprintf(`{"version":"0.8.0","released":"2026-10-29T00:00:00Z","notesUrl":"https://example.test/notes","build":{"commit":"%s","builtAt":"2026-10-28T10:00:00Z"},"artifacts":[{"os":"linux","arch":"amd64","format":"AppImage","url":"https://example.test/nocx.AppImage","sha256":"%s","size":123},{"os":"linux","arch":"arm64","format":"AppImage","url":"https://example.test/nocx-arm64.AppImage","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123},{"os":"darwin","arch":"universal","format":"zip","url":"https://example.test/nocx.zip","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123}],"helpers":[]}`, strings.Repeat("b", 40), strings.Repeat("c", 64)))
}

func validateManifestContract(t *testing.T, body []byte) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if err := manifestContract(t).Validate(doc); err != nil {
		t.Fatalf("manifest violates strict contract: %v\n%s", err, body)
	}
}

func TestUpdateManifest_DTOConformsToContract(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated_helpers_%v", populated), func(t *testing.T) {
			var dto Manifest
			if err := json.Unmarshal(contractDocument(), &dto); err != nil {
				t.Fatal(err)
			}
			if populated {
				dto.Helpers = []HelperArtifact{{OS: "linux", Arch: "amd64", URL: "https://example.test/helper", SHA256: strings.Repeat("d", 64), Size: 456}}
			}
			body, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			validateManifestContract(t, body)
		})
	}
}

func TestUpdateManifest_OverTheWireConformsToContract(t *testing.T) {
	private := channelTestKey(t)
	srv := signedChannelRelay(t, contractDocument(), private)
	fetch := NewGitHubManifestFetcher(nil)
	fetch.BaseURL = srv.URL
	fetched, err := fetch.Fetch(context.Background())
	body, sig := fetched.Body, fetched.Signature
	if err != nil {
		t.Fatal(err)
	}
	// Validate both signed bytes off the socket and the verifier's real result;
	// accepting input while silently dropping build identity is not conformance.
	validateManifestContract(t, body)
	dto, err := VerifyManifest(body, string(sig), []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	validateManifestContract(t, encoded)
}

func TestUpdateManifest_ContractRejectsDrift(t *testing.T) {
	s := manifestContract(t)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing build", func(d map[string]any) { delete(d, "build") }},
		{"unknown top-level field", func(d map[string]any) { d["future"] = true }},
		{"missing commit", func(d map[string]any) { delete(contractBuild(t, d), "commit") }},
		{"missing build time", func(d map[string]any) { delete(contractBuild(t, d), "builtAt") }},
		{"extra build field", func(d map[string]any) { contractBuild(t, d)["future"] = true }},
		{"short commit", func(d map[string]any) { contractBuild(t, d)["commit"] = "abc1234" }},
		{"invalid timestamp", func(d map[string]any) { contractBuild(t, d)["builtAt"] = "2026-02-30T10:00:00Z" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(contractDocument(), &doc); err != nil {
				t.Fatal(err)
			}
			tc.mutate(doc)
			if err := s.Validate(doc); err == nil {
				t.Fatal("contract accepted invalid manifest")
			}
		})
	}
}

func contractBuild(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	build, ok := doc["build"].(map[string]any)
	if !ok {
		t.Fatal("fixture must contain an object build")
	}
	return build
}

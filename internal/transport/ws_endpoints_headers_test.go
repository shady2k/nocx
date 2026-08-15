package transport

// The endpoints wire with references and custom headers (bead nocx-rzjw +
// nocx-lyyk), driven over the REAL socket against a REAL vault: a create that
// references an existing vault secret instead of minting, the refused-name
// and control-character refusals with their messages, and the wire rule that
// a result carries row handles, never references or material.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// mintRow mints one secret into the harness vault and returns its stored
// reference and row handle — the renderer's side of "reference an existing
// vault secret".
func (h *endpointHarness) mintRow(t *testing.T, name string) (string, string) {
	t.Helper()
	id, err := h.v.CreateNamed(t.Context(), credential.NewSecret("material-"+name), vault.SecretMeta{
		Name: name,
		Kind: vault.KindPassword,
	})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	return string(id), vault.RowFor(id)
}

func TestEndpoints_CreateWithKeyRowReferencesInsteadOfMinting(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	ref, row := h.mintRow(t, "shared key")

	params := endpointParams("OpenAI", "https://api.openai.com/v1", "")
	params["credential"] = row
	params["headers"] = []map[string]any{
		{"name": "HTTP-Referer", "value": "nocx", "secret": nil},
		{"name": "api-key", "value": nil, "secret": row},
	}
	created := h.createEndpoint(t, params)

	// The credential IS the referenced row — a mint would have produced a
	// fresh row — and the header rows map the same way.
	if created.Credential == nil || *created.Credential != row {
		t.Fatalf("credential = %v, want the referenced row %s (a mint would mint a new row)", created.Credential, row)
	}
	if len(created.Headers) != 2 {
		t.Fatalf("headers = %+v, want 2", created.Headers)
	}
	if created.Headers[0].Value == nil || *created.Headers[0].Value != "nocx" || created.Headers[0].Secret != nil {
		t.Errorf("headers[0] = %+v, want the literal", created.Headers[0])
	}
	if created.Headers[1].Secret == nil || *created.Headers[1].Secret != row || created.Headers[1].Value != nil {
		t.Errorf("headers[1] = %+v, want the referenced row", created.Headers[1])
	}

	// The persisted record holds the stored REFERENCE, never the row handle.
	eps, err := h.ps.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(eps))
	}
	if eps[0].CredentialRef != ref {
		t.Errorf("stored CredentialRef = %q, want %q", eps[0].CredentialRef, ref)
	}
	if eps[0].Headers[1].ValueRef != ref {
		t.Errorf("stored header ValueRef = %q, want %q", eps[0].Headers[1].ValueRef, ref)
	}
}

func TestEndpoints_CreateWithKeyAndKeyRowRefused(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	_, row := h.mintRow(t, "shared key")

	params := endpointParams("OpenAI", "https://api.openai.com/v1", "sk-typed")
	params["credential"] = row
	raw := jsonrpcCall(t, h.conn, "endpoints.create", params)
	if !strings.Contains(string(raw), "-32602") {
		t.Fatalf("create with key and keyRow = %s, want -32602", raw)
	}
	if eps, _ := h.ps.LoadEndpoints(); len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none", eps)
	}
}

func TestEndpoints_CreateRefusesRefusedHeaderWithReason(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	cases := map[string]string{
		"Authorization":     "credential",
		"authorization":     "credential",
		"Transfer-Encoding": "hop-by-hop",
		"Connection":        "hop-by-hop",
	}

	for name, why := range cases {
		params := endpointParams("OpenAI", "https://api.openai.com/v1", "")
		params["headers"] = []map[string]any{{"name": name, "value": "x", "secret": nil}}
		raw := jsonrpcCall(t, h.conn, "endpoints.create", params)
		if !strings.Contains(string(raw), "-32602") {
			t.Fatalf("header %q = %s, want -32602", name, raw)
		}
		// The refusal names the header and why. HTTP field names are
		// case-insensitive, so the message's spelling may differ from the
		// typed one — compare case-insensitively.
		lower := strings.ToLower(string(raw))
		if !strings.Contains(lower, strings.ToLower(name)) {
			t.Errorf("header %q refusal does not name the header: %s", name, raw)
		}
		if !strings.Contains(lower, strings.ToLower(why)) {
			t.Errorf("header %q refusal does not name why (%s): %s", name, why, raw)
		}
	}
}

func TestEndpoints_CreateRefusesControlCharactersInHeaderValues(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	params := endpointParams("OpenAI", "https://api.openai.com/v1", "")
	params["headers"] = []map[string]any{{"name": "X-Title", "value": "line\nbreak", "secret": nil}}
	raw := jsonrpcCall(t, h.conn, "endpoints.create", params)
	if !strings.Contains(string(raw), "-32602") {
		t.Fatalf("create with a control character = %s, want -32602", raw)
	}
	if !strings.Contains(string(raw), "control") {
		t.Errorf("refusal does not explain: %s", raw)
	}
}

func TestEndpoints_ListCarriesRowHandlesNeverMaterial(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()
	_, row := h.mintRow(t, "shared key")

	params := endpointParams("OpenAI", "https://api.openai.com/v1", "")
	params["headers"] = []map[string]any{
		{"name": "api-key", "value": nil, "secret": row},
	}
	h.createEndpoint(t, params)

	listRaw := jsonrpcCall(t, h.conn, "endpoints.list", nil)
	if strings.Contains(string(listRaw), "sec:v1:") {
		t.Fatalf("the list leaked a stored reference: %s", listRaw)
	}
	if !strings.Contains(string(listRaw), row) {
		t.Fatalf("the list does not carry the header's row handle: %s", listRaw)
	}
	if strings.Contains(string(listRaw), "material-shared") {
		t.Fatalf("the list leaked secret material: %s", listRaw)
	}
}

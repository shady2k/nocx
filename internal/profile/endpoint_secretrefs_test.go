package profile

// The ClearSecretRefs sweep and the reset impact cover the endpoint's custom
// header references too (bead nocx-lyyk): a header whose value is a vault
// secret is a reference exactly like the endpoint's own credential, and a
// vault reset or a secret deletion must not leave a header claiming material
// that no longer exists.

import "testing"

func endpointWithHeaderRefs() Endpoint {
	return Endpoint{
		ID:            "endpoint:custom:header-refs:1",
		Name:          "header refs",
		BaseURL:       "https://api.example.com/v1",
		Schema:        EndpointSchemaOpenAICompatible,
		CredentialRef: "sec:v1:file:cred",
		Models:        []EndpointModel{{Name: "m"}},
		Headers: []EndpointHeader{
			{Name: "HTTP-Referer", Value: ptr("nocx")},
			{Name: "X-Title", ValueRef: "sec:v1:file:title"},
			{Name: "api-key", ValueRef: "sec:v1:file:cred"},
		},
	}
}

func TestCountSecretReferences_CountsEndpointHeaderRefs(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(endpointWithHeaderRefs()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	// Two distinct secrets referenced (cred appears both as the credential
	// and as a header value — one thing, counted once); the endpoint holds
	// references and is counted.
	if impact.SecretCount != 2 {
		t.Errorf("SecretCount = %d, want 2 (credential + header ref, shared counted once)", impact.SecretCount)
	}
	if impact.EndpointCount != 1 {
		t.Errorf("EndpointCount = %d, want 1", impact.EndpointCount)
	}
}

func TestClearSecretRefs_ClearsEndpointHeaderRefsInOneWrite(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(endpointWithHeaderRefs()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	// Deleting the "title" secret drops only its header row; the literal
	// header and the credential stay.
	if err := s.ClearSecretRefs("sec:v1:file:title"); err != nil {
		t.Fatalf("ClearSecretRefs: %v", err)
	}
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(eps))
	}
	headers := eps[0].Headers
	if len(headers) != 2 {
		t.Fatalf("headers = %+v, want 2 rows — the title row is dropped, not kept sourceless", headers)
	}
	if headers[0].Name != "HTTP-Referer" || headers[0].Value == nil || *headers[0].Value != "nocx" {
		t.Errorf("headers[0] = %+v, want the literal row untouched", headers[0])
	}
	if headers[1].Name != "api-key" || headers[1].ValueRef != "sec:v1:file:cred" {
		t.Errorf("headers[1] = %+v, want the credential-shared reference untouched", headers[1])
	}
	if eps[0].CredentialRef != "sec:v1:file:cred" {
		t.Errorf("CredentialRef = %q, want untouched", eps[0].CredentialRef)
	}
}

func TestClearSecretRefs_SharedCredentialClearsHeaderAndCredential(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(endpointWithHeaderRefs()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	// Deleting the credential secret clears the credential reference AND the
	// header row that references the same material. The title row references
	// a different, still-existing secret and survives.
	if err := s.ClearSecretRefs("sec:v1:file:cred"); err != nil {
		t.Fatalf("ClearSecretRefs: %v", err)
	}
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if eps[0].CredentialRef != "" {
		t.Errorf("CredentialRef = %q, want cleared", eps[0].CredentialRef)
	}
	if len(eps[0].Headers) != 2 {
		t.Fatalf("headers = %+v, want 2 rows (literal + title ref)", eps[0].Headers)
	}
	for i, h := range eps[0].Headers {
		if h.ValueRef == "sec:v1:file:cred" {
			t.Errorf("headers[%d] still references the deleted secret: %+v", i, h)
		}
	}
	if eps[0].Headers[1].Name != "X-Title" || eps[0].Headers[1].ValueRef != "sec:v1:file:title" {
		t.Errorf("headers[1] = %+v, want the title reference untouched", eps[0].Headers[1])
	}
}

func TestClearAllSecretReferences_ClearsEndpointHeaderRefs(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(endpointWithHeaderRefs()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	impact, err := s.ClearAllSecretReferences()
	if err != nil {
		t.Fatalf("ClearAllSecretReferences: %v", err)
	}
	if impact.SecretCount != 2 || impact.EndpointCount != 1 {
		t.Fatalf("impact = %+v, want 2 secrets, 1 endpoint", impact)
	}
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if eps[0].CredentialRef != "" {
		t.Errorf("CredentialRef = %q, want cleared", eps[0].CredentialRef)
	}
	if len(eps[0].Headers) != 1 || eps[0].Headers[0].Name != "HTTP-Referer" {
		t.Errorf("headers = %+v, want only the literal row to survive the reset", eps[0].Headers)
	}
	if eps[0].Headers[0].Value == nil || *eps[0].Headers[0].Value != "nocx" {
		t.Errorf("literal header must survive the reset: %+v", eps[0].Headers[0])
	}
}

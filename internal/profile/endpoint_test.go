package profile

import (
	"strings"
	"testing"
)

func validTestEndpoint() Endpoint {
	return Endpoint{
		ID:      "endpoint:custom:openai:1",
		Name:    "OpenAI",
		BaseURL: "https://api.openai.com/v1",
		Schema:  EndpointSchemaOpenAICompatible,
		Models: []EndpointModel{
			{Name: "gpt-4o-mini"},
			{Name: "gpt-4o", Alias: ptr("gpt-4o (fast)")},
		},
	}
}

func TestCreateEndpoint_StoresTheRecord(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()

	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("endpoints = %+v, want one with id %s", got, e.ID)
	}
	if got[0].BaseURL != e.BaseURL || got[0].Schema != e.Schema {
		t.Errorf("record = %+v, want baseUrl and schema preserved", got[0])
	}
	if len(got[0].Models) != 2 || got[0].Models[1].Alias == nil {
		t.Errorf("models = %+v, want both with the alias intact", got[0].Models)
	}
}

func TestCreateEndpoint_RefusesEmptyID(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()
	e.ID = ""
	if err := s.CreateEndpoint(e); err == nil {
		t.Fatal("CreateEndpoint with empty ID must fail")
	}
}

func TestCreateEndpoint_RefusesDuplicateID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(validTestEndpoint()); err != nil {
		t.Fatalf("first CreateEndpoint: %v", err)
	}
	dup := validTestEndpoint()
	dup.Name = "Impostor"
	if err := s.CreateEndpoint(dup); err == nil {
		t.Fatal("CreateEndpoint with an existing ID must fail")
	}
}

// Validation is a data-shape gate, not an address policy: any absolute
// http(s) URL is accepted this pass (the loopback/private rule is nocx-edio),
// but a non-URL, a non-http scheme, a missing host, embedded credentials, an
// unknown schema and an empty model list are all rejected before storage.
func TestCreateEndpoint_ValidatesTheRecord(t *testing.T) {
	cases := map[string]func(*Endpoint){
		"empty name":      func(e *Endpoint) { e.Name = " " },
		"unknown schema":  func(e *Endpoint) { e.Schema = "anthropic-messages" },
		"not a URL":       func(e *Endpoint) { e.BaseURL = "api.openai.com/v1" },
		"bad scheme":      func(e *Endpoint) { e.BaseURL = "ftp://api.openai.com/v1" },
		"no host":         func(e *Endpoint) { e.BaseURL = "https://" },
		"userinfo in URL": func(e *Endpoint) { e.BaseURL = "https://user:pass@api.openai.com/v1" },
		"no models":       func(e *Endpoint) { e.Models = nil },
		"empty model":     func(e *Endpoint) { e.Models = []EndpointModel{{Name: ""}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			e := validTestEndpoint()
			mutate(&e)
			if err := s.CreateEndpoint(e); err == nil {
				t.Fatal("CreateEndpoint must reject the record")
			}
			got, err := s.LoadEndpoints()
			if err != nil {
				t.Fatalf("LoadEndpoints: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("rejected record must not be stored, got %d", len(got))
			}
		})
	}
}

func TestUpdateEndpoint_ReplacesTheRecord(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(validTestEndpoint()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	updated := validTestEndpoint()
	updated.Name = "OpenAI EU"
	updated.BaseURL = "https://api.eu.openai.com/v1"
	updated.Models = []EndpointModel{{Name: "gpt-4o-mini"}}
	if err := s.UpdateEndpoint(updated); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}

	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(got) != 1 || got[0].Name != "OpenAI EU" || got[0].BaseURL != updated.BaseURL {
		t.Fatalf("endpoint = %+v, want the update", got)
	}
	if len(got[0].Models) != 1 {
		t.Errorf("models = %+v, want the replaced set", got[0].Models)
	}
}

func TestUpdateEndpoint_FailsWhenMissing(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()
	e.ID = "endpoint:custom:ghost:1"
	if err := s.UpdateEndpoint(e); err == nil {
		t.Fatal("UpdateEndpoint for an absent id must fail")
	}
}

func TestDeleteEndpoint_ReturnsTheReferenceAndRemovesTheRecord(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()
	e.CredentialRef = "sec:v1:file:deadbeefdeadbeefdeadbeefdeadbeef"
	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	ref, err := s.DeleteEndpoint(e.ID)
	if err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	if ref != e.CredentialRef {
		t.Errorf("ref = %q, want %q (the caller deletes the material)", ref, e.CredentialRef)
	}
	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("endpoints = %+v, want none after delete", got)
	}
}

func TestDeleteEndpoint_IsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ref, err := s.DeleteEndpoint("endpoint:custom:never:1")
	if err != nil {
		t.Fatalf("DeleteEndpoint on an absent id: %v", err)
	}
	if ref != "" {
		t.Errorf("ref = %q, want empty for an absent endpoint", ref)
	}
}

// The metadata-first half of deleting an endpoint's key (ADR-0011 §4,
// ADR-0030): one write removes the record AND clears the reference from
// every remaining record — a profile, a group default and another endpoint
// all lose the binding in the same document write, so nothing can point at
// material that is about to be deleted.
func TestDeleteEndpoint_ClearsTheReferenceFromEveryRecordInOneWrite(t *testing.T) {
	s := newTestStore(t)
	ref := "sec:v1:file:sharedsharedsharedsharedshared1"

	// A profile binding the secret.
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "ssh:p:1", Type: "ssh", Name: "p"},
		Options: StoredSSHProfileOptions{Host: "h", PasswordSecret: ref},
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	// A group default binding the same secret.
	if err := s.CreateGroup(ProfileGroup{
		ID:   "g1",
		Name: "g",
		Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{PasswordSecret: &ref},
		},
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	// Two endpoints binding the same secret.
	first := validTestEndpoint()
	first.CredentialRef = ref
	second := validTestEndpoint()
	second.ID = "endpoint:custom:other:1"
	second.CredentialRef = ref
	for _, e := range []Endpoint{first, second} {
		if err := s.CreateEndpoint(e); err != nil {
			t.Fatalf("CreateEndpoint %s: %v", e.ID, err)
		}
	}

	gotRef, err := s.DeleteEndpoint(first.ID)
	if err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	if gotRef != ref {
		t.Fatalf("ref = %q, want %q", gotRef, ref)
	}

	// Every remaining record must have lost the binding in the same write.
	profs, err := s.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(profs) != 1 || profs[0].Options.PasswordSecret != "" {
		t.Errorf("profile binding = %q, want cleared", profs[0].Options.PasswordSecret)
	}
	groups, err := s.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Defaults == nil || groups[0].Defaults.PasswordSecret != nil {
		t.Errorf("group default binding = %+v, want cleared", groups[0].Defaults)
	}
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != second.ID || eps[0].CredentialRef != "" {
		t.Errorf("remaining endpoint = %+v, want %s with an empty credential", eps, second.ID)
	}
}

func TestNewEndpointID_IsNamespaced(t *testing.T) {
	id := NewEndpointID("My Endpoint")
	if !strings.HasPrefix(id, "endpoint:custom:") {
		t.Errorf("id = %q, want the endpoint:custom: namespace", id)
	}
	if strings.Contains(id, " ") {
		t.Errorf("id = %q, must not contain spaces (slugified name)", id)
	}
}

func ptr(s string) *string { return &s }

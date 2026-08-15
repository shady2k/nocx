package capability_test

// The endpoint write paths with a REFERENCE instead of a mint (bead nocx-rzjw
// half of nocx-lyyk): an endpoint may name a vault secret the vault already
// holds — a row handle in the record's CredentialRef — and a custom header
// value may be a row handle too. The service resolves every renderer row
// handle to its stored reference before anything is written, exactly as
// profile options resolve today. A typed key and a key row are mutually
// exclusive: one source per credential.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

const (
	refA = "sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refB = "sec:v1:file:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rowA = "secrow:0123456789abcdef"
	rowB = "secrow:ffffffffffffffffffffffffffffffff"
)

func TestEndpointCreate_WithKeyRow_ReferencesInsteadOfMinting(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	secrets.rows = map[string]string{rowA: refA, rowB: refB}

	e := testEndpoint()
	e.CredentialRef = rowA
	e.Headers = []profile.EndpointHeader{
		{Name: "HTTP-Referer", Value: profile.Ptr("nocx")},
		{Name: "X-Title", ValueRef: rowB},
	}

	var created profile.Endpoint
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var createErr error
		created, createErr = svc.CreateEndpoint(ctx, e, credential.Secret{})
		return createErr
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if created.CredentialRef != refA {
		t.Errorf("CredentialRef = %q, want the resolved reference %q", created.CredentialRef, refA)
	}

	// The mint path must not have run: no secret was created.
	if minted, _, _ := secrets.snapshot(); len(minted) != 0 {
		t.Fatalf("minted = %v, want none — a referenced key is not minted", minted)
	}

	// The persisted document holds the stored references, never row handles.
	stored := loadStoredEndpoints(t, store)
	if len(stored) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(stored))
	}
	if stored[0].CredentialRef != refA {
		t.Errorf("stored CredentialRef = %q, want %q", stored[0].CredentialRef, refA)
	}
	hdrs := stored[0].Headers
	if len(hdrs) != 2 {
		t.Fatalf("headers = %+v, want 2", hdrs)
	}
	if hdrs[0].Value == nil || *hdrs[0].Value != "nocx" || hdrs[0].ValueRef != "" {
		t.Errorf("headers[0] = %+v, want the literal untouched", hdrs[0])
	}
	if hdrs[1].ValueRef != refB {
		t.Errorf("headers[1] = %+v, want the resolved reference stored", hdrs[1])
	}
}

func TestEndpointCreate_KeyAndKeyRowAreMutuallyExclusive(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	secrets.rows = map[string]string{rowA: refA}

	e := testEndpoint()
	e.CredentialRef = rowA
	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, createErr := svc.CreateEndpoint(ctx, e, credential.NewSecret("sk-test"))
		return createErr
	})
	if err == nil {
		t.Fatal("CreateEndpoint with both a typed key and a key row = nil, want a refusal")
	}
	if eps := loadStoredEndpoints(t, store); len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none", eps)
	}
	if minted, _, _ := secrets.snapshot(); len(minted) != 0 {
		t.Fatalf("minted = %v, want none — the refusal must not mint", minted)
	}
}

func TestEndpointCreate_UnknownRow_RefusedBeforeAnyWrite(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, store, _ := newEndpointEnv(t, secrets)
	e := testEndpoint()
	e.Headers = []profile.EndpointHeader{{Name: "X-Title", ValueRef: "secrow:deadbeefdeadbeefdeadbeefdeadbeef"}}

	err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		_, createErr := svc.CreateEndpoint(ctx, e, credential.Secret{})
		return createErr
	})
	if err == nil {
		t.Fatal("CreateEndpoint with an unknown header row = nil, want an error")
	}
	if eps := loadStoredEndpoints(t, store); len(eps) != 0 {
		t.Fatalf("endpoints = %+v, want none — an unknown row must not write a record", eps)
	}
	if minted, _, _ := secrets.snapshot(); len(minted) != 0 {
		t.Fatalf("minted = %v, want none", minted)
	}
}

func TestEndpointUpdate_WithKeyRow_ReplacesTheReference(t *testing.T) {
	secrets := &fakeEndpointSecrets{}
	op, _, _ := newEndpointEnv(t, secrets)
	secrets.rows = map[string]string{rowA: refA}
	created := createEndpointWithRow(t, op, secrets, rowA)

	secrets.rows[rowB] = refB
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		e := created
		e.CredentialRef = rowB
		updated, updateErr := svc.UpdateEndpoint(ctx, e, nil)
		if updateErr != nil {
			return updateErr
		}
		if updated.CredentialRef != refB {
			t.Errorf("CredentialRef = %q, want the new reference %q", updated.CredentialRef, refB)
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	// No mint, no rotate, no delete: a reference swap touches no material.
	minted, rotated, deleted := secrets.snapshot()
	if len(minted) != 0 || len(rotated) != 0 || len(deleted) != 0 {
		t.Fatalf("minted=%v rotated=%v deleted=%v, want none of them", minted, rotated, deleted)
	}
}

func createEndpointWithRow(t *testing.T, op capability.ConfigOperation, secrets *fakeEndpointSecrets, row string) profile.Endpoint {
	t.Helper()
	e := testEndpoint()
	e.CredentialRef = row
	var created profile.Endpoint
	if err := runConfig(t, op, func(ctx context.Context, svc capability.ConfigService) error {
		var createErr error
		created, createErr = svc.CreateEndpoint(ctx, e, credential.Secret{})
		return createErr
	}); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	return created
}

func loadStoredEndpoints(t *testing.T, store *profile.JSONStore) []profile.Endpoint {
	t.Helper()
	eps, err := store.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	return eps
}

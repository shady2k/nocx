package vault

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

func TestCreateNamed_APITokenAppearsInInventoryWithMeaningfulKind(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("token"), SecretMeta{Kind: "api-token"})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	entries, err := v.BuildInventory(context.Background(), nil)
	if err != nil {
		t.Fatalf("BuildInventory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].ID != RowFor(id) {
		t.Fatalf("entry id = %q, want %q", entries[0].ID, RowFor(id))
	}
	if entries[0].Kind != "api-token" {
		t.Errorf("entry kind = %q, want %q", entries[0].Kind, "api-token")
	}
	if entries[0].Name != "API token" {
		t.Errorf("entry name = %q, want %q", entries[0].Name, "API token")
	}
}

func TestCreateNamed_RejectsDisplayNameThatLooksLikeRowBeforeWriting(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	_, err := v.CreateNamed(context.Background(), credential.NewSecret("token"), SecretMeta{
		Name: "secrow:foo",
		Kind: "api-token",
	})
	if err == nil {
		t.Fatal("CreateNamed accepted a display name that looks like a row handle")
	}
	if !errors.Is(err, ErrSecretNameLooksLikeRow) {
		t.Fatalf("CreateNamed error = %q, want ErrSecretNameLooksLikeRow", err)
	}
	if len(v.doc.Secrets) != 0 {
		t.Fatalf("catalogue records = %d after refused create, want 0", len(v.doc.Secrets))
	}
}

func TestRenameSecret_RejectsDisplayNameThatLooksLikeRowBeforeWriting(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("token"), SecretMeta{
		Name: "old name",
		Kind: "api-token",
	})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}

	err = v.RenameSecret(context.Background(), RowFor(id), "secrow:foo", nil)
	if err == nil {
		t.Fatal("RenameSecret accepted a display name that looks like a row handle")
	}
	if !errors.Is(err, ErrSecretNameLooksLikeRow) {
		t.Fatalf("RenameSecret error = %q, want ErrSecretNameLooksLikeRow", err)
	}
	rec, ok := recordFor(v.doc.Secrets, id)
	if !ok {
		t.Fatal("record disappeared after refused rename")
	}
	if rec.Name != "old name" {
		t.Fatalf("record name = %q after refused rename, want %q", rec.Name, "old name")
	}
}

func TestCreateNamed_AcceptsNameContainingRowPrefixLater(t *testing.T) {
	loweredCost(t)
	v, _, _ := testVault(t, newTestProvider(ProviderSystem))
	mustSetup(t, v, "")
	defer v.Close()

	id, err := v.CreateNamed(context.Background(), credential.NewSecret("token"), SecretMeta{
		Name: "my secrow:thing",
		Kind: "api-token",
	})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	rec, ok := recordFor(v.doc.Secrets, id)
	if !ok {
		t.Fatal("record missing after accepted create")
	}
	if rec.Name != "my secrow:thing" {
		t.Fatalf("record name = %q, want %q", rec.Name, "my secrow:thing")
	}
}

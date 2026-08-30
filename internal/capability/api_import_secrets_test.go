package capability_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

// THE OFFER, END TO END ON THIS SIDE OF THE SOCKET (nocx-zn386).
//
// A Postman export marks a variable `type: secret`. The import offers to put
// it in the vault; the person's answer arrives as one boolean, and the two
// answers are two different files on disk. Neither of them loses the value —
// that is the whole point of the bug this fixes.

// recordingVault is the vault seam an import mints into, and the record of
// what it was asked to do. Delete is here because rollback is half the
// invariant: a record exists from before the collection is written until
// either the write arrives or the record is deleted.
type recordingVault struct {
	created []vault.SecretMeta
	values  []string
	deleted []credential.SecretID
	fail    bool
}

func (v *recordingVault) CreateNamedResolved(
	_ context.Context, value credential.Secret, meta vault.SecretMeta,
) (credential.SecretID, string, error) {
	if v.fail {
		return "", "", os.ErrPermission
	}
	var held string
	_ = value.Use(func(b []byte) error {
		held = string(b)
		return nil
	})
	v.created = append(v.created, meta)
	v.values = append(v.values, held)
	id := credential.SecretID("id-" + meta.Name)
	return id, meta.Name, nil
}

func (v *recordingVault) Delete(_ context.Context, id credential.SecretID) error {
	v.deleted = append(v.deleted, id)
	return nil
}

//nolint:gosec // a synthetic export whose whole point is that it carries a credential-shaped value
const secretExport = `{
  "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
  "variable": [
    {"key": "baseUrl", "value": "https://api.acme.test", "type": "default"},
    {"key": "apiToken", "value": "pm-live-9f3a7c21bd4e8a06", "type": "secret"}
  ],
  "item": [{"name": "ping", "request": {"method": "GET", "url": "{{baseUrl}}/ping"}}]
}`

func importOpWithVault(t *testing.T, v capability.APIImportVault) capability.APIImportOperation {
	t.Helper()
	return capability.NewAPIImportOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate(capability.GateVault, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apiimport.NewOSFS(),
		nil,
		v,
	)
}

func environmentBody(t *testing.T, dest string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dest, "environments"))
	if err != nil {
		t.Fatalf("read the environments folder: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("environments = %d, want 1", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(dest, "environments", entries[0].Name())) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read the environment: %v", err)
	}
	return string(body)
}

func TestAPIImport_TheOfferTaken_PutsTheValueInTheVaultAndAReferenceInTheFile(t *testing.T) {
	v := &recordingVault{}
	op := importOpWithVault(t, v)
	dest := filepath.Join(t.TempDir(), "acme")

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		if _, err := svc.ImportPostmanDocument(ctx, secretExport, dest, true); err != nil {
			t.Fatalf("ImportPostmanDocument: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(v.created) != 1 || v.created[0].Name != "apiToken" || v.created[0].Kind != vault.KindAPIToken {
		t.Fatalf("vault records = %+v, want one apiToken", v.created)
	}
	if v.values[0] != "pm-live-9f3a7c21bd4e8a06" {
		t.Fatalf("the vault was given %q, want the value the export carried", v.values[0])
	}
	env := environmentBody(t, dest)
	if strings.Contains(env, "pm-live-9f3a7c21bd4e8a06") {
		t.Fatalf("the file still carries the value the vault now holds: %s", env)
	}
	if !strings.Contains(env, "{{secret:"+vault.RowFor("id-apiToken")+"}}") {
		t.Fatalf("the file does not name the record that was minted: %s", env)
	}
	// The ordinary variable is untouched: the offer is about the ones
	// Postman marked, and nothing else moves because of it.
	if !strings.Contains(env, "https://api.acme.test") {
		t.Fatalf("an ordinary value was disturbed: %s", env)
	}
}

func TestAPIImport_TheOfferDeclined_WritesTheValueAndMintsNothing(t *testing.T) {
	v := &recordingVault{}
	op := importOpWithVault(t, v)
	dest := filepath.Join(t.TempDir(), "acme")

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		if _, err := svc.ImportPostmanDocument(ctx, secretExport, dest, false); err != nil {
			t.Fatalf("ImportPostmanDocument: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(v.created) != 0 {
		t.Fatalf("vault records = %+v, want none: nobody took the offer", v.created)
	}
	if env := environmentBody(t, dest); !strings.Contains(env, "pm-live-9f3a7c21bd4e8a06") {
		t.Fatalf("the value the export carried is gone from the file too: %s", env)
	}
}

// AND THE INTERVAL HAS BOTH ENDS. A record minted for an import that did not
// arrive is deleted: the alternative is a vault entry for a collection that
// is not on disk, and nobody would know to go looking for it.
func TestAPIImport_AnImportThatDoesNotArriveForgetsWhatItMinted(t *testing.T) {
	v := &recordingVault{}
	op := importOpWithVault(t, v)
	dest := filepath.Join(t.TempDir(), "acme")
	// The destination already exists, which the writer refuses rather than
	// replaces — the ordinary way an import does not arrive.
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		if _, err := svc.ImportPostmanDocument(ctx, secretExport, dest, true); err == nil {
			t.Fatal("an import into an occupied destination succeeded")
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(v.created) != 1 {
		t.Fatalf("vault records = %+v, want the one it minted before it tried", v.created)
	}
	if len(v.deleted) != 1 || v.deleted[0] != credential.SecretID("id-apiToken") {
		t.Fatalf("deleted = %+v, want the record minted for the import that did not arrive", v.deleted)
	}
}

// A build with NO VAULT takes no offer and loses nothing: the values are
// written as the export carried them, which is what a declined offer does.
func TestAPIImport_WithNoVaultTheOfferIsSimplyNotTaken(t *testing.T) {
	op := importOpWithVault(t, nil)
	dest := filepath.Join(t.TempDir(), "acme")

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		if _, err := svc.ImportPostmanDocument(ctx, secretExport, dest, true); err != nil {
			t.Fatalf("ImportPostmanDocument: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if env := environmentBody(t, dest); !strings.Contains(env, "pm-live-9f3a7c21bd4e8a06") {
		t.Fatalf("a build with no vault lost the value: %s", env)
	}
}

// THE PREVIEW IS THE OFFER'S INPUT, and it names the variables without
// carrying their values.
func TestAPIImport_PreviewNamesWhatItWouldOffer(t *testing.T) {
	op := importOpWithVault(t, &recordingVault{})
	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		documents, err := svc.PreviewPostmanDocument(ctx, secretExport)
		if err != nil {
			t.Fatalf("PreviewPostmanDocument: %v", err)
		}
		if len(documents) != 1 || documents[0].Kind != apiimport.ArchiveCollection || documents[0].Name != "acme" {
			t.Fatalf("documents = %+v, want the one collection, named from inside itself", documents)
		}
		if len(documents[0].Secrets) != 1 || documents[0].Secrets[0].Name != "apiToken" {
			t.Fatalf("secrets = %+v, want apiToken", documents[0].Secrets)
		}
		// Nothing was written by a read.
		blob, _ := json.Marshal(documents[0].Secrets)
		if !strings.Contains(string(blob), "apiToken") {
			t.Fatalf("the offer does not name the variable: %s", blob)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

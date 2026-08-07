package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/tunnel"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
)

// contractDir holds the wire schemas. Deliberately not under internal/: the
// renderer generates its types from these files, and a contract only binds if
// it belongs to neither party.
const contractDir = "../../contracts"

func loadSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(contractDir, filepath.Base(name))
	f, openErr := os.Open(path) //nolint:gosec // a test-only path under contracts/
	if openErr != nil {
		t.Fatalf("open %s: %v", path, openErr)
	}
	defer func() { _ = f.Close() }()

	doc, parseErr := jsonschema.UnmarshalJSON(f)
	if parseErr != nil {
		t.Fatalf("parse %s: %v", path, parseErr)
	}
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(name, doc); addErr != nil {
		t.Fatalf("add %s: %v", name, addErr)
	}
	s, err := c.Compile(name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

// validateJSON checks raw against the schema. Takes bytes rather than a Go
// value so it can be handed a real JSON-RPC `result` straight off the socket.
func validateJSON(t *testing.T, s *jsonschema.Schema, raw []byte, what string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: unmarshal: %v", what, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Errorf("%s does not satisfy its contract:\n%v\n\npayload was:\n%s", what, err, raw)
	}
}

// ── vault.status ───────────────────────────────────────────────────────

// The DTO's own conformance: field tags, omitempty behaviour, how a pointer
// renders, whether an enum value spells what the schema says. Cheap, fast, and
// it is NOT the test that catches a missing field — see the WebSocket test
// below for that, and the comment there for why.
func TestVaultStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.status.schema.json")

	cases := map[string]vault.Snapshot{
		// Everything populated, including the fields with omitempty — those are
		// exactly the ones a sparse payload hides.
		"populated": {
			State:           vault.StateUnsealed,
			HasOSKey:        true,
			OSKeyCapable:    true,
			HasPassphrase:   true,
			AutoSealMinutes: 15,
			DefaultProvider: "file",
			Providers: []vault.ProviderSnapshot{
				{ID: "system", Writable: true, Ready: false, Reason: vault.ReasonNoService},
				{ID: "file", Writable: true, Ready: true},
			},
		},
		// The state a fresh install is actually in. `defaultProvider` must be
		// null here and `providers` must be [] rather than null — an empty
		// inventory arriving as null was already a shipped defect once
		// (nocx-25k9.14), and the schema is where that stops being possible.
		"uninitialized": {
			State:           vault.StateUninitialized,
			DefaultProvider: "",
			Providers:       nil,
		},
	}

	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(vaultSnapToStatus(snap))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "vault.status DTO")
		})
	}
}

// The test that would have caught it.
//
// `vault.status` shipped without `defaultProvider` while the renderer read that
// field on every render, so the Vault page could not mark which store new
// secrets go to. Both suites were green: the Go tests decoded the result into
// anonymous structs naming only the fields under test — and a field nobody
// names is a field whose absence nobody notices — while the renderer's tests
// mocked the client with fixtures written FROM the interface, so they contained
// the field because the renderer wanted it, not because anything sent it.
//
// This drives the real handler through the real socket and validates the actual
// `result` bytes. Nothing here names a field, so nothing here can omit one:
// `additionalProperties: false` plus `required` in the schema is what makes the
// key set exact in both directions (nocx-nfld.5).
func TestVaultStatus_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.status.schema.json")

	fake := newFakeVaultLifecycle()
	fake.snap.DefaultProvider = "file"
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected a result")
	}

	validateJSON(t, schema, resp.Result, "vault.status result")
}

// ── vault.reset ────────────────────────────────────────────────────────

func TestVaultReset_DTOsConformToContract(t *testing.T) {
	preview := loadSchema(t, "vault.resetPreview.schema.json")
	result := loadSchema(t, "vault.reset.schema.json")
	rawPreview, err := json.Marshal(vaultResetPreviewResponse{
		SecretCount: 3, ProfileCount: 5,
		SystemKeychainReachable: false, VaultInitialized: true,
	})
	if err != nil {
		t.Fatalf("marshal preview: %v", err)
	}
	validateJSON(t, preview, rawPreview, "vault.resetPreview DTO")

	rawWithResidue, err := json.Marshal(vaultResetResponse{
		SecretCount: 3, ProfileCount: 5,
		Residue: []vaultResetResidueEntry{{Store: "system", Reason: "no-service"}},
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	validateJSON(t, result, rawWithResidue, "vault.reset DTO with residue")

	// And the clean case, which is the one that must not serialise `residue`
	// as null — the renderer types it as a list and iterates it.
	rawClean, err := json.Marshal(vaultResetResponse{
		Residue: []vaultResetResidueEntry{},
	})
	if err != nil {
		t.Fatalf("marshal clean result: %v", err)
	}
	if strings.Contains(string(rawClean), `"residue":null`) {
		t.Errorf("residue serialised as null: %s", rawClean)
	}
	validateJSON(t, result, rawClean, "vault.reset DTO with nothing left behind")
}

// The real methods through the real socket. This is the assertion that would
// have caught vault.status shipping without defaultProvider: nothing here
// names a field, so nothing here can omit one.
func TestVaultReset_OverTheWireConformsToContract(t *testing.T) {
	previewSchema := loadSchema(t, "vault.resetPreview.schema.json")
	resultSchema := loadSchema(t, "vault.reset.schema.json")

	ws, stop := newVaultResetWSServer(t, &fakeVaultReset{})
	defer stop()

	conn := connectWS(t, ws)

	previewResp := vaultCall(t, conn, "vault.resetPreview", map[string]any{}, 1)
	if previewResp.Error != nil {
		t.Fatalf("vault.resetPreview: %+v", previewResp.Error)
	}
	validateJSON(t, previewSchema, previewResp.Result, "vault.resetPreview result")

	resetResp := vaultCall(t, conn, "vault.reset", map[string]any{}, 2)
	if resetResp.Error != nil {
		t.Fatalf("vault.reset: %+v", resetResp.Error)
	}
	validateJSON(t, resultSchema, resetResp.Result, "vault.reset result")
}

// A reset must be reachable on a vault that is broken or half-built, so the
// methods deliberately do not go through the gate that refuses when the vault
// lifecycle is absent. Routing them there would make the way out unavailable
// in exactly the states it exists for.
func TestVaultReset_IsReachableWithNoVaultLifecycleWired(t *testing.T) {
	ws, stop := newVaultResetWSServer(t, &fakeVaultReset{})
	defer stop()

	resp := vaultCall(t, connectWS(t, ws), "vault.reset", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("vault.reset with no vault lifecycle: %+v", resp.Error)
	}
}

func newVaultResetWSServer(t *testing.T, rs VaultResetService) (*WSServer, func()) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultReset(rs))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

type fakeVaultReset struct{}

func (f *fakeVaultReset) Preview(_ context.Context) (vaultreset.Preview, error) {
	return vaultreset.Preview{
		Impact:                  vaultreset.Impact{SecretCount: 3, ProfileCount: 5},
		SystemKeychainReachable: false,
		VaultInitialized:        true,
	}, nil
}

func (f *fakeVaultReset) Execute(_ context.Context) (vaultreset.Result, error) {
	return vaultreset.Result{
		Impact:  vaultreset.Impact{SecretCount: 3, ProfileCount: 5},
		Residue: []vaultreset.Residue{{Store: "system", Reason: "no-service"}},
	}, nil
}

// ── vault.inventory ───────────────────────────────────────────────────

// The DTO's own conformance: field tags, nil-slice-as-null, enum spelling.
// The case that matters is the empty one — entries must marshal to [] never
// null (nocx-25k9.14), and every field must be present (additionalProperties
// false + required makes the key set exact in both directions).
func TestVaultInventory_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.inventory.schema.json")

	cases := map[string]struct {
		entries []vault.InventoryEntry
	}{
		"populated": {
			entries: []vault.InventoryEntry{
				{
					ID:        "secrow:9f0c8a1b2c3d4e5faabbccdd00112233",
					Name:      "root@192.168.0.57",
					Kind:      "password",
					Provider:  "system",
					OwnerID:   "cred:prod:1",
					UsedBy:    3,
					Reachable: true,
				},
				{
					ID:        "secrow:aabbccdd00112233aabbccdd00112233",
					Name:      "Key passphrase",
					Kind:      "key-passphrase",
					Provider:  "file",
					OwnerID:   "",
					UsedBy:    0,
					Reachable: false,
				},
			},
		},
		// The empty vault is the one that must not serialise `entries` as
		// null — the renderer's first `.map` would throw. The vault's
		// BuildInventory always hands back a non-nil slice; this case pins
		// that the response shape then satisfies the schema.
		"empty": {entries: []vault.InventoryEntry{}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(struct {
				Entries []vault.InventoryEntry `json:"entries"`
			}{Entries: tc.entries})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "vault.inventory DTO")
		})
	}
}

// The real result off the real socket: the inventory the renderer actually
// receives. A populated vault (a saved password) and an ownerless secret
// created on the Secrets page — the case ADR-0016 exists for.
func TestVaultInventory_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.inventory.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	// A secret saved on a connection: minted via secrets.savePassword and
	// bound onto a profile's options by row handle (ADR-0017).
	prof := profile.SSHProfile{
		Base: profile.Base{
			ID:   "ssh:prod:1",
			Name: "prod",
			Type: "ssh",
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "vm-dsm01",
			User: profile.Ptr("deploy"),
		},
	}
	h.createProfile(prof)
	passRow := h.mintPassword("hunter2", "deploy@vm-dsm01")
	resp := jsonrpcCall(t, h.conn, "profiles.patch", map[string]any{
		"id":  "ssh:prod:1",
		"set": map[string]any{"options.passwordSecret": passRow},
	})
	var patchResult struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &patchResult); err != nil {
		t.Fatalf("profiles.patch unmarshal: %v\nraw: %s", err, string(resp))
	}
	if patchResult.Error != nil {
		t.Fatalf("profiles.patch: %+v", patchResult.Error)
	}

	// A secret created on the Secrets page — no profile references it.
	jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "hunter2",
	})

	invResp := vaultCall(t, h.conn, "vault.inventory", map[string]any{}, 1)
	if invResp.Error != nil {
		t.Fatalf("vault.inventory: %+v", invResp.Error)
	}
	validateJSON(t, schema, invResp.Result, "vault.inventory result")

	// And the wire carries no secret reference anywhere: every row id is a
	// row handle, every name is a name, and the SecretID appears in neither.
	var inv struct {
		Result struct {
			Entries []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"entries"`
		} `json:"result"`
	}
	if err := json.Unmarshal(invResp.Result, &inv.Result); err != nil {
		t.Fatalf("unmarshal inventory: %v", err)
	}
	if len(inv.Result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(inv.Result.Entries))
	}
	for _, e := range inv.Result.Entries {
		if strings.HasPrefix(e.ID, "sec:v1:") {
			t.Errorf("row id %q looks like a secret reference", e.ID)
		}
		if e.Name == "" {
			t.Error("a name is blank")
		}
	}
}

// ── vault.createSecret / vault.renameSecret ───────────────────────────

// The create and rename results are empty objects, but the schema pins that
// shape and the socket test drives the REAL methods with the fields the
// renderer sends (and the renderer leaves nothing out — there is nothing to
// leave out).
func TestVaultCreateAndRename_OverTheWireConformToContract(t *testing.T) {
	createSchema := loadSchema(t, "vault.createSecret.schema.json")
	renameSchema := loadSchema(t, "vault.renameSecret.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	createResp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "hunter2",
	})
	var createEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(createResp, &createEnvelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(createResp))
	}
	if createEnvelope.Error != nil {
		t.Fatalf("vault.createSecret: %+v", createEnvelope.Error)
	}
	validateJSON(t, createSchema, createEnvelope.Result, "vault.createSecret result")

	// Rename needs the row handle the inventory carries.
	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(inv.Entries))
	}

	renameResp := jsonrpcCall(t, h.conn, "vault.renameSecret", map[string]any{
		"id": inv.Entries[0].ID, "name": "the prod password",
	})
	var renameEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(renameResp, &renameEnvelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(renameResp))
	}
	if renameEnvelope.Error != nil {
		t.Fatalf("vault.renameSecret: %+v", renameEnvelope.Error)
	}
	validateJSON(t, renameSchema, renameEnvelope.Result, "vault.renameSecret result")

	// The rename landed on the vault's own record: the inventory shows it.
	inv2 := h.callInventory()
	if len(inv2.Entries) != 1 {
		t.Fatalf("expected 1 entry after rename, got %d", len(inv2.Entries))
	}
	if inv2.Entries[0].Name != "the prod password" {
		t.Errorf("name after rename = %q, want %q", inv2.Entries[0].Name, "the prod password")
	}
}

// The renderer may not name a secret (nocx-jb20.1): rename accepts the row
// handle, and a SecretID sent in its place must be refused — a row handle is
// a one-way derivative, never the reference.
func TestVaultRenameSecret_RejectsSecretID(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "hunter2",
	})

	// A secret reference is not a valid row handle: unknown row.
	resp := jsonrpcCall(t, h.conn, "vault.renameSecret", map[string]any{
		"id": "sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "name": "x",
	})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatal("expected an error for a SecretID addressed rename")
	}
}

// ── vault.replaceSecret ────────────────────────────────────────────────

// The DTO's own conformance: the replace result is an empty object, and the
// schema pins that shape.
func TestVaultReplaceSecret_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.replaceSecret.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "vault.replaceSecret DTO")
}

// The real method through the real socket, with the fields the renderer sends
// (the row handle the inventory carried). Nothing here names a field, so
// nothing here can omit one.
func TestVaultReplaceSecret_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.replaceSecret.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "hunter2",
	})
	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(inv.Entries))
	}

	replaceResp := jsonrpcCall(t, h.conn, "vault.replaceSecret", map[string]any{
		"id": inv.Entries[0].ID, "value": "hunter3",
	})
	var replaceEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(replaceResp, &replaceEnvelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(replaceResp))
	}
	if replaceEnvelope.Error != nil {
		t.Fatalf("vault.replaceSecret: %+v", replaceEnvelope.Error)
	}
	validateJSON(t, schema, replaceEnvelope.Result, "vault.replaceSecret result")
}

// ── dialog.openFile ────────────────────────────────────────────────────

// The DTO's own conformance: an absolute path, or an empty string for a
// cancelled picker. The `path` field is required and the key set is exact.
func TestDialogOpenFile_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openFile.schema.json")
	dto := struct {
		Path string `json:"path"`
	}{Path: "/home/dev/.ssh/id_ed25519"}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "dialog.openFile DTO")

	// The cancelled case: an empty path is still a valid result.
	rawCancel, err := json.Marshal(struct {
		Path string `json:"path"`
	}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawCancel, "dialog.openFile cancelled DTO")
}

// The real method through the real socket, with the fake service standing in
// for the Wails runtime. The dev-web harness has no Wails at all, so the
// absent-service error path is the state this is actually tested in — the
// surface must degrade, not fail.
func TestDialogOpenFile_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openFile.schema.json")
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{filePath: "/home/dev/.ssh/id_ed25519"})

	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("dialog.openFile: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "dialog.openFile result")
}

// ── secrets.saveKeyMaterial ─────────────────────────────────────────────

// The DTO's own conformance: the mint result has row + fingerprint +
// passphraseWanted, in both interesting shapes. An encrypted key has an empty
// fingerprint AND passphraseWanted=true; an unencrypted one has a fingerprint
// and passphraseWanted=false. A field the renderer must branch on cannot be
// optional.
func TestSaveKeyMaterial_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.saveKeyMaterial.schema.json")

	dto := struct {
		Row              string `json:"row"`
		Fingerprint      string `json:"fingerprint"`
		PassphraseWanted bool   `json:"passphraseWanted"`
	}{Row: "secrow:abc", Fingerprint: "SHA256:abc123", PassphraseWanted: false}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "saveKeyMaterial unencrypted DTO")

	dtoEnc := struct {
		Row              string `json:"row"`
		Fingerprint      string `json:"fingerprint"`
		PassphraseWanted bool   `json:"passphraseWanted"`
	}{Row: "secrow:def", Fingerprint: "", PassphraseWanted: true}
	rawEnc, err := json.Marshal(dtoEnc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawEnc, "saveKeyMaterial encrypted DTO")
}

// The real result off the real socket — the field that must not go missing is
// passphraseWanted, without which the renderer would never ask for the key's
// passphrase and the encrypted key would surface its failure at connect time.
func TestSaveKeyMaterial_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.saveKeyMaterial.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	pem, _ := testEncryptedKeyPEM(t, "contract-passphrase")
	raw := jsonrpcCall(t, h.conn, "secrets.saveKeyMaterial", map[string]any{
		"keyText": pem,
		"name":    "contract-key",
	})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(raw))
	}
	if envelope.Error != nil {
		t.Fatalf("saveKeyMaterial: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "saveKeyMaterial result")

	// The wire carries the row handle, never a secret reference.
	if strings.Contains(string(envelope.Result), "sec:v1:") {
		t.Errorf("saveKeyMaterial result leaks a secret reference: %s", envelope.Result)
	}
}

// ── vault.deleteSecret ────────────────────────────────────────────────

// The DTO's own conformance: the delete result is an empty object — there is
// nothing to return, the row is what changes — and the schema pins that shape.
func TestVaultDeleteSecret_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.deleteSecret.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "vault.deleteSecret DTO")
}

// The real method through the real socket, with the fields the renderer sends
// (the row handle the inventory carried). After deletion the row is gone from
// the inventory, the stored secret is gone from the vault, and the profile no
// longer claims a password is saved — metadata first, stored secret second
// (ADR-0011 §4).
func TestVaultDeleteSecret_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.deleteSecret.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	// A password minted on a connection and bound to a profile.
	prof := profile.SSHProfile{
		Base: profile.Base{
			ID:   "ssh:prod:1",
			Name: "prod",
			Type: "ssh",
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "vm-dsm01",
			User: profile.Ptr("deploy"),
		},
	}
	h.createProfile(prof)
	passRow := h.mintPassword("hunter2", "deploy@vm-dsm01")
	patchResp := jsonrpcCall(t, h.conn, "profiles.patch", map[string]any{
		"id":  "ssh:prod:1",
		"set": map[string]any{"options.passwordSecret": passRow},
	})
	var patchEnvelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(patchResp, &patchEnvelope); err != nil {
		t.Fatalf("profiles.patch unmarshal: %v\nraw: %s", err, string(patchResp))
	}
	if patchEnvelope.Error != nil {
		t.Fatalf("profiles.patch: %+v", patchEnvelope.Error)
	}

	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("precondition: expected 1 entry, got %d", len(inv.Entries))
	}

	// The renderer never sees the SecretID; read it from the stored profile
	// to assert the stored secret is really gone afterwards.
	var secretID credential.SecretID
	{
		profs, err := h.ps.LoadProfiles()
		if err != nil {
			t.Fatalf("LoadProfiles: %v", err)
		}
		for _, p := range profs {
			if p.ID == "ssh:prod:1" {
				secretID = credential.SecretID(p.Options.PasswordSecret)
			}
		}
	}
	if secretID == "" {
		t.Fatal("precondition: no password reference on profile ssh:prod:1")
	}

	deleteResp := jsonrpcCall(t, h.conn, "vault.deleteSecret", map[string]any{
		"id": inv.Entries[0].ID,
	})
	var deleteEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(deleteResp, &deleteEnvelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(deleteResp))
	}
	if deleteEnvelope.Error != nil {
		t.Fatalf("vault.deleteSecret: %+v", deleteEnvelope.Error)
	}
	validateJSON(t, schema, deleteEnvelope.Result, "vault.deleteSecret result")

	// The row is gone, not renamed and not hidden.
	inv2 := h.callInventory()
	if len(inv2.Entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(inv2.Entries))
	}

	// The stored secret is gone from the vault.
	exists, err := h.v.Exists(h.t.Context(), secretID)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("stored secret still exists after delete")
	}

	// The profile reference is gone: nothing points at the deleted secret.
	profsAfter, err := h.ps.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles after delete: %v", err)
	}
	for _, p := range profsAfter {
		if p.ID == "ssh:prod:1" && p.Options.PasswordSecret != "" {
			t.Errorf("profile still references the deleted secret: %q", p.Options.PasswordSecret)
		}
	}
}

// The renderer may not name a secret (nocx-jb20.1): delete accepts the row
// handle, and a SecretID sent in its place must be refused.
func TestVaultDeleteSecret_RejectsSecretID(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "hunter2",
	})

	resp := jsonrpcCall(t, h.conn, "vault.deleteSecret", map[string]any{
		"id": "sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatal("expected an error for a SecretID addressed delete")
	}

	// Nothing was deleted: the secret is still there.
	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("expected the secret to survive a refused delete, got %d entries", len(inv.Entries))
	}
}

// ── vault.createSecret ─────────────────────────────────────────────────

// The DTO's own conformance: the response always carries the name that was
// used — the requested one for an ordinary create, the collision-resolved
// one when the caller asked for resolution. The renderer builds the
// {{secret:NAME}} reference from this answer, never from the name it sent.
func TestVaultCreateSecret_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.createSecret.schema.json")
	cases := map[string]vaultCreateSecretResponse{
		"plain create":    {Name: "prod-password"},
		"resolved create": {Name: "prod-password-2"},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "vault.createSecret DTO")
		})
	}
}

func TestVaultCreateSecret_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.createSecret.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	first := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "openrouter.ai", "kind": "password", "value": "sk-first",
	})
	firstRaw := decodeCreateSecretResult(t, first)
	validateJSON(t, schema, firstRaw, "vault.createSecret result (real socket, plain)")
	var firstResult vaultCreateSecretResponse
	if err := json.Unmarshal(firstRaw, &firstResult); err != nil {
		t.Fatalf("decode first result: %v", err)
	}
	if firstResult.Name != "openrouter.ai" {
		t.Errorf("plain create name = %q, want the requested name", firstResult.Name)
	}

	// The same name again, with resolution: the vault picks the next free
	// suffix and reports it. The renderer could never predict this.
	second := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "openrouter.ai", "kind": "password", "value": "sk-second", "resolve": true,
	})
	secondRaw := decodeCreateSecretResult(t, second)
	validateJSON(t, schema, secondRaw, "vault.createSecret result (real socket, resolved)")
	var secondResult vaultCreateSecretResponse
	if err := json.Unmarshal(secondRaw, &secondResult); err != nil {
		t.Fatalf("decode second result: %v", err)
	}
	if secondResult.Name == "openrouter.ai" {
		t.Errorf("resolved create returned the collided name %q — the vault must suffix", secondResult.Name)
	}

	// Both rows exist in the inventory under their real names.
	inv := h.callInventory()
	names := make([]string, 0, len(inv.Entries))
	for _, e := range inv.Entries {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] == names[1] {
		t.Errorf("inventory names = %v, want two distinct rows", names)
	}
}

// decodeCreateSecretResult reads the raw `result` bytes of a createSecret
// response — the exact payload the socket carried, which is what the
// over-the-wire rule validates — and fails on a JSON-RPC error.
func decodeCreateSecretResult(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(raw))
	}
	if env.Error != nil {
		t.Fatalf("vault.createSecret: %+v", env.Error)
	}
	return env.Result
}

// ── connections.test / connections.trustHostKey ────────────────────────

// The DTO's own conformance: both host-key shapes, and the one field that
// must not leak between them — storedFingerprint is present for a changed
// key and absent for an unknown one. If omitempty ever drops it, the changed
// case fails right here, before the renderer ever sees a key change without
// the stored fingerprint to judge it by.
func TestConnectionsTest_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.probe.schema.json")

	cases := []struct {
		name       string
		dto        connectionsTestResult
		wantStored bool
		wantAbsent bool
	}{
		{"accepted", connectionsTestResult{Outcome: OutcomeAccepted, Detail: "ok"}, false, false},
		{
			"host-key-unknown",
			connectionsTestResult{
				Outcome: OutcomeHostKeyUnknown,
				Detail:  "unknown host key for h:22: ssh-ed25519 SHA256:abc",
				HostKey: &connectionsTestHostKey{
					Host:           "h:22",
					KnownHostsHost: "nocx-v1-route:22",
					Changed:        false,
					Algorithm:      "ssh-ed25519",
					Fingerprint:    "SHA256:abc",
					Key:            "a2V5",
				},
			},
			false,
			true,
		},
		{
			"host-key-changed",
			connectionsTestResult{
				Outcome: OutcomeHostKeyChanged,
				Detail:  "host key mismatch for h:22: got SHA256:new, expected SHA256:old",
				HostKey: &connectionsTestHostKey{
					Host:              "h:22",
					KnownHostsHost:    "nocx-v1-route:22",
					Changed:           true,
					Algorithm:         "ecdsa-sha2-nistp256",
					Fingerprint:       "SHA256:new",
					StoredFingerprint: "SHA256:old",
					Key:               "a2V5",
				},
			},
			true,
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "connections.test result ("+tc.name+")")
			hasStored := strings.Contains(string(raw), "storedFingerprint")
			if tc.wantStored && !hasStored {
				t.Errorf("storedFingerprint must be present for %s, wire: %s", tc.name, raw)
			}
			if tc.wantAbsent && hasStored {
				t.Errorf("storedFingerprint must be absent for %s, wire: %s", tc.name, raw)
			}
		})
	}
}

// The real method through the real socket: the renderer receives the offered
// fingerprint and key, and the stored fingerprint only when the key changed.
func TestConnectionsTest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.probe.schema.json")

	cases := []struct {
		name string
		err  error
	}{
		{"accepted", nil},
		{
			"host-key-unknown",
			&ssh.ErrUnknownHostKey{
				Addr:           "host.example.com:22",
				KnownHostsAddr: "nocx-v1-route:22",
				KeyAlgo:        "ssh-ed25519",
				Fingerprint:    "SHA256:abc",
				Key:            []byte("offered-key-blob"),
			},
		},
		{
			"host-key-changed",
			&ssh.ErrHostKeyMismatch{
				Addr:           "host.example.com:22",
				KnownHostsAddr: "nocx-v1-route:22",
				Fingerprint:    "SHA256:new",
				Expected:       "SHA256:old",
				KeyAlgo:        "ecdsa-sha2-nistp256",
				Key:            []byte("new-key-blob"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProbeTestServer(t, tc.err, &fakeResolver{
				resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
					return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
				},
			})
			conn := connectWS(t, srv)
			defer conn.Close() //nolint:errcheck

			resp := jsonrpcCall(t, conn, "connections.test", map[string]any{
				"profileId": "ssh:test:1",
			})
			var envelope struct {
				Result json.RawMessage `json:"result"`
				Error  *struct{}       `json:"error,omitempty"`
			}
			if err := json.Unmarshal(resp, &envelope); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if envelope.Error != nil {
				t.Fatalf("unexpected RPC error: %s", resp)
			}
			validateJSON(t, schema, envelope.Result, "connections.test result ("+tc.name+") over the wire")
		})
	}
}

func TestConnectionsTrustHostKey_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.trustHostKey.schema.json")
	raw, err := json.Marshal(connectionsTrustHostKeyResult{Fingerprint: "SHA256:trusted"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "connections.trustHostKey result")
}

// The real method through the real socket: the renderer echoes host+key from
// the probe result, and the response carries the fingerprint of the key that
// was appended to known_hosts.
func TestConnectionsTrustHostKey_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.trustHostKey.schema.json")
	truster := &fakeHostKeyTruster{fingerprint: "SHA256:trusted"}

	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	srv := NewWSServer(logger, reg, WithHostKeyTruster(truster))
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })

	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	resp := jsonrpcCall(t, conn, "connections.trustHostKey", map[string]any{
		"host": "host.example.com:22",
		"key":  "b2ZmZXJlZC1rZXktYmxvYg==",
	})
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct{}       `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("unexpected RPC error: %s", resp)
	}
	validateJSON(t, schema, envelope.Result, "connections.trustHostKey result over the wire")
	if !truster.called {
		t.Fatal("expected the truster to be called")
	}
}

// ── history.query ─────────────────────────────────────────────────────────

// The DTO's own conformance: field tags, omitempty behaviour, null-vs-omitted
// for the nullable fields, enum spelling, and the never-null entries slice.
func TestHistoryQuery_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "history.query.schema.json")

	exit := 1
	ended := int64(1_750_000_000_000)
	started := ended - 2_300
	horizon := int64(1_700_000_000_000)
	cases := map[string]historyQueryResponse{
		// Everything populated, including the nullable fields — a running
		// entry must render exitCode as null and endedAt as null, never as 0
		// and never as the epoch; a store with a horizon states it.
		"running": {
			Entries: []historyQueryEntry{
				{ID: "9", Command: "make test", Cwd: "/repo", Host: "", Status: "running", MaskedCount: 0, MaskedKinds: []string{}, Redactions: []redactionWire{}},
			},
			Scope:     "directory",
			Exhausted: true,
			Source:    "store",
			Coverage:  &horizon,
		},
		"populated": {
			Entries: []historyQueryEntry{
				{ID: "42", Command: "ssh prod deploy", Cwd: "/srv/api", Host: "prod.example.com", Status: "failure", ExitCode: &exit, StartedAt: &started, EndedAt: &ended, MaskedCount: 2, MaskedKinds: []string{"openai", "jwt"}, Redactions: []redactionWire{{Kind: "openai", Start: 31, End: 42, Prefix: "sk-p", Suffix: "7890"}}},
			},
			Scope:     "host",
			Exhausted: false,
			Source:    "store",
			Coverage:  &horizon,
		},
		// The empty answer: the store answered and the rung had no matches.
		// entries must marshal to [] never null (nocx-25k9.14 class), and the
		// five required fields must all be present.
		"empty rung": {
			Entries:   []historyQueryEntry{},
			Scope:     "everywhere",
			Exhausted: true,
			Source:    "store",
			Coverage:  &horizon,
		},
		// The unanswerable question: no store at all. Coverage is null —
		// the schema's ["integer","null"] — never omitted: the overlay
		// decides what to render from its value, and a missing key would
		// throw on read.
		"no store": {
			Entries:   []historyQueryEntry{},
			Scope:     "directory",
			Exhausted: true,
			Source:    "session",
			Coverage:  nil,
		},
	}

	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "history.query DTO")
		})
	}
}

// The real method through the real socket. Nothing here names a field, so
// nothing here can omit one; the schema's additionalProperties:false plus
// required makes the key set exact in both directions. Two states are driven:
// a store with rows (source=store, with a horizon) and the source=session
// fallback the overlay must label "this session only".
func TestHistoryQuery_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "history.query.schema.json")
	ctx := context.Background()

	t.Run("store answered", func(t *testing.T) {
		ended := int64(1_750_000_000_000)
		started := ended - 4_100
		horizon := int64(1_700_000_000_000)
		exit := 0
		fake := &fakeHistoryDB{page: content.HistoryPage{
			Entries: []content.CommandRecord{
				{ID: 7, Command: "git status", Cwd: "/repo", Host: "", Status: content.StatusSuccess, ExitCode: &exit, StartedAt: &started, EndedAt: &ended},
				{ID: 6, Command: "make", Cwd: "/repo", Host: "", Status: content.StatusFailure},
			},
			HasRows:   true,
			Exhausted: true,
			Coverage:  &horizon,
		}}
		ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
			WithContentDB(fake))
		if err := ws.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer func() { _ = ws.Stop(ctx) }()

		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{
			"scope": "directory", "cwd": "/repo", "limit": 50,
		}, 1)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "history.query result (store)")

		// The horizon is a real value off the real socket, not a field the
		// test built — decode and name it, the way the renderer will.
		var got struct {
			Coverage *int64 `json:"coverage"`
		}
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("decode coverage: %v", err)
		}
		if got.Coverage == nil || *got.Coverage != horizon {
			t.Fatalf("coverage off the socket = %v, want %d", got.Coverage, horizon)
		}
	})

	t.Run("no store answers session", func(t *testing.T) {
		ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
		if err := ws.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer func() { _ = ws.Stop(ctx) }()

		conn := connectWS(t, ws)
		resp := vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 1)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "history.query result (session)")
	})
}

// ── fs.complete ──────────────────────────────────────────────────────────

// The DTO's own conformance: field tags, omitempty behaviour, and the
// never-null entries slice.
func TestFsComplete_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "fs.complete.schema.json")

	cases := map[string]fsCompleteResponse{
		// Everything populated: a file and a directory. isDir must marshal
		// exactly, and path is the absolute path the renderer keys on.
		"populated": {
			Entries: []fsCompleteEntry{
				{Name: "src", Path: "/repo/src", IsDir: true},
				{Name: "main.go", Path: "/repo/main.go", IsDir: false},
			},
		},
		// The empty answer: no matches is [] never null (nocx-25k9.14 class).
		"empty": {
			Entries: []fsCompleteEntry{},
		},
	}

	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "fs.complete DTO")
		})
	}
}

// The real method through the real socket. Nothing here names a field, so
// nothing here can omit one; the schema's additionalProperties:false plus
// required makes the key set exact in both directions. The fixture is a real
// directory on the backend's filesystem — the only honest source for a
// filesystem completion.
func TestFsComplete_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "fs.complete.schema.json")
	ctx := context.Background()

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	t.Run("relative completion", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, "src"), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		resp := vaultCall(t, conn, "fs.complete", map[string]any{"text": "ma", "cwd": dir}, 2)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "fs.complete result (relative)")

		var got fsCompleteResponse
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(got.Entries) != 1 || got.Entries[0].Name != "main.go" || got.Entries[0].IsDir {
			t.Errorf("entries = %+v, want exactly [main.go (file)]", got.Entries)
		}
	})

	t.Run("no match answers empty", func(t *testing.T) {
		dir := t.TempDir()
		resp := vaultCall(t, conn, "fs.complete", map[string]any{"text": "zzz", "cwd": dir}, 3)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "fs.complete result (empty)")

		var got fsCompleteResponse
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if got.Entries == nil || len(got.Entries) != 0 {
			t.Errorf("entries = %v, want [] (never null)", got.Entries)
		}
	})

	t.Run("empty text is refused", func(t *testing.T) {
		resp := vaultCall(t, conn, "fs.complete", map[string]any{"text": "", "cwd": t.TempDir()}, 4)
		if resp.Error == nil {
			t.Fatal("empty text must be an invalid-params error, got success")
		}
		if resp.Error.Code != -32602 {
			t.Errorf("error code = %d, want -32602", resp.Error.Code)
		}
	})
}

// ── shell.complete ──────────────────────────────────────────────────────

func TestShellComplete_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.complete.schema.json")

	cases := map[string]shellCompleteResponse{
		"populated": {
			Entries: []shellCompleteEntry{
				{Name: "src", Path: "/repo/src", Source: "path", IsDir: true},
				{Name: "main.go", Path: "/repo/main.go", Source: "path", IsDir: false},
				{Name: "git", Source: "command"},
				{Name: "checkout", Source: "function"},
			},
			Truncated: false,
		},
		"empty": {
			Entries:   []shellCompleteEntry{},
			Truncated: false,
		},
		"truncated": {
			Entries:   []shellCompleteEntry{},
			Truncated: true,
			Reason:    "output cap hit",
		},
	}

	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "shell.complete DTO")
		})
	}
}

// shell.complete over the wire with a real completer. The test opens a
// local session and completes a known directory — the same motion as the
// acceptance criterion "the local path completion still works exactly as
// before".
func TestShellComplete_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.complete.schema.json")
	ctx := context.Background()

	ws := NewWSServer(
		log.NewSlogAdapter(nil),
		newRegWithStub(log.NewSlogAdapter(nil)),
		WithCompleters(completion.NewLocal(), nil),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	// Open a local session so we have a session ID the handler can look up.
	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols":   80,
		"rows":   24,
		"xpixel": 800,
		"ypixel": 600,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open error: %+v", openResp.Error)
	}
	// Extract the server-assigned session ID from the open result.
	var openResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &openResult); err != nil {
		t.Fatalf("unmarshal open result: %v", err)
	}
	sid := openResult.SessionID
	if sid == "" {
		t.Fatal("open returned empty sessionId")
	}

	t.Run("path completion over the wire", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		resp := vaultCall(t, conn, "shell.complete", map[string]any{
			"sessionId": sid,
			"cwd":       dir,
			"line":      "cat rea",
			"pos":       7,
		}, 2)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "shell.complete result")

		var got shellCompleteResponse
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(got.Entries) != 1 || got.Entries[0].Name != "readme.md" {
			t.Errorf("entries = %+v, want exactly [readme.md]", got.Entries)
		}
		if got.Entries[0].Source != "path" {
			t.Errorf("source = %q, want 'path'", got.Entries[0].Source)
		}
	})

	t.Run("empty result is [] never null", func(t *testing.T) {
		dir := t.TempDir()
		resp := vaultCall(t, conn, "shell.complete", map[string]any{
			"sessionId": sid,
			"cwd":       dir,
			"line":      "cat zzz",
			"pos":       7,
		}, 3)
		if resp.Error != nil {
			t.Fatalf("unexpected error: %+v", resp.Error)
		}
		validateJSON(t, schema, resp.Result, "shell.complete result (empty)")

		var got shellCompleteResponse
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if got.Entries == nil || len(got.Entries) != 0 {
			t.Errorf("entries = %v, want [] (never null)", got.Entries)
		}
	})

	t.Run("missing content returns error", func(t *testing.T) {
		resp := vaultCall(t, conn, "shell.complete", map[string]any{
			"sessionId": sid,
		}, 4)
		if resp.Error == nil {
			t.Fatal("missing required params must error, got success")
		}
		if resp.Error.Code != -32602 {
			t.Errorf("error code = %d, want -32602", resp.Error.Code)
		}
	})
}

// ── vault.resolveLine ──────────────────────────────────────────────────────

// The DTO's own conformance: field tags, the never-null refs slice, and the
// two-valued resolved flag. The handler never sends a null refs list — no
// references is [].
func TestVaultResolveLine_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.resolveLine.schema.json")
	cases := map[string]vaultResolveLineResponse{
		"no refs": {
			Line: "ls -la",
			Refs: []vaultResolveLineRef{},
		},
		"mixed": {
			Line: "run --password hunter2 --other {{secret:ghost}}",
			Refs: []vaultResolveLineRef{
				{Name: "db-password", Resolved: true},
				{Name: "ghost", Resolved: false},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "vault.resolveLine DTO")
		})
	}
}

// The real method through the real socket: a secret minted the way the
// Secrets page mints one, resolved by the name the inventory reports. The
// resolved value rides the line (it is going to the PTY) and the refs carry
// only the name and the outcome — nothing here names a field, so nothing
// here can omit one.
func TestVaultResolveLine_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.resolveLine.schema.json")
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	h.mintPassword("sk-proj-abcdef1234567890", "prod-api-key")

	resp := vaultCall(t, h.conn, "vault.resolveLine", map[string]any{
		"line": `curl -H "Authorization: Bearer {{secret:prod-api-key}}" https://api`,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("vault.resolveLine: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "vault.resolveLine result (real socket)")

	// The value really landed in the line — the wire carried it to the
	// caller, which is the only place it is allowed to go.
	var got struct {
		Line string `json:"line"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Line != `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api` {
		t.Errorf("line = %q, want the substituted line", got.Line)
	}
}

// ── open ─────────────────────────────────────────────────────────────────

// The DTO's own conformance: the four fields the open ack always carries.
// shellIntegrationReason is present even when empty — a missing field would
// read as "integration happened" to a renderer that defaults to that, which
// is exactly the soft degrade AGENTS.md forbids. desiredMode (the resolved
// destination mode, nocx-mlm7) is present for every session, including
// local ones — a renderer that defaults a missing field to "script" would
// show a raw tab as silently integrated. Each of the three mode values
// must marshal; the schema pins the enum.
func TestOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")

	raw, err := json.Marshal(map[string]string{
		"sessionId":              "0123456789abcdef0123456789abcdef",
		"cwd":                    "~/work",
		"shellIntegrationReason": "no-secure-temp",
		"desiredMode":            "script",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "open DTO (launcher refusal)")

	rawNone, err := json.Marshal(map[string]string{
		"sessionId":              "0123456789abcdef0123456789abcdef",
		"cwd":                    "~/work",
		"shellIntegrationReason": "",
		"desiredMode":            "raw",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawNone, "open DTO (integration never attempted — raw)")

	rawUnknown, err := json.Marshal(map[string]string{
		"sessionId":              "0123456789abcdef0123456789abcdef",
		"cwd":                    "~/work",
		"shellIntegrationReason": "unknown",
		"desiredMode":            "relay",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawUnknown, "open DTO (unclassified refusal, relay mode)")
}

// openProfileResolver resolves every profile to a fixed host and a minimal
// ConnectConfig — the launcher wiring the transport adds is what the test
// is about, so nothing else in the resolved config may interfere.
type openProfileResolver struct {
	host string
}

func (r *openProfileResolver) Resolve(_ string) (string, *ssh.ConnectConfig, error) {
	return r.host, &ssh.ConnectConfig{User: "test"}, nil
}

// openSSHFactory is a session.SSHFactory that records the ConnectOptions the
// registry built and returns a channel carrying a scripted refusal reason.
type openSSHFactory struct {
	channel ssh.Channel
	host    string
	opts    []ssh.ConnectOption
}

func (f *openSSHFactory) Connect(_ context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
	f.host = host
	f.opts = append([]ssh.ConnectOption{}, opts...)
	return f.channel, nil
}

// reasonChannel overrides the stub channel's reason so the wire actually
// carries a non-empty refusal.
type reasonChannel struct {
	*ssh.StubChannel
	reason ssh.RefusalReason
}

func (c *reasonChannel) ShellIntegrationReason() ssh.RefusalReason {
	return c.reason
}

// The real method through the real socket: an SSH open with the launcher
// wired, validated against the schema and asserted to carry the reason the
// channel reports. Nothing here names a field, so nothing here can omit one;
// additionalProperties:false plus required makes the key set exact in both
// directions. It also proves the composition chain that was missing: the
// transport option lands in the ConnectConfig the registry turns into
// ConnectOptions (nocx-ei04).
func TestOpen_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")

	reg := session.New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
	factory := &openSSHFactory{
		channel: &reasonChannel{StubChannel: ssh.NewStubChannel(log.NewSlogAdapter(nil)), reason: ssh.ReasonNoSecureTemp},
	}
	reg = reg.WithSSHFactory(factory)

	ws := NewWSServer(
		log.NewSlogAdapter(nil), reg,
		WithProfileResolver(&openProfileResolver{host: "host.example.com"}),
		WithRemoteLauncher(&fakeRemoteLauncher{}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "ssh", "profileId": "ssh:test:1",
	})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("open: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "open result (real socket)")
	var got struct {
		ShellIntegrationReason string `json:"shellIntegrationReason"`
		DesiredMode            string `json:"desiredMode"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ShellIntegrationReason != "no-secure-temp" {
		t.Errorf("shellIntegrationReason = %q, want %q", got.ShellIntegrationReason, "no-secure-temp")
	}
	// The resolver stamped no mode (openProfileResolver builds a bare
	// config), so the ack must report the default: script (N3 — wrap and
	// install automatically). A direct-host or profile-less open gets the
	// hardcoded default, exactly like a profile that resolves to nothing.
	if got.DesiredMode != "script" {
		t.Errorf("desiredMode = %q, want %q (default when the resolver stamps none)", got.DesiredMode, "script")
	}

	// The launcher the transport option attached must have reached the
	// registry's ConnectOptions — the exact chain that was dead before.
	cfg := &ssh.ConnectConfig{}
	for _, o := range factory.opts {
		o(cfg)
	}
	if cfg.RemoteLauncher == nil {
		t.Error("WithRemoteLauncher did not reach the ConnectConfig: RemoteLauncher is nil in the options the SSH factory received")
	}
}

// fakeRemoteLauncher is the transport-side double: the transport must not
// care which launcher it carries, only that it carries one.
type fakeRemoteLauncher struct{}

func (fakeRemoteLauncher) StartCommand(ssh.ShellKind, ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	return "", ssh.ReasonNone, false
}

// ── tunnel.open / tunnel.stop ──────────────────────────────────────────────

// The DTO's own conformance: field tags, pointer-as-null for stopReason and
// error, enum spelling. The cases that matter: the running record (both
// nullable fields must marshal to null, never be omitted — the schema
// requires them) and the stopped records (the reason and the error that
// explain the stop).
func TestTunnelOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "tunnel.open.schema.json")

	userStop := string(tunnel.StopReasonUser)
	connLost := string(tunnel.StopReasonConnectionLost)
	errMsg := "ssh: tunnel connection lost"

	cases := map[string]tunnelRecord{
		"running": {
			ID:            "ab12",
			Direction:     string(tunnel.DirectionLocal),
			RequestedBind: tunnelBind{Host: "127.0.0.1", Port: 0},
			ActualBind:    tunnelBind{Host: "127.0.0.1", Port: 43210},
			Destination:   "db.internal:5432",
			Scope:         "tab:1",
			State:         string(tunnel.StateRunning),
		},
		"running-remote-with-caveat": {
			ID:            "ab12",
			Direction:     string(tunnel.DirectionRemote),
			RequestedBind: tunnelBind{Host: "0.0.0.0", Port: 0},
			ActualBind:    tunnelBind{Host: "0.0.0.0", Port: 43210},
			Destination:   "db.internal:5432",
			Scope:         "tab:1",
			Caveat:        "bind address 0.0.0.0 requested but not verified: the server may have bound a different address (GatewayPorts), so a URL built from this forward may only work on the server",
			State:         string(tunnel.StateRunning),
		},
		"stopped-user": {
			ID:            "ab12",
			Direction:     string(tunnel.DirectionLocal),
			RequestedBind: tunnelBind{Host: "127.0.0.1", Port: 8080},
			ActualBind:    tunnelBind{Host: "127.0.0.1", Port: 8080},
			Destination:   "db.internal:5432",
			Scope:         "tab:1",
			State:         string(tunnel.StateStopped),
			StopReason:    &userStop,
		},
		"stopped-connection-lost": {
			ID:            "ab12",
			Direction:     string(tunnel.DirectionLocal),
			RequestedBind: tunnelBind{Host: "127.0.0.1", Port: 0},
			ActualBind:    tunnelBind{Host: "127.0.0.1", Port: 43210},
			Destination:   "db.internal:5432",
			Scope:         "tab:1",
			State:         string(tunnel.StateStopped),
			StopReason:    &connLost,
			Error:         &errMsg,
		},
	}

	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "tunnel.open DTO")
		})
	}
}

func TestTunnelStop_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "tunnel.stop.schema.json")
	userStop := string(tunnel.StopReasonUser)
	rec := tunnelRecord{
		ID:            "ab12",
		Direction:     string(tunnel.DirectionLocal),
		RequestedBind: tunnelBind{Host: "127.0.0.1", Port: 0},
		ActualBind:    tunnelBind{Host: "127.0.0.1", Port: 43210},
		Destination:   "db.internal:5432",
		Scope:         "tab:1",
		Caveat:        "",
		State:         string(tunnel.StateStopped),
		StopReason:    &userStop,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "tunnel.stop DTO")
}

// The real methods through the real socket, with the real connector against
// the in-process SSH server: the result bytes are the handler's own, and
// nothing here names a field, so nothing here can omit one.
func TestTunnel_OverTheWireConformsToContract(t *testing.T) {
	openSchema := loadSchema(t, "tunnel.open.schema.json")
	stopSchema := loadSchema(t, "tunnel.stop.schema.json")

	h := newTunnelHarness(t, nil)
	defer h.stop()
	target := startEchoTarget(t)
	conn := connectWS(t, h.ws)

	openResp := tunnelCall(t, conn, "tunnel.open", map[string]any{
		"profileId":   "ssh:p1:1",
		"port":        0,
		"destination": target,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("tunnel.open: %+v", openResp.Error)
	}
	validateJSON(t, openSchema, openResp.Result, "tunnel.open result")

	var rec tunnelRecord
	if err := json.Unmarshal(openResp.Result, &rec); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	// A local open carries no bind caveat — the field is present on the
	// wire (the schema requires it) and empty. The remote strategy's
	// non-empty caveat is covered by the DTO conformance case above.
	if rec.Caveat != "" {
		t.Fatalf("local tunnel caveat = %q, want empty", rec.Caveat)
	}

	stopResp := tunnelCall(t, conn, "tunnel.stop", map[string]any{"id": rec.ID}, 2)
	if stopResp.Error != nil {
		t.Fatalf("tunnel.stop: %+v", stopResp.Error)
	}
	validateJSON(t, stopSchema, stopResp.Result, "tunnel.stop result")
}

// ── shell.integrate ────────────────────────────────────────────────────────

// The DTO's own conformance: field tags and wire names, so the Go struct
// marshals to something the schema accepts.
func TestShellIntegrate_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.integrate.schema.json")
	raw, err := json.Marshal(shellIntegrateResult{
		Wrapper:    "saved=$(stty -g); NOCX_IB_SRC=$(mktemp \"${TMPDIR:-/tmp}/nocx-ib.XXXXXX\" 2>/dev/null) && stty raw -echo && printf '\\033]1337;NOCX_IB_READY\\a' && sed -n '/^NOCX_IB_EOF$/q;p' > \"$NOCX_IB_SRC\"; stty \"$saved\"; rm -f \"$NOCX_IB_SRC\" 2>/dev/null",
		Payload:    "# nocx in-band integration — dispatcher (POSIX sh).\n# nocx-ib-complete\n",
		Terminator: "NOCX_IB_EOF",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "shell.integrate DTO")
}

// The real method through the real socket, backed by the REAL
// *shellintegration.Impl — no fake, because the plan the renderer streams
// must be the plan the product builds. Nothing here names a field, so
// nothing here can omit one; the schema's additionalProperties:false plus
// required makes the key set exact in both directions.
func TestShellIntegrate_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.integrate.schema.json")
	ctx := context.Background()

	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithInBandBootstrapper(shellintegration.New(nil)),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Open a local session the way the renderer does: the id that comes
	// back is the server-authoritative one (AD-7) shell.integrate must
	// accept.
	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open: %+v", openResp.Error)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &opened); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if opened.SessionID == "" {
		t.Fatal("open result carried no sessionId")
	}

	resp := vaultCall(t, conn, "shell.integrate", map[string]any{"sessionId": opened.SessionID}, 2)
	if resp.Error != nil {
		t.Fatalf("shell.integrate: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.integrate result (real socket)")

	// The plan is the real plan: decode and name the parts the renderer
	// executes, not fields the test built.
	var got struct {
		Wrapper    string `json:"wrapper"`
		Payload    string `json:"payload"`
		Terminator string `json:"terminator"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode shell.integrate result: %v", err)
	}
	if !strings.Contains(got.Wrapper, "saved=$(stty -g)") || !strings.Contains(got.Wrapper, `stty "$saved"`) {
		t.Errorf("wrapper lacks the exact save/restore fence: %q", got.Wrapper)
	}
	if !strings.Contains(got.Payload, "# nocx-ib-complete") {
		t.Errorf("payload lacks the completion marker: %q", got.Payload)
	}
	if got.Terminator != "NOCX_IB_EOF" {
		t.Errorf("terminator = %q, want NOCX_IB_EOF", got.Terminator)
	}

	// A well-formed session id the registry does not know is refused:
	// session identity is server-authoritative (AD-7).
	unknown := vaultCall(t, conn, "shell.integrate", map[string]any{
		"sessionId": "0123456789abcdef0123456789abcdef",
	}, 3)
	if unknown.Error == nil || unknown.Error.Code != -32602 {
		t.Fatalf("unknown sessionId: got %+v, want -32602", unknown.Error)
	}

	// Missing params are rejected before anything is built.
	missing := vaultCall(t, conn, "shell.integrate", map[string]any{}, 4)
	if missing.Error == nil || missing.Error.Code != -32602 {
		t.Fatalf("missing sessionId: got %+v, want -32602", missing.Error)
	}
}

// Not wired: shell.integrate must fail closed (-32603), never build a plan
// without the capability.
func TestShellIntegrate_NotWiredFailsClosed(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open: %+v", openResp.Error)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &opened); err != nil {
		t.Fatalf("decode open result: %v", err)
	}

	resp := vaultCall(t, conn, "shell.integrate", map[string]any{"sessionId": opened.SessionID}, 2)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("unwired shell.integrate: got %+v, want -32603", resp.Error)
	}
}

// ── vault.unlockRequest notification contract ─────────────────────────
// The notification is server→client, so there is no result shape — the
// params ARE the contract. Validated against the schema both as a DTO and
// off the real socket.

func TestVaultUnlockRequest_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.unlockRequest.schema.json")
	raw, err := json.Marshal(map[string]any{
		"requestId": "abc123",
		"reason":    "history needs the content key",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "vault.unlockRequest params DTO")
}

func TestVaultUnlockRequest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "vault.unlockRequest.schema.json")
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Verify connection is registered.
	resp := vaultCall(t, conn, "vault.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("vault.status failed: %s", resp.Error.Message)
	}

	// Request an unlock; this sends the real notification over the socket.
	go func() { _ = ws.RequestUnlock(ctx, "history needs the content key") }()

	// Read the notification off the real socket.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}

	var notif struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "vault.unlockRequest" {
		t.Fatalf("expected vault.unlockRequest, got %q", notif.Method)
	}
	validateJSON(t, schema, notif.Params, "vault.unlockRequest params over the wire")
}

// ── connections.passwordRequest notification contract ─────────────────
// Same shape as vault.unlockRequest: the notification is server→client,
// so the params ARE the contract. The prompt must name which password it
// is asking for (nocx-s8jn), so connection/user/host are required fields.

func TestConnectionsPasswordRequest_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.passwordRequest.schema.json")
	raw, err := json.Marshal(map[string]any{
		"requestId":  "abc123",
		"connection": "prod-web",
		"user":       "deploy",
		"host":       "web.example.com",
		"reason":     "no password is stored for this connection",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "connections.passwordRequest params DTO")
}

func TestConnectionsPasswordRequest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "connections.passwordRequest.schema.json")
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(newFakeVaultLifecycle()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	// Request a connection password; this sends the real notification over
	// the socket.
	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestConnectionPassword(ctx, ssh.PasswordRequest{
			Connection: "prod-web",
			User:       "deploy",
			Host:       "web.example.com",
			Reason:     "no password is stored for this connection",
		})
		done <- err
	}()

	// Read the notification off the real socket.
	_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}

	var notif struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "connections.passwordRequest" {
		t.Fatalf("expected connections.passwordRequest, got %q", notif.Method)
	}
	validateJSON(t, schema, notif.Params, "connections.passwordRequest params over the wire")

	// Resolve so the asker does not leak a pending request.
	var params struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(notif.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	resp := vaultCall(t, conn, "connections.passwordResolved", map[string]any{
		"requestId": params.RequestID,
		"outcome":   "cancelled",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("passwordResolved error: %s", resp.Error.Message)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPasswordPromptCancelled) {
			t.Fatalf("RequestConnectionPassword = %v, want ErrPasswordPromptCancelled", err)
		}
	case <-time.After(wantWithin):
		t.Fatal("RequestConnectionPassword did not resolve")
	}
}

// ── shell.launcherCommand ──────────────────────────────────────────────────

func TestShellLauncherCommand_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.launcherCommand.schema.json")

	// Bootstrap: a staged path returned, mode bootstrap, reason null.
	path := "'/home/u/.nocx/run/launcher-123456'"
	raw, err := json.Marshal(shellLauncherCommandResult{
		Mode:          launcherModeBootstrap,
		EnvironmentID: "env-abc-123",
		LauncherPath:  &path,
		Reason:        nil,
	})
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	validateJSON(t, schema, raw, "shell.launcherCommand DTO (bootstrap)")

	// Installed: compact form, no path, no reason.
	raw, err = json.Marshal(shellLauncherCommandResult{
		Mode:          launcherModeInstalled,
		EnvironmentID: "env-def-456",
		LauncherPath:  nil,
		Reason:        nil,
	})
	if err != nil {
		t.Fatalf("marshal installed: %v", err)
	}
	validateJSON(t, schema, raw, "shell.launcherCommand DTO (installed)")

	// Every refusal the handler can state must satisfy the schema's enum;
	// a reason the renderer receives and the contract rejects is a refusal
	// that reaches the product as a decode failure.
	for _, reason := range []string{"remote-command", "oracle-failed", "unsupported", "stage-failed"} {
		r := reason
		raw, err = json.Marshal(shellLauncherCommandResult{
			Mode:          launcherModeRaw,
			EnvironmentID: "env-ghi-789",
			LauncherPath:  nil,
			Reason:        &r,
		})
		if err != nil {
			t.Fatalf("marshal refused %s: %v", reason, err)
		}
		validateJSON(t, schema, raw, "shell.launcherCommand DTO (refused: "+reason+")")
	}
}

// OverTheWire: the real handler, the real RemoteLauncher, the real stager
// and a stub resolver that records the oracle argv — the bytes the far shell
// runs must be the bytes the product builds, and the typed line must be what
// the oracle answers about (nocx-c5az).
func TestShellLauncherCommand_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.launcherCommand.schema.json")
	ctx := context.Background()

	home := t.TempDir()
	resolver := newLauncherTestResolver()
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteLauncher(&realRemoteLauncher{}),
		WithLauncherStager(shellintegration.NewLauncherStager(log.NewSlogAdapter(nil), home)),
		WithSSHConfigResolver(resolver, "/nonexistent/config"),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	resp := vaultCall(t, conn, "shell.launcherCommand", map[string]any{
		"sessionId":  sid,
		"oracleArgv": []string{"ssh", "-G", "-p", "2222", "testhost"},
	}, 2)
	if resp.Error != nil {
		t.Fatalf("shell.launcherCommand: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.launcherCommand result (real socket)")

	var got shellLauncherCommandResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode shell.launcherCommand result: %v", err)
	}
	if got.Mode != launcherModeBootstrap {
		t.Errorf("mode = %q, want bootstrap (no installed fact on a fresh store)", got.Mode)
	}
	if got.LauncherPath == nil || *got.LauncherPath == "" {
		t.Fatal("launcherPath is nil or empty; the real launcher must be staged and named")
	}
	if got.Reason != nil {
		t.Errorf("reason = %q, want nil when a path is present", *got.Reason)
	}
	// The environment id is fresh per attempt and never the tab session id.
	if got.EnvironmentID == "" {
		t.Error("environmentId is empty; the planner must mint one per attempt")
	}
	if got.EnvironmentID == sid {
		t.Error("environmentId equals the tab session id; two attempts would be indistinguishable")
	}
	// The path is shell-quoted: the renderer splices it into a shell line.
	if !strings.HasPrefix(*got.LauncherPath, "'") || !strings.HasSuffix(*got.LauncherPath, "'") {
		t.Errorf("launcherPath not shell-quoted: %q", *got.LauncherPath)
	}

	// The oracle saw the exact typed argv, options included.
	if len(resolver.lastArgv) != 5 || resolver.lastArgv[0] != "ssh" || resolver.lastArgv[1] != "-G" ||
		resolver.lastArgv[2] != "-p" || resolver.lastArgv[3] != "2222" || resolver.lastArgv[4] != "testhost" {
		t.Errorf("oracle argv = %v, want [ssh -G -p 2222 testhost] as typed", resolver.lastArgv)
	}

	// The response carries a PATH, not a payload. This is the whole fix:
	// the launcher is ~35 KB and the line the renderer types has only the
	// tty, whose canonical buffer is 4096 bytes. A response that grew back
	// into a payload would truncate on the wire into the shell again.
	if n := len(resp.Result); n > 512 {
		t.Errorf("result is %d bytes; shell.launcherCommand must return a path, not the launcher", n)
	}

	// And the file it names holds the real launcher, byte for byte, with
	// the freshly minted environment id embedded (NOCX_ENVIRONMENT_ID).
	staged := strings.Trim(*got.LauncherPath, "'")
	body, err := os.ReadFile(staged) // #nosec G304 — path came from our own stager.
	if err != nil {
		t.Fatalf("read staged launcher at %s: %v", staged, err)
	}
	wantLauncher, _, ok := shellintegration.NewRemoteLauncher().StartCommand(
		shellintegration.ShellAuto,
		shellintegration.LaunchOptions{
			SessionID:     sid,
			Enhanced:      true,
			EnvironmentID: got.EnvironmentID,
		},
	)
	if !ok {
		t.Fatal("the real RemoteLauncher refused ShellAuto")
	}
	if string(body) != wantLauncher {
		t.Errorf("staged launcher differs from the product's: got %d bytes, want %d", len(body), len(wantLauncher))
	}
	// The equality above is the embed proof: a handler that passed a
	// different or empty environment id to the launcher would produce a
	// different command, and the comparison would fail.

	// A second attempt mints a SECOND environment id: the renderer's
	// tracker can tell a stale passport from a live one.
	second := launcherCommandCall(t, conn, sid, 3)
	if second.EnvironmentID == got.EnvironmentID {
		t.Error("two attempts minted the same environmentId; a stale passport would be accepted")
	}

	// Unknown sessionId is refused (AD-7).
	unknown := vaultCall(t, conn, "shell.launcherCommand", map[string]any{
		"sessionId":  "0123456789abcdef0123456789abcdef",
		"oracleArgv": []string{"ssh", "-G", "testhost"},
	}, 4)
	if unknown.Error == nil || unknown.Error.Code != -32602 {
		t.Fatalf("unknown sessionId: got %+v, want -32602", unknown.Error)
	}

	// Missing or malformed params are rejected.
	missing := vaultCall(t, conn, "shell.launcherCommand", map[string]any{}, 5)
	if missing.Error == nil || missing.Error.Code != -32602 {
		t.Fatalf("missing params: got %+v, want -32602", missing.Error)
	}
	badArgv := vaultCall(t, conn, "shell.launcherCommand", map[string]any{
		"sessionId":  sid,
		"oracleArgv": []string{"scp", "-G", "testhost"},
	}, 6)
	if badArgv.Error == nil || badArgv.Error.Code != -32602 {
		t.Fatalf("malformed oracleArgv: got %+v, want -32602", badArgv.Error)
	}
}

// ── shell.environmentObserved ───────────────────────────────────────────────

func TestShellEnvironmentObserved_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.environmentObserved.schema.json")

	raw, err := json.Marshal(environmentObservedResult{Processed: true, FactUpdated: true})
	if err != nil {
		t.Fatalf("marshal recorded: %v", err)
	}
	validateJSON(t, schema, raw, "shell.environmentObserved DTO (recorded)")

	raw, err = json.Marshal(environmentObservedResult{Processed: false, FactUpdated: false})
	if err != nil {
		t.Fatalf("marshal unknown: %v", err)
	}
	validateJSON(t, schema, raw, "shell.environmentObserved DTO (unknown id)")
}

// OverTheWire: the real handler, the real launcher and stager, a stub
// resolver, and a real fact store — an accepted passport for a minted
// attempt records the fact, and a duplicate report is a no-op.
func TestShellEnvironmentObserved_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.environmentObserved.schema.json")
	ctx := context.Background()

	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteLauncher(&realRemoteLauncher{}),
		WithLauncherStager(shellintegration.NewLauncherStager(log.NewSlogAdapter(nil), t.TempDir())),
		WithSSHConfigResolver(newLauncherTestResolver(), "/nonexistent/config"),
		WithInstalledFactStore(ssh.NewInstalledFactStore(
			log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "installed-facts.json")),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	first := launcherCommandCall(t, conn, sid, 2)
	if first.Mode != launcherModeBootstrap {
		t.Fatalf("first attempt mode = %q, want bootstrap", first.Mode)
	}

	passport := map[string]any{
		"protocolVersion":     "1",
		"environmentId":       first.EnvironmentID,
		"parentEnvironmentId": "-",
		"scriptVersion":       "0.6.0",
		"tier":                "enhanced",
		"generation":          "v10",
	}
	resp := vaultCall(t, conn, "shell.environmentObserved", map[string]any{
		"environmentId": first.EnvironmentID,
		"passport":      passport,
	}, 3)
	if resp.Error != nil {
		t.Fatalf("shell.environmentObserved: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.environmentObserved result (real socket)")
	var got environmentObservedResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Processed || !got.FactUpdated {
		t.Errorf("first observation = %+v, want processed+factUpdated", got)
	}

	// The next launcherCommand takes the compact line: the fact was written.
	second := launcherCommandCall(t, conn, sid, 4)
	if second.Mode != launcherModeInstalled {
		t.Errorf("second attempt mode = %q, want installed after the passport was accepted", second.Mode)
	}

	// A duplicate observation satisfies the schema and changes nothing.
	dup := vaultCall(t, conn, "shell.environmentObserved", map[string]any{
		"environmentId": first.EnvironmentID,
		"passport":      passport,
	}, 5)
	if dup.Error != nil {
		t.Fatalf("duplicate observation: %+v", dup.Error)
	}
	validateJSON(t, schema, dup.Result, "shell.environmentObserved duplicate (real socket)")

	// An unknown id is processed=false, and still satisfies the schema.
	unknown := vaultCall(t, conn, "shell.environmentObserved", map[string]any{
		"environmentId": "00000000000000000000000000000000",
		"passport":      nil,
	}, 6)
	if unknown.Error != nil {
		t.Fatalf("unknown-id observation: %+v", unknown.Error)
	}
	validateJSON(t, schema, unknown.Result, "shell.environmentObserved unknown-id (real socket)")

	// Missing params are rejected.
	missing := vaultCall(t, conn, "shell.environmentObserved", map[string]any{}, 7)
	if missing.Error == nil || missing.Error.Code != -32602 {
		t.Fatalf("missing params: got %+v, want -32602", missing.Error)
	}
}

// No resolver wired: the oracle is missing, so the rewrite is refused with
// "oracle-failed" (nocx-qwhp) rather than a silent bypass — a rewrite built
// without the oracle's answer is a guess. The renderer sends the original
// line.
func TestShellLauncherCommand_NoResolverRefuses(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	got := launcherCommandCall(t, conn, sid, 2)
	if got.Mode != launcherModeRaw {
		t.Errorf("mode = %q, want raw", got.Mode)
	}
	if got.LauncherPath != nil {
		t.Errorf("launcherPath = %q, want nil when the oracle is missing", *got.LauncherPath)
	}
	if got.Reason == nil || *got.Reason != "oracle-failed" {
		t.Errorf("reason = %v, want oracle-failed", got.Reason)
	}
}

// Launcher and stager not wired: with a working oracle the bootstrap path
// has nowhere to put the payload, so the handler refuses with "unsupported".
func TestShellLauncherCommand_LauncherNotWiredRefuses(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithSSHConfigResolver(newLauncherTestResolver(), "/nonexistent/config"),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	got := launcherCommandCall(t, conn, sid, 2)
	if got.LauncherPath != nil {
		t.Errorf("launcherPath = %q, want nil when not wired", *got.LauncherPath)
	}
	if got.Reason == nil || *got.Reason != "unsupported" {
		t.Errorf("reason = %v, want unsupported", got.Reason)
	}
}

// The launcher builds but cannot be staged: without a place the LOCAL shell
// can read, there is no rewrite to make. The handler says so rather than
// returning a path that does not exist, and the renderer sends the line the
// user typed (ADR-0004 §1). This is the failure path that did not exist
// while the payload travelled inline.
func TestShellLauncherCommand_StageFailureRefuses(t *testing.T) {
	ctx := context.Background()

	// A regular file where the staging directory must go.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".nocx"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteLauncher(&realRemoteLauncher{}),
		WithLauncherStager(shellintegration.NewLauncherStager(log.NewSlogAdapter(nil), home)),
		WithSSHConfigResolver(newLauncherTestResolver(), "/nonexistent/config"),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	got := launcherCommandCall(t, conn, sid, 2)
	if got.LauncherPath != nil {
		t.Errorf("launcherPath = %q, want nil when staging failed", *got.LauncherPath)
	}
	if got.Reason == nil || *got.Reason != "stage-failed" {
		t.Errorf("reason = %v, want stage-failed", got.Reason)
	}
}

// A stager without a launcher, and a launcher without a stager, are both
// "we cannot build a rewrite" — neither may answer with a path.
func TestShellLauncherCommand_StagerWithoutLauncherRefuses(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithLauncherStager(shellintegration.NewLauncherStager(log.NewSlogAdapter(nil), t.TempDir())),
		WithSSHConfigResolver(newLauncherTestResolver(), "/nonexistent/config"),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionForLauncher(t, conn)
	got := launcherCommandCall(t, conn, sid, 2)
	if got.LauncherPath != nil {
		t.Errorf("launcherPath = %q, want nil without a launcher", *got.LauncherPath)
	}
	if got.Reason == nil || *got.Reason != "unsupported" {
		t.Errorf("reason = %v, want unsupported", got.Reason)
	}
}

// openSessionForLauncher opens a local session and returns its id.
func openSessionForLauncher(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	openResp := vaultCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	if openResp.Error != nil {
		t.Fatalf("open: %+v", openResp.Error)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(openResp.Result, &opened); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	return opened.SessionID
}

// launcherCommandCall makes one shell.launcherCommand call and decodes it.
func launcherCommandCall(t *testing.T, conn *websocket.Conn, sid string, id int) shellLauncherCommandResult {
	t.Helper()
	resp := vaultCall(t, conn, "shell.launcherCommand", map[string]any{
		"sessionId":  sid,
		"oracleArgv": []string{"ssh", "-G", "testhost"},
	}, id)
	if resp.Error != nil {
		t.Fatalf("shell.launcherCommand: %+v", resp.Error)
	}
	var got shellLauncherCommandResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// realRemoteLauncher is the production adapter in miniature: it returns
// the real launcher command so the over-the-wire test exercises the real
// payload the renderer will append.
type realRemoteLauncher struct{}

func (realRemoteLauncher) StartCommand(kind ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	// Use the real shellintegration launcher, adapted through the ssh types.
	// The composition root adapts shellintegration.RemoteLauncher to
	// ssh.RemoteLauncher; for the test we call the real one directly.
	l := shellintegration.NewRemoteLauncher()
	// Map the ssh ShellKind to the shellintegration ShellKind.
	var sk shellintegration.ShellKind
	switch kind {
	case ssh.ShellBash:
		sk = shellintegration.ShellBash
	case ssh.ShellZsh:
		sk = shellintegration.ShellZsh
	case ssh.ShellUnknown:
		sk = shellintegration.ShellUnknown
	case ssh.ShellAuto:
		sk = shellintegration.ShellAuto
	default:
		return "", ssh.ReasonUnsupportedShell, false
	}
	lo := shellintegration.LaunchOptions{
		SessionID:     opts.SessionID,
		Enhanced:      opts.Enhanced,
		EnvironmentID: opts.EnvironmentID,
	}
	cmd, reason, ok := l.StartCommand(sk, lo)
	return cmd, ssh.RefusalReason(reason), ok
}

// ── shell.footprint.status ────────────────────────────────────────────────

func TestShellFootprintStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.footprint.status.schema.json")

	removable := "p_01"
	raw, err := json.Marshal(shellFootprintStatusResult{Destinations: []shellFootprintDestination{
		{
			Identity:           "pi@192.168.0.93:22",
			Generation:         "v10",
			Path:               footprintPath,
			ProtocolVersion:    "1",
			ScriptVersion:      "0.6.0",
			LastObservedAt:     time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
			RemovableProfileID: &removable,
		},
	}})
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	validateJSON(t, schema, raw, "shell.footprint.status DTO (populated)")

	// A destination with no saved connection: removableProfileId null.
	raw, err = json.Marshal(shellFootprintStatusResult{Destinations: []shellFootprintDestination{
		{
			Identity:        "root@10.0.0.7:22",
			Generation:      "v10",
			Path:            footprintPath,
			ProtocolVersion: "1",
			ScriptVersion:   "0.6.0",
			LastObservedAt:  time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		},
	}})
	if err != nil {
		t.Fatalf("marshal non-removable: %v", err)
	}
	validateJSON(t, schema, raw, "shell.footprint.status DTO (no saved connection)")

	// Empty: destinations must marshal as [] rather than null.
	raw, err = json.Marshal(shellFootprintStatusResult{Destinations: []shellFootprintDestination{}})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	validateJSON(t, schema, raw, "shell.footprint.status DTO (empty)")
}

// OverTheWire: the real handler, a real fact store holding one
// profile-removable fact and one direct-host fact, and the stub oracle that
// resolves the saved profile to the first destination's identity.
func TestShellFootprintStatus_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.footprint.status.schema.json")
	ctx := context.Background()

	resolver := newLauncherTestResolver()
	resolver.add("pi@192.168.0.93", ssh.HostConfig{User: "pi", HostName: "192.168.0.93", Port: 22})
	facts := ssh.NewInstalledFactStore(
		log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "installed-facts.json")
	if err := facts.Record(ssh.InstalledFact{
		Identity: "pi@192.168.0.93:22", Protocol: "1", ScriptVersion: "0.6.0",
		Generation: "v10", ObservedAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := facts.Record(ssh.InstalledFact{
		Identity: "root@10.0.0.7:22", Protocol: "1", ScriptVersion: "0.5.2",
		Generation: "v9", ObservedAt: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithInstalledFactStore(facts),
		WithSSHConfigResolver(resolver, "/nonexistent/config"),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
		WithProfileRepository(&footprintTestProfileRepo{profiles: []profile.SSHProfile{{
			Base:    profile.Base{ID: "p_01"},
			Options: profile.StoredSSHProfileOptions{Host: "pi@192.168.0.93"},
		}}}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.status", map[string]any{}, 2)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.status: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.footprint.status result (real socket)")

	var got shellFootprintStatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Destinations) != 2 {
		t.Fatalf("destinations = %d, want 2", len(got.Destinations))
	}
	if got.Destinations[0].Identity != "pi@192.168.0.93:22" ||
		got.Destinations[0].RemovableProfileID == nil ||
		*got.Destinations[0].RemovableProfileID != "p_01" {
		t.Errorf("profile destination = %+v, want identity pi@192.168.0.93:22 removable via p_01",
			got.Destinations[0])
	}
	if got.Destinations[1].RemovableProfileID != nil {
		t.Errorf("direct-host destination removableProfileId = %v, want null", *got.Destinations[1].RemovableProfileID)
	}
}

// ── shell.footprint.uninstall ────────────────────────────────────────────

func TestShellFootprintUninstall_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.footprint.uninstall.schema.json")

	raw, err := json.Marshal(shellFootprintUninstallResult{
		Removed:   []string{"integration/v10/nocx.zsh", "integration/v10/nocx.posix", "manifest.json"},
		Conflicts: []string{"integration/v10/nocx.bash"},
	})
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	validateJSON(t, schema, raw, "shell.footprint.uninstall DTO (populated)")

	raw, err = json.Marshal(shellFootprintUninstallResult{Removed: []string{}, Conflicts: []string{}})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	validateJSON(t, schema, raw, "shell.footprint.uninstall DTO (nothing to do)")
}

// OverTheWire: the real handler drives a recording capability with the
// resolved profile config and sends the real result off the real socket —
// a conflict is reported as data, not swallowed as an error.
func TestShellFootprintUninstall_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.footprint.uninstall.schema.json")
	ctx := context.Background()

	rec := &recordingUninstaller{
		removed:   []string{"integration/v10/nocx.zsh", "manifest.json"},
		conflicts: []string{"integration/v10/nocx.bash"},
	}
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteUninstaller(rec),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.uninstall", map[string]any{"profileId": "p_01"}, 3)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.uninstall: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "shell.footprint.uninstall result (real socket)")

	var got shellFootprintUninstallResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Removed) != 2 || got.Removed[0] != "integration/v10/nocx.zsh" || got.Removed[1] != "manifest.json" {
		t.Errorf("removed = %v, want the capability's list verbatim", got.Removed)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != "integration/v10/nocx.bash" {
		t.Errorf("conflicts = %v, want the capability's list verbatim", got.Conflicts)
	}
}

// ── files.* ──────────────────────────────────────────────────────────────
//
// The seven wire shapes of the file-tree control plane (fm-w8): six
// methods plus the files.changed notification, which gets the same three
// checks as a method because an unsolicited notification is exactly where
// an addressing defect hides (spec §5.3).

func TestFilesOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.open.schema.json")

	ep := "v1:attestation"
	cases := map[string]filesOpenResult{
		// The state every local tab is actually in: endpointId must be
		// null — never absent, never "" — and the root carries the
		// inferred label when the caller sent no rootPath.
		"local": {
			BindingID:  "ab12cd34",
			EndpointID: nil,
			Root: filesRootResult{
				Path:           "/home/dev",
				Display:        "~/",
				Inferred:       true,
				InferredReason: "no verified working directory — using home",
			},
		},
		// A remote binding attests its resolved destination (D4); the
		// SFTP wave stamps it here.
		"remote": {
			BindingID:  "ef56",
			EndpointID: &ep,
			Root: filesRootResult{
				Path:    "/home/deploy",
				Display: "/home/deploy",
			},
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.open DTO")
		})
	}
}

func TestFilesOpen_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.open.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()

	resp := jsonrpcCallWithID(t, e.conn, "files.open", map[string]any{
		"sessionId": sid,
		"rootPath":  dir,
	}, 2)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("files.open: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.open result (real socket)")

	var got filesOpenResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BindingID == "" {
		t.Error("bindingId is empty")
	}
	if got.EndpointID != nil {
		t.Errorf("endpointId = %v, want null for a local binding", *got.EndpointID)
	}
	if got.Root.Path != dir {
		t.Errorf("root.path = %q, want %q", got.Root.Path, dir)
	}
	if got.Root.Inferred {
		t.Error("root is inferred although rootPath was given and usable")
	}
}

func TestFilesList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.list.schema.json")

	cases := map[string]any{
		// The normal branch: an empty directory is [] and a symlink
		// carries its target. The unreadable entry collapses to "other"
		// (wireKind) — the schema's enum has no "unreadable", and its
		// open/expand row is exactly "other"'s.
		"ok": filesListOK{
			State:     "ok",
			Path:      "/tmp/panel",
			Canonical: "/private/tmp/panel",
			Entries: []filesListEntry{
				{Name: "a.txt", Path: "/tmp/panel/a.txt", Kind: "regular", Size: 3, ModTime: "2026-08-06T10:00:00Z", Mode: 0o644},
				{Name: "link", Path: "/tmp/panel/link", Kind: "symlink", LinkTarget: "a.txt", LinkKind: "regular", Size: 0, ModTime: "2026-08-06T10:00:00Z", Mode: 0o777},
				{Name: "gone", Path: "/tmp/panel/gone", Kind: wireKind(filesystem.KindUnreadable), Size: 0, ModTime: "2026-08-06T10:00:00Z", Mode: 0},
			},
			Offset:  0,
			Total:   3,
			HasMore: false,
			Rev:     "0123abcdef",
		},
		"empty directory": filesListOK{
			State: "ok", Path: "/tmp/empty", Canonical: "/tmp/empty",
			Entries: []filesListEntry{}, Offset: 0, Total: 0, HasMore: false, Rev: "rev",
		},
		"tooLarge": filesListTooLarge{
			State: "tooLarge", ObservedCount: 12000, Limit: 10000,
		},
		"timedOut": filesListTimedOut{
			State: "timedOut", Timeout: 10000,
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.list DTO")
		})
	}
}

func TestFilesList_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.list.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid,
		"path":      dir,
		"offset":    0,
		"limit":     10,
	}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.list: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.list result (real socket)")

	var got filesListOK
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != "ok" {
		t.Fatalf("state = %q, want ok", got.State)
	}
	if got.Entries == nil {
		t.Fatal("entries is null — an empty directory must be [], never null")
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (a.txt, sub)", len(got.Entries))
	}
	if got.Total != 2 || got.HasMore || got.Rev == "" {
		t.Errorf("total/hasMore/rev = %d/%v/%q, want 2/false/non-empty", got.Total, got.HasMore, got.Rev)
	}
}

func TestFilesRead_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.read.schema.json")
	cases := map[string]filesReadResult{
		"text": {
			Path: "/tmp/a.txt", Canonical: "/private/tmp/a.txt",
			Text: "hello", Size: 5, ModTime: "2026-08-06T10:00:00Z",
			Truncated: false, Binary: false, Lossy: false, Changed: false,
		},
		// A file that changed while being read, and a truncated one: the
		// booleans are the contract's honesty, each present.
		"changed while read": {
			Path: "/tmp/b.bin", Canonical: "/tmp/b.bin",
			Size: 4096, ModTime: "2026-08-06T10:00:00Z",
			Truncated: true, Binary: true, Lossy: false, Changed: true,
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.read DTO")
		})
	}
}

func TestFilesRead_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.read.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	content := "the quick brown fox"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.read", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "f.txt"),
		"maxBytes":  0,
	}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.read: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.read result (real socket)")

	var got filesReadResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Text != content {
		t.Errorf("text = %q, want %q", got.Text, content)
	}
	if got.Canonical == "" || got.Truncated || got.Binary || got.Lossy || got.Changed {
		t.Errorf("canonical/truncated/binary/lossy/changed = %q/%v/%v/%v/%v, want non-empty/false×4",
			got.Canonical, got.Truncated, got.Binary, got.Lossy, got.Changed)
	}
}

func TestFilesWatch_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.watch.schema.json")
	cases := map[string]filesWatchResult{
		"watching":            {Mode: "watching"},
		"designed polling":    {Mode: "polling"},
		"degraded to polling": {Mode: "polling", DegradedReason: "fsnotify unavailable"},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.watch DTO")
		})
	}
}

func TestFilesWatch_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.watch.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.watch", map[string]any{
		"bindingId": bid,
		"paths":     []string{dir},
	}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.watch: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.watch result (real socket)")

	var got filesWatchResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "polling" || got.DegradedReason != "" {
		t.Errorf("mode/degradedReason = %q/%q, want polling with NO reason: watching not being built"+
			" yet is not a degrade, and a reason lights the §5.5 badge permanently",
			got.Mode, got.DegradedReason)
	}
}

func TestFilesClose_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.close.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "files.close DTO")
}

func TestFilesClose_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.close.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.close", map[string]any{"bindingId": bid}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.close: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.close result (real socket)")

	// The binding is gone: a later call must refuse cleanly.
	after := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid, "path": dir, "offset": 0, "limit": 10,
	}, 4)
	var afterEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(after, &afterEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if afterEnv.Error == nil {
		t.Fatal("files.list succeeded on a closed binding")
	}
}

func TestFilesReveal_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.reveal.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "files.reveal DTO")
}

func TestFilesReveal_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.reveal.schema.json")
	revealer := &stubRevealer{}
	e := newFilesTestEnv(t, WithFilesRevealer(revealer))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.reveal", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "f.txt"),
	}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.reveal: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.reveal result (real socket)")
	if got := revealer.revealed(); len(got) != 1 {
		t.Errorf("revealed paths = %v, want exactly one", got)
	}
}

func TestFilesChanged_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.changed.schema.json")
	cases := map[string]filesChangedNotification{
		// rev present: the backend already knew the new digest (SFTP
		// polling necessarily computed it).
		"with rev": {
			JSONRPC: "2.0", Method: "files.changed",
			Params: filesChangedParams{BindingID: "ab12", Path: "/tmp/dir", Rev: "0123abcdef"},
		},
		// rev absent: a local event where nothing has been re-listed.
		"without rev": {
			JSONRPC: "2.0", Method: "files.changed",
			Params: filesChangedParams{BindingID: "ab12", Path: "/tmp/dir"},
		},
	}
	for name, n := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(n)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var frame struct {
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			validateJSON(t, schema, frame.Params, "files.changed DTO")
		})
	}
}

// The over-the-wire notification test: a real watch, a real change, the
// real socket. This is the one that catches an addressing defect — the
// notification must reach the connection that attached, and its params
// must satisfy the schema.
func TestFilesChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.changed.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)

	waitFor(t, "watch baseline", 5*time.Second, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.paths[dir] != ""
	})

	// No subscriber, then a change: the same shape a drop leaves, made
	// deterministic.
	e.ws.getRx(session.ID(sid)).setSubscriber(nil, nil)
	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "dirty path", 5*time.Second, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, ok := w.dirty[dir]
		return ok
	})

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	at := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 4)
	var atEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(at, &atEnv); err != nil {
		t.Fatalf("attach: unmarshal: %v", err)
	}
	if atEnv.Error != nil {
		t.Fatalf("attach: %+v", atEnv.Error)
	}

	raw := readNotification(t, connB, "files.changed", 5*time.Second)
	validateJSON(t, schema, raw, "files.changed params (real socket)")

	var params filesChangedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if params.BindingID != bid || params.Path != dir {
		t.Errorf("bindingId/path = %q/%q, want %q/%q", params.BindingID, params.Path, bid, dir)
	}
	if params.Rev == "" {
		t.Error("rev is absent — the poll loop knew the new digest and must carry it")
	}
}

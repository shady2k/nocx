package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/ssh"
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

// ── sandbox.status and open ─────────────────────────────────────────────

func TestSandboxStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.status.schema.json")
	for _, status := range []sandbox.Status{
		{Available: true, Backend: sandbox.BackendLandlock, ABI: 9},
		{Available: false, Backend: sandbox.BackendUnsupported, Reason: sandbox.ReasonUnsupportedPlatform, Detail: "unsupported"},
	} {
		raw, err := json.Marshal(sandboxStatusResponse{
			Available: status.Available,
			Backend:   status.Backend,
			Reason:    status.Reason,
			Detail:    status.Detail,
			ABI:       status.ABI,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		validateJSON(t, schema, raw, "sandbox.status DTO")
	}
}

func TestSandboxStatus_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sandbox.status.schema.json")
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock, ABI: 9}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	var response struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	raw := jsonrpcCall(t, conn, "sandbox.status", map[string]any{})
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	validateJSON(t, schema, response.Result, "sandbox.status result")
}

func TestOpen_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")
	workspace := t.TempDir()
	svc := &sandboxTestService{
		status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		policy: &sandbox.Policy{
			Workspace:     workspace,
			WritableRoots: []string{workspace},
		},
	}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	var response struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	raw := jsonrpcCall(t, conn, "open", sandboxOpenParams(workspace))
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	validateJSON(t, schema, response.Result, "sandboxed open result")

	raw = jsonrpcCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "enhanced": true})
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal ordinary response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("ordinary open error: %+v", response.Error)
	}
	validateJSON(t, schema, response.Result, "ordinary open result")
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
					Host:        "h:22",
					Algorithm:   "ssh-ed25519",
					Fingerprint: "SHA256:abc",
					Key:         "a2V5",
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
				Addr:        "host.example.com:22",
				KeyAlgo:     "ssh-ed25519",
				Fingerprint: "SHA256:abc",
				Key:         []byte("offered-key-blob"),
			},
		},
		{
			"host-key-changed",
			&ssh.ErrHostKeyMismatch{
				Addr:        "host.example.com:22",
				Fingerprint: "SHA256:new",
				Expected:    "SHA256:old",
				KeyAlgo:     "ecdsa-sha2-nistp256",
				Key:         []byte("new-key-blob"),
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

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/tunnel"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vaultreset"
	"github.com/shady2k/nocx/internal/workspace"
)

// contractDir holds the wire schemas. Deliberately not under internal/: the
// renderer generates its types from these files, and a contract only binds if
// it belongs to neither party.
const contractDir = "../../contracts"

func loadSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	// Cross-file $refs (e.g. git.stage → git.status#/$defs/status) resolve
	// against the referenced schema's $id, so every contract is registered
	// under its canonical $id URL before anything is compiled — otherwise
	// the compiler would try to fetch https://nocx.local/... from the
	// network. The schema under test keeps the plain-name convention every
	// existing call site uses. The single shared declaration this enables
	// is the point: one concept, one owner (contracts/README.md, AD-8).
	entries, err := os.ReadDir(contractDir)
	if err != nil {
		t.Fatalf("read contracts dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		f, openErr := os.Open(filepath.Join(contractDir, e.Name())) //nolint:gosec // test-only path under contracts/
		if openErr != nil {
			t.Fatalf("open %s: %v", e.Name(), openErr)
		}
		doc, parseErr := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if parseErr != nil {
			t.Fatalf("parse %s: %v", e.Name(), parseErr)
		}
		if addErr := c.AddResource("https://nocx.local/contracts/"+e.Name(), doc); addErr != nil {
			t.Fatalf("add %s: %v", e.Name(), addErr)
		}
	}
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

// validateJSONErr is the negative of validateJSON: it returns the schema
// validation error instead of failing the test, so a test can assert that a
// payload the DTO could marshal is REFUSED by the contract.
func validateJSONErr(s *jsonschema.Schema, raw []byte) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	return s.Validate(doc)
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
	h.ws.SetDialogService(&fakeDialogService{path: "/home/dev/.ssh/id_ed25519"})

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

// ── shell.openUrl (brief, nocx-hc0m) ────────────────────────────────────

// The DTO's own conformance: the result is the empty object, exactly like
// files.reveal — the browser either opened or the method failed.
func TestShellOpenUrl_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.openUrl.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "shell.openUrl DTO")
}

// The real method through the real socket, with the fake opener standing in
// for the Wails runtime: the opener receives exactly the URL the renderer
// asked to open, and the result satisfies the contract.
func TestShellOpenUrl_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "shell.openUrl.schema.json")
	h := newInventoryHarness(t)
	opener := &fakeUrlOpener{}
	h.ws.SetUrlOpener(opener)

	resp := jsonrpcCall(t, h.conn, "shell.openUrl", map[string]any{"url": "https://github.com/shady2k/nocx"})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("shell.openUrl: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "shell.openUrl result (real socket)")
	if got := opener.opened(); len(got) != 1 || got[0] != "https://github.com/shady2k/nocx" {
		t.Fatalf("opener received %v, want the one URL", got)
	}
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
		done := ledgerRow("0192f0aa-0000-7000-8000-000000000007", "git status", "/repo", "", content.EntrySuccess)
		done.StartedAt, done.EndedAt = &started, &ended
		done.Payload = content.ShellPayloadJSON(&exit)
		failed := ledgerRow("0192f0aa-0000-7000-8000-000000000006", "make", "/repo", "", content.EntryFailure)
		fake := &fakeHistoryDB{page: content.LedgerPage{
			Entries:   []content.LedgerEntrySummary{done, failed},
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

// The DTO's own conformance: the five fields the open ack always carries.
// shellIntegrationReason is deliberately NOT among them any more (nocx-dvql):
// it answered "is this session integrated" once, at open, and the two
// failures that matter most arrive after it. session.integrationChanged owns
// that question now, and additionalProperties:false is what makes the
// removal real rather than a comment — a handler that still sent the field
// would fail here. desiredMode (the resolved destination mode, nocx-mlm7) is
// present for every session, including local ones — a renderer that
// defaulted a missing field to "script" would show a raw tab as silently
// integrated. Each of the three mode values must marshal; the schema pins
// the enum. instanceId + sessionEpoch (nocx-3oupk) name the incarnation
// and are present on every ack, so the renderer always learns the identity
// the backend minted.
func TestOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "open.schema.json")

	for name, mode := range map[string]string{
		"script": "script",
		"raw":    "raw",
		"relay":  "relay",
	} {
		raw, err := json.Marshal(openResult{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 1,
			WorkspaceID:  string(workspace.Default),
			Cwd:          "~/work",
			DesiredMode:  mode,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		validateJSON(t, schema, raw, "open DTO ("+name+" mode)")
	}

	// The removed field is removed, not merely unset: an ack that still
	// carried it must fail the contract, or "removed" is a claim nothing
	// checks.
	stale, err := json.Marshal(map[string]any{
		"sessionId":              "0123456789abcdef0123456789abcdef",
		"instanceId":             "fedcba9876543210fedcba9876543210",
		"sessionEpoch":           1,
		"cwd":                    "~/work",
		"desiredMode":            "script",
		"shellIntegrationReason": "no-secure-temp",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := schema.Validate(mustUnmarshalAny(t, stale)); err == nil {
		t.Error("open schema still accepts shellIntegrationReason: the field was removed from the ack, and additionalProperties:false is what has to enforce it")
	}
}

// mustUnmarshalAny decodes JSON into the any-shaped value the schema
// validator takes. validateJSON does the same thing and fails the test on a
// violation; this is its inverse, for the case where the violation IS the
// assertion.
func mustUnmarshalAny(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return v
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
		SessionID   string `json:"sessionId"`
		DesiredMode string `json:"desiredMode"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The launcher refusal reaches the product, and it reaches it on the
	// notification rather than in this ack (nocx-dvql). Read AFTER the ack,
	// which is the ordering AD-7 requires: a status frame that overtook the
	// open result would address a session the renderer does not yet have.
	integration := loadSchema(t, "session.integrationChanged.schema.json")
	raw := readNotification(t, conn, "session.integrationChanged", wantWithin)
	validateJSON(t, integration, raw, "session.integrationChanged params (real socket, ssh refusal)")
	var status integrationChangedParams
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode integration status: %v", err)
	}
	if status.SessionID != got.SessionID {
		t.Errorf("integration status addresses %q, want the opened session %q", status.SessionID, got.SessionID)
	}
	if status.Status != IntegrationConventional || status.Reason != string(ssh.ReasonNoSecureTemp) {
		t.Errorf("integration status = %+v, want conventional/no-secure-temp", status)
	}
	// The profile pinned no shell, so the far dispatcher chose: "auto" is
	// what this side honestly knows, and inventing a name would be the
	// confidence the details surface exists to avoid.
	if status.Shell != string(ssh.ShellAuto) {
		t.Errorf("shell = %q, want %q for an unpinned remote", status.Shell, ssh.ShellAuto)
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

	// The server must have REGISTERED this connection before anything is
	// broadcast to it. connectWS returns when the client's handshake is done,
	// which is not the same instant: broadcastAsk reads s.conns, and on an
	// empty set it does not write — it returns ErrPasswordNoClientConnected
	// and the goroutine below swallows that into a channel nobody reads
	// before the socket read. So a lost race produced no notification at all
	// and this test sat out its full 30s deadline reporting a timeout, which
	// names the clock instead of the cause (nocx-8b47).
	//
	// waitForConns is the existing answer — password_requester_test.go calls
	// it before every one of its asks. These contract tests never did.
	waitForConns(t, ws, 1)

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
	cases := map[string]filesChangedParams{
		// rev present: the backend already knew the new digest (SFTP
		// polling necessarily computed it).
		"with rev": {
			BindingID: "ab12", Path: "/tmp/dir", Rev: "0123abcdef",
		},
		// rev absent: a local event where nothing has been re-listed.
		"without rev": {
			BindingID: "ab12", Path: "/tmp/dir",
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.changed DTO")
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

	waitFor(t, "watch baseline", wantWithin, func() bool {
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
	waitFor(t, "dirty path", wantWithin, func() bool {
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

	raw := readNotification(t, connB, "files.changed", wantWithin)
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

// ── git.* ───────────────────────────────────────────────────────────────

// The eleven wire shapes of the git-manager control plane (spec §5.2,
// §5.3): ten methods plus the git.changed notification, which gets the
// same three checks as a method because an unsolicited notification is
// exactly where an addressing defect hides. status is declared ONCE in
// contracts/git.status.schema.json and referenced with a cross-file $ref
// from the six other results that carry it; the loader registers every
// contract under its $id so those refs resolve.

func TestGitStatus_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.status.schema.json")
	three, one := 3, 1
	populated := gitStatusWire{
		Branch: "main", Detached: false, Unborn: false, Head: "abc1234",
		Upstream: "origin/main", Ahead: 1, Behind: 2,
		Staged:     []gitEntryWire{{Path: "staged.txt", X: "M", Y: "."}},
		Unstaged:   []gitEntryWire{{Path: "unstaged.txt", X: ".", Y: "M", Added: &three, Deleted: &one}},
		Conflicted: []gitEntryWire{},
		Total:      2, Completeness: "complete",
	}
	cases := map[string]gitStatusPollResult{
		"populated": {
			Status: populated, EnvState: "resolved",
		},
		// The counts are the brief's acceptance shape: +3 −1 rides the
		// entry, and its ABSENCE (untracked, binary, conflicted, bounded
		// out) is the wire's "no count exists" — never a 0 the panel
		// would have to second-guess.
		"counts are optional": {
			Status: gitStatusWire{
				Branch: "main", Head: "abc1234",
				Staged:     []gitEntryWire{{Path: "new.txt", X: "?", Y: "?"}},
				Unstaged:   []gitEntryWire{},
				Conflicted: []gitEntryWire{},
				Total:      1, Completeness: "complete",
			},
			EnvState: "resolved",
		},
		"unborn": {
			Status: gitStatusWire{
				Branch: "master", Unborn: true,
				Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
				Total: 0, Completeness: "complete",
			},
			EnvState: "resolved",
		},
		"detached": {
			Status: gitStatusWire{
				Branch: "", Detached: true, Head: "abc1234",
				Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
				Total: 0, Completeness: "complete",
			},
			EnvState: "resolved",
		},
		// The degraded case is the one D6 exists for, and envReason rides
		// exactly it — never a resolved result carrying a reason.
		"degraded env": {
			Status:    populated,
			EnvState:  "degraded",
			EnvReason: "the shell environment has not been resolved yet; the first commit will wait for it",
		},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.status DTO")
		})
	}
}

func TestGitOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.open.schema.json")
	st := func() *gitStatusWire {
		return &gitStatusWire{
			Branch: "main", Head: "abc1234",
			Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
			Total: 0, Completeness: "complete",
		}
	}
	cases := map[string]gitOpenResult{
		"ok": {
			State: "ok", BindingID: "ab12", Toplevel: "/tmp/repo",
			GitVersion: "2.55.0", EnvState: "resolved", Status: st(),
		},
		"degraded env": {
			State: "ok", BindingID: "ab12", Toplevel: "/tmp/repo",
			GitVersion: "2.55.0", EnvState: "degraded",
			EnvReason: "environment resolution failed: no login shell",
			Status:    st(),
		},
		"inline status failed": {
			State: "ok", BindingID: "ab12", Toplevel: "/tmp/repo",
			GitVersion: "2.55.0", EnvState: "resolved",
		},
		"not a repository":   {State: "notARepository"},
		"git unavailable":    {State: "gitUnavailable"},
		"git too old":        {State: "gitTooOld", GitVersion: "2.20.1"},
		"no cwd":             {State: "noCwd"},
		"remote unsupported": {State: "remoteUnsupported"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.open DTO")
		})
	}
}

func TestGitDiff_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.diff.schema.json")
	cases := map[string]gitDiffResult{
		"ok":       {State: "ok", Text: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n"},
		"tooLarge": {State: "tooLarge", Text: "--- a/x", Truncated: true},
		"binary":   {State: "binary", Text: ""},
		"empty":    {State: "empty", Text: ""},
		"gone":     {State: "gone", Text: ""},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.diff DTO")
		})
	}
}

func TestGitStage_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.stage.schema.json")
	raw, err := json.Marshal(gitStatusResult{Status: gitStatusWire{
		Branch: "main", Head: "abc1234",
		Staged:   []gitEntryWire{{Path: "a.txt", X: "M", Y: "."}},
		Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
		Total: 1, Completeness: "complete",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "git.stage DTO")
}

func TestGitUnstage_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.unstage.schema.json")
	status := gitStatusWire{
		Branch: "main", Head: "abc1234",
		Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
		Total: 0, Completeness: "complete",
	}
	unborn := status
	unborn.Branch, unborn.Unborn, unborn.Head = "master", true, ""
	cases := map[string]gitUnstageResult{
		"ok":     {State: "ok", Status: status},
		"unborn": {State: "unborn", Status: unborn},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.unstage DTO")
		})
	}
}

func TestGitStageAll_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.stageAll.schema.json")
	raw, err := json.Marshal(gitStatusResult{Status: gitStatusWire{
		Branch: "main", Head: "abc1234",
		Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
		Total: 0, Completeness: "complete",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "git.stageAll DTO")
}

func TestGitUnstageAll_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.unstageAll.schema.json")
	raw, err := json.Marshal(gitStatusResult{Status: gitStatusWire{
		Branch: "main", Head: "abc1234",
		Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
		Total: 0, Completeness: "complete",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "git.unstageAll DTO")
}

func TestGitCommit_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.commit.schema.json")
	status := gitStatusWire{
		Branch: "main", Head: "abc1234",
		Staged: []gitEntryWire{}, Unstaged: []gitEntryWire{}, Conflicted: []gitEntryWire{},
		Total: 0, Completeness: "complete",
	}
	cases := map[string]gitCommitResult{
		"ok": {
			State: "ok", Head: "abc1234", OutputTruncated: false, Status: &status,
		},
		"failed": {
			State: "failed", Output: "pre-commit hook failed", OutputTruncated: false,
		},
		"failed truncated": {
			State: "failed", Output: "…", OutputTruncated: true,
		},
		"committed but stale": {
			State: "ok", Head: "abc1234", OutputTruncated: false, StatusStale: true,
		},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.commit DTO")
		})
	}
}

func TestGitHeadMessage_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.headMessage.schema.json")
	cases := map[string]gitHeadMessageResult{
		"ok":   {State: "ok", Message: "subject\n\nbody"},
		"none": {State: "none"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.headMessage DTO")
		})
	}
}

func TestGitLog_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.log.schema.json")
	cases := map[string]gitLogResult{
		"populated": {
			Log: gitLogWire{
				Entries: []gitLogEntryWire{{
					Hash:       "5738d62b66777a78af894c0708d3a7e8798a4d8d",
					ShortHash:  "5738d62",
					Subject:    "third",
					AuthorName: "Test Author",
					AuthoredAt: "2026-08-07T12:52:40+03:00",
					Refs:       []string{"main", "v1.0"},
				}},
				Total:        1,
				Completeness: "complete",
			},
		},
		"empty": {
			Log: gitLogWire{
				Entries:      []gitLogEntryWire{},
				Total:        0,
				Completeness: "complete",
			},
		},
		"capped": {
			Log: gitLogWire{
				Entries: []gitLogEntryWire{{
					Hash: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", ShortHash: "aaaa111",
					Subject: "one", AuthorName: "A", AuthoredAt: "2026-08-07T12:52:40+03:00",
					Refs: []string{},
				}},
				Total:        51,
				Completeness: "capped",
			},
		},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.log DTO")
		})
	}
}

// git.remote (brief, nocx-hc0m): the DTO must marshal to a schema-valid
// result in both states — ok with the remote's URL, and none, which is the
// ordinary "nothing to open" answer and never an error.
func TestGitRemote_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.remote.schema.json")
	for name, dto := range map[string]gitRemoteResult{
		"ok":   {State: "ok", URL: "git@github.com:shady2k/nocx.git"},
		"none": {State: "none"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "git.remote DTO ("+name+")")
		})
	}
}

// The real method through the real socket: a branch tracking a remote
// answers the remote's own URL, and the none state stays a result.
func TestGitRemote_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.remote.schema.json")
	repo := newStubGitRepo()
	e := gitContractEnv(t, repo)
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.remote", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.remote result (real socket)")
	var got struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("git.remote: decode: %v", err)
	}
	if got.State != "ok" || got.URL != "git@github.com:shady2k/nocx.git" {
		t.Fatalf("git.remote = %+v, want ok with the remote's URL", got)
	}
}

// The none state over the real socket: the no-remote answer must arrive as
// a RESULT the schema accepts, never a transport error.
func TestGitRemote_NoneOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.remote.schema.json")
	repo := &stubGitRepo{status: stubStatus(), remoteErr: &git.ErrNoRemote{}}
	e := gitContractEnv(t, repo)
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.remote", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.remote none result (real socket)")
}

func TestGitClose_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.close.schema.json")
	raw, err := json.Marshal(gitCloseResult{Closed: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "git.close DTO")
}

// TestGitChanged_DTOConformsToContract validates the notification's PARAMS
// object only, exactly as files.changed's test does: an unsolicited
// notification has no result shape to check, so the params are the
// contract.
func TestGitChanged_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.changed.schema.json")
	raw, err := json.Marshal(gitChangedParams{BindingID: "ab12", Reason: "sessionClosed"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "git.changed DTO")
}

// ── git.* over the wire ──────────────────────────────────────────────────

// gitContractEnv boots the git test env with a stub factory whose repo
// answers the given canned values.
func gitContractEnv(t *testing.T, repo *stubGitRepo) *gitTestEnv {
	t.Helper()
	return newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo:  func() git.Repo { return repo },
		outcome: stubOpenOutcome(),
	}))
}

// gitWireCall drives a git.* method through the real socket and returns the
// raw result, failing on any RPC error.
func gitWireCall(t *testing.T, e *gitTestEnv, method string, params any, id int) json.RawMessage {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, method, params, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("%s: unmarshal: %v\nraw: %s", method, err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("%s: %+v", method, envelope.Error)
	}
	return envelope.Result
}

func TestGitOpen_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.open.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)

	raw := gitWireCall(t, e, "git.open", map[string]any{"sessionId": sid, "cwd": "/tmp/repo"}, 2)
	validateJSON(t, schema, raw, "git.open result (real socket)")
}

func TestGitStatus_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.status.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.status", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.status result (real socket)")
}

// TestGitStatusEnvStateRepeats_OverTheWire is the interval's wire half
// (nocx-69ey, AGENTS.md rule 3 — stated with both ends): the poll carries
// the environment fact on EVERY response, so a warning shown for an open
// that landed in the pre-settle window is withdrawn by a later poll that
// carries the settled resolution — the same binding, no re-open. The stub
// scripts the transition the real resolver performs; the schema's required
// envState is what makes this test catch a server that never sends it.
func TestGitStatusEnvStateRepeats_OverTheWire(t *testing.T) {
	schema := loadSchema(t, "git.status.schema.json")
	repo := newStubGitRepo()
	repo.mu.Lock()
	repo.envState = git.EnvDegraded
	repo.envReason = "the shell environment has not been resolved yet; the first commit will wait for it"
	repo.mu.Unlock()
	e := newGitTestEnv(t, WithGitRepoFactory(&stubGitFactory{
		mkRepo: func() git.Repo { return repo },
		outcome: git.OpenOutcome{
			State: git.OpenOK, Toplevel: "/tmp/repo", GitDir: "/tmp/repo/.git",
			GitVersion: "2.55.0", EnvState: git.EnvDegraded, EnvReason: repo.envReason,
		},
	}))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	// First poll: the resolution has not settled — degraded, with the
	// reason the panel renders.
	raw := gitWireCall(t, e, "git.status", map[string]any{"bindingId": bid}, 3)
	var first struct {
		EnvState  string `json:"envState"`
		EnvReason string `json:"envReason"`
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("first poll: unmarshal: %v", err)
	}
	if first.EnvState != "degraded" || first.EnvReason == "" {
		t.Fatalf("first poll: envState=%q envReason=%q, want degraded with a reason", first.EnvState, first.EnvReason)
	}
	validateJSON(t, schema, raw, "git.status result, pre-settle (real socket)")

	// The resolution settles; the NEXT poll carries resolved — same
	// binding, nothing re-opened.
	repo.mu.Lock()
	repo.envState = git.EnvResolved
	repo.envReason = ""
	repo.mu.Unlock()
	raw = gitWireCall(t, e, "git.status", map[string]any{"bindingId": bid}, 4)
	var second struct {
		EnvState  string `json:"envState"`
		EnvReason string `json:"envReason"`
	}
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatalf("second poll: unmarshal: %v", err)
	}
	if second.EnvState != "resolved" {
		t.Fatalf("second poll: envState=%q, want resolved — the warning must be withdrawable", second.EnvState)
	}
	if second.EnvReason != "" {
		t.Fatalf("second poll: envReason=%q, want absent once resolved", second.EnvReason)
	}
	validateJSON(t, schema, raw, "git.status result, settled (real socket)")
}

func TestGitDiff_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.diff.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.diff", map[string]any{
		"bindingId": bid, "path": "unstaged.txt", "side": "unstaged", "maxBytes": 1 << 20,
	}, 3)
	validateJSON(t, schema, raw, "git.diff result (real socket)")
}

func TestGitStage_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.stage.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.stage", map[string]any{
		"bindingId": bid, "paths": []string{"unstaged.txt"},
	}, 3)
	validateJSON(t, schema, raw, "git.stage result (real socket)")
}

func TestGitUnstage_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.unstage.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.unstage", map[string]any{
		"bindingId": bid, "paths": []string{"staged.txt"},
	}, 3)
	validateJSON(t, schema, raw, "git.unstage result (real socket)")
}

func TestGitStageAll_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.stageAll.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.stageAll", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.stageAll result (real socket)")
}

func TestGitUnstageAll_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.unstageAll.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.unstageAll", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.unstageAll result (real socket)")
}

func TestGitCommit_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.commit.schema.json")
	for name, repo := range map[string]*stubGitRepo{
		"ok": newStubGitRepo(),
		"failed": {
			status: stubStatus(),
			commit: git.CommitOutcome{
				State: git.CommitFailed, Output: "pre-commit hook failed", OutputTruncated: false,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := gitContractEnv(t, repo)
			sid := e.openSession(t, 1)
			bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
			raw := gitWireCall(t, e, "git.commit", map[string]any{
				"bindingId": bid, "message": "subject\n\nbody", "amend": false,
			}, 3)
			validateJSON(t, schema, raw, "git.commit result (real socket)")
		})
	}
}

func TestGitHeadMessage_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.headMessage.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.headMessage", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.headMessage result (real socket)")
}

func TestGitLog_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.log.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.log", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.log result (real socket)")
}

func TestGitClose_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.close.schema.json")
	e := gitContractEnv(t, newStubGitRepo())
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)
	raw := gitWireCall(t, e, "git.close", map[string]any{"bindingId": bid}, 3)
	validateJSON(t, schema, raw, "git.close result (real socket)")
}

// TestGitChanged_OverTheWireConformsToContract drives a real server
// emission and asserts the addressing: closing a real session delivers the
// notification to the closing connection, with the right bindingId and
// reason, and the params satisfy the schema. The teardown path is exactly
// where files.changed is undeliverable today (nocx-lzfb): both teardown
// paths removed the session's receiver before cleaning up bindings, so an
// emit-time lookup found nobody. The capture-first fix is what this test
// watches.
func TestGitChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "git.changed.schema.json")
	e := newGitTestEnv(t, WithGitRepoFactory(newStubGitFactory()))
	sid := e.openSession(t, 1)
	bid := e.openGitBinding(t, sid, "/tmp/repo", 2)

	// One loop for both frames, because git.changed may precede the close
	// response on the wire and jsonrpcCallWithID DISCARDS notifications
	// while it hunts for an id. This test used to do exactly that and then
	// wait for the frame it had just thrown away — a 30-second deadline on
	// a delivery that had already happened, and once gorilla stores that
	// first read error it returns it from every later read, so the wait
	// could never recover. closeSessionCollectNotification exists in this
	// package for precisely this hazard (ws_git_test.go) and documents it;
	// the contract test simply was not using it.
	closeResp, raw := closeSessionCollectNotification(t, e.conn, sid, "git.changed", 3, wantWithin)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &closeEnv); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("close: %+v", closeEnv.Error)
	}

	validateJSON(t, schema, raw, "git.changed params (real socket)")
	var params gitChangedParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if params.BindingID != bid {
		t.Errorf("bindingId = %q, want %q", params.BindingID, bid)
	}
	if params.Reason != "sessionClosed" {
		t.Errorf("reason = %q, want sessionClosed", params.Reason)
	}
}

// ── lifecycle.changed ─────────────────────────────────────────────────────

// The lifecycle.changed notification gets the same three checks as a method
// (AGENTS.md rule 5; ADR-0024 decision 7). It is the published fact of the
// authenticated lifecycle protocol — what the kernel concluded, projected by
// internal/lifecyclepub — and an unsolicited notification is exactly where an
// addressing or shape defect hides. Its schema covers the params object only,
// exactly as files.changed's and git.changed's do.

func TestLifecycleChanged_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.changed.schema.json")
	started := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Second)
	code := 0
	code3 := 3
	fence := strings.Repeat("51", 32)
	cases := map[string]lifecyclepub.Fact{
		// The handshake's product: PromptReady(domain), carrying the domain
		// and its epoch — the fact the frontend keys enhanced mode on.
		"prompt ready": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecyclePromptReady,
			Domain: "dom-1", Epoch: 3,
		},
		// An app-originated attempt awaiting its authenticated start.
		"running open attempt": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleRunning,
			Domain: "dom-1", Epoch: 3,
			Attempt: &lifecyclepub.Attempt{
				ID: "att-1", State: lifecyclepub.AttemptOpen,
				Command: "make", Origin: lifecyclepub.OriginApp, StartedAt: started,
			},
		},
		// The completion: exit status, completion timestamp and the render
		// fence present exactly when the attempt completed.
		"completed attempt": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleRunning,
			Domain: "dom-1", Epoch: 3,
			Attempt: &lifecyclepub.Attempt{
				ID: "att-1", State: lifecyclepub.AttemptCompleted,
				Command: "make", Origin: lifecyclepub.OriginApp,
				StartedAt: started, ExitCode: &code, CompletedAt: &completed, Fence: fence,
			},
		},
		// A non-zero exit status must survive the wire.
		"completed with nonzero exit": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleRunning,
			Domain: "dom-1", Epoch: 3,
			Attempt: &lifecyclepub.Attempt{
				ID: "att-1", State: lifecyclepub.AttemptCompleted,
				Command: "false", Origin: lifecyclepub.OriginShell,
				StartedAt: started, ExitCode: &code3, CompletedAt: &completed, Fence: fence,
			},
		},
		// An ssh child domain names where it is (nocx-ax79): the
		// destination its parent's domain_request carried, and nothing the
		// user typed (ADR-0025). Descriptive, never authority — the wire
		// test below is what proves the capability still never travels.
		"ssh child names its destination": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecyclePromptReady,
			Domain: "dom-2", Epoch: 4,
			Destination: &lifecyclepub.Destination{Host: "192.168.0.93", User: "pi", Port: 22},
		},
		// A destination with no user and no port: the ssh client resolves
		// its own defaults, which nocx does not model, so the fact says only
		// what the request said.
		"destination host only": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecyclePromptReady,
			Domain: "dom-2", Epoch: 4,
			Destination: &lifecyclepub.Destination{Host: "build-box"},
		},
		// Abandoned: the lane stays running, the attempt is unknown, and no
		// exit status is invented.
		"abandoned attempt": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleRunning,
			Domain: "dom-1", Epoch: 3,
			Attempt: &lifecyclepub.Attempt{
				ID: "att-1", State: lifecyclepub.AttemptUnknown,
				Command: "sleep 1000", Origin: lifecyclepub.OriginShell, StartedAt: started,
			},
		},
		// A lane with no live domain: no domain, no epoch, no attempt.
		"native lane": {Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleNative},
		"lost lane":   {Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleLost},
		// A lost lane opening a restoration episode (ADR-0024 decision 8):
		// the recovery contract rides the fact, carrying both halves of the
		// composite ack — the fence to match and the generation to echo.
		"lost with recovery": {
			Lane: "lane-1", Lifecycle: lifecyclepub.LifecycleLost,
			Recovery: &lifecyclepub.Recovery{
				Fence:      strings.Repeat("51", 32),
				Generation: strings.Repeat("51", 32),
			},
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			n := lifecycleChangedNotification{
				JSONRPC: "2.0",
				Method:  "lifecycle.changed",
				Params: lifecycleChangedParams{
					SessionID:    "sid-1",
					InstanceID:   "0123456789abcdef0123456789abcdef",
					SessionEpoch: 3,
					Fact:         params,
				},
			}
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
			validateJSON(t, schema, frame.Params, "lifecycle.changed DTO")
		})
	}
}

// The over-the-wire notification test: the real publisher, the real server,
// the real socket. The publisher is driven with authenticated envelopes
// exactly as a lifecycle adapter would (the kernel's own test seam — the
// adapter is proven separately in internal/lifecyclechannel); everything from
// the projection to the writeJSON frame is the production code path. This is
// the test that catches the server not sending what the DTO could have.
func TestLifecycleChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.changed.schema.json")
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t)
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	raw := readNotification(t, e.conn, "lifecycle.changed", wantWithin)
	validateJSON(t, schema, raw, "lifecycle.changed params (real socket)")
	var params lifecycleChangedParams
	if derr := json.Unmarshal(raw, &params); derr != nil {
		t.Fatalf("decode: %v", derr)
	}
	if params.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", params.SessionID, sid)
	}
	if params.Lifecycle != lifecyclepub.LifecyclePromptReady {
		t.Errorf("lifecycle = %q, want prompt_ready", params.Lifecycle)
	}
	if params.Domain != string(h.Domain) || params.Epoch != h.Epoch {
		t.Errorf("domain/epoch = %q/%d, want %q/%d", params.Domain, params.Epoch, h.Domain, h.Epoch)
	}
	// The session identity rides the fact (nocx-3oupk), distinct from the
	// domain epoch asserted above: the renderer compares it against the
	// open ack's pair, so a fact out of a previous incarnation is refused.
	sess, err := e.ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if params.InstanceID != string(sess.Identity().InstanceID) {
		t.Errorf("instanceId = %q, want %q", params.InstanceID, sess.Identity().InstanceID)
	}
	if params.SessionEpoch != sess.Identity().Epoch {
		t.Errorf("sessionEpoch = %d, want %d", params.SessionEpoch, sess.Identity().Epoch)
	}
}

// lifecycleRecordingPort records every outbound envelope the publisher
// flushed to the transport, for the decision-9 acceptance assertions.
type lifecycleRecordingPort struct {
	mu   sync.Mutex
	sent []lifecycle.Envelope
}

func (r *lifecycleRecordingPort) Send(env lifecycle.Envelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, env)
	return nil
}

func (r *lifecycleRecordingPort) kinds() []lifecycle.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]lifecycle.EventKind, 0, len(r.sent))
	for _, e := range r.sent {
		out = append(out, e.Event.Kind)
	}
	return out
}

// ── lifecycle.recoverAck (ADR-0024 decision 8) ───────────────────────────

// The DTO's own conformance: the result is exactly {ok: true} — the schema
// pins the key set and the value, so a future refactor that adds fields to
// the acknowledgement (a domain, an epoch, an attempt, a status) fails here
// before any renderer could depend on it. The params' narrowness (session
// identity + generation, nothing else) is enforced by the handler's
// rejection tests and by the over-the-wire call below.
func TestLifecycleRecoverAck_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.recoverAck.schema.json")
	raw, err := json.Marshal(map[string]bool{"ok": true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "lifecycle.recoverAck DTO")
}

// The real method through the real socket: a lost lane with a pending
// restoration episode, the renderer's exact two-field payload, and the
// actual result bytes validated against the schema.
func TestLifecycleRecoverAck_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.recoverAck.schema.json")
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	_ = readNotification(t, e.conn, "lifecycle.changed", wantWithin) // prompt_ready
	if err := pub.TransportLost("T"); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	raw := readNotification(t, e.conn, "lifecycle.changed", wantWithin)
	var lost lifecyclepub.Fact
	if err := json.Unmarshal(raw, &lost); err != nil {
		t.Fatalf("decode lost: %v", err)
	}
	if lost.Recovery == nil {
		t.Fatal("a live session's lost fact must carry the recovery contract")
	}

	resp := jsonrpcCallWithID(t, e.conn, "lifecycle.recoverAck", map[string]any{
		"sessionId": sid, "generation": lost.Recovery.Generation,
	}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("recoverAck: unmarshal: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("recoverAck: %+v", env.Error)
	}
	validateJSON(t, schema, env.Result, "lifecycle.recoverAck result (real socket)")
}

// ── lifecycle.establishAck (ADR-0024 decision 9) ─────────────────────────

// The DTO's own conformance: the result is exactly {ok: true} — the schema
// pins the key set and the value, so a future refactor that adds fields to
// the acknowledgement fails here before any renderer could depend on it.
func TestLifecycleEstablishAck_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.establishAck.schema.json")
	raw, err := json.Marshal(map[string]bool{"ok": true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "lifecycle.establishAck DTO")
}

// The real method through the real socket: a pending establishment, the
// renderer's exact five-field payload (the generation the published fact
// carried), the actual result bytes validated against the schema — and the
// accept reaching the transport ONLY after that acknowledgement, never
// before (decision 9).
func TestLifecycleEstablishAck_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.establishAck.schema.json")
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	port := &lifecycleRecordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	raw := readNotification(t, e.conn, "lifecycle.changed", wantWithin)
	var ready lifecyclepub.Fact
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatalf("decode prompt_ready: %v", err)
	}
	if ready.Generation == "" {
		t.Fatal("the published prompt_ready fact must carry the establishment generation")
	}
	// No acknowledgement yet: the accept has not reached the transport.
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("accept flushed before the acknowledgement: %v", got)
	}
	resp := jsonrpcCallWithID(t, e.conn, "lifecycle.establishAck", map[string]any{
		"sessionId": sid, "lane": string(lane), "domain": string(h.Domain),
		"epoch": h.Epoch, "generation": ready.Generation,
	}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("establishAck: unmarshal: %v\nraw: %s", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("establishAck: %+v", env.Error)
	}
	validateJSON(t, schema, env.Result, "lifecycle.establishAck result (real socket)")
	// The acknowledgement was the closing event: the accept went out once.
	if got := port.kinds(); len(got) != 1 || got[0] != lifecycle.KindAccept {
		t.Fatalf("after ack: %v, want exactly one accept", got)
	}
}

// TestLifecycleEstablishAck_ReplacedSubscriberCantRelease: an old
// connection's acknowledgement must not release an accept after the
// subscriber has been replaced (decision 9; the brief's frontend-reconnect
// item). connA opens the session and the establishment is pending; connB
// attaches (the subscriber slot moves to connB); connA's late ack is
// refused — only connB's ack flushes.
func TestLifecycleEstablishAck_ReplacedSubscriberCantRelease(t *testing.T) {
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	connA := e.conn
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	port := &lifecycleRecordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	raw := readNotification(t, connA, "lifecycle.changed", wantWithin)
	var ready lifecyclepub.Fact
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatalf("decode prompt_ready: %v", err)
	}

	// connB attaches to the session: it becomes the current subscriber.
	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	at := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": sid, "offset": 0}, 7)
	var atEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(at, &atEnv); err != nil {
		t.Fatalf("attach: unmarshal: %v", err)
	}
	if atEnv.Error != nil {
		t.Fatalf("attach: %+v", atEnv.Error)
	}

	// connA's late ack must not release the accept: it is no longer the
	// subscriber.
	resp := jsonrpcCallWithID(t, connA, "lifecycle.establishAck", map[string]any{
		"sessionId": sid, "lane": string(lane), "domain": string(h.Domain),
		"epoch": h.Epoch, "generation": ready.Generation,
	}, 8)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("old ack: unmarshal: %v\nraw: %s", err, resp)
	}
	if env.Error == nil {
		t.Fatal("a replaced connection's ack must be refused")
	}
	if got := port.kinds(); len(got) != 0 {
		t.Fatalf("old connection's ack flushed the accept: %v", got)
	}

	// The current subscriber's ack (after its attach replay) is the only
	// one that flushes.
	_ = readNotification(t, connB, "lifecycle.changed", wantWithin) // the attach replay
	resp = jsonrpcCallWithID(t, connB, "lifecycle.establishAck", map[string]any{
		"sessionId": sid, "lane": string(lane), "domain": string(h.Domain),
		"epoch": h.Epoch, "generation": ready.Generation,
	}, 9)
	// A FRESH struct: unmarshaling a success does not clear the Error
	// pointer of the struct connA's refusal populated.
	var env2 struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env2); err != nil {
		t.Fatalf("new ack: unmarshal: %v", err)
	}
	if env2.Error != nil {
		t.Fatalf("current subscriber's ack: %+v", env2.Error)
	}
	if got := port.kinds(); len(got) != 1 || got[0] != lifecycle.KindAccept {
		t.Fatalf("after current ack: %v, want exactly one accept", got)
	}
}

// ── lifecycle.submitAttempt ────────────────────────────────────────────────

// The DTO's own conformance: field tags, enum spelling, and the exact key
// set (additionalProperties:false plus required makes it exact in both
// directions). The state is always "open" and the origin always "app" —
// this call is the app-originated half of decision 5 — and cwd and host are
// present even when empty (a local session has no host).
func TestLifecycleSubmitAttempt_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.submitAttempt.schema.json")
	started := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cases := map[string]lifecycleSubmitAttemptResult{
		// An SSH session: host and cwd both known.
		"remote attempt": {
			ID: "att-1", Domain: "dom-1", State: lifecyclepub.AttemptOpen,
			Command: "make", Cwd: "/srv/app", Host: "build.example.com",
			Origin: lifecyclepub.OriginApp, StartedAt: started,
		},
		// A local session: no host, and the cwd may be unknown.
		"local attempt": {
			ID: "att-2", Domain: "dom-2", State: lifecyclepub.AttemptOpen,
			Command: "ls", Cwd: "", Host: "",
			Origin: lifecyclepub.OriginApp, StartedAt: started,
		},
	}
	for name, dto := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "lifecycle.submitAttempt DTO")
		})
	}
}

// The real method through the real socket: a live domain at a ready prompt,
// the renderer's exact payload, and the actual result bytes validated
// against the schema. Nothing here names a field, so nothing here can omit
// one — a future refactor that drops cwd, host or origin from the response
// fails right here.
func TestLifecycleSubmitAttempt_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "lifecycle.submitAttempt.schema.json")
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)
	const lane = lifecycle.LaneID("lane-1")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, h, e.conn)

	resp := jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", map[string]string{
		"domain": string(h.Domain), "command": "make", "cwd": "/srv/app", "host": "build.example.com",
	}, 41)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("submitAttempt: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("submitAttempt: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "lifecycle.submitAttempt result (real socket)")

	// The result is not just schema-shaped: it is the actual kernel attempt,
	// and the domain the renderer addressed is the one the attempt opened on.
	var got lifecycleSubmitAttemptResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Domain != string(h.Domain) {
		t.Errorf("domain = %q, want %q", got.Domain, h.Domain)
	}
	if att, ok := kernel.Attempt(lifecycle.AttemptID(got.ID)); !ok || att.Command != "make" {
		t.Errorf("kernel attempt = %+v (ok=%v), want the submitted command", att, ok)
	}
}

// ── session.integrationChanged ────────────────────────────────────────────

// The session.integrationChanged notification gets the same three checks as a
// method (AGENTS.md rule 5): it is server-initiated and unsolicited, so
// nothing correlates it and nothing checks its shape at the call site.
//
// It answers "is this session integrated, and if not why" — the question the
// open ack's shellIntegrationReason used to answer once and could therefore
// never revise. The two failures that matter most (a handshake that expires
// ten seconds after the ack, a channel lost mid-session) are exactly the ones
// a one-shot field cannot carry (nocx-dvql, nocx-viil).

func TestSessionIntegrationChanged_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	cases := map[string]integrationChangedParams{
		// The honest interval: a session that asked for integration and has
		// not yet proved anything. No reason, because nothing has gone
		// wrong yet — the schema forbids one here.
		"starting": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationStarting,
			Shell:     "/bin/bash",
		},
		"integrated": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationIntegrated,
			Shell:     "/opt/homebrew/bin/bash",
		},
		// The measured local failure: the shell never completed the
		// handshake, ten seconds after an ack that could not have said so.
		"conventional after a handshake timeout": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationConventional,
			Reason:    string(ssh.ReasonHandshakeTimeout),
			Shell:     "/bin/bash",
		},
		// A launcher decline on the remote path, carried by the same
		// notification rather than a second one (AD-8).
		"conventional after a launcher decline": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationConventional,
			Reason:    string(ssh.ReasonUnsupportedShell),
			Shell:     "zsh",
		},
		// The unpinned remote: nocx did not choose a shell, the far
		// dispatcher did, and "auto" is what this side honestly knows.
		"conventional on an unpinned remote": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationConventional,
			Reason:    string(ssh.ReasonRemoteCommand),
			Shell:     string(ssh.ShellAuto),
		},
		"lost mid-session": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationLost,
			Reason:    string(ssh.ReasonChannelLost),
			Shell:     "/bin/bash",
		},
		// The best-effort half, which the product must label as a guess.
		// Name only — no path, no arguments, no command line.
		"conventional with an observation": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Status:    IntegrationConventional,
			Reason:    string(ssh.ReasonHandshakeTimeout),
			Shell:     "/bin/bash",
			Detail:    &integrationDetail{ObservedProcess: "fish"},
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			// Every fact carries the session identity the open ack minted
			// (nocx-3oupk); the loop stamps it so no case can omit it.
			params.InstanceID = "0123456789abcdef0123456789abcdef"
			params.SessionEpoch = 1
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "session.integrationChanged DTO ("+name+")")
		})
	}
}

// The test the bead names, and it is red without this change: a LOCAL session
// whose shell never completes the handshake reports handshake-timeout, off
// the real socket.
//
// Everything but the shell itself is production code — a real
// lifecyclechannel.Adapter over a real socketpair, wired to a real publisher
// and a real server exactly as internal/app wires them, with the handshake
// bound shortened because a test may not wait ten seconds. Nothing here waits
// on a duration: the assertion is on the frame arriving.
//
// Note what could NOT have produced this notification: no lifecycle.changed
// fact is published at all on this path. The domain never establishes, so the
// lane's projection never moves and the publisher (correctly) announces
// nothing — which is why the adapter's loss cause is a seam of its own.
func TestSessionIntegrationChanged_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	logger := log.NewSlogAdapter(nil)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)

	// The composition root's two seams, verbatim: the loss cause crosses as
	// its string, and the adapter's own constant is the single spelling.
	ch, child, err := lifecyclechannel.New(logger, pub,
		lifecyclechannel.WithHelloTimeout(50*time.Millisecond),
		lifecyclechannel.WithLossReporter(func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause) {
			e.ws.NoteIntegrationLoss(lane, string(cause))
		}))
	if err != nil {
		t.Fatalf("lifecyclechannel.New: %v", err)
	}
	t.Cleanup(func() { _ = child.Close() })
	e.ws.RegisterLifecycleLane(ch.Lane(), session.ID(sid))
	e.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone)
	e.ws.emitIntegration(session.ID(sid))

	// First frame: the honest interval. A product that claimed either
	// outcome here would be guessing.
	raw := readNotification(t, e.conn, "session.integrationChanged", wantWithin)
	validateJSON(t, schema, raw, "session.integrationChanged params (real socket, starting)")
	var starting integrationChangedParams
	if err := json.Unmarshal(raw, &starting); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if starting.Status != IntegrationStarting || starting.Reason != "" {
		t.Errorf("first fact = %+v, want status=starting with no reason", starting)
	}
	if starting.SessionID != sid || starting.Shell != "/bin/bash" {
		t.Errorf("first fact addressing = %q/%q, want %q//bin/bash", starting.SessionID, starting.Shell, sid)
	}

	// The shell never speaks. The handshake bound expires, the adapter names
	// the path that lost the channel, and the product finally hears about it.
	raw = readNotification(t, e.conn, "session.integrationChanged", wantWithin)
	validateJSON(t, schema, raw, "session.integrationChanged params (real socket, handshake timeout)")
	var timedOut integrationChangedParams
	if err := json.Unmarshal(raw, &timedOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if timedOut.Status != IntegrationConventional {
		t.Errorf("status = %q, want %q", timedOut.Status, IntegrationConventional)
	}
	if timedOut.Reason != string(ssh.ReasonHandshakeTimeout) {
		t.Errorf("reason = %q, want %q", timedOut.Reason, ssh.ReasonHandshakeTimeout)
	}
	if timedOut.Shell != "/bin/bash" {
		t.Errorf("shell = %q, want /bin/bash — a diagnosis that omits the shell is not actionable", timedOut.Shell)
	}
}

// ── snippets.* ─────────────────────────────────────────────────────────

// memSnippetStore is an in-memory snippet.Store for the wire tests: a slice
// plus an existed flag, so "the document exists and is empty" (its seeding
// already happened) is a state the service can actually be in.
type memSnippetStore struct {
	list    []snippet.Snippet
	existed bool
}

func (m *memSnippetStore) LoadAll() ([]snippet.Snippet, error) {
	return append([]snippet.Snippet(nil), m.list...), nil
}

func (m *memSnippetStore) SaveAll(s []snippet.Snippet) error {
	m.list = append([]snippet.Snippet(nil), s...)
	m.existed = true
	return nil
}

func (m *memSnippetStore) Exists() (bool, error) { return m.existed, nil }

// newSnippetWSServer builds a server whose snippet service sits over an
// in-memory store pre-seeded with `seeded`. Passing an EMPTY slice means the
// document already exists and is empty, so the service's first-creation
// seeding does not fire: "empty library" is then a real state rather than an
// unwritten one, which is what the [] assertion below needs.
func newSnippetWSServer(t *testing.T, seeded []snippet.Snippet) (*WSServer, func()) {
	t.Helper()
	store := &memSnippetStore{list: seeded, existed: true}
	id := 0
	svc := snippet.NewService(store, func() string { id++; return fmt.Sprintf("id-%d", id) })
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithSnippets(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// snippetCall mirrors vaultCall; split out so a failure names its domain.
func snippetCall(t *testing.T, conn *websocket.Conn, method string, params any, id int) jsonrpcResponse {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var msg jsonrpcResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if string(msg.ID) == strconv.Itoa(id) {
			return msg
		}
	}
}

func TestSnippetsList_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.list.schema.json")
	cases := map[string][]snippet.Snippet{
		"populated": {
			{ID: "a", Title: "one", Body: "first body"},
			{ID: "b", Title: "two", Body: "second body"},
		},
		// wireSnippetList(nil) is the handler's answer for an empty library;
		// it must marshal as [] and never null — the renderer's first .map
		// assumes it.
		"empty": nil,
	}
	for name, snips := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(wireSnippetList(snips))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "snippets.list DTO")
		})
	}
}

func TestSnippetsCreate_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.create.schema.json")
	raw, err := json.Marshal(snippet.Snippet{ID: "id-1", Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "snippets.create DTO")
}

func TestSnippetsUpdate_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.update.schema.json")
	raw, err := json.Marshal(snippet.Snippet{ID: "id-1", Title: "new title", Body: "new body"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "snippets.update DTO")
}

func TestSnippetsDelete_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.delete.schema.json")
	raw, err := json.Marshal(snippetDeleteResponse{ID: "id-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "snippets.delete DTO")
}

func TestSnippetsReorder_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.reorder.schema.json")
	raw, err := json.Marshal(wireSnippetList([]snippet.Snippet{
		{ID: "b", Title: "B", Body: "b"},
		{ID: "a", Title: "A", Body: "a"},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "snippets.reorder DTO")
}

func TestSnippetsList_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.list.schema.json")
	ws, stop := newSnippetWSServer(t, []snippet.Snippet{})
	defer stop()

	resp := snippetCall(t, connectWS(t, ws), "snippets.list", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "snippets.list result")

	// The shape this directory exists for: an empty collection is [] and not
	// null. Its very first run found exactly this on vault.status's providers.
	var got struct {
		Snippets []map[string]any `json:"snippets"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Snippets == nil {
		t.Fatal("snippets marshalled as null; must be []")
	}
}

func TestSnippetsCreate_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.create.schema.json")
	ws, stop := newSnippetWSServer(t, []snippet.Snippet{})
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "snippets.create",
		map[string]any{"title": "t", "body": "b"}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "snippets.create result")
}

func TestSnippetsUpdate_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.update.schema.json")
	ws, stop := newSnippetWSServer(t, []snippet.Snippet{{ID: "a", Title: "A", Body: "a"}})
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "snippets.update",
		map[string]any{"id": "a", "title": "B", "body": "b"}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "snippets.update result")
}

func TestSnippetsDelete_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.delete.schema.json")
	ws, stop := newSnippetWSServer(t, []snippet.Snippet{{ID: "a", Title: "A", Body: "a"}})
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "snippets.delete",
		map[string]any{"id": "a"}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "snippets.delete result")
}

func TestSnippetsReorder_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "snippets.reorder.schema.json")
	ws, stop := newSnippetWSServer(t, []snippet.Snippet{
		{ID: "a", Title: "A", Body: "a"},
		{ID: "b", Title: "B", Body: "b"},
	})
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "snippets.reorder",
		map[string]any{"ids": []string{"b", "a"}}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "snippets.reorder result")

	// Validating the payload is not enough: the ORDER is the method's whole
	// output, and a schema cannot express it.
	var got struct {
		Snippets []struct {
			ID string `json:"id"`
		} `json:"snippets"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Snippets) != 2 || got.Snippets[0].ID != "b" || got.Snippets[1].ID != "a" {
		t.Fatalf("reorder did not answer with the order asked for: %+v", got.Snippets)
	}
}

// ── notes.* ────────────────────────────────────────────────────────────

// newNoteWSServer builds a server whose notes service sits over a real
// encrypted store in a temp directory — the store IS the thing under test
// here, and a fake one would prove the DTO and nothing about the wire.
func newNoteWSServer(t *testing.T) (*WSServer, *note.Service, func()) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	st, err := note.Open(context.Background(), note.Config{
		Path: filepath.Join(t.TempDir(), "notes.db"),
		Key:  key,
	})
	if err != nil {
		t.Fatalf("open notes store: %v", err)
	}
	id := 0
	svc := note.NewService(st, func() string { id++; return fmt.Sprintf("note-%d", id) }, time.Now)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithNotes(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, svc, func() { _ = ws.Stop(ctx); _ = st.Close() }
}

func TestNotesList_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notes.list.schema.json")
	ws, svc, stop := newNoteWSServer(t)
	defer stop()
	if _, err := svc.Create(context.Background(), "# Deploy\n\nkubectl rollout status api\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := snippetCall(t, connectWS(t, ws), "notes.list", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "notes.list result")

	var got struct {
		Notes []map[string]any `json:"notes"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Notes == nil {
		t.Fatal("notes marshalled as null; must be []")
	}
	// The list is a LIST: a row carries a derived title and an excerpt, and
	// never the body (design §5). A row with a body would be a payload
	// nobody asked for on every open of the panel.
	if _, hasBody := got.Notes[0]["body"]; hasBody {
		t.Fatal("a list row carries the body")
	}
	if got.Notes[0]["title"] != "Deploy" {
		t.Fatalf("title is derived from the first line: %+v", got.Notes[0])
	}
}

func TestNotesListOnAnEmptyLibraryIsAnEmptyArray(t *testing.T) {
	schema := loadSchema(t, "notes.list.schema.json")
	ws, _, stop := newNoteWSServer(t)
	defer stop()
	resp := snippetCall(t, connectWS(t, ws), "notes.list", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "notes.list result")
	if !strings.Contains(string(resp.Result), `"notes":[]`) {
		t.Fatalf("an empty library must be [], got %s", resp.Result)
	}
}

func TestNotesCreateGetUpdateDelete_OverTheWireConformToContract(t *testing.T) {
	ws, _, stop := newNoteWSServer(t)
	defer stop()
	conn := connectWS(t, ws)

	created := snippetCall(t, conn, "notes.create", map[string]any{"body": "first line\nsecond"}, 1)
	if created.Error != nil {
		t.Fatalf("create: %+v", created.Error)
	}
	validateJSON(t, loadSchema(t, "notes.create.schema.json"), created.Result, "notes.create result")
	var note1 struct {
		ID        string `json:"id"`
		Body      string `json:"body"`
		CreatedAt int64  `json:"createdAt"`
	}
	if err := json.Unmarshal(created.Result, &note1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := snippetCall(t, conn, "notes.get", map[string]any{"id": note1.ID}, 2)
	if got.Error != nil {
		t.Fatalf("get: %+v", got.Error)
	}
	validateJSON(t, loadSchema(t, "notes.get.schema.json"), got.Result, "notes.get result")

	updated := snippetCall(t, conn, "notes.update",
		map[string]any{"id": note1.ID, "body": "edited\nsecond"}, 3)
	if updated.Error != nil {
		t.Fatalf("update: %+v", updated.Error)
	}
	validateJSON(t, loadSchema(t, "notes.update.schema.json"), updated.Result, "notes.update result")
	var note2 struct {
		Body      string `json:"body"`
		CreatedAt int64  `json:"createdAt"`
	}
	if err := json.Unmarshal(updated.Result, &note2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if note2.Body != "edited\nsecond" {
		t.Fatalf("update did not land: %q", note2.Body)
	}
	if note2.CreatedAt != note1.CreatedAt {
		t.Fatal("an edit moved createdAt; an edit is not a new note")
	}

	deleted := snippetCall(t, conn, "notes.delete", map[string]any{"id": note1.ID}, 4)
	if deleted.Error != nil {
		t.Fatalf("delete: %+v", deleted.Error)
	}
	validateJSON(t, loadSchema(t, "notes.delete.schema.json"), deleted.Result, "notes.delete result")

	gone := snippetCall(t, conn, "notes.get", map[string]any{"id": note1.ID}, 5)
	if gone.Error == nil {
		t.Fatal("get of a deleted note must be an error, not an empty note")
	}
	if gone.Error.Code != -32602 {
		t.Fatalf("a missing note is the caller's error: code %d", gone.Error.Code)
	}
}

func TestNotesSearch_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notes.search.schema.json")
	ws, svc, stop := newNoteWSServer(t)
	defer stop()
	ctx := context.Background()
	if _, err := svc.Create(ctx, "Deploy notes\n\nkubectl rollout status api\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Create(ctx, "Something else\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := snippetCall(t, connectWS(t, ws), "notes.search", map[string]any{"query": "rollout"}, 1)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "notes.search result")

	var got struct {
		Matches []struct {
			Title   string `json:"title"`
			Excerpt string `json:"excerpt"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("want the one note whose BODY carries the word, got %d", len(got.Matches))
	}
	if !strings.Contains(strings.ToLower(got.Matches[0].Excerpt), "rollout") {
		t.Fatalf("the excerpt does not carry the match: %q", got.Matches[0].Excerpt)
	}
}

func TestNotesWithoutAServiceRefuseRatherThanAnswerEmpty(t *testing.T) {
	// The soft degrade, stated on the wire: a build with no notes service
	// says the method is not there. An empty list would tell somebody their
	// notes are gone (design §8, §11.5).
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	resp := snippetCall(t, connectWS(t, ws), "notes.list", map[string]any{}, 1)
	if resp.Error == nil {
		t.Fatal("an unwired notes domain must refuse, not answer with an empty library")
	}
}

// ── agent.captureFrame / agent.ask (nocx-f4s5, design §7) ────────────────

// The DTOs' own conformance: field tags, omitempty behaviour, whether the
// enum spells what the schema says.
func TestAgentCaptureFrame_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.captureFrame.schema.json")
	raw, err := json.Marshal(captureFrameResponse{FrameID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "agent.captureFrame DTO")
}

func TestAgentAsk_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.ask.schema.json")
	raw, err := json.Marshal(agentAskResponse{
		RunID: 7, QuestionID: "ask-1", AnswerEntryID: "answer-1",
		State: string(content.RunPrepared), IngestSeq: 3, Replayed: false,
		Model: "qwen3",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "agent.ask DTO")
}

// The real methods through the real socket — the assertion that would have
// caught a field nobody sends. Nothing here names a field, so nothing here
// can omit one.
func TestAgentCaptureFrame_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.captureFrame.schema.json")
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	resp := jsonrpcCallWithID(t, conn, "agent.captureFrame", frameParams(sid, "cap-contract"), 1)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("captureFrame error: %+v", env.Error)
	}
	validateJSON(t, schema, env.Result, "agent.captureFrame result")
}

func TestAgentAsk_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.ask.schema.json")
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hi"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	frameID, errObj := captureFrameOverWire(t, conn, frameParams(sid, "cap-contract"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}

	resp := jsonrpcCallWithID(t, conn, "agent.ask", map[string]any{
		"askId": "ask-contract", "sessionId": sid, "question": "q", "cwd": "/repo",
		"references": []any{map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}}},
	}, 2)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("ask error: %+v", env.Error)
	}
	validateJSON(t, schema, env.Result, "agent.ask result")
}

// ── agent.readScreenRequest / agent.readScreenResolved (nocx-ljfwz) ──────

// The request notification is SERVER-built, so its contract check is the
// real payload off the real socket: the broker's request params (requestId
// + sessionId, the narrowing already applied) must satisfy the schema the
// renderer's generated type was declared from.
func TestAgentReadScreenRequest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.readScreenRequest.schema.json")
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestScreen(t.Context(), sid, nil)
		done <- err
	}()
	raw := readNotification(t, conn, "agent.readScreenRequest", 5*time.Second)
	validateJSON(t, schema, raw, "agent.readScreenRequest notification params")

	// Settle the request so the goroutine above exits: a failed outcome is
	// the honest terminal answer for a test that only wants the params.
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("requestId decode: %v", err)
	}
	resp := jsonrpcCall(t, conn, "agent.readScreenResolved", map[string]any{
		"requestId": req.RequestID,
		"outcome":   "failed",
		"error":     "contract test",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("resolution refused: %+v", env.Error)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "could not capture the screen") {
			t.Fatalf("RequestScreen returned %v, want the failed-outcome error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestScreen never settled")
	}
}

func TestAgentReadScreenResolved_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.readScreenResolved.schema.json")
	body, err := json.Marshal(readScreenResolvedParams{
		Outcome: "frame",
		Rows: []frameRowWire{{
			Kind: "cells",
			Cells: []frameCellWire{
				{Char: "h", Attrs: frameAttrsWire{}},
				{Char: "i", Attrs: frameAttrsWire{}},
			},
		}},
		Cursor: &frameCursorWire{Line: 0, Col: 0},
		Identity: &frameIdentityWire{
			Buffer: frameBufferWire{Kind: "normal"},
			Cols:   2, Rows: 1, Generation: 1,
		},
		Range: &frameRangeWire{Start: 0, End: 1},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The wire envelope carries the broker-minted requestId beside the
	// resolution body — the renderer echoes it back (the broker correlates
	// on it before the kind's shape check runs).
	var wire map[string]any
	if wireErr := json.Unmarshal(body, &wire); wireErr != nil {
		t.Fatalf("decode body: %v", wireErr)
	}
	wire["requestId"] = "0123456789abcdef"
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	validateJSON(t, schema, raw, "agent.readScreenResolved DTO")
}

// ── agent.runRequest / agent.runResolved (nocx-tjppv) ─────────────────────

// The run request notification is SERVER-built, so its contract check is the
// real payload off the real socket: the broker's request params (requestId +
// sessionId + command) must satisfy the schema the renderer's generated type
// was declared from.
func TestAgentRunRequest_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runRequest.schema.json")
	ws, _, stop := newAgentWSServer(t)
	defer stop()
	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	sid := openLocalSession(t, conn)

	done := make(chan error, 1)
	go func() {
		_, err := ws.RequestRun(t.Context(), sid, "ls -la")
		done <- err
	}()
	raw := readNotification(t, conn, "agent.runRequest", 5*time.Second)
	validateJSON(t, schema, raw, "agent.runRequest notification params")

	// Settle the request so the goroutine above exits: a failed outcome is
	// the honest terminal answer for a test that only wants the params.
	var req struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("requestId decode: %v", err)
	}
	resp := jsonrpcCall(t, conn, "agent.runResolved", map[string]any{
		"requestId": req.RequestID,
		"outcome":   "failed",
		"error":     "contract test",
	})
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("resolution refused: %+v", env.Error)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "could not run the command") {
			t.Fatalf("RequestRun returned %v, want the failed-outcome error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestRun never settled")
	}
}

func TestAgentRunResolved_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runResolved.schema.json")
	body, err := json.Marshal(runResolvedParams{
		Outcome:  "completed",
		EntryID:  "entry-7",
		ExitCode: new(0),
		Status:   "success",
		Total:    2,
		Start:    0,
		End:      2,
		Text:     "file1\nfile2",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The wire envelope carries the broker-minted requestId beside the
	// resolution body — the renderer echoes it back (the broker correlates
	// on it before the kind's shape check runs).
	var wire map[string]any
	if wireErr := json.Unmarshal(body, &wire); wireErr != nil {
		t.Fatalf("decode body: %v", wireErr)
	}
	wire["requestId"] = "0123456789abcdef"
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	validateJSON(t, schema, raw, "agent.runResolved DTO")
}

// The failed outcome's shape: no run body, only the requestId and the
// failure sentence.
func TestAgentRunResolved_FailedDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.runResolved.schema.json")
	raw, err := json.Marshal(map[string]any{
		"requestId": "0123456789abcdef",
		"outcome":   "failed",
		"error":     "the agent lane is not prompt-ready",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "agent.runResolved failed DTO")
}

// ── agent.runDelta / agent.runState notifications (nocx-x8s2.2, design §7) ─

// The DTOs' conformance: field tags and enum spelling.
func TestAgentRunNotifications_DTOsConformToContract(t *testing.T) {
	deltaSchema := loadSchema(t, "agent.runDelta.schema.json")
	raw, err := json.Marshal(agentRunDelta{RunID: 7, EntryID: "answer-1", Seq: 0, Text: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, deltaSchema, raw, "agent.runDelta DTO")

	stateSchema := loadSchema(t, "agent.runState.schema.json")
	raw, err = json.Marshal(agentRunState{RunID: 7, State: string(content.RunFailed), Error: "the model returned no text"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, stateSchema, raw, "agent.runState DTO (failed with error)")

	raw, err = json.Marshal(agentRunState{RunID: 7, State: string(content.RunCompleted)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, stateSchema, raw, "agent.runState DTO (completed, no error)")
}

// The real notifications off the real socket satisfy their contracts — the
// assertion that would catch a field nobody sends.
func TestAgentRunNotifications_OverTheWireConformToContract(t *testing.T) {
	deltaSchema := loadSchema(t, "agent.runDelta.schema.json")
	stateSchema := loadSchema(t, "agent.runState.schema.json")

	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hello", "world"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)
	frameID, errObj := captureFrameOverWire(t, conn, frozenWireFrame(sid, "frame-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}
	_, errObj = askOverWire(t, conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "q", "cwd": "/repo",
		"references": []any{map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}}},
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}

	for range 2 {
		raw := readNotification(t, conn, "agent.runDelta", 5*time.Second)
		validateJSON(t, deltaSchema, raw, "agent.runDelta params (real socket)")
	}
	raw := readNotification(t, conn, "agent.runState", 5*time.Second)
	validateJSON(t, stateSchema, raw, "agent.runState params (real socket)")
}

// ── exit notification (nocx-ictcq) ────────────────────────────────────────

// The exit notification carries the discriminator that separates an
// authoritative shell exit from a loss, so a tab whose ssh connection
// dropped is marked instead of destroyed. The DTO's conformance: the field
// tags, the enum spelling, and the "status present exactly when exited"
// rule — marshalled, because that is where omitempty does its work.
func TestExit_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "exit.schema.json")
	status := 42
	cases := map[string]exitNotificationParams{
		"exited with status": {
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 2,
			Cause:        string(session.ExitExited),
			Status:       &status,
		},
		"interrupted, no status": {
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 2,
			Cause:        string(session.ExitInterrupted),
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "exit DTO")
		})
	}
}

// A status must never ride an interrupted exit, and an exited exit must
// never lose its status — the oneOf branches make both a schema error, and
// the marshalled DTO is where the field tags would hide either.
func TestExit_DTOStatusRulesAreExact(t *testing.T) {
	schema := loadSchema(t, "exit.schema.json")

	status := 0
	bad := []struct {
		name   string
		params exitNotificationParams
	}{
		{"interrupted carries a status", exitNotificationParams{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 2,
			Cause:        string(session.ExitInterrupted),
			Status:       &status,
		}},
		{"exited carries no status", exitNotificationParams{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 2,
			Cause:        string(session.ExitExited),
		}},
		{"unknown cause", exitNotificationParams{
			SessionID:    "0123456789abcdef0123456789abcdef",
			InstanceID:   "fedcba9876543210fedcba9876543210",
			SessionEpoch: 2,
			Cause:        "the-wind",
		}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := validateJSONErr(schema, raw); err == nil {
				t.Fatalf("schema accepted %s: %s", c.name, raw)
			}
		})
	}
}

// ── policy.get / policy.set ──────────────────────────────────────────────

// The DTO's own conformance for policy.get: the matrix's wire form — seven
// rows, effective decisions for unstated rows, non-null scopes arrays — must
// satisfy the schema exactly (additionalProperties false on the result AND on
// every row AND every scope, so nothing extra can ride along).
func TestPolicyGet_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "policy.get.schema.json")

	askOnMutate := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}
	raw, err := json.Marshal(policyResult{Policy: askOnMutate})
	if err != nil {
		t.Fatalf("marshal ask-on-mutate: %v", err)
	}
	validateJSON(t, schema, raw, "policy.get ask-on-mutate DTO")

	var finer content.EffectPolicy
	finer.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	finer.MutateDestructive = content.EffectRow{Decision: content.DecisionRefuse}
	rawFiner, err := json.Marshal(policyResult{Policy: finer})
	if err != nil {
		t.Fatalf("marshal finer: %v", err)
	}
	validateJSON(t, schema, rawFiner, "policy.get finer-than-presets DTO")
}

// The real result off the real socket: the handler's response — not a test
// payload — must satisfy the contract, with the scope and decisions the store
// actually holds (the third row of the contracts README's table).
func TestPolicyGet_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "policy.get.schema.json")
	h, store := newPolicyHarness(t)
	var p content.EffectPolicy
	p.Observe = content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	p.Delegate = content.EffectRow{Decision: content.DecisionRefuse}
	if err := store.SetPolicy(p); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	resp := jsonrpcCall(t, h.conn, "policy.get", nil)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("policy.get: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "policy.get result (real socket)")
}

// The policy.set result's conformance: {ok: true}, asserted off the real
// socket after a set the validator accepted.
func TestPolicySet_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "policy.set.schema.json")
	h, _ := newPolicyHarness(t)

	resp := jsonrpcCall(t, h.conn, "policy.set", map[string]any{
		"policy": map[string]any{
			"observe": map[string]any{
				"decision": "permit",
				"scopes":   []any{map[string]any{"kind": "path", "id": "/workspace"}},
			},
		},
	})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("policy.set: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "policy.set result (real socket)")
}

// ── ledger.open / ledger.bind / ledger.close ─────────────────────────────

// The DTO's own conformance: one case per interesting shape of the ack the
// three lifecycle methods share — the applied event, the replay and the
// drop. Enum spelling and the integer types are what this catches.
func TestLedgerEvents_DTOsConformToContract(t *testing.T) {
	cases := map[string]struct {
		schema string
		dto    ledgerEventResponse
	}{
		"open applied": {"ledger.open.schema.json", ledgerEventResponse{
			ID: "01924f9c-0000-7000-8000-000000000001", ClientSeq: 0, Seq: 1,
			SubmittedAt: 1_750_000_000_000, Phase: "open", Outcome: ledgerApplied,
		}},
		"open replay": {"ledger.open.schema.json", ledgerEventResponse{
			ID: "01924f9c-0000-7000-8000-000000000001", ClientSeq: 4, Seq: 12,
			SubmittedAt: 1_750_000_000_000, Phase: "open", Outcome: ledgerReplay,
		}},
		"bind applied": {"ledger.bind.schema.json", ledgerEventResponse{
			ID: "01924f9c-0000-7000-8000-000000000002", ClientSeq: 5, Seq: 13,
			SubmittedAt: 1_750_000_000_000, Phase: "bound", Outcome: ledgerApplied,
		}},
		"bind dropped after close": {"ledger.bind.schema.json", ledgerEventResponse{
			ID: "01924f9c-0000-7000-8000-000000000002", ClientSeq: 5, Seq: 13,
			SubmittedAt: 1_750_000_000_000, Phase: "closed", Outcome: ledgerDropped,
		}},
		"close applied": {"ledger.close.schema.json", ledgerEventResponse{
			ID: "01924f9c-0000-7000-8000-000000000003", ClientSeq: 6, Seq: 14,
			SubmittedAt: 1_750_000_000_000, Phase: "closed", Outcome: ledgerApplied,
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			schema := loadSchema(t, c.schema)
			raw, err := json.Marshal(c.dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, name+" DTO")
		})
	}
}

// The real methods through the real socket, into the real store. Nothing here
// names a field, so nothing here can omit one; additionalProperties:false plus
// required makes the key set exact in both directions. Every state the ack has
// is driven off the socket: applied on a created row, applied on an advance,
// replay on a re-delivery, and dropped on a bind that arrives after the close.
func TestLedgerEvents_OverTheWireConformToContract(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	openSchema := loadSchema(t, "ledger.open.schema.json")
	bindSchema := loadSchema(t, "ledger.bind.schema.json")
	closeSchema := loadSchema(t, "ledger.close.schema.json")

	// The close carries every fact it may (nocx-rtg0.23), so the ack that is
	// validated is the one the widened method actually answers with.
	closeParams := func(id string, seq int) map[string]any {
		return map[string]any{
			"envelope":   ledgerEnv(sid, id, "make", seq),
			"status":     "success",
			"facts":      map[string]any{"terminationReason": "completed", "exitCode": 0},
			"durationMs": 42,
			"startedAt":  1_750_000_000_000,
		}
	}

	steps := []struct {
		name   string
		method string
		schema *jsonschema.Schema
		params map[string]any
	}{
		{"open creates", "ledger.open", openSchema, map[string]any{"envelope": ledgerEnv(sid, "wire-1", "make", 1)}},
		{"open replayed", "ledger.open", openSchema, map[string]any{"envelope": ledgerEnv(sid, "wire-1", "make", 1)}},
		{"bind advances", "ledger.bind", bindSchema, map[string]any{
			"envelope": ledgerEnv(sid, "wire-1", "make", 2),
			"facts":    map[string]any{"interactivity": "tty", "executor": "zsh"},
		}},
		{"close advances", "ledger.close", closeSchema, closeParams("wire-1", 3)},
		{"bind dropped after close", "ledger.bind", bindSchema, map[string]any{
			"envelope": ledgerEnv(sid, "wire-1", "make", 4),
		}},
		{"close creates a row nobody opened", "ledger.close", closeSchema, closeParams("wire-2", 1)},
	}

	id := 2
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			resp := vaultCall(t, conn, step.method, step.params, id)
			id++
			if resp.Error != nil {
				t.Fatalf("%s: unexpected error: %+v", step.method, resp.Error)
			}
			validateJSON(t, step.schema, resp.Result, step.method+" result ("+step.name+")")
		})
	}
}

// ── ledger.query / ledger.get ────────────────────────────────────────────

// The DTOs' own conformance: the shapes with everything populated and the
// shapes with nothing. The empty cases are the ones that matter here — a nil
// slice marshals as null, and `entries` arriving as null rather than [] is
// the exact defect vault.status shipped with `providers`.
func TestLedgerReads_DTOsConformToContract(t *testing.T) {
	host := "deploy@prod.example.com"
	started := int64(1_750_000_000_000)
	ended := int64(1_750_000_001_000)
	duration := int64(1000)
	exit := 2
	entry := ledgerEntryWire{
		ID: "01924f9c-0000-7000-8000-000000000001", Seq: 7,
		EnvID: "3f1a", Host: &host, Cwd: "/repo", Kind: "shell",
		Intent: "make deploy", Phase: "closed", Status: "failure",
		SubmittedAt: started, StartedAt: &started, EndedAt: &ended,
		DurationMs: &duration, ExitCode: &exit, MaskedCount: 1,
		MaskedKinds: []string{"openai"},
		Redactions: []redactionWire{
			{Kind: "openai", Start: 4, End: 20, Prefix: "sk-p", Suffix: "7890"},
		},
	}
	// The row a live command produces: no end, no exit code, no host row.
	running := ledgerEntryWire{
		ID: "01924f9c-0000-7000-8000-000000000002", Seq: 8,
		EnvID: "3f1a", Host: nil, Cwd: "/repo", Kind: "shell",
		Intent: "make watch", Phase: "bound", Status: "running",
		SubmittedAt: started, MaskedKinds: []string{}, Redactions: []redactionWire{},
	}

	queryCases := map[string]ledgerQueryResponse{
		"populated": {
			Entries: []ledgerEntryWire{entry, running}, Scope: "host",
			Exhausted: false, HasRows: true, Coverage: &ended,
		},
		"empty ledger": {
			Entries: []ledgerEntryWire{}, Scope: "everywhere",
			Exhausted: true, HasRows: false, Coverage: nil,
		},
	}
	querySchema := loadSchema(t, "ledger.query.schema.json")
	for name, dto := range queryCases {
		t.Run("query/"+name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, querySchema, raw, "ledger.query "+name+" DTO")
		})
	}

	cols, rows := 120, 40
	stream := "combined"
	truncated := "cap"
	offset, end := int64(0), int64(4096)
	getCases := map[string]ledgerGetResponse{
		"populated": {
			Entry: entry,
			Edges: []ledgerEdgeWire{{
				From: entry.ID, To: running.ID, Rel: "rerun-of",
				Payload: json.RawMessage(`{"note":"anything"}`),
			}},
			Artifacts: []ledgerArtifactWire{{
				ID: "artifact-1", ExecutionID: 3, MediaType: "text/plain",
				DerivedFrom: nil, State: "sealed", ByteLen: 4096, ChunkCount: 2,
				Pinned: true, Truncated: &truncated, CaptureMethod: "raw-output",
				CaptureVersion: 1, TerminalCols: &cols, TerminalRows: &rows,
				Stream: &stream, ByteOffset: &offset, ByteEnd: &end, Encoding: "utf-8",
				Gaps:    []ledgerGapWire{{Start: 10, End: 20, Reason: "dropped"}},
				Payload: json.RawMessage(`{}`),
			}},
		},
		"an entry with no relations and no captures": {
			Entry: running, Edges: []ledgerEdgeWire{}, Artifacts: []ledgerArtifactWire{},
		},
	}
	getSchema := loadSchema(t, "ledger.get.schema.json")
	for name, dto := range getCases {
		t.Run("get/"+name, func(t *testing.T) {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, getSchema, raw, "ledger.get "+name+" DTO")
		})
	}
}

// The real methods through the real socket, into the real store. Nothing
// here names a field, so nothing here can omit one; additionalProperties
// false plus required makes the key set exact in both directions. The empty
// answer is driven first, because that is the payload a fixture-built test
// would never produce.
func TestLedgerReads_OverTheWireConformToContract(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	querySchema := loadSchema(t, "ledger.query.schema.json")
	getSchema := loadSchema(t, "ledger.get.schema.json")

	empty := vaultCall(t, conn, "ledger.query", map[string]any{"scope": "everywhere"}, 2)
	if empty.Error != nil {
		t.Fatalf("ledger.query on an empty ledger: %+v", empty.Error)
	}
	validateJSON(t, querySchema, empty.Result, "ledger.query result (empty ledger)")

	// A row with a secret in it, closed with an exit code: the receipt and
	// the shell arm are both on the wire, off the real payload.
	openEntry(t, conn, sid, "wire-1", "deploy --token=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCD", 3)
	closeEntryOverWire(t, conn, sid, "wire-1", "failure", 3, 4)
	openEntry(t, conn, sid, "wire-2", "make test", 5)

	for _, c := range []struct {
		name   string
		params map[string]any
	}{
		{"everywhere", map[string]any{"scope": "everywhere"}},
		{"a rung with no matches", map[string]any{
			"scope": "directory", "environmentId": localEnvironmentID(), "cwd": "/nowhere",
		}},
		{"a page that is not exhausted", map[string]any{"scope": "everywhere", "limit": 1}},
	} {
		t.Run("query/"+c.name, func(t *testing.T) {
			resp := vaultCall(t, conn, "ledger.query", c.params, 10)
			if resp.Error != nil {
				t.Fatalf("ledger.query: %+v", resp.Error)
			}
			validateJSON(t, querySchema, resp.Result, "ledger.query result ("+c.name+")")
		})
	}

	got := vaultCall(t, conn, "ledger.get", map[string]any{"id": "wire-1"}, 11)
	if got.Error != nil {
		t.Fatalf("ledger.get: %+v", got.Error)
	}
	validateJSON(t, getSchema, got.Result, "ledger.get result (a closed entry)")
}

// And the negative: the schema REFUSES a page that is missing a required
// field or carrying a key nobody declared. additionalProperties:false plus
// an explicit required is what makes the check exact in both directions —
// without them the schema accepts anything and the gate is theatre.
func TestLedgerQuery_ContractRefusesWhatItMustRefuse(t *testing.T) {
	schema := loadSchema(t, "ledger.query.schema.json")
	bad := map[string]string{
		"entries as null":     `{"entries":null,"scope":"host","exhausted":true,"hasRows":true,"coverage":null}`,
		"hasRows missing":     `{"entries":[],"scope":"host","exhausted":true,"coverage":null}`,
		"an undeclared field": `{"entries":[],"scope":"host","exhausted":true,"hasRows":true,"coverage":null,"source":"store"}`,
		"a rung nobody named": `{"entries":[],"scope":"repository","exhausted":true,"hasRows":true,"coverage":null}`,
		"an entry with no host key": `{"entries":[{"id":"a","seq":1,"environmentId":"e","cwd":"/","kind":"shell",` +
			`"intent":"x","phase":"open","status":"pending","submittedAt":1,"startedAt":null,"endedAt":null,` +
			`"durationMs":null,"exitCode":null,"maskedCount":0,"maskedKinds":[],"redactions":[]}],` +
			`"scope":"host","exhausted":true,"hasRows":true,"coverage":null}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if err := validateJSONErr(schema, []byte(raw)); err == nil {
				t.Fatalf("the contract accepted %s: %s", name, raw)
			}
		})
	}
}

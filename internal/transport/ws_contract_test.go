package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
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
	"github.com/shady2k/nocx/internal/sandbox"
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

// ── dialog.openFileForUpload ────────────────────────────────────────────

// The DTO's own conformance. Both branches: a picked file, and a cancelled
// picker — where the ticket is the empty string and the pattern has to
// accept it, because cancel is "no change" and not an error.
func TestDialogOpenFileForUpload_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openFileForUpload.schema.json")
	raw, err := json.Marshal(SourcePick{
		Ticket: "0123456789abcdef0123456789abcdef",
		Name:   "report.pdf",
		Size:   4096,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "dialog.openFileForUpload DTO")

	rawCancel, err := json.Marshal(SourcePick{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawCancel, "dialog.openFileForUpload cancelled DTO")
}

// The ticket's PATTERN is load-bearing, not decoration: an unconstrained
// string lets a regression emit a malformed or truncated ticket and still
// pass conformance. This is the assertion that the schema would reject one.
func TestDialogOpenFileForUpload_ContractRefusesAMalformedTicket(t *testing.T) {
	schema := loadSchema(t, "dialog.openFileForUpload.schema.json")
	for _, bad := range []string{"not-a-ticket", "ABCDEF0123456789ABCDEF0123456789", "0123456789abcdef"} {
		raw, err := json.Marshal(SourcePick{Ticket: bad, Name: "x", Size: 1})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := validateJSONErr(schema, raw); err == nil {
			t.Errorf("the contract accepted %q as a source ticket", bad)
		}
	}
}

// The real method through the real socket, with a picker standing in for
// the Wails runtime and a real file standing in for the human's choice.
func TestDialogOpenFileForUpload_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openFileForUpload.schema.json")
	h := newInventoryHarness(t)
	picker := &fakeUploadPicker{path: seedFile(t, "over-the-wire.bin", 9), store: NewSourceTicketStore(nil)}
	h.ws.SetDialogService(picker)

	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("dialog.openFileForUpload: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "dialog.openFileForUpload result")
}

// ── files.dropped ───────────────────────────────────────────────────────

// The DTO's own conformance: the notification params as the emitter builds
// them, key set exact.
func TestFilesDropped_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.dropped.schema.json")
	raw, err := json.Marshal(map[string]any{
		"sessionId": "0123456789abcdef0123456789abcdef",
		"target":    "terminal",
		"sources": []SourcePick{
			{Ticket: "abcdef0123456789abcdef0123456789", Name: "a.txt", Size: 2},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "files.dropped DTO")

	// And the local tab's shape, which is the other one this struct has: no
	// ticket, and the absolute path the prompt insert needs (D9).
	rawLocal, err := json.Marshal(map[string]any{
		"sessionId": "0123456789abcdef0123456789abcdef",
		"target":    "api-import",
		"sources": []SourcePick{
			{Name: "a.txt", Size: 2, LocalPath: "/home/dev/Downloads/a.txt"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawLocal, "files.dropped DTO (local tab)")
}

// The real notification off the real socket — the row that matters, because
// a payload the test itself built proves the struct is well-formed and not
// that the server sends it. The drop is driven through the store the window
// drop calls, so this is the whole native path minus the OS.
func TestFilesDropped_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.dropped.schema.json")
	e := newDropEnv(t)
	if err := e.ws.UploadSources().Dropped(
		[]string{seedFile(t, "dropped-over-the-wire.bin", 3)},
		map[string]string{"data-session-id": e.sid, "data-file-drop-target": "terminal"},
	); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	params := readNotification(t, e.conn, "files.dropped", 5*time.Second)
	validateJSON(t, schema, params, "files.dropped notification")
	// The regression that would matter, asserted on the bytes rather than on
	// a decoded struct: a REMOTE tab is where a credential exists and bytes
	// will move, and it learns a name and a size and nothing else about the
	// backend's filesystem. The KEY is what is checked, so an empty path
	// cannot pass for an absent one.
	if bytes.Contains(params, []byte("localPath")) {
		t.Fatalf("a remote tab's files.dropped carries a path: %s", params)
	}
	// The schema can only say the field is there and non-empty. Which
	// surface it names is what the subscriber filters on, so the VALUE is
	// asserted off the wire too: a target the server rewrote or defaulted
	// would pass the schema and still deliver the drop to the wrong pane.
	var addressed struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(params, &addressed); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, params)
	}
	if addressed.Target != "terminal" {
		t.Fatalf("target = %q, want the drop element's own data-file-drop-target", addressed.Target)
	}
}

// And the same notification for the tab that mints nothing: a drop on a
// LOCAL tab still tells the renderer what was dropped, so the prompt insert
// works in the Wails window, and carries no ticket because nothing may be
// uploaded onto the machine the file is already on (D9). The empty ticket
// is part of the contract rather than an accident of it.
func TestFilesDropped_ALocalTabsNotificationConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.dropped.schema.json")
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1) // the `open` method opens a LOCAL session
	path := seedFile(t, "local-drop.bin", 3)
	if err := e.ws.UploadSources().Dropped(
		[]string{path},
		map[string]string{"data-session-id": sid, "data-file-drop-target": "api-import"},
	); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	params := readNotification(t, e.conn, "files.dropped", 5*time.Second)
	validateJSON(t, schema, params, "files.dropped notification (local tab)")
	var got struct {
		Sources []SourcePick `json:"sources"`
	}
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Ticket != "" {
		t.Fatalf("sources = %+v, want one entry with no ticket", got.Sources)
	}
	// The half D9 promised and the wire did not keep until now: the prompt
	// insert needs the PATH, and a base name that looks like one resolves
	// against whatever the shell's cwd happens to be.
	if got.Sources[0].LocalPath != path {
		t.Fatalf("localPath = %q off the socket, want %q", got.Sources[0].LocalPath, path)
	}
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
	sandboxRaw, err := json.Marshal(openResult{
		SessionID:    "0123456789abcdef0123456789abcdef",
		InstanceID:   "fedcba9876543210fedcba9876543210",
		SessionEpoch: 1,
		WorkspaceID:  string(workspace.Default),
		Cwd:          "~/work",
		DesiredMode:  "script",
		Sandbox: &sandbox.SessionInfo{
			Backend:       sandbox.BackendLandlock,
			Workspace:     "/home/user/work",
			WritableRoots: []string{"/home/user/work"},
			ReadOnlyRoots: []string{"/usr"},
			HomeProjections: []sandbox.HomeProjection{
				{HostPath: "/home/user/work", RelativePath: "work"},
				{HostPath: "/home/user/.config/opencode", RelativePath: ".config/opencode"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal sandbox open result: %v", err)
	}
	validateJSON(t, schema, sandboxRaw, "sandbox open DTO")

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

// A launcher that declines has no bootstrap to prepare either.
func (fakeRemoteLauncher) Prepare(ssh.ShellKind, ssh.LaunchOptions) (string, ssh.BootstrapRun, ssh.BootstrapGate, bool) {
	return "", nil, nil, false
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
		// A build WITH a revealer wired: revealAvailable is a build fact
		// constant for the life of the process — the same value on every
		// binding — so the case names the build, never the binding kind.
		// The local/remote distinction is carried by endpointId alone
		// (null vs attestation, D4); this case is a local binding on a
		// supported build.
		"supportedBuild": {
			BindingID:       "ab12cd34",
			EndpointID:      nil,
			RevealAvailable: true,
			Root: filesRootResult{
				Path:           "/home/dev",
				Display:        "~/",
				Inferred:       true,
				InferredReason: "no verified working directory — using home",
			},
		},
		// A build with NO revealer: the same binding shapes as above, with
		// revealAvailable false — and here a remote binding attests its
		// destination (D4), proving the field does not ride the endpoint
		// kind. Both boolean values are exercised so the DTO is pinned for
		// each rather than passing on the zero value's accident.
		"unsupportedBuild": {
			BindingID:       "ef56",
			EndpointID:      &ep,
			RevealAvailable: false,
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
	if got.RevealAvailable {
		t.Error("revealAvailable is true although no revealer is wired")
	}
}

// TestFilesOpen_RevealAvailableRidesTheWireWhenWired is the paired
// positive: with a revealer wired through the option that exists, the
// open result carries revealAvailable=true off the real socket. The
// schema's required+additionalProperties:false proves the field is
// present; this proves its VALUE is the composition seam's, not a
// default (nocx-ngf3u).
func TestFilesOpen_RevealAvailableRidesTheWireWhenWired(t *testing.T) {
	schema := loadSchema(t, "files.open.schema.json")
	e := newFilesTestEnv(t, WithFilesRevealer(&stubRevealer{}))
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
	validateJSON(t, schema, envelope.Result, "files.open result (real socket, revealer wired)")

	var got filesOpenResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.RevealAvailable {
		t.Error("revealAvailable is false although a revealer is wired")
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

func TestFilesUpload_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.upload.schema.json")

	// One case per branch of the union. They are separate Go types rather
	// than one struct with everything optional, so "exactly one branch
	// matches" is a property of the code and not only of the schema.
	cases := map[string]any{
		"collision": filesUploadCollision{Collision: "exists"},
		"started (no body needed)": filesUploadStarted{
			TransferID: "0123456789abcdef0123456789abcdef",
		},
		"stream (the sink is waiting for a body)": filesUploadStream{
			TransferID: "0123456789abcdef0123456789abcdef",
			Ticket:     strings.Repeat("ab", uploadTicketHexLen/2),
			URL:        "/upload/" + strings.Repeat("ab", uploadTicketHexLen/2),
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.upload DTO")
		})
	}
}

// TestFilesUpload_ContractRefusesValuesItOnlyUsedToDescribe. The schema
// described transferId as 32 lowercase hex, ticket as 64 and url as
// /upload/{ticket}, and typed all three as unrestricted strings — so a
// regression emitting an empty id, an uppercase ticket or an unrelated URL
// satisfied every conformance check in this file. additionalProperties:false
// makes the SHAPE exact; these patterns are what make the security-relevant
// VALUES exact, and a pattern with nothing asserting it refuses anything is
// the same theatre as a schema without them.
func TestFilesUpload_ContractRefusesValuesItOnlyUsedToDescribe(t *testing.T) {
	schema := loadSchema(t, "files.upload.schema.json")
	good := strings.Repeat("ab", uploadTicketHexLen/2)
	cases := map[string]any{
		"an empty transfer id": filesUploadStarted{TransferID: ""},
		"a transfer id of the wrong width": filesUploadStarted{
			TransferID: "0123456789abcdef",
		},
		"an uppercase ticket": filesUploadStream{
			TransferID: "0123456789abcdef0123456789abcdef",
			Ticket:     strings.ToUpper(good),
			URL:        "/upload/" + strings.ToUpper(good),
		},
		"a url that is not this route": filesUploadStream{
			TransferID: "0123456789abcdef0123456789abcdef",
			Ticket:     good,
			URL:        "https://elsewhere.example.com/collect",
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := validateJSONErr(schema, raw); err == nil {
				t.Fatalf("the contract accepted %s:\n%s", name, raw)
			}
		})
	}
}

// The one that matters: all three branches taken off the real socket, from
// the real handler, against the real schema. A payload the test itself
// built proves the struct is well-formed, not that the server sends it.
func TestFilesUpload_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.upload.schema.json")
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "taken.txt"), []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	// Branch 3: a free name — the sink is waiting for a body.
	stream := callUpload(t, e.conn, uploadParams(bid, dir, "fresh.txt", 5), 3)
	if stream.Error != nil {
		t.Fatalf("files.upload (stream): %+v", stream.Error)
	}
	validateJSON(t, schema, stream.Result, "files.upload result, stream branch (real socket)")

	// Branch 1: a taken name and no decision.
	collision := callUpload(t, e.conn, uploadParams(bid, dir, "taken.txt", 5), 4)
	if collision.Error != nil {
		t.Fatalf("files.upload (collision): %+v", collision.Error)
	}
	validateJSON(t, schema, collision.Result, "files.upload result, collision branch (real socket)")

	// Branch 2: skip — a transfer that needs no body.
	skipParams := uploadParams(bid, dir, "taken.txt", 5)
	skipParams["onExists"] = "skip"
	skipped := callUpload(t, e.conn, skipParams, 5)
	if skipped.Error != nil {
		t.Fatalf("files.upload (skip): %+v", skipped.Error)
	}
	validateJSON(t, schema, skipped.Result, "files.upload result, started branch (real socket)")

	// And the branches really are distinct on the wire, not one shape with
	// fields that happened to be empty.
	if got := stream.mustResult(t); got.Collision != "" || got.Ticket == "" {
		t.Errorf("stream branch = %+v", got)
	}
	if got := collision.mustResult(t); got.Collision != "exists" || got.TransferID != "" {
		t.Errorf("collision branch = %+v", got)
	}
	if got := skipped.mustResult(t); got.TransferID == "" || got.Ticket != "" {
		t.Errorf("started branch = %+v", got)
	}
}

func TestFilesUploadCancel_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadCancel.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "files.uploadCancel DTO")
}

func TestFilesUploadCancel_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadCancel.schema.json")
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	started := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)

	resp := jsonrpcCallWithID(t, e.conn, "files.uploadCancel", map[string]any{
		"transferId": started.TransferID,
	}, 4)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.uploadCancel: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.uploadCancel result (real socket)")
	if state := awaitTransferState(t, e.ws, started.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
}

func TestFilesUploadProgress_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadProgress.schema.json")
	cases := map[string]filesTransferProgressParams{
		// Mid-transfer: the ordinary frame.
		"in flight": {TransferID: strings.Repeat("a1", 16), Bytes: 4096, Total: 1 << 20},
		// Nothing confirmed yet — the first chunk has not landed.
		"at zero": {TransferID: strings.Repeat("b2", 16), Bytes: 0, Total: 1 << 20},
		// An empty file is a file: total 0 must not be an omitempty hole.
		"an empty file": {TransferID: strings.Repeat("c3", 16), Bytes: 0, Total: 0},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.uploadProgress DTO")
		})
	}
}

// The real progress notification off the real socket. The sink holds inside
// Put after reporting, so the frame is guaranteed rather than raced: the
// emitter chooses between the progress mailbox and the transfer's done
// channel, and a transfer that could settle first would legitimately emit
// nothing.
func TestFilesUploadProgress_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadProgress.schema.json")
	sink := newPausingSink()
	e := newUploadTestEnvWithSink(t, sink)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	params := uploadParams(bid, dir, "watched.bin", 4096)
	params["onExists"] = "skip" // the branch that needs no body
	started := callUpload(t, e.conn, params, 3).mustResult(t)

	<-sink.reported
	raw := readNotification(t, e.conn, "files.uploadProgress", wantWithin)
	validateJSON(t, schema, raw, "files.uploadProgress params (real socket)")

	var got filesTransferProgressParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TransferID != started.TransferID {
		t.Errorf("transferId = %q, want %q", got.TransferID, started.TransferID)
	}
	if got.Total != 4096 {
		t.Errorf("total = %d, want the declared size 4096", got.Total)
	}
	sink.release()
	awaitTransferState(t, e.ws, started.TransferID)
}

func TestFilesUploadDone_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadDone.schema.json")
	cases := map[string]filesUploadDoneParams{
		"written": {
			TransferID: strings.Repeat("a1", 16), Outcome: uploadStateWritten,
			FinalName: "report.pdf", Stranded: []string{},
		},
		// keepBoth renamed it, and the backup unlink failed afterwards: a
		// success that still stranded a path (§6).
		"written and stranded": {
			TransferID: strings.Repeat("b2", 16), Outcome: uploadStateWritten,
			FinalName: "report (1).pdf", Stranded: []string{"/srv/.report.pdf.nocx-bak"},
		},
		"skipped": {
			TransferID: strings.Repeat("c3", 16), Outcome: uploadStateSkipped, Stranded: []string{},
		},
		"cancelled carries no error": {
			TransferID: strings.Repeat("d4", 16), Outcome: uploadStateCancelled, Stranded: []string{},
		},
		// The fallback lost its second rename: BOTH paths, which is why
		// stranded is a list.
		"failed": {
			TransferID: strings.Repeat("e5", 16), Outcome: uploadStateFailed,
			Error:    "transfer: promote /srv/report.pdf: connection lost",
			Stranded: []string{"/srv/.report.pdf.nocx-tmp", "/srv/.report.pdf.nocx-bak"},
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.uploadDone DTO")
			// stranded must be [] and never null: a nil slice marshals to
			// null and the renderer's first .map would throw on it. The DTO
			// cases above all pass a non-nil slice, so this is the check
			// that the SERVER never builds one that does not.
			if bytes.Contains(raw, []byte(`"stranded":null`)) {
				t.Errorf("stranded marshalled as null: %s", raw)
			}
		})
	}
}

// The real terminal notification off the real socket, in both of the ways
// it can reach a person: delivered live, and RETAINED across a reconnect
// and flushed on attach. The second is the one an addressing defect hides
// in — an outcome addressed like progress would be emitted into nothing.
func TestFilesUploadDone_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.uploadDone.schema.json")

	t.Run("live, failed with stranded paths", func(t *testing.T) {
		sink := &failingSink{
			err:      errors.New("transfer: promote /srv/f: connection lost"),
			stranded: []string{"/srv/.f.nocx-tmp", "/srv/.f.nocx-bak"},
		}
		e := newUploadTestEnvWithSink(t, sink)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		bid := e.openBinding(t, sid, dir, 2)
		body := []byte("bytes that never land")
		started := callUpload(t, e.conn, uploadParams(bid, dir, "f", int64(len(body))), 3).mustResult(t)
		go postUploadAsync(e.ws, started.Ticket, body)

		raw := readNotification(t, e.conn, "files.uploadDone", wantWithin)
		validateJSON(t, schema, raw, "files.uploadDone params (real socket, live)")
		var got filesUploadDoneParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Outcome != uploadStateFailed || got.Error == "" || len(got.Stranded) != 2 {
			t.Fatalf("got %+v; want a failed outcome carrying its reason and both stranded paths", got)
		}
	})

	t.Run("retained across a reconnect, written", func(t *testing.T) {
		e := newUploadTestEnv(t)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		bid := e.openBinding(t, sid, dir, 2)
		body := []byte("finished while nobody was watching")
		started := callUpload(t, e.conn, uploadParams(bid, dir, "late.txt", int64(len(body))), 3).mustResult(t)

		dropSubscriber(t, e, sid)
		if code, resp := postUpload(t, e.ws, started.Ticket, body); code != http.StatusOK {
			t.Fatalf("POST /upload = %d %q, want 200", code, resp)
		}
		awaitTransferState(t, e.ws, started.TransferID)

		connB := reattach(t, e, sid, 4)
		raw := readNotification(t, connB, "files.uploadDone", wantWithin)
		validateJSON(t, schema, raw, "files.uploadDone params (real socket, flushed on attach)")
		var got filesUploadDoneParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.TransferID != started.TransferID || got.Outcome != uploadStateWritten || got.FinalName != "late.txt" {
			t.Fatalf("got %+v; want the written outcome of %s", got, started.TransferID)
		}
		// The empty case of the null check: a transfer that stranded
		// nothing must still send [].
		if got.Stranded == nil {
			t.Error("stranded is null on the wire — the renderer's first .map would throw")
		}
	})
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
	readLifecycleWhere(t, e.conn, "the hello's prompt_ready fact",
		lifecycleIs(lifecyclepub.LifecyclePromptReady))
	if err := pub.TransportLost("T"); err != nil {
		t.Fatalf("TransportLost: %v", err)
	}
	// The lost fact BY NAME, for the reason readLifecycleWhere gives: the
	// open handler's replay can put a second prompt_ready exactly here.
	_, lost := readLifecycleWhere(t, e.conn, "the lane's lost fact",
		lifecycleIs(lifecyclepub.LifecycleLost))
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
		"domain": string(h.Domain), "command": "make", "cwd": "/srv/app", "host": "build.example.com", "source": "user",
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
	//
	// Asked for BY THE PROPERTY that makes it the outcome, never by position.
	// The axis legitimately re-sends `starting` — the open handler emits the
	// status once after its ack, and registering the axis after `open` has
	// returned races that emit — so "the next session.integrationChanged" is
	// not the same question as "the one that says how it ended", and on the
	// run where the handler loses the race the two have different answers.
	raw, timedOut := readIntegrationWhere(t, e.conn, sid,
		"the fact that concludes the axis", integrationConcluded)
	validateJSON(t, schema, raw, "session.integrationChanged params (real socket, handshake timeout)")
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

func TestAgentAsk_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.ask.schema.json")
	raw, err := json.Marshal(agentAskResponse{
		RunID: 7, EntryID: "ask-1",
		State: string(content.RunPrepared), IngestSeq: 3, Replayed: false,
		Model: "qwen3",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "agent.ask DTO")
}

func TestAgentAsk_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.ask.schema.json")
	h := newAskHarness(t, &scriptedAssistantClient{deltas: []string{"hi"}})
	h.createEndpoint()
	conn := h.conn
	sid := openLocalSession(t, conn)

	resp := jsonrpcCallWithID(t, conn, "agent.ask", map[string]any{
		"askId": "ask-contract", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
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
	raw, err := json.Marshal(agentRunDelta{
		RunID: 7, EntryID: "answer-1", BlockID: "answer-1/text-1", Seq: 0, Text: "hello",
	})
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
	_, errObj := askOverWire(t, conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "q", "cwd": "/repo",
		"attachedContent": []any{},
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
		EnvID: "3f1a", Host: &host, Cwd: "/repo", Kind: "shell", Source: "user",
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
		EnvID: "3f1a", Host: nil, Cwd: "/repo", Kind: "shell", Source: "user",
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
	effect := "observe"
	getCases := map[string]ledgerGetResponse{
		"populated": {
			Entry: entry,
			Edges: []ledgerEdgeWire{{
				From: entry.ID, To: running.ID, Rel: "rerun-of",
				Payload: json.RawMessage(`{"note":"anything"}`),
			}},
			// The causal flow, both kinds it can hold: a tool call, whose
			// effect and resource are its own, and a command the turn ran,
			// which has neither and says so with nulls (nocx-h1l4o).
			Caused: []ledgerCausedWire{
				{
					EntryID: "01924f9c-0000-7000-8000-00000000000a", Position: 0,
					Kind: "action", Source: "assistant", Intent: "files.read", Effect: &effect,
					Resource: &content.GrantScope{Kind: content.ResourcePath, ID: "/repo/go.mod"},
				},
				{
					EntryID: running.ID, Position: 1, Kind: "shell", Source: "assistant",
					Intent: "make watch", Effect: nil, Resource: nil,
				},
			},
			Artifacts: []ledgerArtifactWire{{
				ID: "artifact-1", ExecutionID: 3, MediaType: "text/plain",
				DerivedFrom: nil, State: "sealed", ByteLen: 4096, ChunkCount: 2,
				Pinned: true, Truncated: &truncated, CaptureMethod: "raw-output",
				CaptureVersion: 1, TerminalCols: &cols, TerminalRows: &rows,
				Stream: &stream, ByteOffset: &offset, ByteEnd: &end, Encoding: "utf-8",
				Gaps:    []ledgerGapWire{{Start: 10, End: 20, Reason: "dropped"}},
				Payload: json.RawMessage(`{}`),
			}},
			ProseEvicted: true,
		},
		"an entry with no relations and no captures": {
			Entry: running, Edges: []ledgerEdgeWire{}, Artifacts: []ledgerArtifactWire{},
			Caused: []ledgerCausedWire{}, ProseEvicted: false,
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

	// And an entry that CAUSED something (nocx-h1l4o). The relation is
	// written through the store's own seam because no JSON-RPC method
	// writes one — the backend does, from a fact the renderer never sends
	// — but the read under test is the real handler answering off the real
	// socket, which is the row that matters in contracts/README.md.
	led := db.Ledger()
	action := content.SubmitEntry{
		ID: "01924f9c-0000-7000-8000-0000000000aa", Client: "agent",
		EnvironmentID: localEnvironmentID(), Cwd: "/", Kind: content.EntryAction,
		Intent: "files.read",
		Payload: `{"tool":"files.read","effect":"observe","opensBlock":true,` +
			`"resource":{"kind":"path","id":"/repo/go.mod"}}`,
	}
	if _, err := led.Submit(context.Background(), action); err != nil {
		t.Fatalf("submit the action entry: %v", err)
	}
	for _, caused := range []string{action.ID, "wire-2"} {
		if _, err := led.AddCause(context.Background(), "wire-1", caused); err != nil {
			t.Fatalf("AddCause(%s): %v", caused, err)
		}
	}
	withCauses := vaultCall(t, conn, "ledger.get", map[string]any{"id": "wire-1"}, 12)
	if withCauses.Error != nil {
		t.Fatalf("ledger.get with causes: %+v", withCauses.Error)
	}
	validateJSON(t, getSchema, withCauses.Result, "ledger.get result (an entry that caused two others)")
	var body struct {
		Caused []struct {
			EntryID    string `json:"entryId"`
			Position   int    `json:"position"`
			Kind       string `json:"kind"`
			OpensBlock bool   `json:"opensBlock"`
			Effect     *string
			Resource   *content.GrantScope
		} `json:"caused"`
	}
	if err := json.Unmarshal(withCauses.Result, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Caused) != 2 {
		t.Fatalf("caused off the socket = %+v, want both", body.Caused)
	}
	if body.Caused[0].EntryID != action.ID || body.Caused[0].Position != 0 ||
		body.Caused[0].Kind != "action" {
		t.Fatalf("the first cause off the socket = %+v", body.Caused[0])
	}
	if body.Caused[0].Effect == nil || *body.Caused[0].Effect != "observe" {
		t.Fatalf("the action's effect off the socket = %v, want observe", body.Caused[0].Effect)
	}
	if body.Caused[0].Resource == nil || body.Caused[0].Resource.ID != "/repo/go.mod" {
		t.Fatalf("the action's resource off the socket = %+v", body.Caused[0].Resource)
	}
	// The command the turn ran is not a tool call: both are null, and the
	// renderer draws it as the block it is.
	if body.Caused[1].EntryID != "wire-2" || body.Caused[1].Position != 1 {
		t.Fatalf("the second cause off the socket = %+v", body.Caused[1])
	}
	if body.Caused[1].Effect != nil || body.Caused[1].Resource != nil {
		t.Fatalf("a command came back with tool facts: %+v", body.Caused[1])
	}
	if !body.Caused[0].OpensBlock {
		t.Fatalf("the action's opensBlock off the socket = false, want the stored true")
	}
	if body.Caused[1].OpensBlock {
		t.Fatalf("a command came back saying it opened a block: %+v", body.Caused[1])
	}
}

// The negative for the causal flow: the schema refuses what it must, or the
// `caused` list is a shape nobody is holding to anything.
func TestLedgerGet_ContractRefusesACausedItMustRefuse(t *testing.T) {
	schema := loadSchema(t, "ledger.get.schema.json")
	entry := `{"id":"a","seq":1,"environmentId":"e","host":null,"cwd":"/","kind":"ask","source":"user",` +
		`"intent":"x","phase":"closed","status":"success","submittedAt":1,"startedAt":null,` +
		`"endedAt":null,"durationMs":null,"exitCode":null,"maskedCount":0,"maskedKinds":[],` +
		`"redactions":[]}`
	body := func(caused string) string {
		return `{"entry":` + entry + `,"edges":[],"artifacts":[],"proseEvicted":false,"caused":` + caused + `}`
	}
	// And the shape itself: the schema refuses a ledger.get result that
	// will not say whether the prose of the run is gone — absent and false
	// must not be the same wire state for the one fact a turn's reader
	// cannot re-derive.
	if err := validateJSONErr(schema, []byte(
		`{"entry":`+entry+`,"edges":[],"artifacts":[],"caused":[]}`,
	)); err == nil {
		t.Fatal("the schema accepted a ledger.get result that omits proseEvicted")
	}
	bad := map[string]string{
		"caused as null": body(`null`),
		"a cause with no position": body(
			`[{"entryId":"b","kind":"shell","intent":"ls","effect":null,"resource":null}]`),
		"a negative position": body(
			`[{"entryId":"b","position":-1,"kind":"shell","intent":"ls","effect":null,"resource":null}]`),
		"an effect nobody named": body(
			`[{"entryId":"b","position":0,"kind":"action","intent":"x","effect":"rummage","resource":null}]`),
		"a half-named resource": body(
			`[{"entryId":"b","position":0,"kind":"action","intent":"x","effect":"observe","resource":{"kind":"path"}}]`),
		"an undeclared field on a cause": body(
			`[{"entryId":"b","position":0,"kind":"shell","intent":"ls","effect":null,"resource":null,"parent":"a"}]`),
		// THE ANCHOR IS GONE FROM THE CONTRACT (ADR-0040), so it is refused
		// like any other field nobody declared — additionalProperties does
		// that, and this is the assertion that it really is off the schema
		// rather than merely unset by the server.
		"a cause still carrying the anchor": body(
			`[{"entryId":"b","position":0,"at":0,"kind":"shell","intent":"ls","effect":null,"resource":null,"opensBlock":false}]`),
		// The block fact is required for the reason the position is: a cause
		// that will not say whether it opened a block is one a restored turn
		// cannot draw, and "absent" and "false" must not be the same thing on
		// the wire.
		"a cause that will not say whether it opened a block": body(
			`[{"entryId":"b","position":0,"kind":"shell","intent":"ls","effect":null,"resource":null}]`),
		// The arguments are required for the reason the resource is: two
		// calls of one session-scoped tool have the same tool and the same
		// resource, and a cause that will not say what it ASKED FOR is one a
		// restored turn cannot tell from its neighbour (ADR-0040). A shell
		// child says `null` — it asked for nothing — and that is still
		// saying it.
		"a cause that will not say what it asked for": body(
			`[{"entryId":"b","position":0,"kind":"shell","intent":"ls","effect":null,"resource":null,"opensBlock":false}]`),
		"arguments that are not an object": body(
			`[{"entryId":"b","position":0,"kind":"action","intent":"x","args":"{\"a\":1}","effect":"observe","resource":null,"opensBlock":false}]`),
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if err := validateJSONErr(schema, []byte(raw)); err == nil {
				t.Fatalf("the schema accepted %s — additionalProperties/required is not doing its job", name)
			}
		})
	}
	// And the shape it must ACCEPT, so the refusals above are a contract
	// and not an accident of a schema that refuses everything.
	ok := body(`[{"entryId":"b","position":0,"kind":"action","source":"assistant","intent":"files.read",` +
		`"args":{"path":"/repo/go.mod"},` +
		`"effect":"observe","resource":{"kind":"path","id":"/repo/go.mod"},"opensBlock":false}]`)
	if err := validateJSONErr(schema, []byte(ok)); err != nil {
		t.Fatalf("the schema refused a well-formed cause: %v", err)
	}
	// And a run of prose, which is a child like any other since ADR-0040: it
	// names no intent, has no effect and no resource, and opens no block. The
	// server sends these now, so a schema that could not express one would be
	// a contract the wire cannot keep.
	prose := body(`[{"entryId":"t","position":1,"kind":"text","source":"assistant","intent":"","args":null,` +
		`"effect":null,"resource":null,"opensBlock":false}]`)
	if err := validateJSONErr(schema, []byte(prose)); err != nil {
		t.Fatalf("the schema refused a run of prose: %v", err)
	}
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

// ── session.signal ─────────────────────────────────────────────────────────

// The DTO's own conformance: the two enums' spelling and the tags. Every
// (signal, outcome) pair the handler can produce, because the field the
// renderer branches on is the outcome and a word it has never seen is a
// branch it does not have.
func TestSessionSignal_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.signal.schema.json")
	for _, sig := range []string{signalInterrupt, signalStop} {
		for _, outcome := range []string{
			string(foregroundDelivered), string(foregroundNothingRunning), "unsupported",
		} {
			raw, err := json.Marshal(signalResult{Signal: sig, Outcome: outcome})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, fmt.Sprintf("session.signal DTO (%s/%s)", sig, outcome))
		}
	}
}

// And the negative, which is what makes the pair above evidence: the schema
// REFUSES an outcome nobody declared, a missing field and an undeclared one.
func TestSessionSignal_ContractRefusesWhatItMustRefuse(t *testing.T) {
	schema := loadSchema(t, "session.signal.schema.json")
	bad := map[string]string{
		"an outcome nobody named": `{"signal":"stop","outcome":"maybe"}`,
		"a signal nobody named":   `{"signal":"sighup","outcome":"delivered"}`,
		"the outcome missing":     `{"signal":"stop"}`,
		"the signal missing":      `{"outcome":"delivered"}`,
		"an undeclared field":     `{"signal":"stop","outcome":"delivered","pid":4242}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if err := validateJSONErr(schema, []byte(raw)); err == nil {
				t.Fatalf("the contract accepted %s: %s", name, raw)
			}
		})
	}
}

// THE REAL RESULT OFF THE REAL SOCKET — the row contracts/README.md says is
// the reason the directory exists. A payload this test built would prove the
// struct is well-formed; only driving the method through the socket proves
// the handler sends it.
//
// Both outcomes a local session can produce are driven here, in one
// connection, because a contract satisfied by only the happy shape is a
// contract with an untested half: the refusal is the one a person meets when
// their command has just finished, and it travels in the result rather than
// as an error precisely so the renderer can read it.
func TestSessionSignal_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.signal.schema.json")
	conn, tap := signalServer(t)
	sid := openSessionTapped(t, conn, tap)

	// At a prompt: nothing to signal. The shell echoing its own output is
	// what says the prompt is there — never a sleep.
	submitCommand(t, conn, sid, "printf %s%s CONTRACT -PROMPT")
	tapDataFor(t, tap, sid, "CONTRACT-PROMPT", 20*time.Second)
	atPrompt := tapCall(t, conn, tap, 21, "session.signal", map[string]any{
		"sessionId": sid, "signal": signalInterrupt,
	})
	validateJSON(t, schema, resultOf(t, atPrompt), "session.signal result at a prompt (real socket)")

	// And with a job in the foreground: delivered.
	submitCommand(t, conn, sid, "sh -c 'printf %s%s CONTRACT -RUNNING; sleep 300'")
	tapDataFor(t, tap, sid, "CONTRACT-RUNNING", 20*time.Second)
	running := tapCall(t, conn, tap, 22, "session.signal", map[string]any{
		"sessionId": sid, "signal": signalStop,
	})
	validateJSON(t, schema, resultOf(t, running), "session.signal result over a running command (real socket)")
}

// resultOf pulls the `result` out of a JSON-RPC response envelope, failing
// on an error object — so a schema assertion can never silently validate
// nothing.
func resultOf(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("the method answered an error: %+v", env.Error)
	}
	if len(env.Result) == 0 {
		t.Fatalf("the method answered no result: %s", raw)
	}
	return env.Result
}

// ── api.* (the API-testing collection) ──────────────────────────────────
//
// Two boundaries are tested here and they are not the same boundary. The
// DTO tests pin what the transport's own wire structs marshal to; the
// over-the-wire tests drive the real method through the real socket against
// a real folder on disk and a real HTTP server, which is the only test that
// can report a field the handler never sends.
//
// The third test in this block is neither: it asserts that no api.* method
// except collections.open and import.postman will ACCEPT a path at all
// (design §13.1). That rule is what makes the backend-held handle mean
// something, and a rule nobody can fail is a rule nobody keeps.

// apiFakeBindings is an in-memory apibind.Store for the import test. The
// real one does not exist yet (internal/apibind declares the interface
// ahead of its implementation), and api.import.postman writes secret values
// through it, so the test supplies the seam the composition root will.
type apiFakeBindings struct {
	mu    sync.Mutex
	bound map[apibind.Key]string
	// bindErr, when set, is what Bind returns — the failure half of
	// "every external call has a test where it fails".
	bindErr error
}

func newAPIFakeBindings() *apiFakeBindings {
	return &apiFakeBindings{bound: map[apibind.Key]string{}}
}

func (b *apiFakeBindings) Lookup(k apibind.Key) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.bound[k]
	return v, ok, nil
}

func (b *apiFakeBindings) Bind(_ context.Context, k apibind.Key, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bindErr != nil {
		return b.bindErr
	}
	b.bound[k] = string(value)
	return nil
}

func (b *apiFakeBindings) Unbind(_ context.Context, k apibind.Key) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.bound, k)
	return nil
}

func (b *apiFakeBindings) UnbindCollection(_ context.Context, collection string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.bound {
		if k.Collection == collection {
			delete(b.bound, k)
		}
	}
	return nil
}

func (b *apiFakeBindings) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bound)
}

// newAPIWSServer starts a server with the whole api.* surface wired, the way
// the composition root wires it plus the binding store that does not exist
// yet.
//
// The import fetcher gets a route table with NO leaser, which is the shape
// the app has whenever the SSH side is not wired: the direct route dials and
// every connection route refuses by name. A test that needs a connection
// route to carry bytes supplies its own pool through the variant below.
func newAPIWSServer(t *testing.T, bindings apibind.Store) (*WSServer, *websocket.Conn) {
	t.Helper()
	return newAPIWSServerWithPool(t, bindings, nil)
}

// newAPIWSServerWithPool is the same server with the import fetcher's route
// table built over the caller's pool. Only the POOL is a double — the
// fetcher, the route table, the transport, the capability and the writer are
// all the real ones, because a fetcher double would certify the test's own
// object instead of the seam.
func newAPIWSServerWithPool(t *testing.T, bindings apibind.Store, leaser apisend.ConnectionLeaser) (*WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	opts := []WSServerOption{
		WithAPI(apicoll.NewCollections(apiTestPaths(t)), apisend.New(apisend.WithLogger(logger))),
		WithAPIImportFetcher(apifetch.New(apisend.NewRoutes(leaser), logger)),
	}
	if bindings != nil {
		opts = append(opts, WithAPIBindings(bindings))
		// The read half is a separate option because the two halves wire
		// apart (ws.go). A store that has one gets it; apiFakeBindings does
		// not, which is how the import tests keep exercising a build where
		// an auth variable resolves to nothing.
		if values, ok := bindings.(apibind.ValueResolver); ok {
			opts = append(opts, WithAPIVariables(values))
		}
	}
	ws := NewWSServer(logger, newRegWithStub(logger), opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, connectWS(t, ws)
}

// apiCollectionFolder builds a real collection on disk: a manifest and two
// request files, one of which points at url.
func apiCollectionFolder(t *testing.T, url string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	write("ping.json", `{"id":"r1","name":"ping","method":"GET","url":"`+url+`",`+
		`"headers":[{"name":"X-Probe","value":"1","enabled":true}],"query":[],`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("nested/echo.json", `{"id":"r2","name":"echo","method":"POST","url":"`+url+`",`+
		`"body":{"kind":"raw","text":"hello"},"auth":{"kind":"none"}}`)
	return root
}

// openAPICollection opens root over the socket and returns the handle.
func openAPICollection(t *testing.T, conn *websocket.Conn, root string, id int) string {
	t.Helper()
	resp := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, id)
	if resp.Error != nil {
		t.Fatalf("api.collections.open: %+v", resp.Error)
	}
	var got struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal open result: %v", err)
	}
	if got.Handle == "" {
		t.Fatal("api.collections.open returned an empty handle")
	}
	return got.Handle
}

func TestAPICollectionsOpen_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.collections.open.schema.json")

	cases := map[string]apiOpenResponse{
		"populated": {
			Handle: "0123456789abcdef0123456789abcdef",
			Collection: apiCollectionWire{
				Name: "acme",
				Requests: []apiRequestRefWire{
					{RelPath: "ping.json", Name: "ping", Method: "GET"},
				},
				// A folder holding a request and one holding nothing: the
				// second is the case this list exists for.
				Folders:         []string{"v1", "v1/reports"},
				VariableFolders: []string{},
				Malformed: []apiMalformedRefWire{
					{RelPath: "broken.json", Reason: "invalid JSON"},
					{RelPath: "environments/bad.json", Reason: "not a regular file; symlinks are not followed"},
				},
				Environments: []apiEnvironmentRefWire{
					// The NAME differs from the file's stem on purpose: the
					// two are separate facts and only the file knows the
					// first, which is why the ref carries both.
					{RelPath: "environments/default.json", Name: "prod"},
				},
			},
		},
		// The empty folder: every list is [] and never null — the
		// renderer's first .map assumes it (nocx-25k9.14 class).
		"empty": {
			Handle: "0123456789abcdef0123456789abcdef",
			Collection: apiCollectionWire{
				Name:            "acme",
				Requests:        []apiRequestRefWire{},
				Folders:         []string{},
				VariableFolders: []string{},
				Malformed:       []apiMalformedRefWire{},
				Environments:    []apiEnvironmentRefWire{},
			},
		},
		// The folder that was ALREADY open. It is a case here because the
		// field is a bool and false is its zero value: a struct that had
		// dropped it would marshal indistinguishably from the two above,
		// and the schema would go on accepting them.
		"already open": {
			Handle:      "0123456789abcdef0123456789abcdef",
			AlreadyOpen: true,
			Collection: apiCollectionWire{
				Name:            "acme",
				Requests:        []apiRequestRefWire{},
				Folders:         []string{},
				VariableFolders: []string{},
				Malformed:       []apiMalformedRefWire{},
				Environments:    []apiEnvironmentRefWire{},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.collections.open DTO")
		})
	}
}

// api.collections.create answers the handle and collection
// api.collections.open does — a create leaves the collection open — through
// the same assembler, validated against its own schema. What it does not
// carry is alreadyOpen: a folder minted a moment ago cannot have been open,
// and the schema's additionalProperties:false is what holds the two results
// apart on that one field. The interesting case is the one the method
// actually produces: an empty collection, whose two lists must be [] and
// never null.
func TestAPICollectionsCreate_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.collections.create.schema.json")

	cases := map[string]apiCreateResponse{
		"a collection just made": {
			Handle: "0123456789abcdef0123456789abcdef",
			Collection: apiCollectionWire{
				Name:            "acme",
				Requests:        []apiRequestRefWire{},
				Folders:         []string{},
				VariableFolders: []string{},
				Malformed:       []apiMalformedRefWire{},
				// Create makes `environments/` and leaves it empty, so a
				// freshly minted collection carries [] rather than null.
				Environments: []apiEnvironmentRefWire{},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.collections.create DTO")
		})
	}
}

// The real result off the real socket. A test that validates a payload the
// test itself built proves the struct is well-formed, not that the server
// sends it — which is the whole reason contracts/ exists.
func TestAPICollectionsCreate_OverTheWireConformsToContract(t *testing.T) {
	createSchema := loadSchema(t, "api.collections.create.schema.json")
	listSchema := loadSchema(t, "api.collections.list.schema.json")

	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	resp := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if resp.Error != nil {
		t.Fatalf("api.collections.create: %+v", resp.Error)
	}
	validateJSON(t, createSchema, resp.Result, "api.collections.create result")

	var made apiOpenResponse
	if err := json.Unmarshal(resp.Result, &made); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if made.Collection.Name != "acme" {
		t.Errorf("name = %q, want acme", made.Collection.Name)
	}

	// And it is open: the listing carries it, which is the difference
	// between "a folder was written" and "the user has a collection".
	listed := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if listed.Error != nil {
		t.Fatalf("api.collections.list: %+v", listed.Error)
	}
	validateJSON(t, listSchema, listed.Result, "api.collections.list after create")
	var list apiCollectionsListResponse
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	chosen := chosenCollections(list.Collections)
	if len(chosen) != 1 || chosen[0].Handle != made.Handle {
		t.Fatalf("opened folders = %+v, want the collection just created", chosen)
	}
}

// api.collections.createFolder carries the folder that was made AND the
// collection it is in, so the two shapes are validated together. The
// interesting case is the empty one: a folder made a second ago holds
// nothing, and `folders` is the only field in which it exists at all.
func TestAPICollectionsCreateFolder_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.collections.createFolder.schema.json")

	cases := map[string]apiCollectionsCreateFolderResponse{
		"a folder at the root": {
			RelPath: "users",
			Collection: apiCollectionWire{
				Name:            "acme",
				Requests:        []apiRequestRefWire{},
				Folders:         []string{"users"},
				VariableFolders: []string{},
				Malformed:       []apiMalformedRefWire{},
				Environments:    []apiEnvironmentRefWire{},
			},
		},
		"a folder inside a folder, beside a request": {
			RelPath: "v1/users",
			Collection: apiCollectionWire{
				Name:            "acme",
				Requests:        []apiRequestRefWire{{RelPath: "ping.json", Name: "ping", Method: "GET"}},
				Folders:         []string{"v1", "v1/users"},
				VariableFolders: []string{},
				Malformed:       []apiMalformedRefWire{},
				Environments:    []apiEnvironmentRefWire{},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.collections.createFolder DTO")
		})
	}
}

// The real result off the real socket, and then the real listing: a folder
// the tree cannot see is a folder that does not exist as far as a person is
// concerned, so the assertion is not that the call succeeded but that
// api.collections.list carries it afterwards.
func TestAPICollectionsCreateFolder_OverTheWireConformsToContract(t *testing.T) {
	folderSchema := loadSchema(t, "api.collections.createFolder.schema.json")
	listSchema := loadSchema(t, "api.collections.list.schema.json")

	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	created := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if created.Error != nil {
		t.Fatalf("api.collections.create: %+v", created.Error)
	}
	var made apiOpenResponse
	if err := json.Unmarshal(created.Result, &made); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if made.Collection.Folders == nil || len(made.Collection.Folders) != 0 {
		t.Errorf("a new collection's folders = %v, want []", made.Collection.Folders)
	}

	resp := vaultCall(t, conn, "api.collections.createFolder",
		map[string]any{"handle": made.Handle, "name": "users"}, 2)
	if resp.Error != nil {
		t.Fatalf("api.collections.createFolder: %+v", resp.Error)
	}
	validateJSON(t, folderSchema, resp.Result, "api.collections.createFolder result")

	var folder apiCollectionsCreateFolderResponse
	if err := json.Unmarshal(resp.Result, &folder); err != nil {
		t.Fatalf("unmarshal createFolder result: %v", err)
	}
	if folder.RelPath != "users" {
		t.Errorf("relPath = %q, want users", folder.RelPath)
	}

	// One inside it, named by the relPath the call before handed back —
	// which is how nesting is spelled on this surface.
	nested := vaultCall(t, conn, "api.collections.createFolder",
		map[string]any{"handle": made.Handle, "parentRelPath": folder.RelPath, "name": "admin"}, 3)
	if nested.Error != nil {
		t.Fatalf("api.collections.createFolder nested: %+v", nested.Error)
	}
	validateJSON(t, folderSchema, nested.Result, "api.collections.createFolder nested result")
	var deeper apiCollectionsCreateFolderResponse
	if err := json.Unmarshal(nested.Result, &deeper); err != nil {
		t.Fatalf("unmarshal nested result: %v", err)
	}
	if deeper.RelPath != "users/admin" {
		t.Errorf("nested relPath = %q, want users/admin", deeper.RelPath)
	}

	// And the listing every surface actually reads carries both, with
	// nothing in either of them.
	listed := vaultCall(t, conn, "api.collections.list", map[string]any{}, 4)
	if listed.Error != nil {
		t.Fatalf("api.collections.list: %+v", listed.Error)
	}
	validateJSON(t, listSchema, listed.Result, "api.collections.list after createFolder")
	var list apiCollectionsListResponse
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	chosen := chosenCollections(list.Collections)
	if len(chosen) != 1 {
		t.Fatalf("opened folders = %+v, want the one collection", chosen)
	}
	got := map[string]bool{}
	for _, f := range chosen[0].Collection.Folders {
		got[f] = true
	}
	for _, want := range []string{"users", "users/admin"} {
		if !got[want] {
			t.Errorf("api.collections.list folders = %v, want %q among them",
				chosen[0].Collection.Folders, want)
		}
	}
	if len(chosen[0].Collection.Requests) != 0 {
		t.Errorf("requests = %+v, want none — the folders are visible because they are listed",
			chosen[0].Collection.Requests)
	}
}

// The refusals, each with the pair that succeeds on the same socket
// (AGENTS.md testing rule 3). A name that is not one path component is
// refused BY NAME rather than sanitised — a folder quietly created under a
// name the person did not type is a surface reporting something it did not
// do — and every one of them is the caller's error, so every one is -32602.
func TestAPICollectionsCreateFolder_RefusesOverTheWire(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	created := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if created.Error != nil {
		t.Fatalf("api.collections.create: %+v", created.Error)
	}
	var made apiOpenResponse
	if err := json.Unmarshal(created.Result, &made); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}

	id := 10
	refused := func(what string, params map[string]any) {
		t.Helper()
		id++
		resp := vaultCall(t, conn, "api.collections.createFolder", params, id)
		if resp.Error == nil {
			t.Errorf("%s was accepted, want a refusal", what)
			return
		}
		if resp.Error.Code != -32602 {
			t.Errorf("%s answered %d, want -32602 — the caller's move is to name something else",
				what, resp.Error.Code)
		}
		if resp.Error.Message == "" {
			t.Errorf("%s answered no sentence; a surface has nothing to show", what)
		}
	}

	refused("a name that is a path", map[string]any{"handle": made.Handle, "name": "a/b"})
	refused("a traversal", map[string]any{"handle": made.Handle, "name": ".."})
	refused("an empty name", map[string]any{"handle": made.Handle, "name": ""})
	refused("a name that is only dots", map[string]any{"handle": made.Handle, "name": "..."})
	refused("the environments directory", map[string]any{"handle": made.Handle, "name": "environments"})
	refused("a parent that is not there",
		map[string]any{"handle": made.Handle, "parentRelPath": "nope", "name": "users"})
	refused("a parent outside the collection",
		map[string]any{"handle": made.Handle, "parentRelPath": "../..", "name": "users"})
	refused("a handle nobody minted",
		map[string]any{"handle": "0123456789abcdef0123456789abcdef", "name": "users"})

	// And on an ordinary machine it succeeds — twice for one name is the
	// only refusal left, and it is a refusal rather than a merge.
	id++
	ok := vaultCall(t, conn, "api.collections.createFolder",
		map[string]any{"handle": made.Handle, "name": "users"}, id)
	if ok.Error != nil {
		t.Fatalf("an ordinary folder name was refused: %+v", ok.Error)
	}
	refused("a name that is already taken", map[string]any{"handle": made.Handle, "name": "users"})
}

// api.request.move answers ONE string: the request's new path. The DTO's
// conformance is a single populated case — there is exactly one field, and
// every interesting thing about it is "it is there and it is the path the
// move landed at", which the shape itself says (apiMethodErrorCode owns the
// refusal half).
func TestAPIRequestMove_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.move.schema.json")

	cases := map[string]apiRequestMoveResponse{
		"into a folder":       {RelPath: "users/ping.json"},
		"back to the root":    {RelPath: "ping.json"},
		"a deeply nested one": {RelPath: "v1/users/admin/ping.json"},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.request.move DTO")
		})
	}
}

// The real method through the real socket, against a real folder: the file
// is written, moved, and read back at the new path — the address the result
// handed back is the address that WORKS, which is what "the result carries
// the new relPath" has to mean. The bytes at the new path are the bytes at
// the old, and the old path no longer answers.
func TestAPIRequestMove_OverTheWireConformsToContract(t *testing.T) {
	moveSchema := loadSchema(t, "api.request.move.schema.json")

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	// A folder to move into, made the way the product makes one.
	folder := vaultCall(t, conn, "api.collections.createFolder",
		map[string]any{"handle": handle, "name": "users"}, 2)
	if folder.Error != nil {
		t.Fatalf("api.collections.createFolder: %+v", folder.Error)
	}

	moved := vaultCall(t, conn, "api.request.move",
		map[string]any{"handle": handle, "relPath": "ping.json", "toRelPath": "users/ping.json"}, 3)
	if moved.Error != nil {
		t.Fatalf("api.request.move: %+v", moved.Error)
	}
	validateJSON(t, moveSchema, moved.Result, "api.request.move result")
	var got apiRequestMoveResponse
	if err := json.Unmarshal(moved.Result, &got); err != nil {
		t.Fatalf("unmarshal move result: %v", err)
	}
	if got.RelPath != "users/ping.json" {
		t.Errorf("relPath = %q, want users/ping.json", got.RelPath)
	}

	// The path the result carried is a path that READS. The request that
	// was at the root answers at the folder now, bytes and all.
	read := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": handle, "relPath": "users/ping.json"}, 4)
	if read.Error != nil {
		t.Fatalf("api.request.read at the new path: %+v", read.Error)
	}
	var back apiRequestReadResponse
	if err := json.Unmarshal(read.Result, &back); err != nil {
		t.Fatalf("unmarshal read result: %v", err)
	}
	if back.Request.Name != "ping" || back.Request.URL != "https://example.test/ping" {
		t.Errorf("read at the new path = %+v, want the request that was at the root", back.Request)
	}

	// And the old path no longer answers: a move leaves exactly one place
	// the file is.
	gone := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": handle, "relPath": "ping.json"}, 5)
	if gone.Error == nil {
		t.Fatal("api.request.read still answers at the OLD path after the move; the request is at both")
	}

	// And back to the root, which is the other direction a person moves.
	backMove := vaultCall(t, conn, "api.request.move",
		map[string]any{"handle": handle, "relPath": "users/ping.json", "toRelPath": "ping.json"}, 6)
	if backMove.Error != nil {
		t.Fatalf("api.request.move back to the root: %+v", backMove.Error)
	}
	validateJSON(t, moveSchema, backMove.Result, "api.request.move result (back to the root)")
	var backGot apiRequestMoveResponse
	if err := json.Unmarshal(backMove.Result, &backGot); err != nil {
		t.Fatalf("unmarshal move result: %v", err)
	}
	if backGot.RelPath != "ping.json" {
		t.Errorf("relPath = %q, want ping.json", backGot.RelPath)
	}
}

// A move that CANNOT happen is refused over the wire, each by the sentence
// a surface can show, and each paired with the success it belongs to in the
// happy-path test above (AGENTS.md testing rule 3). A collision is the one
// that matters most: the whole reason the move is a no-replace rename.
func TestAPIRequestMove_RefusesOverTheWire(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	if fe := vaultCall(t, conn, "api.collections.createFolder",
		map[string]any{"handle": handle, "name": "users"}, 2); fe.Error != nil {
		t.Fatalf("createFolder: %+v", fe.Error)
	}
	// A second file colliding with the move.
	if we := vaultCall(t, conn, "api.request.write", map[string]any{
		"handle": handle, "relPath": "users/ping.json",
		"request": map[string]any{
			"id": "r9", "name": "other", "method": "GET",
			"url": "https://example.test/other", "body": map[string]any{"kind": "none"},
			"auth": map[string]any{"kind": "none"},
		},
	}, 3); we.Error != nil {
		t.Fatalf("write users/ping: %+v", we.Error)
	}

	id := 10
	refused := func(what string, params map[string]any) {
		t.Helper()
		id++
		resp := vaultCall(t, conn, "api.request.move", params, id)
		if resp.Error == nil {
			t.Errorf("%s was accepted, want a refusal", what)
			return
		}
		if resp.Error.Code != -32602 {
			t.Errorf("%s answered %d, want -32602 — the caller's move is to name something else",
				what, resp.Error.Code)
		}
		if resp.Error.Message == "" {
			t.Errorf("%s answered no sentence; a surface has nothing to show", what)
		}
	}

	refused("a destination that already holds a file",
		map[string]any{"handle": handle, "relPath": "ping.json", "toRelPath": "users/ping.json"})
	refused("a destination folder that is not there",
		map[string]any{"handle": handle, "relPath": "ping.json", "toRelPath": "nope/ping.json"})
	refused("a destination outside the collection",
		map[string]any{"handle": handle, "relPath": "ping.json", "toRelPath": "../ping.json"})
	refused("a source that is not there",
		map[string]any{"handle": handle, "relPath": "ghost.json", "toRelPath": "users/ghost.json"})
	refused("a handle nobody minted",
		map[string]any{
			"handle":  "0123456789abcdef0123456789abcdef",
			"relPath": "ping.json", "toRelPath": "users/ping.json",
		})

	// And on an ordinary machine the same move succeeds — the pair that
	// proves the refusals are the method's and not this build's.
	ok := vaultCall(t, conn, "api.request.move",
		map[string]any{"handle": handle, "relPath": "ping.json", "toRelPath": "users/other-ping.json"}, id+1)
	if ok.Error != nil {
		t.Fatalf("an ordinary move was refused: %+v", ok.Error)
	}
}

func TestAPIRequestRead_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.read.schema.json")

	cases := map[string]apiRequestReadResponse{
		"populated": {Request: apiRequestWire{
			ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/{{path}}",
			Headers: []apiHeaderWire{{Name: "X-Probe", Value: "1", Enabled: true}},
			Query:   []apiParamWire{{Name: "q", Value: "{{term}}", Enabled: false}},
			// The request's OWN variables — the third list, carried like the
			// other two, including a disabled row: one the person keeps and
			// has switched off, which resolves nothing.
			Variables: []apiParamWire{
				{Name: "path", Value: "users", Enabled: true},
				{Name: "term", Value: "unused", Enabled: false},
			},
			Body: apiBodyWire{Kind: "raw", Text: "hello"},
			Auth: apiAuthWire{Kind: "bearer", Token: "{{token}}"},
		}},
		// A request with no headers, no query and no variables: all three
		// are [], never null.
		"bare": {Request: apiRequestWire{
			ID: "r2", Name: "bare", Method: "GET", URL: "https://example.test",
			Headers: []apiHeaderWire{}, Query: []apiParamWire{}, Variables: []apiParamWire{},
			Body: apiBodyWire{Kind: "none"}, Auth: apiAuthWire{Kind: "none"},
		}},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.request.read DTO")
		})
	}
}

// The whole collection surface through the real socket, against a real
// folder: open, list, read, write, read back, close. Nothing here names a
// field of the result, so nothing here can omit one — the schema's
// additionalProperties:false plus required makes the key set exact in both
// directions.
func TestAPICollections_OverTheWireConformsToContract(t *testing.T) {
	openSchema := loadSchema(t, "api.collections.open.schema.json")
	listSchema := loadSchema(t, "api.collections.list.schema.json")
	readSchema := loadSchema(t, "api.request.read.schema.json")
	writeSchema := loadSchema(t, "api.request.write.schema.json")
	closeSchema := loadSchema(t, "api.collections.close.schema.json")

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")

	// The empty list, before anything is opened: [] and never null.
	empty := vaultCall(t, conn, "api.collections.list", map[string]any{}, 1)
	if empty.Error != nil {
		t.Fatalf("api.collections.list on a fresh server: %+v", empty.Error)
	}
	validateJSON(t, listSchema, empty.Result, "api.collections.list result (nothing open)")

	openResp := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 2)
	if openResp.Error != nil {
		t.Fatalf("api.collections.open: %+v", openResp.Error)
	}
	validateJSON(t, openSchema, openResp.Result, "api.collections.open result")

	var opened apiOpenResponse
	if err := json.Unmarshal(openResp.Result, &opened); err != nil {
		t.Fatalf("unmarshal open result: %v", err)
	}
	if opened.Collection.Name != "acme" {
		t.Errorf("collection name = %q, want acme", opened.Collection.Name)
	}
	if len(opened.Collection.Requests) != 2 {
		t.Errorf("requests = %+v, want the two request files", opened.Collection.Requests)
	}
	if opened.AlreadyOpen {
		t.Error("the first open of a folder answered alreadyOpen:true")
	}

	listResp := vaultCall(t, conn, "api.collections.list", map[string]any{}, 3)
	if listResp.Error != nil {
		t.Fatalf("api.collections.list: %+v", listResp.Error)
	}
	validateJSON(t, listSchema, listResp.Result, "api.collections.list result (one open)")
	var listed apiCollectionsListResponse
	if err := json.Unmarshal(listResp.Result, &listed); err != nil {
		t.Fatalf("unmarshal list result: %v", err)
	}
	chosen := chosenCollections(listed.Collections)
	if len(chosen) != 1 || chosen[0].Handle != opened.Handle {
		t.Fatalf("opened-folder list = %+v, want the one folder just opened", chosen)
	}
	if chosen[0].Path != root {
		t.Errorf("listed path = %q, want %q", chosen[0].Path, root)
	}

	// THE SAME FOLDER, NAMED A SECOND WAY, off the real socket. This is the
	// sequence that put one collection in the tree twice under two handles:
	// the importer opens its destination, the person then reaches for "Open
	// a collection folder…" out of habit, and the two spellings of the path
	// need not match (nocx-ghuq3). One folder has one handle, the list does
	// not grow, and the RESULT says which of the two things happened so the
	// renderer need not read it off the tree.
	againResp := vaultCall(t, conn, "api.collections.open",
		map[string]any{"path": root}, 10)
	if againResp.Error != nil {
		t.Fatalf("api.collections.open of a folder already open: %+v", againResp.Error)
	}
	validateJSON(t, openSchema, againResp.Result, "api.collections.open result (already open)")
	var again apiOpenResponse
	if err := json.Unmarshal(againResp.Result, &again); err != nil {
		t.Fatalf("unmarshal the second open: %v", err)
	}
	if again.Handle != opened.Handle {
		t.Errorf("re-opening minted %q beside %q; one folder has one handle", again.Handle, opened.Handle)
	}
	if !again.AlreadyOpen {
		t.Error("re-opening answered alreadyOpen:false; a surface cannot then tell an open from an already-open")
	}
	if again.Collection.Name != opened.Collection.Name || len(again.Collection.Requests) != 2 {
		t.Errorf("the second open answered %+v, want the same collection", again.Collection)
	}
	sameList := vaultCall(t, conn, "api.collections.list", map[string]any{}, 11)
	if sameList.Error != nil {
		t.Fatalf("api.collections.list after a re-open: %+v", sameList.Error)
	}
	validateJSON(t, listSchema, sameList.Result, "api.collections.list result (after a re-open)")
	var listedAgain apiCollectionsListResponse
	if err := json.Unmarshal(sameList.Result, &listedAgain); err != nil {
		t.Fatalf("unmarshal list after a re-open: %v", err)
	}
	if still := chosenCollections(listedAgain.Collections); len(still) != 1 || still[0].Handle != opened.Handle {
		t.Errorf("opened-folder list after a re-open = %+v, want the one folder under the one handle", still)
	}

	// And a DIFFERENT path that leads to the same folder. The wire refuses a
	// path that is not already clean and absolute (validateAPIFolderPath),
	// so a symlink is the spelling that actually reaches this method twice —
	// and it is the one a person hits, since a dialog answers with whatever
	// name they walked in by.
	link := filepath.Join(t.TempDir(), "acme-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	byLink := vaultCall(t, conn, "api.collections.open", map[string]any{"path": link}, 12)
	if byLink.Error != nil {
		t.Fatalf("api.collections.open through a symlink: %+v", byLink.Error)
	}
	validateJSON(t, openSchema, byLink.Result, "api.collections.open result (a second name)")
	var linked apiOpenResponse
	if err := json.Unmarshal(byLink.Result, &linked); err != nil {
		t.Fatalf("unmarshal the open through a symlink: %v", err)
	}
	if linked.Handle != opened.Handle || !linked.AlreadyOpen {
		t.Errorf("opening %q answered handle %q alreadyOpen=%v, want the handle that exists and true — "+
			"two names for one directory are one collection", link, linked.Handle, linked.AlreadyOpen)
	}
	// WHERE A NEW COLLECTION WOULD GO, off the real socket. The renderer
	// proposes an import destination from it, so a listing that answered ""
	// on a build that HAS an app directory is an ask back to demanding an
	// absolute path with nothing in it (nocx-6hg2w.14).
	if listed.DefaultRoot == "" {
		t.Error("defaultRoot is empty on a server built with an app directory")
	}
	if filepath.Base(listed.DefaultRoot) != apicoll.DefaultCollectionsDirName {
		t.Errorf("defaultRoot = %q, want the collections directory under the data dir", listed.DefaultRoot)
	}

	readResp := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": opened.Handle, "relPath": "ping.json"}, 4)
	if readResp.Error != nil {
		t.Fatalf("api.request.read: %+v", readResp.Error)
	}
	validateJSON(t, readSchema, readResp.Result, "api.request.read result")
	var read apiRequestReadResponse
	if err := json.Unmarshal(readResp.Result, &read); err != nil {
		t.Fatalf("unmarshal read result: %v", err)
	}
	if read.Request.Name != "ping" || read.Request.Method != "GET" {
		t.Errorf("request = %+v, want the ping request", read.Request)
	}

	// Write it back with a changed name, and read it back changed: the
	// user's edit reaches disk and comes back, which is what the pane does.
	edited := read.Request
	edited.Name = "ping renamed"
	writeResp := vaultCall(t, conn, "api.request.write", map[string]any{
		"handle": opened.Handle, "relPath": "ping.json", "request": edited,
	}, 5)
	if writeResp.Error != nil {
		t.Fatalf("api.request.write: %+v", writeResp.Error)
	}
	validateJSON(t, writeSchema, writeResp.Result, "api.request.write result")

	reread := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": opened.Handle, "relPath": "ping.json"}, 6)
	if reread.Error != nil {
		t.Fatalf("api.request.read after write: %+v", reread.Error)
	}
	var back apiRequestReadResponse
	if err := json.Unmarshal(reread.Result, &back); err != nil {
		t.Fatalf("unmarshal reread: %v", err)
	}
	if back.Request.Name != "ping renamed" {
		t.Errorf("name after write = %q, want %q — the edit did not reach disk", back.Request.Name, "ping renamed")
	}

	closeResp := vaultCall(t, conn, "api.collections.close", map[string]any{"handle": opened.Handle}, 7)
	if closeResp.Error != nil {
		t.Fatalf("api.collections.close: %+v", closeResp.Error)
	}
	validateJSON(t, closeSchema, closeResp.Result, "api.collections.close result")

	// The closing event, asserted: the handle stops working the moment the
	// collection is closed, and the folder leaves the list.
	afterClose := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": opened.Handle, "relPath": "ping.json"}, 8)
	if afterClose.Error == nil {
		t.Fatal("api.request.read answered on a CLOSED handle; a closed collection must refuse")
	}
	gone := vaultCall(t, conn, "api.collections.list", map[string]any{}, 9)
	if gone.Error != nil {
		t.Fatalf("api.collections.list after close: %+v", gone.Error)
	}
	validateJSON(t, listSchema, gone.Result, "api.collections.list result (after close)")
	var empties apiCollectionsListResponse
	if err := json.Unmarshal(gone.Result, &empties); err != nil {
		t.Fatalf("unmarshal list after close: %v", err)
	}
	if left := chosenCollections(empties.Collections); len(left) != 0 {
		t.Errorf("opened-folder list after close = %+v, want empty", left)
	}
}

// The failure half. Every one of these is a real external condition — a
// folder that is not a collection, a handle nobody minted, a request file
// that is not there, a path trying to leave the collection — and each is
// paired with the success above.
func TestAPICollections_FailuresAreReportedNotPaperedOver(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	notACollection := t.TempDir()
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(outside, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for name, tc := range map[string]struct {
		method string
		params map[string]any
	}{
		"a folder with no manifest":      {"api.collections.open", map[string]any{"path": notACollection}},
		"a handle nobody minted":         {"api.request.read", map[string]any{"handle": "ffffffffffffffffffffffffffffffff", "relPath": "ping.json"}},
		"a request that is not there":    {"api.request.read", map[string]any{"handle": handle, "relPath": "nope.json"}},
		"a path leaving the folder":      {"api.request.read", map[string]any{"handle": handle, "relPath": "../../id_ed25519"}},
		"an absolute path":               {"api.request.read", map[string]any{"handle": handle, "relPath": outside}},
		"closing a handle nobody minted": {"api.collections.close", map[string]any{"handle": "ffffffffffffffffffffffffffffffff"}},
	} {
		t.Run(name, func(t *testing.T) {
			resp := vaultCall(t, conn, tc.method, tc.params, 20)
			if resp.Error == nil {
				t.Fatalf("%s(%v) succeeded; %s must be refused by name", tc.method, tc.params, name)
			}
			if strings.Contains(resp.Error.Message, "PRIVATE KEY") {
				t.Fatalf("the refusal echoed the file it refused to read: %s", resp.Error.Message)
			}
		})
	}
}

// ── the bootstrap's outcome, off the real socket ──────────────────────────

// Assertion 23, and the third of AGENTS.md rule 5's three checks, which is the
// one that matters: THE REAL RESULT OFF THE REAL SOCKET. A payload the test
// itself built would prove the struct is well-formed, not that the server
// sends it.
//
// What is red without P5: every one of these reasons reached the product as
// `unknown`. The bootstrap concluded before any domain existed, so no lifecycle
// fact carried it and no transport-loss cause described it — the precise
// outcome went to a log, and the session either sat in `starting` until some
// other detector concluded it with a vaguer word, or (on the remote path,
// which has no loss reporter at all) sat there for the life of the tab.
//
// The table is the §6.4 matrix plus the bootstrap's own set, one case each. It
// asserts the value the RENDERER will key on, after the schema has accepted
// the frame — a reason outside the closed enum fails validateJSON here rather
// than arriving as a card with no words in it.
func TestSessionIntegrationChanged_BootstrapOutcomesAreReadableOffTheWire(t *testing.T) {
	cases := []struct {
		name   string
		reason ssh.RefusalReason
	}{
		// §6.4, by real channel type.
		{"the primary session refused", ssh.ReasonSessionUnavailable},
		{"pty-req refused after the session", ssh.ReasonPTYUnavailable},
		{"exec refused, the channel alive", ssh.ReasonExecRefused},
		{"exec accepted and substituted", ssh.ReasonExecSubstituted},
		{"the SFTP subsystem refused", ssh.ReasonPublishUnavailable},
		{"the lifecycle channel refused", ssh.ReasonChannelUnavailable},
		{"an open channel severed mid-frame", ssh.ReasonBootstrapInterrupted},
		// The bootstrap's own closed set. The first is the one the brief
		// names: the backend knew `generation-unavailable` and the product
		// said "conventional, unknown".
		{"no generation installed on the far host", ssh.ReasonGenerationUnavailable},
		{"no hasher on the far host", ssh.ReasonStageDigestUnavailable},
		{"the stage-1 digest did not match", ssh.ReasonStageDigestMismatch},
		{"the far side never announced itself", ssh.ReasonReceiverUnready},
		{"the far side went quiet after announcing", ssh.ReasonBootstrapTimeout},
		{"a token arrived twice or out of order", ssh.ReasonBootstrapOutOfOrder},
		{"the capability could not be stored privately", ssh.ReasonCapabilityWriteFailed},
		{"the far side refused a key addressed elsewhere", ssh.ReasonSecretNotForThisSession},
	}
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLifecycleTestEnv(t)
			sid := e.openSession(t, 1)
			lane := lifecycle.LaneID(fmt.Sprintf("lane-bootstrap-%d", i))
			e.ws.RegisterLifecycleLane(lane, session.ID(sid))
			e.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone)
			e.ws.emitIntegration(session.ID(sid))

			// The honest interval first: nothing has failed yet, and a
			// product that named an outcome here would be guessing.
			raw := readNotification(t, e.conn, "session.integrationChanged", wantWithin)
			validateJSON(t, schema, raw, "session.integrationChanged params (real socket, starting)")
			var starting integrationChangedParams
			if err := json.Unmarshal(raw, &starting); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if starting.Status != IntegrationStarting || starting.Reason != "" {
				t.Fatalf("first fact = %+v, want status=starting with no reason", starting)
			}

			// The bootstrap reaches its terminal outcome.
			e.ws.NoteBootstrapOutcome(lane, tc.reason)

			// Asked for BY THE PROPERTY that makes it the outcome. The
			// first `session.integrationChanged` after this point is not
			// reliably the terminal one: the open handler emits the status
			// once after its ack, so a test that enters the axis afterwards
			// races that emit and an extra `starting` can land exactly here.
			raw, got := readIntegrationWhere(t, e.conn, sid,
				"the fact that concludes the axis", integrationConcluded)
			validateJSON(t, schema, raw, "session.integrationChanged params (real socket, "+tc.name+")")
			if got.Status != IntegrationConventional {
				t.Errorf("status = %q, want %q", got.Status, IntegrationConventional)
			}
			if got.Reason != string(tc.reason) {
				t.Errorf("reason off the wire = %q, want %q — the backend knew which it was",
					got.Reason, tc.reason)
			}
			if got.Reason == string(ssh.ReasonUnknown) {
				t.Error("the product was told the backend cannot say why, and it can")
			}
		})
	}
}

// A collection whose root is replaced under it. The handle is re-validated
// per operation rather than trusted from open time (§13.1's fourth rule),
// and the list REPORTS the failure on the entry rather than dropping the
// folder silently — a soft degrade the UI contradicts is how a feature that
// does not exist survives a release.
func TestAPICollectionsList_ReportsARootThatWasReplaced(t *testing.T) {
	listSchema := loadSchema(t, "api.collections.list.schema.json")
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	parent := t.TempDir()
	root := filepath.Join(parent, "coll")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nocx-collection.json"),
		[]byte(`{"schemaVersion":1,"name":"acme"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	handle := openAPICollection(t, conn, root, 1)

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	resp := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if resp.Error != nil {
		t.Fatalf("api.collections.list: %+v", resp.Error)
	}
	validateJSON(t, listSchema, resp.Result, "api.collections.list result (root replaced)")
	var listed apiCollectionsListResponse
	if err := json.Unmarshal(resp.Result, &listed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	chosen := chosenCollections(listed.Collections)
	if len(chosen) != 1 {
		t.Fatalf("collections = %+v, want the folder still listed with its failure named", chosen)
	}
	if chosen[0].Handle != handle {
		t.Errorf("handle = %q, want %q", chosen[0].Handle, handle)
	}
	if chosen[0].Error == "" {
		t.Error("a folder whose root was removed listed with no error; the failure must be reported, not papered over")
	}
}

// api.request.send through the real socket, against a real HTTP server. The
// gate is released before the dial (capability/api.go) — asserted here by
// a second, unrelated method answering WHILE a send is in flight against a
// server that does not reply until it is let go.
func TestAPIRequestSend_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.send.schema.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "t-1"}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "api.request.send result")

	var got apiSendResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	if got.Response.Status != 201 {
		t.Errorf("status = %d, want 201", got.Response.Status)
	}
	if got.Response.Text != "pong" {
		t.Errorf("text = %q, want %q", got.Response.Text, "pong")
	}
	if got.Response.Size != int64(len("pong")) {
		t.Errorf("size = %d, want %d", got.Response.Size, len("pong"))
	}
}

// The api gate is NOT held across the dial. Asserted, rather than asserted
// about in a comment.
//
// A send is put in flight against a server that will not answer until this
// test lets it. While it hangs there, an unrelated api.* method is called on
// a SECOND socket of the same server and must answer. The api gate is
// capacity one and both methods hold it, so a send that kept it across the
// exchange would make the second call wait behind a remote server — which
// in production is every collection operation blocking on one hung host.
//
// The waits are on observable state — the server goroutine reports it has
// the request; the second method's response arrives — and never on a
// duration. A spec that needs a slow machine to pass is broken on a fast
// one too, it has just not been caught yet.
func TestAPIRequestSend_DoesNotHoldTheGateAcrossTheDial(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(arrived)
		<-release
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	defer letGo()

	ws, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	// Write the send WITHOUT reading its response, so the exchange is
	// genuinely outstanding while the next call is made.
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "api.request.send",
		"params": map[string]any{"handle": handle, "relPath": "ping.json", "token": "t-inflight"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write send: %v", err)
	}
	<-arrived // the request has reached the server; the send is mid-exchange

	// A second socket of the same WSServer, so it shares the api gate.
	second := connectWS(t, ws)
	listed := vaultCall(t, second, "api.collections.list", map[string]any{}, 3)
	if listed.Error != nil {
		t.Fatalf("api.collections.list while a send was in flight: %+v — the send is "+
			"holding the api gate across the dial", listed.Error)
	}

	letGo()
	// And the send still completes: releasing the gate early did not cost
	// the exchange its answer.
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read send response: %v", readErr)
		}
		var msg vaultRPCResult
		if json.Unmarshal(data, &msg) != nil || msg.ID != 2 {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("api.request.send: %+v", msg.Error)
		}
		var got apiSendResponse
		if err := json.Unmarshal(msg.Result, &got); err != nil {
			t.Fatalf("unmarshal send result: %v", err)
		}
		if got.Response.Text != "late" {
			t.Errorf("text = %q, want %q", got.Response.Text, "late")
		}
		return
	}
}

// The DTO's own conformance, on ALL THREE OUTCOMES. It is cheap and it is
// not the test that catches a missing field — the socket test below is —
// but it is the one that catches the three shapes disagreeing with each
// other: a `response` pointer that marshals as `{}` rather than `null`, a
// phase spelt differently from the enum, a certificate list that arrives as
// null on the path nobody populated it on.
//
// A FAILED and a STOPPED case, deliberately, and not only the answered one.
// The whole change is that a run exists when there is no answer; a
// conformance table covering only the answer would check the half that was
// never in doubt.
func TestAPIRequestSend_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.request.send.schema.json")

	route := apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:bastion:1", InsecureTLS: true}
	request := apisend.Raw{Text: "GET /users HTTP/1.1\nHost: api.internal\n\n", Spans: []apisend.Span{}}

	cases := map[string]apisend.Exchange{
		"answered": {
			Outcome:      apisend.Answered,
			Request:      request,
			RemoteAddr:   "10.0.0.4:443",
			Timings:      apisend.Timings{DNS: time.Millisecond, Connect: 2 * time.Millisecond, Total: 9 * time.Millisecond},
			Certificates: []apisend.Certificate{{Subject: "CN=api.internal", Issuer: "CN=api.internal", SelfSigned: true, DNSNames: []string{"api.internal"}, IPAddresses: []string{}}},
			Response: &apisend.Response{
				Status:  200,
				Headers: []apicoll.Header{{Name: "Content-Type", Value: "application/json", Enabled: true}},
				Text:    `{"ok":true}`,
				Size:    11,
				Raw:     apisend.Raw{Text: "HTTP/1.1 200 OK\n\n", Spans: []apisend.Span{}},
				// The route above has verification off, and this is what
				// that route's run actually accepted — the state a badge is
				// drawn from, carrying the verifier's own sentence.
				Trust: apisend.Trust{
					State:  apisend.TrustUncheckedUntrusted,
					Reason: "x509: certificate signed by unknown authority",
				},
			},
		},
		// The ordinary connection: verification ran and there is nothing to
		// report. Beside the case above so the two states that a run can
		// legitimately carry are both marshalled, and neither is only ever
		// seen as the other's absence.
		"answered over a verified chain": {
			Outcome:      apisend.Answered,
			Request:      request,
			RemoteAddr:   "10.0.0.4:443",
			Certificates: []apisend.Certificate{},
			Response: &apisend.Response{
				Status:  200,
				Headers: []apicoll.Header{},
				Raw:     apisend.Raw{Spans: []apisend.Span{}},
				Trust:   apisend.Trust{State: apisend.TrustVerified},
			},
		},
		// The failure the whole change exists for: a request, a route, how
		// far it got — and NO response.
		"failed at dial": {
			Outcome:      apisend.Failed,
			Request:      request,
			Certificates: []apisend.Certificate{},
			Timings:      apisend.Timings{DNS: 3 * time.Millisecond},
			Failure:      &apisend.Failure{Phase: apisend.PhaseDial, Reason: "apisend: GET http://api.internal/users: connection refused"},
		},
		// A stop is not a failure, and the wire has to be able to say so:
		// the outcome differs while the failure block is present in both.
		"stopped": {
			Outcome:      apisend.Stopped,
			Request:      request,
			RemoteAddr:   "10.0.0.4:443",
			Certificates: []apisend.Certificate{},
			Failure:      &apisend.Failure{Phase: apisend.PhaseStopped, Reason: "context canceled"},
		},
		// And the phase with nothing composed behind it: the request block
		// is still there, empty, because the renderer walks it either way.
		"failed at compose": {
			Outcome:      apisend.Failed,
			Request:      apisend.Raw{Spans: []apisend.Span{}},
			Certificates: []apisend.Certificate{},
			Failure:      &apisend.Failure{Phase: apisend.PhaseCompose, Reason: `apisend: "users" is not an absolute URL`},
		},
	}

	for name, ex := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(wireExchange(ex, "prod", route))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "api.request.send DTO")

			// `null` and not `{}`: the pointer is the whole reason a reader
			// can tell "no answer" from "an answer with nothing in it".
			var probe struct {
				Response json.RawMessage `json:"response"`
				Failure  json.RawMessage `json:"failure"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if ex.Response == nil && string(probe.Response) != "null" {
				t.Errorf("response = %s for an exchange with no answer, want null", probe.Response)
			}
			if ex.Failure == nil && string(probe.Failure) != "null" {
				t.Errorf("failure = %s for an answered exchange, want null", probe.Failure)
			}
		})
	}
}

// An accepted bootstrap says nothing at all on this axis, and must not: "a
// domain is live" is the kernel's word, and an accepted bootstrap means the
// shell is on its way to proving itself rather than that it has. Reporting
// `integrated` here would be the transport re-deriving the kernel's
// conclusion, which is the two-owners defect AD-8 names.
func TestSessionIntegrationChanged_AnAcceptedBootstrapClaimsNothing(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := e.openSession(t, 1)
	lane := lifecycle.LaneID("lane-bootstrap-accepted")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	e.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationStarting, ssh.ReasonNone)
	e.ws.emitIntegration(session.ID(sid))
	readNotification(t, e.conn, "session.integrationChanged", wantWithin)

	e.ws.NoteBootstrapOutcome(lane, ssh.ReasonNone)

	e.ws.integrationMu.Lock()
	st := *e.ws.integrations[session.ID(sid)]
	e.ws.integrationMu.Unlock()
	if st.status != IntegrationStarting || st.reason != ssh.ReasonNone {
		t.Errorf("axis = %q/%q after an accepted bootstrap, want it untouched at %q",
			st.status, st.reason, IntegrationStarting)
	}
}

// The first answer wins, and this is the third detector to meet that rule.
// A session already concluded — by the launch itself, by the process observer,
// or by a transport loss — is not re-explained by a bootstrap report arriving
// afterwards.
func TestSessionIntegrationChanged_ABootstrapOutcomeNeverOverwritesAnAnswer(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := e.openSession(t, 1)
	lane := lifecycle.LaneID("lane-bootstrap-answered")
	e.ws.RegisterLifecycleLane(lane, session.ID(sid))
	e.ws.RegisterIntegration(session.ID(sid), "/bin/bash", IntegrationConventional, ssh.ReasonRemoteCommand)

	e.ws.NoteBootstrapOutcome(lane, ssh.ReasonGenerationUnavailable)

	e.ws.integrationMu.Lock()
	st := *e.ws.integrations[session.ID(sid)]
	e.ws.integrationMu.Unlock()
	if st.reason != ssh.ReasonRemoteCommand {
		t.Errorf("reason = %q, want the first answer %q", st.reason, ssh.ReasonRemoteCommand)
	}
}

// ── files.download* ──────────────────────────────────────────────────────

func TestFilesDownload_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.download.schema.json")
	good := strings.Repeat("ab", uploadTicketHexLen/2)
	cases := map[string]filesDownloadResult{
		"an ordinary file": {
			TransferID: "0123456789abcdef0123456789abcdef",
			Ticket:     good,
			URL:        "/download/" + good,
			Name:       "report.pdf",
			Size:       1 << 20,
		},
		// An empty file is a file: size 0 must not be an omitempty hole.
		"an empty file": {
			TransferID: "0123456789abcdef0123456789abcdef",
			Ticket:     good,
			URL:        "/download/" + good,
			Name:       "empty",
			Size:       0,
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.download DTO")
		})
	}
}

// The patterns are what make the security-relevant VALUES exact. A schema
// that described a 64-hex ticket and typed it as an unrestricted string
// would accept an empty one, an uppercase one, or a URL pointing somewhere
// else entirely — and a pattern with nothing asserting that it refuses
// anything is the same theatre as a schema without one.
func TestFilesDownload_ContractRefusesValuesItOnlyUsedToDescribe(t *testing.T) {
	schema := loadSchema(t, "files.download.schema.json")
	good := strings.Repeat("ab", uploadTicketHexLen/2)
	base := filesDownloadResult{
		TransferID: "0123456789abcdef0123456789abcdef",
		Ticket:     good,
		URL:        "/download/" + good,
		Name:       "a.txt",
		Size:       1,
	}
	empty := base
	empty.TransferID = ""
	upper := base
	upper.Ticket = strings.ToUpper(good)
	elsewhere := base
	elsewhere.URL = "https://elsewhere.example.com/collect"
	wrongRoute := base
	wrongRoute.URL = "/upload/" + good
	negative := base
	negative.Size = -1

	cases := map[string]filesDownloadResult{
		"an empty transfer id":              empty,
		"an uppercase ticket":               upper,
		"a url that is not this backend":    elsewhere,
		"a url naming the OTHER byte route": wrongRoute,
		"a negative size":                   negative,
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := validateJSONErr(schema, raw); err == nil {
				t.Fatalf("the contract accepted %s:\n%s", name, raw)
			}
		})
	}
}

// The one that matters: the real result off the real socket, from the real
// handler, against the real schema. A payload the test itself built proves
// the struct is well-formed, not that the server sends it.
func TestFilesDownload_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.download.schema.json")
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "report.bin", strings.Repeat("x", 4096))
	empty := fixture(t, dir, "empty", "")
	bid := e.openBinding(t, sid, dir, 2)

	resp := callDownload(t, e.conn, downloadParams(bid, p), 3)
	if resp.Error != nil {
		t.Fatalf("files.download: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "files.download result (real socket)")

	// And the zero-size shape, which is where an omitempty defect would
	// hide: a size that vanished from the payload fails `required`.
	respEmpty := callDownload(t, e.conn, downloadParams(bid, empty), 4)
	if respEmpty.Error != nil {
		t.Fatalf("files.download of an empty file: %+v", respEmpty.Error)
	}
	validateJSON(t, schema, respEmpty.Result, "files.download result, empty file (real socket)")
	if !bytes.Contains(respEmpty.Result, []byte(`"size":0`)) {
		t.Fatalf("an empty file's size vanished from the payload: %s", respEmpty.Result)
	}
}

func TestFilesDownloadCancel_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadCancel.schema.json")
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "files.downloadCancel DTO")
}

func TestFilesDownloadCancel_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadCancel.schema.json")
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", strings.Repeat("x", 4096))
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	resp := jsonrpcCallWithID(t, e.conn, "files.downloadCancel", map[string]any{
		"transferId": started.TransferID,
	}, 4)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.downloadCancel: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "files.downloadCancel result (real socket)")
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state = %q, want %q", state, downloadStateCancelled)
	}
}

func TestFilesDownloadProgress_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadProgress.schema.json")
	cases := map[string]filesTransferProgressParams{
		"in flight":     {TransferID: strings.Repeat("a1", 16), Bytes: 4096, Total: 1 << 20},
		"at zero":       {TransferID: strings.Repeat("b2", 16), Bytes: 0, Total: 1 << 20},
		"an empty file": {TransferID: strings.Repeat("c3", 16), Bytes: 0, Total: 0},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.downloadProgress DTO")
		})
	}
}

// THE FAILED EXCHANGE, OFF THE REAL SOCKET — and this is the one that
// matters, because a payload the test itself built proves the struct is
// well formed and says nothing about whether the server sends it.
//
// A server that is not there. Before this change the answer was a JSON-RPC
// error: one sentence, no request text, no route, no timing — while apisend
// was holding all of it at the moment it failed. Now it is a RUN, and every
// assertion below is a thing that used to reach the renderer nowhere at all.
func TestAPIRequestSend_AFailedExchangeIsARunOverTheWire(t *testing.T) {
	schema := loadSchema(t, "api.request.send.schema.json")

	// A listener opened and immediately closed gives an address nothing is
	// on, without depending on a port being free by guesswork.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := "http://" + l.Addr().String() + "/gone"
	_ = l.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, dead)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "t-1"}, 2)
	if resp.Error != nil {
		t.Fatalf("a send that could not connect answered an ERROR (%+v); it is an exchange "+
			"that failed, and a person who pressed Send has a run whatever the world did next", resp.Error)
	}
	// The SCHEMA on the failure path. Without this the contract would be
	// checked only on the shape that already worked.
	validateJSON(t, schema, resp.Result, "api.request.send result (failed)")

	var got apiSendResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	if got.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", got.Outcome)
	}
	if got.Response != nil {
		t.Errorf("a failed exchange carries a response: %+v", *got.Response)
	}
	if got.Failure == nil {
		t.Fatal("a failed exchange carries no failure")
	}
	if got.Failure.Phase != "dial" {
		t.Errorf("phase = %q, want dial — nothing accepted a connection", got.Failure.Phase)
	}
	if got.Failure.Reason == "" {
		t.Error("the failure has no reason; the run has nothing to show a person")
	}
	// The whole point: what was SENT is on the failed run.
	if !strings.Contains(got.Request.Text, "GET /gone HTTP/1.1") {
		t.Errorf("the failed run carries no request line:\n%s", got.Request.Text)
	}
	if got.Request.Spans == nil {
		t.Error("request.spans is null; a side with nothing to mark is []")
	}
	if got.Certificates == nil {
		t.Error("certificates is null; a chain of none is []")
	}
	// And the route, which a failed send used to leave the renderer
	// guessing at — the panel knows what it configured, not what was used.
	if got.Route.Kind != "direct" {
		t.Errorf("route.kind = %q, want direct", got.Route.Kind)
	}
}

// api.request.cancel refuses a token that names no running exchange, BY
// NAME. "There was nothing to stop" and "it is stopped" are different facts,
// and a caller that cannot tell them apart cannot report either.
func TestAPIRequestCancel_AnUnknownTokenIsRefusedByName(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	resp := vaultCall(t, conn, "api.request.cancel", map[string]any{"token": "nothing-is-running"}, 1)
	if resp.Error == nil {
		t.Fatal("cancelling a token nothing is running under was accepted")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "nothing-is-running") {
		t.Errorf("message = %q, want it to name the token it does not know", resp.Error.Message)
	}
}

// api.request.send is refused when no sender is wired, rather than answering
// an empty response. The whole point of the -32601 gate: the caller's next
// move is to stop calling it.
func TestAPIRequestSend_RefusedWhenNothingIsWired(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)

	for _, method := range []string{
		"api.collections.list", "api.collections.open", "api.collections.close",
		"api.request.read", "api.request.write", "api.request.move",
		"api.folder.read", "api.folder.write",
		"api.request.send", "api.import.postman",
	} {
		resp := vaultCall(t, conn, method, map[string]any{}, 1)
		if resp.Error == nil {
			t.Errorf("%s answered on a server with no api wiring; it must report the method is not there", method)
			continue
		}
		if resp.Error.Code != -32601 {
			t.Errorf("%s error code = %d, want -32601 (method not found)", method, resp.Error.Code)
		}
	}
}

func TestAPIImportCurl_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.import.curl.schema.json")
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

	resp := vaultCall(t, conn, "api.import.curl", map[string]any{
		"line": `curl -X POST https://example.test/v1/things?page=2 -H 'X-Probe: 1' -d '{"a":1}'`,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.curl: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "api.import.curl result")

	var got apiImportCurlResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Request.Method != "POST" {
		t.Errorf("method = %q, want POST", got.Request.Method)
	}
	if got.Request.URL != "https://example.test/v1/things" {
		t.Errorf("url = %q, want the URL with its query split off", got.Request.URL)
	}

	// The failure half, and it is a real one: a line curl itself could not
	// parse. An unterminated quote is refused rather than guessed at.
	bad := vaultCall(t, conn, "api.import.curl", map[string]any{"line": `curl -H 'X: 1`}, 2)
	if bad.Error == nil {
		t.Fatal("an unterminated quote was accepted; a line that cannot be parsed must be refused")
	}
}

func TestAPIImportPostman_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "api.import.postman.schema.json")
	bindings := newAPIFakeBindings()
	_, conn := newAPIWSServer(t, bindings)

	doc := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(doc, []byte(`{
      "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "variable": [{"key": "token", "value": "sk-secret-value", "type": "secret"}],
      "item": [{"name": "ping", "request": {"method": "GET", "url": "https://example.test/ping"}}]
    }`), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "imported")

	resp := vaultCall(t, conn, "api.import.postman", map[string]any{"path": doc, "dest": dest}, 1)
	if resp.Error != nil {
		t.Fatalf("api.import.postman: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "api.import.postman result")

	// AND THE IMPORTED FOLDER OPENS. Restored here per the instruction left by
	// TestAPIImportPostman_ImportedFolderCannotBeOpened_DEFECT, which recorded
	// the manifest mismatch as a test so it could not evaporate between rounds
	// and went red the moment nocx-1qtef fixed it. Import and open are one
	// user gesture in two halves — you import a Postman export in order to
	// work in it — so the assertion that they agree belongs on the happy path
	// rather than in a test of its own.
	openAPICollection(t, conn, dest, 2)

	// The secret value went to the binding store and NOT into the folder.
	if bindings.count() != 1 {
		t.Errorf("bound values = %d, want 1 — the secret must reach the binding store", bindings.count())
	}
	walkErr := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(p) //nolint:gosec // a test-only path under t.TempDir()
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "sk-secret-value") {
			t.Errorf("%s contains the secret VALUE; a collection file names a variable, never a secret", p)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", dest, walkErr)
	}

	// AND THE THIRD ENTRANCE, off the same socket. A conformance test that
	// certifies two entrances out of three certifies the wrong thing: the
	// URL route reaches the same writer by a different path through the
	// handler, and it is the only one whose result nobody had validated
	// against the schema.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
      "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "variable": [{"key": "token", "value": "sk-secret-value", "type": "secret"}],
      "item": [{"name": "ping", "request": {"method": "GET", "url": "https://example.test/ping"}}]
    }`))
	}))
	defer srv.Close()

	byURL := filepath.Join(t.TempDir(), "fetched")
	fetched := vaultCall(t, conn, "api.import.postman", map[string]any{
		"url":   srv.URL + "/export.json",
		"route": map[string]any{"kind": "direct"},
		"dest":  byURL,
	}, 3)
	if fetched.Error != nil {
		t.Fatalf("api.import.postman by url: %+v", fetched.Error)
	}
	validateJSON(t, schema, fetched.Result, "api.import.postman result (url)")
	// `unsupported: []` and not `null`: an export that converts whole says
	// so with an empty LIST, which is what the renderer's first .map walks.
	if !strings.Contains(string(fetched.Result), `"unsupported":[]`) {
		t.Errorf("result = %s, want an empty unsupported list on an export that converts whole", fetched.Result)
	}
	openAPICollection(t, conn, byURL, 4)
	if bindings.count() != 2 {
		t.Errorf("bound values = %d, want 2 — the secret must reach the binding store on this route too", bindings.count())
	}
}

// The import's failure half, both external calls that can fail: the document
// that is not there, and the binding store that refuses. The second one
// matters most — the folder must not survive a binding that failed.
func TestAPIImportPostman_FailuresLeaveNoCollection(t *testing.T) {
	bindings := newAPIFakeBindings()
	bindings.bindErr = errors.New("no vault")
	_, conn := newAPIWSServer(t, bindings)

	doc := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(doc, []byte(`{
      "info": {"name": "acme", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "variable": [{"key": "token", "value": "sk-secret-value", "type": "secret"}],
      "item": [{"name": "ping", "request": {"method": "GET", "url": "https://example.test/ping"}}]
    }`), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "not-there.json")
	dest := filepath.Join(t.TempDir(), "imported")

	gone := vaultCall(t, conn, "api.import.postman", map[string]any{"path": missing, "dest": dest}, 1)
	if gone.Error == nil {
		t.Fatal("importing a document that is not there succeeded")
	}

	refused := vaultCall(t, conn, "api.import.postman", map[string]any{"path": doc, "dest": dest}, 2)
	if refused.Error == nil {
		t.Fatal("an import whose binding store refused reported success")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want not-exist — a failed import leaves no collection behind", dest, err)
	}
}

// Design §13.1, made enforceable rather than remembered.
//
// Opening a collection mints a backend-held handle, and `root` is never
// accepted again — but "never accepted" is only a rule if a params object
// carrying one is REFUSED. A tolerant decoder would IGNORE the extra field,
// which reads identically from the renderer and leaves the property as a
// habit somebody has to keep. Every api.* method but collections.open and
// import.postman is asserted here to refuse a path outright.
func TestAPIMethods_OnlyOpenAndImportPostmanAcceptAPath(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Every method on the surface except the two that legitimately take
	// one, with valid params for the method plus a path bolted on.
	base := map[string]map[string]any{
		"api.collections.list":         {},
		"api.collections.create":       {"name": "made-up"},
		"api.collections.createFolder": {"handle": handle, "name": "made-up"},
		"api.collections.close":        {"handle": handle},
		"api.request.read":             {"handle": handle, "relPath": "ping.json"},
		"api.request.write":            {"handle": handle, "relPath": "ping.json", "request": map[string]any{"id": "r1", "name": "ping", "method": "GET", "url": "https://example.test", "body": map[string]any{"kind": "none"}, "auth": map[string]any{"kind": "none"}}},
		"api.request.send":             {"handle": handle, "relPath": "ping.json"},
		"api.import.curl":              {"line": "curl https://example.test"},
	}
	// The names a path arrives under. Any of them reaching a handler would
	// be a second way to address a file.
	pathKeys := []string{"path", "root", "rootPath", "dest", "file"}

	id := 10
	for method, params := range base {
		for _, key := range pathKeys {
			id++
			withPath := map[string]any{}
			for k, v := range params {
				withPath[k] = v
			}
			withPath[key] = secret
			resp := vaultCall(t, conn, method, withPath, id)
			if resp.Error == nil {
				t.Errorf("%s accepted a %q param; only api.collections.open and api.import.postman may take a path (design §13.1)", method, key)
				continue
			}
			if resp.Error.Code != -32602 {
				t.Errorf("%s with a %q param: code = %d, want -32602", method, key, resp.Error.Code)
			}
		}
	}

	// And the two that may: they answer rather than refuse. Without this
	// half the test above would pass on a surface that refused everything.
	ok := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 90)
	if ok.Error != nil {
		t.Fatalf("api.collections.open must accept a path: %+v", ok.Error)
	}
}

// The negatives: the schemas refuse what they must refuse. Without
// additionalProperties:false and an explicit required list a schema accepts
// anything and the gate is theatre.
func TestAPIContracts_RefuseWhatTheyMustRefuse(t *testing.T) {
	for name, tc := range map[string]struct {
		schema string
		raw    string
	}{
		"open with no handle":                    {"api.collections.open.schema.json", `{"collection":{"name":"a","requests":[],"malformed":[]}}`},
		"create with no handle":                  {"api.collections.create.schema.json", `{"collection":{"name":"a","requests":[],"malformed":[]}}`},
		"create with a field nobody declared":    {"api.collections.create.schema.json", `{"handle":"a","collection":{"name":"a","requests":[],"malformed":[]},"path":"/tmp/acme"}`},
		"open with an undeclared key":            {"api.collections.open.schema.json", `{"handle":"h","collection":{"name":"a","requests":[],"malformed":[]},"root":"/etc"}`},
		"a null request list":                    {"api.collections.open.schema.json", `{"handle":"h","collection":{"name":"a","requests":null,"malformed":[]}}`},
		"list with null collections":             {"api.collections.list.schema.json", `{"collections":null,"defaultRoot":""}`},
		"list with no defaultRoot":               {"api.collections.list.schema.json", `{"collections":[]}`},
		"a read with no request":                 {"api.request.read.schema.json", `{}`},
		"a request with null variables":          {"api.request.read.schema.json", `{"request":{"id":"r","name":"n","method":"GET","url":"u","headers":[],"query":[],"variables":null,"body":{"kind":"none","text":"","fileRef":""},"auth":{"kind":"none","var":"","user":""}}}`},
		"a request with no variables key":        {"api.request.read.schema.json", `{"request":{"id":"r","name":"n","method":"GET","url":"u","headers":[],"query":[],"body":{"kind":"none","text":"","fileRef":""},"auth":{"kind":"none","var":"","user":""}}}`},
		"a send with no outcome":                 {"api.request.send.schema.json", `{"request":{"text":"","spans":[]},"response":null,"failure":{"phase":"dial","reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":[]}`},
		"a send with no request block":           {"api.request.send.schema.json", `{"outcome":"failed","response":null,"failure":{"phase":"dial","reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":[]}`},
		"a send with a phase nobody declared":    {"api.request.send.schema.json", `{"outcome":"failed","request":{"text":"","spans":[]},"response":null,"failure":{"phase":"handshake","reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":[]}`},
		"a send with an outcome nobody declared": {"api.request.send.schema.json", `{"outcome":"cancelled","request":{"text":"","spans":[]},"response":null,"failure":{"phase":"stopped","reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":[]}`},
		"a failure with no phase":                {"api.request.send.schema.json", `{"outcome":"failed","request":{"text":"","spans":[]},"response":null,"failure":{"reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":[]}`},
		"a send with null certificates":          {"api.request.send.schema.json", `{"outcome":"failed","request":{"text":"","spans":[]},"response":null,"failure":{"phase":"dial","reason":"x"},"environment":"","route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"","timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},"certificates":null}`},
		"a cancel that says something":           {"api.request.cancel.schema.json", `{"stopped":true}`},
		"an import with null list":               {"api.import.postman.schema.json", `{"unsupported":null}`},
		"a close that says something":            {"api.collections.close.schema.json", `{"closed":true}`},
	} {
		t.Run(name, func(t *testing.T) {
			schema := loadSchema(t, tc.schema)
			if err := validateJSONErr(schema, []byte(tc.raw)); err == nil {
				t.Fatalf("%s accepted %s", tc.schema, tc.raw)
			}
		})
	}
}

// The real progress notification off the real socket. The source holds
// inside Get after reporting, so the frame is guaranteed rather than raced.
func TestFilesDownloadProgress_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadProgress.schema.json")
	src := newPausingSource()
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "watched.bin", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()
	<-src.reported

	raw := readNotification(t, e.conn, "files.downloadProgress", wantWithin)
	validateJSON(t, schema, raw, "files.downloadProgress params (real socket)")
	src.release()
	awaitTransferState(t, e.ws, started.TransferID)
}

func TestFilesDownloadDone_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadDone.schema.json")
	cases := map[string]filesDownloadDoneParams{
		"sent": {
			TransferID: strings.Repeat("a1", 16), Outcome: downloadStateSent,
			Name: "report.pdf", Bytes: 1 << 20, Total: 1 << 20,
		},
		// A partial transfer that cannot be taken back: bytes is the whole
		// of the account.
		"failed part-way": {
			TransferID: strings.Repeat("b2", 16), Outcome: downloadStateFailed,
			Name: "report.pdf", Bytes: 4096, Total: 1 << 20,
			Error: "transfer: read remote: connection lost",
		},
		"cancelled carries no error": {
			TransferID: strings.Repeat("c3", 16), Outcome: downloadStateCancelled,
			Name: "report.pdf", Bytes: 0, Total: 1 << 20,
		},
		"an empty file": {
			TransferID: strings.Repeat("d4", 16), Outcome: downloadStateSent,
			Name: "empty", Bytes: 0, Total: 0,
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "files.downloadDone DTO")
			// bytes and total are required, so a zero that vanished behind
			// an omitempty would fail the schema — asserted directly too,
			// because that is the defect class this directory exists for.
			if !bytes.Contains(raw, []byte(`"bytes":`)) || !bytes.Contains(raw, []byte(`"total":`)) {
				t.Errorf("a count vanished from the payload: %s", raw)
			}
		})
	}
}

// chosenCollections drops the BUILT-IN collection from a listing.
//
// api.collections.list opens the Playground once per process before it
// answers (capability.apiCollectionService.ensureStarter), because a panel
// with nothing in it asks a person to do administration before it will show
// them anything. The assertions below are about the folder the TEST opened —
// what a create leaves open, what a close removes, how a replaced root is
// reported — and the built-in row is not part of any of those questions.
func chosenCollections(in []apiOpenCollectionWire) []apiOpenCollectionWire {
	out := make([]apiOpenCollectionWire, 0, len(in))
	for _, c := range in {
		if filepath.Base(c.Path) == apicoll.StarterName {
			continue
		}
		out = append(out, c)
	}
	return out
}

// The real terminal notification off the real socket, in both of the ways
// it can reach a person: delivered live, and RETAINED across a reconnect
// and flushed on attach. The second is the one an addressing defect hides
// in — an outcome addressed like progress would be emitted into nothing.
func TestFilesDownloadDone_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "files.downloadDone.schema.json")

	t.Run("live, failed part-way", func(t *testing.T) {
		src := &failingSource{err: errors.New("transfer: read remote: connection lost"), sent: 1024}
		e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		p := fixture(t, dir, "f", "x")
		bid := e.openBinding(t, sid, dir, 2)
		started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
		go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()

		raw := readNotification(t, e.conn, "files.downloadDone", wantWithin)
		validateJSON(t, schema, raw, "files.downloadDone params (real socket, live)")
		var got filesDownloadDoneParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Outcome != downloadStateFailed || got.Error == "" || got.Bytes != 1024 {
			t.Fatalf("got %+v; want a failed outcome carrying its reason and how far it got", got)
		}
	})

	t.Run("retained across a reconnect, sent", func(t *testing.T) {
		e := newDownloadTestEnv(t)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		body := "finished while nobody was watching"
		p := fixture(t, dir, "late.txt", body)
		bid := e.openBinding(t, sid, dir, 2)
		started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

		dropSubscriber(t, e, sid)
		if code, _, _, err := getDownloadFull(e.ws, started.Ticket, nil); err != nil || code != 200 {
			t.Fatalf("GET = %d %v", code, err)
		}
		awaitTransferState(t, e.ws, started.TransferID)

		connB := reattach(t, e, sid, 4)
		raw := readNotification(t, connB, "files.downloadDone", wantWithin)
		validateJSON(t, schema, raw, "files.downloadDone params (real socket, flushed on attach)")
		var got filesDownloadDoneParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.TransferID != started.TransferID || got.Outcome != downloadStateSent || got.Name != "late.txt" {
			t.Fatalf("got %+v; want the sent outcome of %s", got, started.TransferID)
		}
	})
}

func TestAPIFolderReadAndWrite_DTOConformToContracts(t *testing.T) {
	readSchema := loadSchema(t, "api.folder.read.schema.json")
	writeSchema := loadSchema(t, "api.folder.write.schema.json")
	rawRead, err := json.Marshal(apiFolderReadResponse{Variables: []apiParamWire{{
		Name: "baseUrl", Value: "https://example.test", Enabled: true,
	}}})
	if err != nil {
		t.Fatalf("marshal read: %v", err)
	}
	validateJSON(t, readSchema, rawRead, "api.folder.read DTO")

	rawWrite, err := json.Marshal(apiFolderWriteResponse{Variables: []apiParamWire{{
		Name: "baseUrl", Value: "https://example.test", Enabled: true,
	}}})
	if err != nil {
		t.Fatalf("marshal write: %v", err)
	}
	validateJSON(t, writeSchema, rawWrite, "api.folder.write DTO")
}

func TestAPIFolderReadAndWrite_OverTheWireConformToContracts(t *testing.T) {
	readSchema := loadSchema(t, "api.folder.read.schema.json")
	writeSchema := loadSchema(t, "api.folder.write.schema.json")
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	written := vaultCall(t, conn, "api.folder.write", map[string]any{
		"handle":  handle,
		"relPath": "",
		"variables": []map[string]any{{
			"name": "baseUrl", "value": "https://example.test", "enabled": true,
		}},
	}, 2)
	if written.Error != nil {
		t.Fatalf("api.folder.write: %+v", written.Error)
	}
	validateJSON(t, writeSchema, written.Result, "api.folder.write result")

	read := vaultCall(t, conn, "api.folder.read", map[string]any{
		"handle": handle, "relPath": "",
	}, 3)
	if read.Error != nil {
		t.Fatalf("api.folder.read: %+v", read.Error)
	}
	validateJSON(t, readSchema, read.Result, "api.folder.read result")
}

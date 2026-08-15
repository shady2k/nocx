package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/vault/vaulttest"
)

// endpointHarness is the endpoints.*-specific harness: a real vault (file
// provider), a real profile store (which is also the endpoint repository)
// and the real socket. The dir is exposed so tests can read the persisted
// document and prove key material never lands in it.
type endpointHarness struct {
	t    *testing.T
	v    *vault.Vault
	ps   *profile.JSONStore
	dir  string
	ws   *WSServer
	conn *websocket.Conn
}

func newEndpointHarness(t *testing.T) *endpointHarness {
	t.Helper()
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)

	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)

	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v),
		WithVaultLifecycle(v))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	return &endpointHarness{t: t, v: v, ps: ps, dir: dir, ws: ws, conn: conn}
}

func (h *endpointHarness) setupAndUnseal() {
	h.t.Helper()
	if _, err := h.v.Setup(h.t.Context(), vault.SetupRequest{Passphrase: "test"}); err != nil {
		h.t.Fatalf("Setup: %v", err)
	}
}

func endpointParams(name, baseURL, key string) map[string]any {
	params := map[string]any{
		"name":    name,
		"baseUrl": baseURL,
		"schema":  "openai-compatible",
		"models":  []map[string]any{{"name": "gpt-4o-mini"}},
	}
	if key != "" {
		params["key"] = key
	}
	return params
}

func decodeEndpointResult(t *testing.T, raw []byte) (profile.EndpointDTO, int) {
	t.Helper()
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		return profile.EndpointDTO{}, env.Error.Code
	}
	var result endpointResultResponse
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, env.Result)
	}
	return result.Endpoint, 0
}

func (h *endpointHarness) createEndpoint(t *testing.T, params map[string]any) profile.EndpointDTO {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "endpoints.create", params)
	e, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d\nraw: %s", code, raw)
	}
	return e
}

func (h *endpointHarness) listEndpoints(t *testing.T) []profile.EndpointDTO {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "endpoints.list", nil)
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result struct {
			Endpoints []profile.EndpointDTO `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal list: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("endpoints.list: %+v\nraw: %s", env.Error, raw)
	}
	return env.Result.Endpoints
}

// The full lifecycle over the real socket (the brief's done-when): create,
// list, edit, delete — with the key minted into the vault and only a row
// handle on the wire.
func TestEndpointsCRUD_OverTheWire(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	created := h.createEndpoint(t, endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))
	if created.ID == "" || created.Credential == nil {
		t.Fatalf("created = %+v, want a minted id and a row handle", created)
	}
	if strings.Contains(*created.Credential, "sk-test-123") {
		t.Fatalf("credential = %q, must be a row handle, never the key", *created.Credential)
	}
	row := *created.Credential

	listed := h.listEndpoints(t)
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created endpoint", listed)
	}
	if listed[0].Credential == nil || *listed[0].Credential != row {
		t.Errorf("list credential = %v, want the same row handle %q", listed[0].Credential, row)
	}

	// Edit: rename, no key — the credential must be unchanged.
	updateParams := endpointParams("OpenAI EU", "https://api.eu.openai.com/v1", "")
	updateParams["id"] = created.ID
	raw := jsonrpcCall(t, h.conn, "endpoints.update", updateParams)
	updated, rpcErr := decodeEndpointResult(t, raw)
	if rpcErr != 0 {
		t.Fatalf("endpoints.update: %+v", rpcErr)
	}
	if updated.Name != "OpenAI EU" || updated.Credential == nil || *updated.Credential != row {
		t.Fatalf("updated = %+v, want the new name and the unchanged credential", updated)
	}

	// Delete: empty object result, then the list is empty.
	delRaw := jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": created.ID})
	if isErrorResponse(t, delRaw) {
		t.Fatalf("endpoints.delete: %s", delRaw)
	}
	if got := h.listEndpoints(t); len(got) != 0 {
		t.Fatalf("list after delete = %+v, want none", got)
	}
}

// The renderer's create/update params (frontend/src/endpoints.ts
// EndpointWrite) carry NO schema field — the form has no dialect control
// until a second implementation exists (design §4.5, decision 2), and the
// backend owns the value until it does. This is the exact shape that
// shipped nocx-qtim: the harness helpers above always send a schema, so a
// test built on them can never see what the renderer actually sends
// (AGENTS.md rule 5 — the payload must not be one the test invented).
// Driving the real socket with the renderer's shape must create and update
// a stored endpoint whose schema is openai-compatible.
func TestEndpoints_RendererShapeCreateAndUpdate_StoresOpenAICompatible(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	// Exactly frontend/src/endpoints.ts EndpointWrite as save() builds it:
	// name, baseUrl, key, models with an explicit alias per row (a blank
	// alias becomes null) — and no schema.
	createParams := map[string]any{
		"name":    "My provider",
		"baseUrl": "http://127.0.0.1:8787/v1",
		"key":     "sk-renderer-shape",
		"models":  []map[string]any{{"name": "gpt-4o", "alias": nil}},
	}
	raw := jsonrpcCall(t, h.conn, "endpoints.create", createParams)
	created, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create with the renderer's schema-less params: code %d\nraw: %s", code, raw)
	}
	if created.Schema != profile.EndpointSchemaOpenAICompatible {
		t.Fatalf("created schema = %q, want %q", created.Schema, profile.EndpointSchemaOpenAICompatible)
	}

	// The STORED record carries the schema, not just the create result.
	listed := h.listEndpoints(t)
	if len(listed) != 1 || listed[0].Schema != profile.EndpointSchemaOpenAICompatible {
		t.Fatalf("list = %+v, want one endpoint with schema %q", listed, profile.EndpointSchemaOpenAICompatible)
	}

	// The update path has the same hole: the renderer's edit sends the same
	// schema-less shape, and it must replace the record without refusing or
	// dropping the schema.
	updateParams := map[string]any{
		"id":      created.ID,
		"name":    "Renamed provider",
		"baseUrl": "http://127.0.0.1:8787/v1",
		"key":     "",
		"models":  []map[string]any{{"name": "gpt-4o", "alias": nil}},
	}
	raw = jsonrpcCall(t, h.conn, "endpoints.update", updateParams)
	updated, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.update with the renderer's schema-less params: code %d\nraw: %s", code, raw)
	}
	if updated.Schema != profile.EndpointSchemaOpenAICompatible {
		t.Fatalf("updated schema = %q, want %q", updated.Schema, profile.EndpointSchemaOpenAICompatible)
	}

	// The STORED record after update carries the schema too — persistence,
	// not just the response.
	listed = h.listEndpoints(t)
	if len(listed) != 1 || listed[0].Schema != profile.EndpointSchemaOpenAICompatible {
		t.Fatalf("list after update = %+v, want one endpoint with schema %q", listed, profile.EndpointSchemaOpenAICompatible)
	}
}

// The key given at creation is stored in the vault with only an opaque
// reference on the record: reading the persisted document back must find no
// key material (the brief's assertion, checked on the file).
func TestEndpointsCreate_KeyNeverLandsInThePersistedRecord(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	h.createEndpoint(t, endpointParams("OpenAI", "https://api.openai.com/v1", "sk-very-secret-42"))

	raw, err := os.ReadFile(filepath.Join(h.dir, "p.json"))
	if err != nil {
		t.Fatalf("read persisted document: %v", err)
	}
	if strings.Contains(string(raw), "sk-very-secret-42") {
		t.Fatal("the API key VALUE is persisted in the profile document")
	}
	if !strings.Contains(string(raw), "credentialRef") || !strings.Contains(string(raw), "sec:v1:file:") {
		t.Fatalf("document must hold the endpoint's opaque reference, got: %s", raw)
	}
}

// The brief's "deleting an endpoint does not orphan its secret": the vault
// material is deleted with the record (metadata-first, ADR-0030). Proven
// through the vault itself: the secret no longer exists after the delete.
func TestEndpointsDelete_DoesNotOrphanTheSecret(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	created := h.createEndpoint(t, endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))
	id, ok := h.v.ResolveRowForTest(*created.Credential)
	if !ok {
		t.Fatalf("resolve row %q before delete", *created.Credential)
	}
	if exists, err := h.v.Exists(t.Context(), id); err != nil || !exists {
		t.Fatalf("secret must exist before the delete (exists=%v err=%v)", exists, err)
	}

	delRaw := jsonrpcCall(t, h.conn, "endpoints.delete", map[string]any{"id": created.ID})
	if isErrorResponse(t, delRaw) {
		t.Fatalf("endpoints.delete: %s", delRaw)
	}

	if exists, err := h.v.Exists(t.Context(), id); err != nil || exists {
		t.Fatalf("secret must be gone after the delete (exists=%v err=%v) — deleting the endpoint must not orphan its key", exists, err)
	}
}

// Update with a new key rotates the material behind the endpoint's OWN
// secret: the row handle is unchanged, the value behind it is the new key.
func TestEndpointsUpdate_NewKey_RotatesTheMaterialInPlace(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	created := h.createEndpoint(t, endpointParams("OpenAI", "https://api.openai.com/v1", "sk-original"))
	id, ok := h.v.ResolveRowForTest(*created.Credential)
	if !ok {
		t.Fatalf("resolve row")
	}
	before := h.readSecret(t, id)
	if before != "sk-original" {
		t.Fatalf("material before update = %q", before)
	}

	updateParams := endpointParams("OpenAI", "https://api.openai.com/v1", "sk-rotated")
	updateParams["id"] = created.ID
	raw := jsonrpcCall(t, h.conn, "endpoints.update", updateParams)
	updated, rpcErr := decodeEndpointResult(t, raw)
	if rpcErr != 0 {
		t.Fatalf("endpoints.update: %+v", rpcErr)
	}
	if updated.Credential == nil || *updated.Credential != *created.Credential {
		t.Fatalf("credential changed on rotate: %v -> %v — rotation keeps the id", *created.Credential, updated.Credential)
	}
	if got := h.readSecret(t, id); got != "sk-rotated" {
		t.Fatalf("material after update = %q, want the rotated key", got)
	}
}

func (h *endpointHarness) readSecret(t *testing.T, id credential.SecretID) string {
	t.Helper()
	sec, err := h.v.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	var out string
	if err := sec.Use(func(b []byte) error {
		out = string(b)
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return out
}

// The list is never null: an empty store answers [] (nocx-25k9.14).
func TestEndpointsList_NeverNull(t *testing.T) {
	h := newEndpointHarness(t)
	// No setupAndUnseal: the list needs no vault at all.
	raw := jsonrpcCall(t, h.conn, "endpoints.list", nil)
	var env struct {
		Result struct {
			Endpoints json.RawMessage `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(env.Result.Endpoints) != "[]" {
		t.Fatalf("endpoints = %s, want []", env.Result.Endpoints)
	}
}

// The endpoint methods are config-domain: without a profile repository the
// whole family refuses -32601, exactly like profiles.list does.
func TestEndpoints_NotWiredFailsClosed(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "endpoints.list", nil)
	if !strings.Contains(string(resp), "-32601") {
		t.Fatalf("endpoints.list without wiring = %s, want -32601", resp)
	}
}

// The brief's failure contract, asserted through the vault's journal: when
// the provider refuses the material delete, the endpoint record is gone
// (the user's intent), the vault journals the pending delete and drops the
// catalogue record in the same document write (vault.go:1241-1254), and
// the NEXT START reconciles it — Reconcile re-attempts the provider delete
// and clears the entry (journal.go:119-137). The material-side deletion on
// retry is the vault's own proven contract (journal_test.go — "delete the
// orphan secret"); what this test proves is that the ENDPOINT path fails
// into that machinery: the failure is journaled, never dropped.
//
// The provider here is vaulttest.Fake, the repo's exported test seam: a
// lock-independent provider, the shape of the OS keychain (the product's
// default), whose Delete answers whether or not the vault is sealed. A
// lock-DEPENDENT provider (the file store) cannot be reconciled at a
// sealed start — its entry is retained and logged blocked until a reset
// sweeps it; the metadata is gone either way, so nothing dangles
// (ADR-0030 §4).
func TestEndpointsDelete_ProviderFailureIsReconciledAtNextStart(t *testing.T) {
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	provider := vaulttest.NewFake()
	reg, err := vault.NewRegistry(provider)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v1, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v1.Close)
	if _, setupErr := v1.Setup(t.Context(), vault.SetupRequest{Passphrase: "test"}); setupErr != nil {
		t.Fatalf("Setup: %v", setupErr)
	}

	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v1), WithVaultLifecycle(v1))
	ctx := t.Context()
	if startErr := ws.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	raw := jsonrpcCall(t, conn, "endpoints.create", endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))
	created, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	id, ok := v1.ResolveRowForTest(*created.Credential)
	if !ok {
		t.Fatalf("resolve created row")
	}

	// The provider refuses the material delete. The record is gone; the
	// vault doc now holds the pending delete and no catalogue record.
	provider.SetFailure(errors.New("simulated provider delete failure"))
	if resp := jsonrpcCall(t, conn, "endpoints.delete", map[string]any{"id": created.ID}); isErrorResponse(t, resp) {
		t.Fatalf("endpoints.delete: %s", resp)
	}
	if got := h_listHelper(t, conn); len(got) != 0 {
		t.Fatalf("endpoints after delete = %+v, want none", got)
	}
	if _, ok := v1.ResolveRowForTest(*created.Credential); ok {
		t.Fatal("catalogue record must be dropped when the delete is journaled")
	}
	doc := readVaultDocument(t, docStore)
	// The mint's PhaseSecretWritten entry is the NORMAL state (production
	// never calls AttachTarget/CommitMetadata — the catalogue record is the
	// durable proof, ws_vault_inventory_test.go:441); what this test needs
	// is the DELETE entry the failed material-delete left behind.
	var deleteEntry *vault.JournalEntry
	for i := range doc.Journal {
		if doc.Journal[i].Op == "delete" && doc.Journal[i].NewID == id {
			deleteEntry = &doc.Journal[i]
		}
	}
	if deleteEntry == nil {
		t.Fatalf("journal = %+v, want a pending delete of %q", doc.Journal, id)
	}
	// The failure flag was consumed by the delete above; clear it so the
	// material check reads the store, not the injected error.
	provider.SetFailure(nil)
	if sec, getErr := provider.Get(t.Context(), id); getErr != nil || sec.IsEmpty() {
		t.Fatalf("material must still exist before the restart reconciles (err=%v)", getErr)
	}
	// The next start: a fresh vault over the same store with a HEALTHY
	// provider. Reconcile retries the pending delete and clears the entry.
	healthyReg, err := vault.NewRegistry(vaulttest.NewFake())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	v2, err := vault.New(docStore, healthyReg, logger)
	if err != nil {
		t.Fatalf("restart vault.New: %v", err)
	}
	t.Cleanup(v2.Close)
	doc = readVaultDocument(t, docStore)
	for _, e := range doc.Journal {
		if e.Op != "" {
			t.Fatalf("journal entry %+v must be reconciled (cleared) at the next start", e)
		}
	}
}

// readVaultDocument reads the vault's own document — the same file
// saveDocument writes (storage.DocumentStore over "vault.json") — so the
// test can observe the journal the failure left behind.
func readVaultDocument(t *testing.T, docStore storage.DocumentStore) vault.Document {
	t.Helper()
	var doc vault.Document
	found, err := docStore.Read("vault.json", &doc)
	if err != nil {
		t.Fatalf("read vault document: %v", err)
	}
	if !found {
		t.Fatal("vault document missing")
	}
	return doc
}

func h_listHelper(t *testing.T, conn *websocket.Conn) []profile.EndpointDTO {
	t.Helper()
	raw := jsonrpcCall(t, conn, "endpoints.list", nil)
	var env struct {
		Error  *struct{ Code int } `json:"error"`
		Result struct {
			Endpoints []profile.EndpointDTO `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("endpoints.list: %+v", env.Error)
	}
	return env.Result.Endpoints
}

// The paired positive the brief demands: an endpoint created on an ordinary
// machine is still there after a restart — a fresh backend over the same
// directories sees the record, with the credential still resolvable.
func TestEndpoints_PersistAcrossRestart(t *testing.T) {
	h := newEndpointHarness(t)
	h.setupAndUnseal()

	created := h.createEndpoint(t, endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))

	// "Restart": a brand-new backend over the same dirs.
	docStore := storage.NewDocumentStore(h.dir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v2, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v2.Close)
	ps2 := profile.NewJSONStore(filepath.Join(h.dir, "p.json"))
	ws2 := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps2), WithGroupRepository(ps2),
		WithCredentialStore(v2), WithVaultLifecycle(v2))
	ctx := t.Context()
	if err := ws2.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws2.Stop(ctx) })
	conn2 := connectWS(t, ws2)
	t.Cleanup(func() { _ = conn2.Close() })

	got := h_listHelper(t, conn2)
	if len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("endpoints after restart = %+v, want the created endpoint", got)
	}
	if got[0].Credential == nil || *got[0].Credential != *created.Credential {
		t.Fatalf("credential after restart = %v, want the same row handle", got[0].Credential)
	}
}

// The endpoints path must carry the vault's reason on the wire when the key
// mint fails because the vault needs setup or is sealed — the renderer's
// operation-first wrapper (saveSecretWithVault) and the dispatcher's sealed
// interception both key on data.reason, and a bare -32603 with prose is
// indistinguishable from a disk error: the setup/unlock sheet never opens
// and the save dies in a toast (the whole of nocx-25k9.7, re-lit on the
// endpoint surface by nocx-4egm).
func TestEndpointsCreate_CarriesVaultReasons(t *testing.T) {
	h := newEndpointHarness(t)

	// Fresh vault: minting the key fails with vault-uninitialized. The RPC
	// must say so with the reason, not a bare internal error.
	raw := jsonrpcCall(t, h.conn, "endpoints.create",
		endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))
	var env struct {
		Error *struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %s", err, raw)
	}
	if env.Error == nil {
		t.Fatal("create with a key on an uninitialized vault must fail")
	}
	if env.Error.Code != -32000 {
		t.Errorf("uninitialized code = %d, want -32000 (vault-uninitialized)", env.Error.Code)
	}
	if env.Error.Data == nil || env.Error.Data["reason"] != "vault-uninitialized" {
		t.Errorf("uninitialized data = %v, want reason %q", env.Error.Data, "vault-uninitialized")
	}
	// The failed save must not have created a record: an endpoint that
	// exists without its key is the exact data loss the bead is about.
	if eps := h_listHelper(t, h.conn); len(eps) != 0 {
		t.Errorf("endpoints after a failed key mint = %+v, want none", eps)
	}

	// Sealed vault: same shape, vault-sealed — the dispatcher's sealed
	// interception re-sends the request after the unlock sheet.
	h.setupAndUnseal()
	h.v.Seal()
	raw = jsonrpcCall(t, h.conn, "endpoints.create",
		endpointParams("OpenAI", "https://api.openai.com/v1", "sk-test-123"))
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %s", err, raw)
	}
	if env.Error == nil {
		t.Fatal("create with a key on a sealed vault must fail")
	}
	if env.Error.Code != -32001 {
		t.Errorf("sealed code = %d, want -32001 (vault-sealed)", env.Error.Code)
	}
	if env.Error.Data == nil || env.Error.Data["reason"] != "vault-sealed" {
		t.Errorf("sealed data = %v, want reason %q", env.Error.Data, "vault-sealed")
	}
	if eps := h_listHelper(t, h.conn); len(eps) != 0 {
		t.Errorf("endpoints after a sealed key mint = %+v, want none", eps)
	}
}

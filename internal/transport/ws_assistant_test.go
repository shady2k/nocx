package transport

// The assistant's control-plane methods (nocx-edio): agent.status and
// endpoints.probe, exercised over the real socket with a stub engine. The
// stub is the point of the interface: the engine's own integration tests
// (internal/assistant) prove the real streaming against a fake OpenAI
// server; here the handler's wiring — params, gates, store, wire shapes —
// is proven without dialling anything.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// stubAssistantClient is the injected engine: it answers Probe exactly as
// configured, so the tests drive every outcome without a network.
type stubAssistantClient struct {
	probe func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error)
}

func (s *stubAssistantClient) Probe(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
	if s.probe == nil {
		return assistant.ProbeResult{}, nil
	}
	return s.probe(ctx, p)
}

// Discard implements assistant.Client. This fake holds no suspended
// state, so there is nothing to drop.
func (*stubAssistantClient) Discard(string) {}

func (s *stubAssistantClient) Ask(ctx context.Context, p assistant.AskParams, onEvent func(assistant.AskEvent) error) error {
	return nil
}

// errProbeRefused is a Go error a stub can return to prove the handler
// surfaces engine refusals as RPC errors rather than probe outcomes.
var errProbeRefused = &probeRefusedError{}

type probeRefusedError struct{}

func (*probeRefusedError) Error() string { return "probe refused" }

type assistantHarness struct {
	t      *testing.T
	v      *vault.Vault
	ps     *profile.JSONStore
	probes *assistant.ProbeStore
	ws     *WSServer
	conn   *websocket.Conn
}

// scriptedEndpoints replaces the ENDPOINT read of the wired store with a
// fixed answer: a list the wire cannot produce (an endpoint offering no
// models — ValidateEndpoint refuses one at create AND at update, so the
// only way into that state is a document written underneath us), or a
// failure. Everything else — roles, the default, profiles, groups — is the
// real JSONStore, because those are the reads the handler must still make.
type scriptedEndpoints struct {
	list []profile.Endpoint
	err  error
}

// scriptedEndpointStore is the real store with LoadEndpoints scripted. It
// embeds *profile.JSONStore, so it satisfies every repository interface the
// server asserts for (profiles, groups, endpoints, roles) and only the one
// read is faked.
type scriptedEndpointStore struct {
	*profile.JSONStore
	script *scriptedEndpoints
}

func (s *scriptedEndpointStore) LoadEndpoints() ([]profile.Endpoint, error) {
	if s.script.err != nil {
		return nil, s.script.err
	}
	return s.script.list, nil
}

func newAssistantHarness(t *testing.T, stub *stubAssistantClient) *assistantHarness {
	t.Helper()
	return newAssistantHarnessWith(t, stub, nil)
}

func newAssistantHarnessWith(t *testing.T, stub *stubAssistantClient, script *scriptedEndpoints) *assistantHarness {
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

	var profiles profile.ProfileRepository = ps
	if script != nil {
		profiles = &scriptedEndpointStore{JSONStore: ps, script: script}
	}
	opts := []WSServerOption{
		WithProfileRepository(profiles), WithGroupRepository(ps),
		WithCredentialStore(v), WithVaultUnsealer(v), WithVaultLifecycle(v),
	}
	if stub != nil {
		opts = append(opts, WithAssistantClient(stub))
	}
	probes := assistant.NewProbeStore()
	opts = append(opts, WithAssistantProbeStore(probes))

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	return &assistantHarness{t: t, v: v, ps: ps, probes: probes, ws: ws, conn: conn}
}

func (h *assistantHarness) setupAndUnseal() {
	h.t.Helper()
	if _, err := h.v.Setup(h.t.Context(), vault.SetupRequest{Passphrase: "test"}); err != nil {
		h.t.Fatalf("Setup: %v", err)
	}
}

func (h *assistantHarness) createEndpoint(t *testing.T, params map[string]any) profile.EndpointDTO {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "endpoints.create", params)
	e, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d\nraw: %s", code, raw)
	}
	return e
}

func decodeAgentStatus(t *testing.T, raw []byte) (agentStatusResult, int) {
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
		return agentStatusResult{}, env.Error.Code
	}
	var res agentStatusResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, env.Result)
	}
	return res, 0
}

// credentialOf renders the wire credential enum for an assertion message.
func credentialOf(res agentStatusResult) string {
	if res.Credential == nil {
		return "null"
	}
	return *res.Credential
}

func testEndpointParams() map[string]any {
	return map[string]any{
		"name":    "Local",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	}
}

// ── agent.status ─────────────────────────────────────────────────────────

func TestAgentStatus_NoEndpoints(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	res, code := decodeAgentStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if res.EndpointConfigured {
		t.Fatalf("status = %+v, want nothing configured", res)
	}
	if res.Credential != nil {
		t.Fatalf("credential = %v, want null with no endpoint", *res.Credential)
	}
	if res.LastProbe != nil {
		t.Fatalf("lastProbe = %+v, want null", res.LastProbe)
	}
}

func TestAgentStatus_EndpointWithoutKey(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	p := testEndpointParams()
	delete(p, "key")
	e := h.createEndpoint(t, p)
	// The credential is now a fact about the endpoint the ROLE resolves to,
	// so the role must resolve before there is a credential to report at all
	// (nocx-rikz5). What this test is about — the classification — is
	// unchanged; its precondition is not.
	h.mustSetDefault(e.ID, "qwen3")

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	res, code := decodeAgentStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if !res.EndpointConfigured {
		t.Fatal("endpointConfigured = false, want true")
	}
	if res.Credential == nil || *res.Credential != credNone {
		t.Fatalf("credential = %v, want %q with no key", credentialOf(res), credNone)
	}
}

func TestAgentStatus_CredentialResolvable(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	e := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(e.ID, "qwen3")

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	res, code := decodeAgentStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if !res.EndpointConfigured {
		t.Fatalf("status = %+v, want configured", res)
	}
	if res.Credential == nil || *res.Credential != credResolvable {
		t.Fatalf("credential = %v, want %q", credentialOf(res), credResolvable)
	}
}

func TestAgentStatus_SealedVaultNotResolvable(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	e := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(e.ID, "qwen3")
	// Seal the vault: the key material is no longer readable, so the
	// credential is unresolvable even though the record still references it.
	h.v.Seal()

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	res, code := decodeAgentStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if !res.EndpointConfigured {
		t.Fatal("endpointConfigured = false, want true")
	}
	if res.Credential == nil || *res.Credential != credSealed {
		t.Fatalf("credential = %v, want %q with a sealed vault", credentialOf(res), credSealed)
	}
	// A read that REPORTS must never prompt: agent.status asks whether the
	// credential resolves and answers "no" while the vault is sealed — it
	// does not raise the unlock dialog on a settings-page read (ADR-0032).
	assertNoPendingAsk(t, h.ws)
}

func TestAgentStatus_LastProbe(t *testing.T) {
	ran := false
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			ran = true
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now()}, nil
		},
	})
	h.setupAndUnseal()

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk", "model": "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe: %s", raw)
	}
	if !ran {
		t.Fatal("the engine was not called")
	}

	raw = jsonrpcCall(t, h.conn, "agent.status", nil)
	res, code := decodeAgentStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if res.LastProbe == nil || !res.LastProbe.OK || res.LastProbe.EndpointName != "Local" {
		t.Fatalf("lastProbe = %+v, want the recorded probe", res.LastProbe)
	}
}

func TestAgentStatus_Unwired(t *testing.T) {
	// No profile repository: the endpoints gate refuses like profiles do.
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	raw := jsonrpcCall(t, conn, "agent.status", nil)
	if !strings.Contains(string(raw), "-32601") {
		t.Fatalf("agent.status without wiring = %s, want -32601", raw)
	}
}

// ── agent.status: the answering role's resolution (nocx-rikz5) ───────────
//
// These decode the wire into an ANONYMOUS struct rather than
// agentStatusResult on purpose: the point of the first one is to fail on the
// assertion — "ready with no model chosen" — and not on a compile error
// about a field that does not exist yet.

type wireAnswering struct {
	Ready    bool    `json:"ready"`
	Reason   *string `json:"reason"`
	Endpoint *string `json:"endpoint"`
	Model    *string `json:"model"`
}

type wireAgentStatus struct {
	EndpointConfigured bool          `json:"endpointConfigured"`
	Credential         *string       `json:"credential"`
	Answering          wireAnswering `json:"answering"`
}

func decodeWireStatus(t *testing.T, raw []byte) (wireAgentStatus, int) {
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
		return wireAgentStatus{}, env.Error.Code
	}
	var got wireAgentStatus
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, env.Result)
	}
	return got, 0
}

func reasonOf(a wireAnswering) string {
	if a.Reason == nil {
		return "null"
	}
	return *a.Reason
}

func (h *assistantHarness) mustSetDefault(endpointID, model string) {
	h.t.Helper()
	if err := h.ps.SetDefaultModel(profile.DefaultModel{EndpointID: endpointID, Model: model}); err != nil {
		h.t.Fatalf("SetDefaultModel: %v", err)
	}
}

// mustForceDefault writes a DANGLING default straight into the document,
// bypassing SetDefaultModel — which now refuses one, because the store owns
// the invariant "a stored default names an endpoint that exists and a model
// it offers" (bead nocx-rikz5). The state is still reachable in the product:
// another process deletes the endpoint, or an update drops the model, after
// the default was written. That is exactly what these rungs report, so the
// tests have to be able to produce it — the same reason scriptedEndpoints
// exists for an endpoint offering no models.
func (h *assistantHarness) mustForceDefault(endpointID, model string) {
	h.t.Helper()
	d, err := h.ps.LoadAll()
	if err != nil {
		h.t.Fatalf("LoadAll: %v", err)
	}
	d.DefaultModel = profile.DefaultModel{EndpointID: endpointID, Model: model}
	if err := h.ps.WriteAll(d); err != nil {
		h.t.Fatalf("WriteAll: %v", err)
	}
}

// endpointParamsWithModel extends the existing endpointParams (which pins
// one model id for every endpoint it makes) with the model NAMED: a role
// resolves to one (endpoint, model) pair, so these tests need the two
// endpoints to offer different models, which is exactly what endpointParams
// cannot express.
func endpointParamsWithModel(name, model string, withKey bool) map[string]any {
	key := "sk-test-123"
	if !withKey {
		key = ""
	}
	p := endpointParams(name, "http://127.0.0.1:11434/v1", key)
	p["models"] = []map[string]any{{"name": model}}
	return p
}

func TestAgentStatus_AnEndpointWithNoModelChosenIsNotReady(t *testing.T) {
	// The defect this task exists for: an endpoint with a resolvable key and
	// nothing assigned reported "Ready", and the refusal arrived one
	// keystroke later.
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	h.createEndpoint(t, testEndpointParams())

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	got, code := decodeWireStatus(t, raw)
	if code != 0 {
		t.Fatalf("agent.status: code %d\nraw: %s", code, raw)
	}
	if !got.EndpointConfigured {
		t.Fatal("endpointConfigured = false, want true — the endpoint exists")
	}
	if got.Answering.Ready {
		t.Fatalf("answering.ready = true with no model chosen — this is the lie\nraw: %s", raw)
	}
	if reasonOf(got.Answering) != "unassigned" {
		t.Fatalf("reason = %s, want unassigned", reasonOf(got.Answering))
	}
	// No resolved endpoint means no key the question is about: the old
	// aggregate would have reported the fleet's fact here.
	if got.Credential != nil {
		t.Fatalf("credential = %q, want null — no endpoint resolved", *got.Credential)
	}
}

func TestAgentStatus_NoEndpointsSaysSoRatherThanUnassigned(t *testing.T) {
	// Before `unassigned`: with no endpoints there is nothing to assign, and
	// sending a person to choose from an empty list is worse than silence.
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if got.Answering.Ready || reasonOf(got.Answering) != "no-endpoints" {
		t.Fatalf("answering = %+v, want not-ready with reason no-endpoints", got.Answering)
	}
}

func TestAgentStatus_ADefaultMakesItReadyAndNamesTheModel(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	e := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(e.ID, "qwen3")

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if !got.Answering.Ready {
		t.Fatalf("answering = %+v, want ready", got.Answering)
	}
	if got.Answering.Endpoint == nil || *got.Answering.Endpoint != "Local" {
		t.Fatalf("answering.endpoint = %v, want Local", got.Answering.Endpoint)
	}
	if got.Answering.Model == nil || *got.Answering.Model != "qwen3" {
		t.Fatalf("answering.model = %v, want qwen3", got.Answering.Model)
	}
	if got.Answering.Reason != nil {
		t.Fatalf("reason = %q on a ready status, want null", *got.Answering.Reason)
	}
}

func TestAgentStatus_AnAssignmentOutranksTheDefaultAndNamesItsOwnModel(t *testing.T) {
	// The role's own assignment wins: agent.status must report what will
	// actually answer, not the convenience it overrode.
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	chosen := h.createEndpoint(t, endpointParamsWithModel("chosen", "m-a", true))
	other := h.createEndpoint(t, endpointParamsWithModel("fallback", "m-b", true))
	h.mustSetDefault(other.ID, "m-b")
	if err := h.ps.AssignRole(profile.RoleAssignment{
		Role: profile.RoleAnswering, EndpointID: chosen.ID, Model: "m-a",
	}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	got, _ := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if !got.Answering.Ready || got.Answering.Endpoint == nil || *got.Answering.Endpoint != "chosen" {
		t.Fatalf("answering = %+v, want ready on chosen", got.Answering)
	}
}

func TestAgentStatus_TheCredentialIsTheResolvedEndpointsAndNoOther(t *testing.T) {
	// The defect: an unrelated healthy endpoint used to make the whole thing
	// report resolvable while the endpoint that would actually answer had no
	// key at all.
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	chosen := h.createEndpoint(t, endpointParamsWithModel("chosen", "m-a", false))
	h.createEndpoint(t, endpointParamsWithModel("unrelated", "m-b", true))
	h.mustSetDefault(chosen.ID, "m-a")

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if got.Credential == nil || *got.Credential != credNone {
		t.Fatalf("credential = %v, want none — chosen answers and chosen has no key", got.Credential)
	}
}

func TestAgentStatus_ADefaultWhoseEndpointIsGoneSaysEndpointGone(t *testing.T) {
	// The default's endpoint deleted by another process between load and
	// resolve: the clear-on-delete is the tidy path, not the safety net.
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	h.createEndpoint(t, testEndpointParams())
	h.mustForceDefault("endpoint:custom:gone:0000", "qwen3")

	got, _ := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if got.Answering.Ready || reasonOf(got.Answering) != "endpoint-gone" {
		t.Fatalf("answering = %+v, want reason endpoint-gone", got.Answering)
	}
	if got.Credential != nil {
		t.Fatalf("credential = %q, want null on a refusal", *got.Credential)
	}
}

func TestAgentStatus_ADefaultNamingARemovedModelSaysModelGone(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})
	h.setupAndUnseal()
	e := h.createEndpoint(t, testEndpointParams())
	h.mustForceDefault(e.ID, "a-model-this-endpoint-never-offered")

	got, _ := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if got.Answering.Ready || reasonOf(got.Answering) != "model-gone" {
		t.Fatalf("answering = %+v, want reason model-gone", got.Answering)
	}
}

func TestAgentStatus_AnEndpointOfferingNoModelsSaysSo(t *testing.T) {
	// "Choose a model" would open a picker with no options — a repair the
	// person cannot perform, so it gets its own rung.
	h := newAssistantHarnessWith(t, &stubAssistantClient{}, &scriptedEndpoints{
		list: []profile.Endpoint{{
			ID: "e1", Name: "empty", BaseURL: "http://127.0.0.1:11434/v1",
			Schema: profile.EndpointSchemaOpenAICompatible,
		}},
	})
	h.setupAndUnseal()

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if !got.EndpointConfigured {
		t.Fatal("endpointConfigured = false, want true — the endpoint exists")
	}
	if got.Answering.Ready || reasonOf(got.Answering) != "no-models" {
		t.Fatalf("answering = %+v, want reason no-models", got.Answering)
	}
}

// `no-models` may not shadow `endpoint-gone`. A default naming an endpoint
// that is gone, while the surviving endpoint happens to offer no models,
// used to report `no-models` — which sends the person to add a model to an
// endpoint they never chose, instead of to the choice that actually broke.
func TestAgentStatus_ADanglingEndpointOutranksNoModels(t *testing.T) {
	h := newAssistantHarnessWith(t, &stubAssistantClient{}, &scriptedEndpoints{
		list: []profile.Endpoint{{
			ID: "e-empty", Name: "empty", BaseURL: "http://127.0.0.1:11434/v1",
			Schema: profile.EndpointSchemaOpenAICompatible,
		}},
	})
	h.setupAndUnseal()
	h.mustForceDefault("endpoint:custom:gone:0000", "qwen3")

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if got.Answering.Ready || reasonOf(got.Answering) != "endpoint-gone" {
		t.Fatalf("reason = %s, want endpoint-gone — an explicit dangling selection keeps its own rung", reasonOf(got.Answering))
	}
}

// And `no-models` may not shadow `model-gone`. The chosen endpoint survives
// but no longer offers the chosen model, and it is the only endpoint there,
// so nothing offers a model at all — the answer is still what broke, not
// the fleet-wide fact.
func TestAgentStatus_ARemovedModelOutranksNoModels(t *testing.T) {
	h := newAssistantHarnessWith(t, &stubAssistantClient{}, &scriptedEndpoints{
		list: []profile.Endpoint{{
			ID: "e1", Name: "stripped", BaseURL: "http://127.0.0.1:11434/v1",
			Schema: profile.EndpointSchemaOpenAICompatible,
		}},
	})
	h.setupAndUnseal()
	h.mustForceDefault("e1", "qwen3")

	got, code := decodeWireStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if got.Answering.Ready || reasonOf(got.Answering) != "model-gone" {
		t.Fatalf("reason = %s, want model-gone — the selected endpoint is still there, its model is not", reasonOf(got.Answering))
	}
}

func TestAgentStatus_AStoreThatCannotAnswerIsARungNotAnError(t *testing.T) {
	h := newAssistantHarnessWith(t, &stubAssistantClient{}, &scriptedEndpoints{
		err: errors.New("disk gone"),
	})
	h.setupAndUnseal()

	raw := jsonrpcCall(t, h.conn, "agent.status", nil)
	got, code := decodeWireStatus(t, raw)
	if code != 0 {
		t.Fatalf("code = %d; a store failure must be a reported state, not an RPC error\nraw: %s", code, raw)
	}
	if got.Answering.Ready || reasonOf(got.Answering) != "unavailable" {
		t.Fatalf("answering = %+v, want reason unavailable", got.Answering)
	}
	if got.EndpointConfigured {
		t.Fatal("endpointConfigured = true off a store that cannot answer")
	}
}

func TestAgentStatus_AProbeOfAnotherModelIsNotReportedForThisOne(t *testing.T) {
	// A probe describes ONE endpoint and model. "Last test ok" about a model
	// nobody is asking is a lie the person cannot see through.
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			return assistant.ProbeResult{
				EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now(),
			}, nil
		},
	})
	h.setupAndUnseal()
	e := h.createEndpoint(t, testEndpointParams())
	h.mustSetDefault(e.ID, "qwen3")

	// A probe of a DIFFERENT model on the same endpoint.
	if raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk", "model": "some-other-model",
	}); isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe: %s", raw)
	}
	res, code := decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if code != 0 {
		t.Fatalf("agent.status: code %d", code)
	}
	if res.LastProbe != nil {
		t.Fatalf("lastProbe = %+v, want null — it describes some-other-model", res.LastProbe)
	}

	// The same endpoint AND the same model: now it does describe this one.
	if raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk", "model": "qwen3",
	}); isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe: %s", raw)
	}
	res, _ = decodeAgentStatus(t, jsonrpcCall(t, h.conn, "agent.status", nil))
	if res.LastProbe == nil || res.LastProbe.Model != "qwen3" {
		t.Fatalf("lastProbe = %+v, want the qwen3 probe", res.LastProbe)
	}
}

// ── endpoints.probe ───────────────────────────────────────────────────────

func TestEndpointsProbe_ProbesTheDraft(t *testing.T) {
	var got assistant.ProbeParams
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			got = p
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, ElapsedMS: 12, At: time.Now()}, nil
		},
	})
	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "sk-draft", "model": "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe: %s", raw)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %s", err, raw)
	}
	var res assistant.ProbeResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, env.Result)
	}
	if got.BaseURL != "http://127.0.0.1:11434/v1" || got.Model != "qwen3" || got.Name != "Local" {
		t.Fatalf("engine got %+v, want the draft values", got)
	}
	// The key rides the params once (an input, like create/update) and the
	// engine receives it as a credential.Secret.
	if got.Key.IsEmpty() {
		t.Fatal("engine received an empty key")
	}
	// The probe is recorded for agent.status.
	if last := h.probes.Last(); last == nil || !last.OK {
		t.Fatalf("probe store last = %+v, want the recorded probe", last)
	}
}

func TestEndpointsProbe_ProbeFailureIsAResult(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: false, Error: "dial tcp: connection refused", At: time.Now()}, nil
		},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:1/v1", "key": "", "noKey": true, "model": "qwen3",
	})

	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe failure should be a result, got an error: %s", raw)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\nraw: %s", err, raw)
	}
	var res assistant.ProbeResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, env.Result)
	}
	if res.OK || !strings.Contains(res.Error, "connection refused") {
		t.Fatalf("result = %+v, want !OK with the dial error", res)
	}
}

func TestEndpointsProbe_EngineGoErrorIsAnRPCError(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			return assistant.ProbeResult{}, errProbeRefused
		},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "", "noKey": true, "model": "qwen3",
	})
	if !strings.Contains(string(raw), "-32603") {
		t.Fatalf("endpoints.probe with a Go error = %s, want -32603", raw)
	}
	if last := h.probes.Last(); last != nil {
		t.Fatalf("a refused probe must not be recorded, got %+v", last)
	}
}

func TestEndpointsProbe_InvalidParams(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{"name": "Local"})
	if !strings.Contains(string(raw), "-32602") {
		t.Fatalf("endpoints.probe without baseUrl/model = %s, want -32602", raw)
	}
}

func TestEndpointsProbe_Unwired(t *testing.T) {
	h := newAssistantHarness(t, nil)
	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "key": "", "model": "qwen3",
	})
	if !strings.Contains(string(raw), "-32601") {
		t.Fatalf("endpoints.probe without an engine = %s, want -32601", raw)
	}
}

// ── endpoints.probe: the credential resolution rule (nocx-reu5) ──────────

// secretMaterial reads a credential.Secret's plaintext for an assertion.
func secretMaterial(t *testing.T, s credential.Secret) string {
	t.Helper()
	var material string
	if err := s.Use(func(b []byte) error {
		material = string(b)
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return material
}

// The saved-endpoint case the button exists for: an EMPTY key field probes
// with the credential the endpoint OWNS — resolved by the backend from the
// vault, never re-sent by the renderer (ADR-0030 §3). The draft's baseUrl
// and model stay the probe target; only the credential is resolved.
func TestEndpointsProbe_SavedEndpointResolvesStoredCredential(t *testing.T) {
	var got assistant.ProbeParams
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			got = p
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now()}, nil
		},
	})
	h.setupAndUnseal()
	created := h.createEndpoint(t, map[string]any{
		"name":    "OpenAI",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-stored-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "OpenAI",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "",
		"model":      "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe on a saved endpoint: %s", raw)
	}
	if got.Key.IsEmpty() {
		t.Fatal("engine received an empty key, want the stored credential")
	}
	if material := secretMaterial(t, got.Key); material != "sk-stored-123" {
		t.Fatalf("engine key = %q, want the stored credential sk-stored-123", material)
	}
	if got.BaseURL != "http://127.0.0.1:11434/v1" || got.Model != "qwen3" {
		t.Fatalf("engine got %+v, want the draft's target", got)
	}
}

// A key typed into the form WINS over the stored one — testing a new key
// before saving it is the other half of what the button is for. The stored
// credential must not be consulted (or dialled) when the form has a key.
func TestEndpointsProbe_TypedKeyWinsOverStored(t *testing.T) {
	var got assistant.ProbeParams
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			got = p
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now()}, nil
		},
	})
	h.setupAndUnseal()
	created := h.createEndpoint(t, map[string]any{
		"name":    "OpenAI",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-stored-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "OpenAI",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "sk-typed-456",
		"model":      "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe with a typed key: %s", raw)
	}
	if material := secretMaterial(t, got.Key); material != "sk-typed-456" {
		t.Fatalf("engine key = %q, want the TYPED key sk-typed-456 — a typed key must win over the stored one", material)
	}
}

// A sealed vault with a saved credential is a probe RESULT naming that —
// never a Go error, never a silent no-key dial (which would 401 and lie).
// The engine must not be called at all.
func TestEndpointsProbe_SealedVaultIsTheCanonicalError(t *testing.T) {
	// A sealed vault is a sealed-vault failure: the canonical -32001 /
	// vault-sealed error the renderer's dispatcher turns into the unlock
	// prompt, and the probe is re-sent once the vault answers (ADR-0032).
	// Never a probe RESULT naming the sealed state — that was the dead end
	// this bead exists to delete.
	var called bool
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			called = true
			return assistant.ProbeResult{OK: true}, nil
		},
	})
	h.setupAndUnseal()
	created := h.createEndpoint(t, map[string]any{
		"name":    "OpenAI",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-stored-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	})
	h.v.Seal()

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "OpenAI",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "",
		"model":      "qwen3",
	})
	if called {
		t.Fatal("the engine was called without a credential — the probe must refuse, not dial unauthenticated")
	}
	var errResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(raw))
	}
	if errResp.Error == nil {
		t.Fatalf("a sealed vault must be the canonical RPC error, not a result: %s", raw)
	}
	if errResp.Error.Code != vaultSealedCode {
		t.Fatalf("code = %d, want %d (vault-sealed)", errResp.Error.Code, vaultSealedCode)
	}
	if errResp.Error.Data == nil || errResp.Error.Data.Reason != "vault-sealed" {
		t.Fatalf("data.reason = %v, want vault-sealed", errResp.Error.Data)
	}
	// No outcome was recorded as a probe result: the sealed failure is a
	// prompt, not a verdict about the endpoint.
	if last := h.probes.Last(); last != nil {
		t.Fatalf("probe store last = %+v, want no recorded outcome for a sealed failure", last)
	}
	assertNoPendingAsk(t, h.ws)
}

// A credential whose secret is deleted while the unlock is in flight: the
// Operation stance unlocks, the read then finds the secret gone, and the
// probe must be a refused RESULT naming the state — never a generic -32603
// toast (the review bug). The engine must not be called at all.
func TestEndpointsProbe_DeletedCredentialAfterUnlockIsARefusedResult(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			t.Fatal("the engine must not be called for a deleted credential")
			return assistant.ProbeResult{}, nil
		},
	})
	h.setupAndUnseal()
	created := h.createEndpoint(t, map[string]any{
		"name":    "OpenAI",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-stored-123",
		"models":  []map[string]any{{"name": "qwen3"}},
	})

	eps, err := h.ps.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	var ref credential.SecretID
	for _, ep := range eps {
		if ep.ID == created.ID {
			ref = credential.SecretID(ep.CredentialRef)
			break
		}
	}
	if ref == "" {
		t.Fatal("the created endpoint carries no credential reference")
	}

	h.v.Seal()
	// The unlock succeeds AND deletes the credential before the read returns:
	// the exact "deleted in parallel with the unlock" shape the bug describes.
	h.v.SetUnlockRequester(unlockRequesterFunc(func(ctx context.Context, reason string) error {
		if err := h.v.Unseal(ctx, vault.UnsealRequest{Passphrase: "test"}); err != nil {
			return err
		}
		return h.v.Delete(ctx, ref)
	}))

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "OpenAI",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "",
		"model":      "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("a deleted credential after unlock is a probe RESULT, not an RPC error: %s", raw)
	}
	var result assistant.ProbeResult
	if err := json.Unmarshal(mustResult(t, raw), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.OK {
		t.Fatalf("probe = %+v, want !OK for a deleted credential", result)
	}
	if last := h.probes.Last(); last == nil || last.OK {
		t.Fatalf("a refused credential probe must be recorded, got %+v", last)
	}
}

// An endpoint with NO credential (a local model) still probes without one.
func TestEndpointsProbe_KeylessEndpointProbesWithoutAKey(t *testing.T) {
	var got assistant.ProbeParams
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			got = p
			return assistant.ProbeResult{EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p), OK: true, At: time.Now()}, nil
		},
	})
	h.setupAndUnseal()
	created := h.createEndpoint(t, map[string]any{
		"name":    "Local",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"models":  []map[string]any{{"name": "qwen3"}},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "Local",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "",
		"model":      "qwen3",
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe on a keyless endpoint: %s", raw)
	}
	if !got.Key.IsEmpty() {
		t.Fatal("engine received a key, want none for a credential-less endpoint")
	}
}

// The renderer names a record that does not exist (deleted meanwhile): a
// caller error, exactly as connections.test surfaces a profile that does
// not resolve — never a fabricated probe verdict.
func TestEndpointsProbe_UnknownEndpointIsAnRPCError(t *testing.T) {
	called := false
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			called = true
			return assistant.ProbeResult{OK: true}, nil
		},
	})
	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": "endpoint:custom:nope:1",
		"name":       "OpenAI",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"key":        "",
		"model":      "qwen3",
	})
	if !strings.Contains(string(raw), "-32603") {
		t.Fatalf("endpoints.probe with an unknown endpoint id = %s, want -32603", raw)
	}
	if called {
		t.Fatal("the engine was called for an endpoint that does not exist")
	}
}

// probeKindFor mirrors the engine's own routing in the fakes: an empty model
// means the connection check ran, a named model means the model check did.
// The fakes must not report a kind the params could not have produced.
func probeKindFor(p assistant.ProbeParams) assistant.ProbeKind {
	if p.Model == "" {
		return assistant.ProbeConnection
	}
	return assistant.ProbeModel
}

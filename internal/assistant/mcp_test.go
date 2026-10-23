package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

type recordingMCPRuntime struct {
	mu           sync.Mutex
	err          error
	result       *mcp.Result
	closed       []string
	calls        int
	invocation   mcp.Invocation
	beforeInvoke func() error
}

func (*recordingMCPRuntime) Refresh(context.Context, mcp.Activation) (mcp.Catalog, error) {
	return mcp.Catalog{}, errors.New("unexpected MCP refresh")
}

func (r *recordingMCPRuntime) Invoke(_ context.Context, invocation mcp.Invocation) (mcp.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.beforeInvoke != nil {
		if err := r.beforeInvoke(); err != nil {
			return mcp.Result{}, err
		}
	}
	r.calls++
	r.invocation = invocation
	if r.err != nil {
		return mcp.Result{}, r.err
	}
	if r.result != nil {
		return *r.result, nil
	}
	return mcp.Result{ServerID: invocation.Activation.ServerID, Tool: invocation.RemoteTool, Text: []string{"called"}, Resources: []mcp.Resource{}, Omitted: []mcp.Omitted{}}, nil
}

func (r *recordingMCPRuntime) CloseRun(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, id)
}
func (*recordingMCPRuntime) CloseServer(string) {}
func (*recordingMCPRuntime) Close() error       { return nil }

func assistantMCPServer() profile.MCPServer {
	input := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"string"}}}`)
	canonical, err := profile.CanonicalMCPToolDescriptor(profile.MCPTool{Name: "echo", InputSchema: input})
	if err != nil {
		panic(err)
	}
	input = canonical.InputSchema
	descriptorDigest := canonical.DescriptorDigest
	catalog := sha256.New()
	_, _ = catalog.Write([]byte(descriptorDigest))
	_, _ = catalog.Write([]byte{0})
	now := time.Now().UTC()
	return profile.MCPServer{
		ID: "server-kernel", Revision: 9, Name: "Kernel fixture", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/echo", Argv: []string{}, Env: []profile.MCPEnvBinding{}},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
		Catalog: profile.MCPCatalog{State: profile.MCPCatalogFresh, RefreshedAt: &now, Digest: hex.EncodeToString(catalog.Sum(nil)), Tools: []profile.MCPTool{{Name: "echo", InputSchema: input, DescriptorDigest: descriptorDigest, Enabled: true, Status: profile.MCPToolUnchanged}}},
	}
}

func TestMCPRuntimeIsReachedOnlyThroughEffectKernel(t *testing.T) {
	snapshot, err := agenttools.NewMCPCatalogSnapshot(assistantMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	base, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := base.WithMCP([]agenttools.MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &recordingMCPRuntime{}
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-kernel"}})
	ledger := &fakeLedger{}
	runtime.beforeInvoke = func() error {
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		for _, event := range ledger.log {
			if len(event) >= len("start:") && event[:len("start:")] == "start:" {
				return nil
			}
		}
		return errors.New("runtime invoked before ledger StartExecution")
	}
	kernel, err := newEffectKernel(nil, grant, reg, ledger, NewApprovalStore(), &fakeKnownMaterial{}, "run-kernel", "", 1, "", nil, Attachments{}, nil, nil, toolSeams{mcpRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	// Registry construction, eligibility, projection, and search are all local.
	_ = reg.ForGrant(grant)
	_ = reg.Project(grant, agenttools.PresentationConfig{Lazy: true, SchemaTokenLimit: 1})
	_ = reg.Search(grant, "echo", nil)
	if runtime.calls != 0 {
		t.Fatalf("inactive registry work invoked runtime %d times", runtime.calls)
	}
	name := agenttools.MCPModelName("server-kernel", "echo")
	out, err := kernel.Invoke(context.Background(), name, "call-1", `{"value":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 {
		t.Fatalf("runtime calls = %d, want exactly 1", runtime.calls)
	}
	if runtime.invocation.RunID != "run-kernel" || runtime.invocation.Activation.ServerID != "server-kernel" || runtime.invocation.Activation.ServerRevision != 9 || runtime.invocation.RemoteTool != "echo" || string(runtime.invocation.Arguments) != `{"value":"hello"}` {
		t.Fatalf("invocation was not exact: %+v", runtime.invocation)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("kernel output is not MCP result JSON: %q", out)
	}
}

func TestBoundedMCPErrorStillMatchesTheMCPResultContract(t *testing.T) {
	ctx := withToolBound(context.Background(), agenttools.ResultBound{
		MaxBytes:   1024,
		Truncation: agenttools.TruncationDropTail,
	})
	scope := &agenttools.MCPScope{ServerID: "server-kernel", RemoteTool: "echo"}
	out, err := boundedMCPError(mcp.ErrActivationChanged, ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var result mcp.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("failure result is not JSON: %v; output %q", err, out)
	}
	if result.ServerID != scope.ServerID || result.Tool != scope.RemoteTool || !result.IsError {
		t.Fatalf("failure result lost MCP identity: %+v", result)
	}
	if len(result.Text) != 1 || result.Text[0] != "MCP tool call failed: "+mcp.ErrActivationChanged.Error() {
		t.Fatalf("failure result text = %q", result.Text)
	}
	if result.Resources == nil || result.Omitted == nil {
		t.Fatalf("failure result has nullable collections: %+v", result)
	}
}

func TestMCPApprovalSuspensionDoesNotInvokeRuntime(t *testing.T) {
	snapshot, err := agenttools.NewMCPCatalogSnapshot(assistantMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	base, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := base.WithMCP([]agenttools.MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	grant := allRows(content.DecisionAsk).AsGrant([]content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-kernel"}})
	runtime := &recordingMCPRuntime{}
	kernel, err := newEffectKernel(nil, grant, reg, &fakeLedger{}, NewApprovalStore(), &fakeKnownMaterial{}, "run-ask", "", 1, "", nil, Attachments{}, nil, nil, toolSeams{mcpRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kernel.Invoke(context.Background(), agenttools.MCPModelName("server-kernel", "echo"), "call-ask", `{"value":"hello"}`)
	var approval *ApprovalRequestedError
	if !errors.As(err, &approval) {
		t.Fatalf("error = %v, want approval suspension", err)
	}
	if runtime.calls != 0 {
		t.Fatalf("approval suspension invoked runtime %d times", runtime.calls)
	}
}

func TestAskSetupComposesMCPWithoutInvokingRuntime(t *testing.T) {
	snapshot, err := agenttools.NewMCPCatalogSnapshot(assistantMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	fake, server := newFakeOpenAI(nil)
	defer server.Close()
	client, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-kernel"}})
	runtime := &recordingMCPRuntime{}
	params := testAskParams(server.URL)
	params.RunID = "run-setup"
	params.Grant = &grant
	params.AttemptLedger = &fakeLedger{}
	params.KnownMaterial = &fakeKnownMaterial{}
	params.MCPCatalogs = []agenttools.MCPCatalogSnapshot{snapshot}
	params.MCPRuntime = runtime
	if err := client.Ask(context.Background(), params, func(AskEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 {
		t.Fatalf("Ask setup/model declaration invoked runtime %d times", runtime.calls)
	}
	names := toolNames(t, requestTools(t, fake.body()))
	mcpName := agenttools.MCPModelName("server-kernel", "echo")
	for _, name := range names {
		if name == mcpName {
			t.Fatalf("MCP tool %q was declared before tools.search loaded it", mcpName)
		}
	}
	foundSearch := false
	for _, name := range names {
		if name == "tools.search" {
			foundSearch = true
			break
		}
	}
	if !foundSearch {
		t.Fatalf("model-visible declared tools %v do not contain tools.search", names)
	}
}

func mcpKernelFor(t *testing.T, policy content.EffectPolicy, runtime *recordingMCPRuntime, ledger *fakeLedger, approvals *ApprovalStore, known KnownMaterial) *effectKernel {
	t.Helper()
	snapshot, err := agenttools.NewMCPCatalogSnapshot(assistantMCPServer())
	if err != nil {
		t.Fatal(err)
	}
	base, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := base.WithMCP([]agenttools.MCPCatalogSnapshot{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-kernel"}})
	kernel, err := newEffectKernel(nil, grant, registry, ledger, approvals, known, "run-mcp", "", 1, "", nil, Attachments{}, nil, nil, toolSeams{mcpRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	return kernel
}

func TestMCPFailuresPreserveLedgerClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		recoverable bool
	}{
		{"stale", mcp.ErrCatalogStale, true},
		{"revision", mcp.ErrActivationChanged, true},
		{"reconnect", mcp.ErrOAuthReconnectRequired, true},
		{"credential", mcp.ErrSecretUnavailable, true},
		{"protocol", mcp.ErrProtocol, false},
		{"schema", mcp.ErrSchema, false},
		{"deadline", context.DeadlineExceeded, false},
		{"cancel", context.Canceled, false},
		{"unknown", errors.New("unknown failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &recordingMCPRuntime{err: tc.err}
			ledger := &fakeLedger{}
			kernel := mcpKernelFor(t, autonomousMatrix(), runtime, ledger, NewApprovalStore(), &fakeKnownMaterial{})
			out, err := kernel.Invoke(context.Background(), agenttools.MCPModelName("server-kernel", "echo"), "call", `{"value":"a"}`)
			if tc.recoverable {
				if err != nil {
					t.Fatal(err)
				}
				var envelope mcp.Result
				if json.Unmarshal([]byte(out), &envelope) != nil || !envelope.IsError {
					t.Fatalf("missing recoverable envelope: %q", out)
				}
			} else {
				if err == nil || out != "" {
					t.Fatalf("terminal failure became a successful result: %q %v", out, err)
				}
			}
			want := "finish:" + string(content.EntryFailure)
			if tc.recoverable {
				want = "finish:" + string(content.EntrySuccess)
			}
			if len(ledger.log) == 0 || ledger.log[len(ledger.log)-1] != want {
				t.Fatalf("ledger = %v, want %q", ledger.log, want)
			}
		})
	}
}

func TestMCPRefusalUnknownArgumentsAndFailedAttemptNeverActivate(t *testing.T) {
	name := agenttools.MCPModelName("server-kernel", "echo")
	for _, tc := range []struct {
		name, tool, args  string
		refuse, failStart bool
	}{
		{name: "refusal", tool: name, args: `{"value":"a"}`, refuse: true},
		{name: "unknown", tool: "mcp_not_in_snapshot", args: `{}`},
		{name: "invalid arguments", tool: name, args: `{"value":7}`},
		{name: "failed durable attempt", tool: name, args: `{"value":"a"}`, failStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := autonomousMatrix()
			if tc.refuse {
				policy = allRows(content.DecisionRefuse)
			}
			runtime := &recordingMCPRuntime{}
			kernel := mcpKernelFor(t, policy, runtime, &fakeLedger{failStart: tc.failStart}, NewApprovalStore(), &fakeKnownMaterial{})
			_, _ = kernel.Invoke(context.Background(), tc.tool, "call", tc.args)
			if runtime.calls != 0 {
				t.Fatalf("unauthorized work activated %d times", runtime.calls)
			}
		})
	}
}

func TestMCPExactApprovalAndRetainedEgressNeverReinvoke(t *testing.T) {
	runtime := &recordingMCPRuntime{}
	approvals := NewApprovalStore()
	kernel := mcpKernelFor(t, allRows(content.DecisionAsk), runtime, &fakeLedger{}, approvals, &fakeKnownMaterial{})
	name := agenttools.MCPModelName("server-kernel", "echo")
	args := `{"value":"approved"}`
	_, err := kernel.Invoke(context.Background(), name, "call", args)
	var requested *ApprovalRequestedError
	if !errors.As(err, &requested) {
		t.Fatalf("expected approval: %v", err)
	}
	if requested.Request.Effect != content.EffectDelegate || requested.Request.Resource == nil || requested.Request.Resource.ID != "mcp+stdio:server-kernel" {
		t.Fatalf("approval lost exact delegate destination: %+v", requested.Request)
	}
	if runtime.calls != 0 || len(runtime.closed) == 0 {
		t.Fatalf("approval activated or retained sessions: %+v", runtime)
	}
	approval := kernel.proposal(name, "call", args)
	wrong := approval
	wrong.ArgHash = canonicalArgHash(`{"value":"different"}`)
	if approvals.Approve(wrong) {
		t.Fatal("approved different arguments")
	}
	if !approvals.Approve(approval) {
		t.Fatal("exact approval not pending")
	}
	if _, invokeErr := kernel.Invoke(context.Background(), name, "call", args); invokeErr != nil {
		t.Fatal(invokeErr)
	}
	if runtime.calls != 1 {
		t.Fatalf("approved call count = %d", runtime.calls)
	}

	const secret = "sk-proj-abcdefghijklmnopqrstuvwx"
	runtime.result = &mcp.Result{ServerID: "server-kernel", Tool: "echo", Text: []string{secret}}
	kernel = mcpKernelFor(t, autonomousMatrix(), runtime, &fakeLedger{}, approvals, &fakeKnownMaterial{})
	_, err = kernel.Invoke(context.Background(), name, "egress-call", args)
	var egress *EgressRequestedError
	if !errors.As(err, &egress) {
		t.Fatalf("expected withheld egress: %v", err)
	}
	before := runtime.calls
	approval = kernel.proposal(name, "egress-call", args)
	if !approvals.Approve(approval) {
		t.Fatal("egress approval not pending")
	}
	runtime.err = errors.New("must not execute again")
	out, err := kernel.Invoke(context.Background(), name, "egress-call", args)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != before || !strings.Contains(out, secret) {
		t.Fatalf("retained replay changed: calls %d/%d, %q", runtime.calls, before, out)
	}
	if _, _, retained := approvals.RetainedResult(approval); retained {
		t.Fatal("released result remained retained")
	}
}

func TestMCPResultIdentitySchemaAndBoundsFailClosed(t *testing.T) {
	for _, result := range []mcp.Result{
		{ServerID: "other-server", Tool: "echo"},
		{ServerID: "server-kernel", Tool: "echo", Text: make([]string, 65)},
		{ServerID: "server-kernel", Tool: "echo", Text: []string{strings.Repeat("x", 262144)}},
	} {
		runtime := &recordingMCPRuntime{result: &result}
		kernel := mcpKernelFor(t, autonomousMatrix(), runtime, &fakeLedger{}, NewApprovalStore(), &fakeKnownMaterial{})
		if out, err := kernel.Invoke(context.Background(), agenttools.MCPModelName("server-kernel", "echo"), "call", `{"value":"a"}`); err == nil || out != "" {
			t.Fatalf("invalid runtime result escaped: %q %v", out, err)
		}
	}
}

func TestMCPAskSuspensionsCloseSessionsButKeepResumableCheckpoint(t *testing.T) {
	for _, gate := range []string{"policy", "egress"} {
		t.Run(gate, func(t *testing.T) {
			snapshot, err := agenttools.NewMCPCatalogSnapshot(assistantMCPServer())
			if err != nil {
				t.Fatal(err)
			}
			name, args := agenttools.MCPModelName("server-kernel", "echo"), `{"value":"approved"}`
			fake, server := newFakeOpenAI(reProposingModel(name, args))
			defer server.Close()
			clientAPI, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
			if err != nil {
				t.Fatal(err)
			}
			impl, ok := clientAPI.(*client)
			if !ok {
				t.Fatalf("client = %T, want *client", clientAPI)
			}
			policy := allRows(content.DecisionAsk)
			runtime := &recordingMCPRuntime{}
			if gate == "egress" {
				policy = autonomousMatrix()
				runtime.result = &mcp.Result{ServerID: "server-kernel", Tool: "echo", Text: []string{"sk-proj-abcdefghijklmnopqrstuvwx"}}
			}
			grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceDestination, ID: "mcp+stdio:server-kernel"}})
			approvals := NewApprovalStore()
			params := askParams(server.URL, &grant, &fakeLedger{}, approvals)
			params.MCPCatalogs, params.MCPRuntime = []agenttools.MCPCatalogSnapshot{snapshot}, runtime
			err = clientAPI.Ask(context.Background(), params, func(AskEvent) error { return nil })
			var approval *ApprovalRequestedError
			var egress *EgressRequestedError
			if gate == "policy" && !errors.As(err, &approval) || gate == "egress" && !errors.As(err, &egress) {
				t.Fatalf("wrong suspension for %s: %v", gate, err)
			}
			if _, ok := impl.checkpoints.resumable(params.RunID); !ok {
				t.Fatal("suspension discarded checkpoint")
			}
			if len(runtime.closed) == 0 {
				t.Fatal("suspension retained MCP session")
			}
			expectedCalls := 0
			if gate == "egress" {
				expectedCalls = 1
			}
			if runtime.calls != expectedCalls {
				t.Fatalf("calls before approval = %d", runtime.calls)
			}
			ap := Approval{RunID: params.RunID, Attempt: params.Attempt, Tool: name, CallID: "call_1", ArgHash: canonicalArgHash(args)}
			if !approvals.Approve(ap) {
				t.Fatal("exact suspended proposal was not pending")
			}
			if err := clientAPI.Ask(context.Background(), params, func(AskEvent) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if runtime.calls != 1 {
				t.Fatalf("resume invoked %d times, want once total", runtime.calls)
			}
			if _, ok := impl.checkpoints.resumable(params.RunID); ok {
				t.Fatal("terminal run retained checkpoint")
			}
			if !strings.Contains(fake.body(), "untrusted data") {
				t.Fatal("MCP result reached provider without untrusted framing")
			}
			impl.mcpMu.Lock()
			remaining := len(impl.mcpRuns)
			impl.mcpMu.Unlock()
			if remaining != 0 {
				t.Fatal("terminal run retained runtime ownership")
			}
		})
	}
}

func TestMCPLocalSearchLoadsUntrustedCatalogWithoutActivation(t *testing.T) {
	runtime := &recordingMCPRuntime{}
	kernel := mcpKernelFor(t, autonomousMatrix(), runtime, &fakeLedger{}, NewApprovalStore(), &fakeKnownMaterial{})
	middleware := &policyMiddleware{kernel: kernel, presentationState: newPresentationState(nil)}
	if projection := middleware.presentationProjection(); !projection.Lazy || len(projection.Catalog) == 0 {
		t.Fatal("small MCP catalog has no local discovery")
	}
	out, err := middleware.searchTools(context.Background(), `{"query":"echo"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "untrusted data") || !strings.Contains(out, agenttools.MCPModelName("server-kernel", "echo")) {
		t.Fatalf("catalog was not framed and discoverable: %q", out)
	}
	if runtime.calls != 0 {
		t.Fatal("local search activated runtime")
	}
	found := false
	for _, tool := range middleware.presentationProjection().Visible {
		if tool.Name == agenttools.MCPModelName("server-kernel", "echo") {
			found = true
		}
	}
	if !found {
		t.Fatal("local search failed to load discovered declaration")
	}
	kernel.grant = allRows(content.DecisionRefuse).AsGrant(kernel.grant.Scopes)
	for _, tool := range middleware.presentationProjection().Visible {
		if tool.Name == agenttools.MCPModelName("server-kernel", "echo") {
			t.Fatal("loaded metadata survived revoked grant")
		}
	}
}

func TestMCPDeclinedProposalNeverInvokes(t *testing.T) {
	runtime := &recordingMCPRuntime{}
	approvals := NewApprovalStore()
	kernel := mcpKernelFor(t, allRows(content.DecisionAsk), runtime, &fakeLedger{}, approvals, &fakeKnownMaterial{})
	name, args := agenttools.MCPModelName("server-kernel", "echo"), `{"value":"refused"}`
	_, err := kernel.Invoke(context.Background(), name, "call", args)
	var requested *ApprovalRequestedError
	if !errors.As(err, &requested) {
		t.Fatal(err)
	}
	if !approvals.Decline(kernel.proposal(name, "call", args), DeclineCallOnce) {
		t.Fatal("decline did not bind")
	}
	if out, err := kernel.Invoke(context.Background(), name, "call", args); err != nil || out == "" {
		t.Fatalf("decline did not return a refusal: %q %v", out, err)
	}
	if runtime.calls != 0 {
		t.Fatal("declined call activated runtime")
	}
}

func TestMCPKnownMaterialInSuccessAndRecoverableErrorIsWithheld(t *testing.T) {
	const secret = "short-private-value"
	for _, failed := range []bool{false, true} {
		runtime := &recordingMCPRuntime{}
		if failed {
			runtime.err = errors.Join(mcp.ErrSecretUnavailable, errors.New(secret))
		} else {
			runtime.result = &mcp.Result{ServerID: "server-kernel", Tool: "echo", Text: []string{secret}}
		}
		ledger := &fakeLedger{}
		kernel := mcpKernelFor(t, autonomousMatrix(), runtime, ledger, NewApprovalStore(), &knownMatcher{value: secret, name: "MCP-only secret"})
		out, err := kernel.Invoke(context.Background(), agenttools.MCPModelName("server-kernel", "echo"), "call", `{"value":"safe"}`)
		var egress *EgressRequestedError
		if out != "" || !errors.As(err, &egress) {
			t.Fatalf("known material escaped result: %q %v", out, err)
		}
		wire, err := json.Marshal(egress.Request)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), secret) {
			t.Fatal("approval exposed known material")
		}
		if len(ledger.captures) != 0 {
			t.Fatal("withheld known material persisted as a tool result")
		}
		if len(runtime.closed) == 0 {
			t.Fatal("egress suspension retained sessions")
		}
	}
}

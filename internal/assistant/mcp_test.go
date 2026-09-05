package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

type recordingMCPRuntime struct {
	calls        int
	invocation   mcp.Invocation
	beforeInvoke func() error
}

func (*recordingMCPRuntime) Refresh(context.Context, mcp.Activation) (mcp.Catalog, error) {
	return mcp.Catalog{}, errors.New("unexpected MCP refresh")
}

func (r *recordingMCPRuntime) Invoke(_ context.Context, invocation mcp.Invocation) (mcp.Result, error) {
	if r.beforeInvoke != nil {
		if err := r.beforeInvoke(); err != nil {
			return mcp.Result{}, err
		}
	}
	r.calls++
	r.invocation = invocation
	return mcp.Result{ServerID: invocation.Activation.ServerID, Tool: invocation.RemoteTool, Text: []string{"called"}, Resources: []mcp.Resource{}, Omitted: []mcp.Omitted{}}, nil
}
func (*recordingMCPRuntime) CloseRun(string)    {}
func (*recordingMCPRuntime) CloseServer(string) {}
func (*recordingMCPRuntime) Close() error       { return nil }

func assistantMCPServer() profile.MCPServer {
	input := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"string"}}}`)
	descriptor := sha256.New()
	_, _ = descriptor.Write([]byte("echo"))
	_, _ = descriptor.Write([]byte{0})
	_, _ = descriptor.Write(input)
	_, _ = descriptor.Write([]byte{0})
	descriptorDigest := hex.EncodeToString(descriptor.Sum(nil))
	catalog := sha256.New()
	_, _ = catalog.Write([]byte(descriptorDigest))
	_, _ = catalog.Write([]byte{0})
	now := time.Now().UTC()
	return profile.MCPServer{
		ID: "server-kernel", Revision: 9, Name: "Kernel fixture", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio:     &profile.MCPStdioConfig{Command: "/bin/echo", Argv: []string{}, Env: []profile.MCPEnvBinding{}},
		Limits:    profile.DefaultMCPLimits(),
		Catalog:   profile.MCPCatalog{State: profile.MCPCatalogFresh, RefreshedAt: &now, Digest: hex.EncodeToString(catalog.Sum(nil)), Tools: []profile.MCPTool{{Name: "echo", InputSchema: input, DescriptorDigest: descriptorDigest, Enabled: true, Status: profile.MCPToolUnchanged}}},
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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

func TestStdioRefreshAndCancellationCloseTheProcess(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	stopped := filepath.Join(t.TempDir(), "stopped")
	record := profile.MCPServer{
		ID: "mcp:stdio", Revision: 3, Name: "stdio", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio: &profile.MCPStdioConfig{
			Command: os.Args[0], Argv: []string{"-test.run=^TestStdioHelper$"},
			Env: []profile.MCPEnvBinding{
				{Name: "NOCX_MCP_HELPER", Value: literal("1")},
				{Name: "NOCX_MCP_STARTED", Value: literal(started)},
				{Name: "NOCX_MCP_STOPPED", Value: literal(stopped)},
			},
		},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
		Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}},
	}
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("stdio Refresh: %v", err)
	}
	waitFile(t, started)
	waitFile(t, stopped)
	stored, _ := catalog.ProfileCatalog()
	record.Catalog = stored
	activation, _ = ActivationFromServer(record)
	var waitDigest, doneDigest string
	for _, tool := range stored.Tools {
		switch tool.Name {
		case "wait":
			waitDigest = tool.DescriptorDigest
		case "done":
			doneDigest = tool.DescriptorDigest
		}
	}
	if waitDigest == "" || doneDigest == "" {
		t.Fatalf("stdio catalog tools = %+v, want wait and done", stored.Tools)
	}
	_ = os.Remove(stopped)
	_ = os.Remove(started)
	ctx, cancel := context.WithCancel(t.Context())
	called := make(chan error, 1)
	go func() {
		_, invokeErr := runtime.Invoke(ctx, Invocation{RunID: "cancel", Activation: activation, RemoteTool: "wait", DescriptorDigest: waitDigest, Arguments: json.RawMessage(`{}`)})
		called <- invokeErr
	}()
	waitFile(t, started)
	cancel()
	if invokeErr := <-called; !errors.Is(invokeErr, context.Canceled) {
		t.Fatalf("Invoke error = %v, want context.Canceled", invokeErr)
	}
	waitFile(t, stopped)

	_ = os.Remove(stopped)
	_ = os.Remove(started)
	record.Limits.CallTimeoutMS = 500
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Invoke(t.Context(), Invocation{
		RunID: "timeout", Activation: activation, RemoteTool: "wait",
		DescriptorDigest: waitDigest, Arguments: json.RawMessage(`{}`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed Invoke error = %v, want context.DeadlineExceeded", err)
	}
	waitFile(t, started)
	waitFile(t, stopped)

	_ = os.Remove(stopped)
	_ = os.Remove(started)
	record.Limits = profile.MCPLimits{
		StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
		IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
	}
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Invoke(t.Context(), Invocation{
		RunID: "mutation", Activation: activation, RemoteTool: "done",
		DescriptorDigest: doneDigest, Arguments: json.RawMessage(`{}`),
	})
	if err != nil || len(result.Text) != 1 || result.Text[0] != "done" {
		t.Fatalf("pooled Invoke result = %+v, err = %v", result, err)
	}
	waitFile(t, started)
	if err := runtime.RunServerMutations(t.Context(), []string{record.ID}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("RunServerMutations: %v", err)
	}
	waitFile(t, stopped)
}

func literal(value string) profile.MCPValueBinding {
	return profile.MCPValueBinding{Kind: profile.MCPBindingLiteral, Literal: &value}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

func TestStdioHelper(t *testing.T) {
	if os.Getenv("NOCX_MCP_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("NOCX_MCP_STARTED"), []byte("started"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "start marker")
		return
	}
	defer func() { _ = os.WriteFile(os.Getenv("NOCX_MCP_STOPPED"), []byte("stopped"), 0o600) }()
	if childStopped := os.Getenv("NOCX_MCP_CHILD_STOPPED"); childStopped != "" {
		// Keep the group leader alive until its child has observed group TERM.
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		// #nosec G204 -- re-executes this test binary with a fixed helper test selector.
		child := exec.Command(os.Args[0], "-test.run=^TestStdioDescendant$")
		// The race detector normally sleeps for one second after this helper's
		// test returns. That artificial delay prevents child.Wait from completing
		// before the production TERM/KILL boundary and kills the waiting leader
		// before its stopped marker. Disable only that delay, not race detection.
		child.Env = []string{
			"NOCX_MCP_DESCENDANT=1",
			"NOCX_MCP_CHILD_STOPPED=" + childStopped,
			"GORACE=atexit_sleep_ms=0",
		}
		child.Stderr = os.Stderr
		_ = os.Remove(childStopped + ".ready")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Wait() }()
		waitFile(t, childStopped+".ready")
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "stdio-fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		if called := os.Getenv("NOCX_MCP_CALLED"); called != "" {
			_ = os.WriteFile(called, []byte("called"), 0o600)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	server.AddTool(&sdk.Tool{Name: "done", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done"}}}, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "server stopped")
	}
}

func TestStdioDescendant(t *testing.T) {
	if os.Getenv("NOCX_MCP_DESCENDANT") != "1" {
		return
	}
	signal.Ignore(os.Interrupt)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if err := os.WriteFile(os.Getenv("NOCX_MCP_CHILD_STOPPED")+".ready", []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if err := os.WriteFile(os.Getenv("NOCX_MCP_CHILD_STOPPED"), []byte("group terminated"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStdioGroupDrainsBeforeRunCloseShutdownAndMutation(t *testing.T) {
	for _, action := range []string{"run", "shutdown", "mutation"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			started, stopped := filepath.Join(root, "started"), filepath.Join(root, "stopped")
			called, childStopped := filepath.Join(root, "called"), filepath.Join(root, "child-stopped")
			record := profile.MCPServer{
				ID: "mcp:group", Revision: 1, Name: "group", Enabled: true, Transport: profile.MCPTransportStdio,
				Stdio: &profile.MCPStdioConfig{Command: os.Args[0], Argv: []string{"-test.run=^TestStdioHelper$"}, Env: []profile.MCPEnvBinding{
					{Name: "NOCX_MCP_HELPER", Value: literal("1")},
					{Name: "NOCX_MCP_STARTED", Value: literal(started)},
					{Name: "NOCX_MCP_STOPPED", Value: literal(stopped)},
					{Name: "NOCX_MCP_CALLED", Value: literal(called)},
					{Name: "NOCX_MCP_CHILD_STOPPED", Value: literal(childStopped)},
				}},
				Limits:  profile.MCPLimits{StartupTimeoutMS: 5000, CallTimeoutMS: 10000, IdleTimeoutMS: 30000, MaxResultBytes: 4096},
				Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing},
			}
			manager := NewManager(nil)
			defer closeTestResource(t, manager)
			a := discoverFor(t, manager, activationFor(t, record))
			waitFile(t, stopped)
			waitFile(t, childStopped)
			if err := os.Remove(stopped); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(childStopped); err != nil {
				t.Fatal(err)
			}
			var digest string
			for _, tool := range a.Tools {
				if tool.Name == "wait" {
					digest = tool.DescriptorDigest
				}
			}
			done := make(chan error, 1)
			go func() {
				_, err := manager.Invoke(t.Context(), Invocation{RunID: "group-run", Activation: a, RemoteTool: "wait", DescriptorDigest: digest, Arguments: json.RawMessage(`{}`)})
				done <- err
			}()
			waitFile(t, called)
			assertExited := func() {
				if _, err := os.Stat(stopped); err != nil {
					t.Error("leader had not exited before mutation completed", err)
				}
				if _, err := os.Stat(childStopped); err != nil {
					t.Error("descendant had not exited before mutation completed", err)
				}
			}
			switch action {
			case "run":
				manager.CloseRun("group-run")
				assertExited()
			case "shutdown":
				_ = manager.Close()
				assertExited()
			case "mutation":
				if err := manager.RunServerMutations(t.Context(), []string{record.ID}, func(context.Context) error { assertExited(); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if err := await(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

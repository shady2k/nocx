package serverbin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/assistant/pydantic"
	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

// This intentionally uses the shipped CPython and native SDK, not a protocol
// echo child. A removable copy stands in for the mounted AppImage source.
func TestRuntimeTreeSurvivesSourceDeletion(t *testing.T) {
	packaged := os.Getenv("NOCX_TEST_AGENT_RUNTIME")
	if packaged == "" {
		if os.Getenv("NOCX_TEST_AGENT_RUNTIME_REQUIRED") == "1" {
			t.Fatal("required packaged runtime: set NOCX_TEST_AGENT_RUNTIME")
		}
		t.Skip("requires the native packaged runtime")
	}
	ctx, logger := logtest.New(t)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	target := runtime.GOOS + "/" + runtime.GOARCH
	installer := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS())
	mount := filepath.Join(t.TempDir(), "mounted-runtime")
	mounted, err := installer.Ensure(ctx, packaged, mount, target)
	if err != nil {
		t.Fatalf("create removable package source: %v", err)
	}
	retainInstall(t, mounted)
	staged, err := installer.Ensure(ctx, mounted.Path, filepath.Join(t.TempDir(), "profile", "agent-runtime"), target)
	if err != nil {
		t.Fatalf("stage mounted package: %v", err)
	}
	retainInstall(t, staged)
	if err = mounted.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(mount); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(mounted.Path); !os.IsNotExist(err) {
		t.Fatalf("source is still present: %v", err)
	}
	locator := runtimebundle.New(staged.Path, staged.Hash, target)
	if err = locator.Adopt(staged.Lease); err != nil {
		_ = staged.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locator.Close() })
	resolved, err := locator.Locate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ManifestDigest != mounted.Hash || resolved.Version != mounted.Hash {
		t.Fatalf("staging changed identity: %#v", resolved)
	}
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	client, err := pydantic.NewClient(pydantic.Config{NewHosts: func(g assistant.TextGenerator) assistant.RunHostFactory {
		return assistant.NewRunHostFactory(registry, content.Floor{}, g)
	}, HTTP: assistant.NewModelHTTPFactory(logger, nil), Registry: registry, Runtime: locator})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil || body.Model != "copied-runtime" || !body.Stream {
			t.Errorf("native request=%#v error=%v", body, decodeErr)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"staged\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"copied-runtime\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"copied runtime alive\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	type result struct {
		answer string
		err    error
	}
	results := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		answer, generateErr := client.Generate(ctx, assistant.ModelTarget{BaseURL: server.URL, Model: "copied-runtime"}, []assistant.Message{{Role: "user", Content: "answer after the source mount is gone"}})
		results <- result{answer, generateErr}
	}()
	defer func() {
		cancel()
		unblock()
		server.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("native worker did not stop")
		}
	}()
	select {
	case <-started:
	case early := <-results:
		t.Fatalf("worker exited before model request: %#v", early)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Drop every Go-owned descriptor as a coordinator crash would. The
	// actual Python process, blocked in native HTTP, must alone pin its tree.
	if err = locator.Close(); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(staged.Path)
	if lease, lockErr := runtimebundle.TryExclusive(parent, staged.Hash); !errors.Is(lockErr, runtimebundle.ErrLeaseBusy) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("Python did not inherit its live shared lease: %v", lockErr)
	}
	newSource, _ := treeFixtureTarget(t, "new expected version", target)
	current, err := installer.Ensure(ctx, newSource, parent, target)
	if err != nil {
		t.Fatal(err)
	}
	retainInstall(t, current)
	if err = installer.Prune(ctx, parent, current.Hash, target); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(staged.Path); err != nil {
		t.Fatalf("pruned live Python runtime: %v", err)
	}
	unblock()
	var completed result
	select {
	case completed = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if completed.err != nil || completed.answer != "copied runtime alive" || requests.Load() != 1 {
		t.Fatalf("copied native worker answer=%q requests=%d error=%v", completed.answer, requests.Load(), completed.err)
	}
	if err = installer.Prune(ctx, parent, current.Hash, target); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(staged.Path); !os.IsNotExist(err) {
		t.Fatalf("unused old runtime remained after child exit: %v", err)
	}
	if _, err = os.Stat(filepath.Join(parent, runtimebundle.LeaseDirectory, staged.Hash+".lock")); err != nil {
		t.Fatalf("pruning removed stable lock inode: %v", err)
	}
	if _, err = os.Stat(current.Path); err != nil {
		t.Fatalf("pruned current expected runtime: %v", err)
	}
}

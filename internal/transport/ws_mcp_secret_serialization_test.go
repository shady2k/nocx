package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

type serialTestConfigOperation struct {
	mu sync.Mutex
}

func (*serialTestConfigOperation) Disposition() capability.Disposition {
	return capability.Disposition{}
}

func (o *serialTestConfigOperation) Run(ctx context.Context, fn func(context.Context, capability.ConfigService) error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return fn(ctx, nil)
}

type blockingMCPSecretStore struct {
	deleteStarted chan struct{}
	releaseDelete chan struct{}
}

func (*blockingMCPSecretStore) Create(context.Context, credential.Secret) (credential.SecretID, error) {
	return "", errors.New("unused")
}

func (s *blockingMCPSecretStore) Delete(context.Context, credential.SecretID) error {
	close(s.deleteStarted)
	<-s.releaseDelete
	return nil
}

func (*blockingMCPSecretStore) Exists(context.Context, credential.SecretID) (bool, error) {
	return false, nil
}

func TestMCPServerDeleteSerializesOwnedSecretCleanupWithConfigOperations(t *testing.T) {
	repo := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	created, err := repo.CreateMCPServer(profile.MCPServer{
		Name:      "Owned secret",
		Enabled:   true,
		Transport: profile.MCPTransportStdio,
		Stdio: &profile.MCPStdioConfig{
			Command: "/usr/bin/printf",
			Argv:    []string{"ok"},
			Env: []profile.MCPEnvBinding{{
				Name: "TOKEN",
				Value: profile.MCPValueBinding{
					Kind: profile.MCPBindingSecret, SecretRef: "owned-secret", Owned: true,
				},
			}},
		},
		Limits: profile.MCPLimits{
			StartupTimeoutMS: 15_000, CallTimeoutMS: 60_000,
			IdleTimeoutMS: 30_000, MaxResultBytes: 262_144,
		},
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	secrets := &blockingMCPSecretStore{
		deleteStarted: make(chan struct{}), releaseDelete: make(chan struct{}),
	}
	op := &serialTestConfigOperation{}
	responder := &recordingResponder{}
	handlers := mcpServerHandlers{op: op, repo: repo, secrets: secrets, r: responder}
	deleteDone := make(chan struct{})
	go func() {
		handlers.handleDelete(context.Background(), jsonrpcRequest{
			ID:     json.RawMessage(`1`),
			Params: mustMarshal(mcpIDRevisionParams{ID: created.ID, Revision: created.Revision}),
		})
		close(deleteDone)
	}()

	select {
	case <-secrets.deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("delete never reached owned-secret cleanup")
	}

	secondOperationStarted := make(chan struct{})
	secondOperationDone := make(chan error, 1)
	go func() {
		secondOperationDone <- op.Run(context.Background(), func(context.Context, capability.ConfigService) error {
			close(secondOperationStarted)
			return nil
		})
	}()
	select {
	case <-secondOperationStarted:
		t.Fatal("config operation entered while owned-secret cleanup was in progress")
	case <-time.After(20 * time.Millisecond):
	}

	close(secrets.releaseDelete)
	select {
	case <-deleteDone:
	case <-time.After(time.Second):
		t.Fatal("server delete did not finish after secret cleanup was released")
	}
	select {
	case <-secondOperationStarted:
	case <-time.After(time.Second):
		t.Fatal("waiting config operation did not enter after cleanup")
	}
	if operationErr := <-secondOperationDone; operationErr != nil {
		t.Fatalf("second config operation: %v", operationErr)
	}
	servers, err := repo.ListMCPServers()
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers after delete = %v, err=%v", servers, err)
	}
	if responder.code != 0 {
		t.Fatalf("delete returned error: %d %s", responder.code, responder.msg)
	}
}

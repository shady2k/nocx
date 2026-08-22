package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
)

type accessRPCResponder struct {
	result json.RawMessage
	rpcErr *RPCError
}

func (r *accessRPCResponder) TryResult(_ json.RawMessage, result json.RawMessage) error {
	r.result = append(r.result[:0], result...)
	return nil
}

func (r *accessRPCResponder) TryError(_ json.RawMessage, rpcErr RPCError) error {
	r.rpcErr = &rpcErr
	return nil
}
func (r *accessRPCResponder) TryNotify(string, json.RawMessage) error { return nil }

type accessGrantStore struct {
	access sandbox.AccessClass
	path   string
}

func (s *accessGrantStore) AppendSandboxPath(access sandbox.AccessClass, path string) (int, error) {
	s.access, s.path = access, path
	return 9, nil
}

func TestSandboxAccessHandlersListAndResolveByEventID(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "outside")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	grant := &accessGrantStore{}
	inbox := sandbox.NewAccessInbox(grant)
	inbox.Record(sandbox.AccessObservation{
		Identity: sandbox.SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
		Shell:    "/bin/zsh", Executable: "/usr/bin/cat", Path: filepath.Join(directory, "file.txt"),
		Access: sandbox.AccessReadOnly, Operation: "openat", Source: sandbox.AccessSourceLinuxSeccomp, At: time.Unix(1, 0).UTC(),
	})
	responder := &accessRPCResponder{}
	handlers := sandboxAccessHandlers{inbox: inbox, r: responder}
	handlers.handleList(t.Context(), jsonrpcRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{"limit":10}`)})
	var page sandbox.AccessPage
	if err := json.Unmarshal(responder.result, &page); err != nil || len(page.Events) != 1 {
		t.Fatalf("list result = %s, err = %v", responder.result, err)
	}

	params, _ := json.Marshal(sandboxAccessResolveParams{EventID: page.Events[0].ID, Decision: sandbox.AccessDecisionGlobalReadWrite})
	responder.result, responder.rpcErr = nil, nil
	handlers.handleResolve(t.Context(), jsonrpcRequest{ID: json.RawMessage(`2`), Params: params})
	if responder.rpcErr != nil || grant.access != sandbox.AccessReadWrite || grant.path != directory {
		t.Fatalf("resolve err = %#v, grant = %#v", responder.rpcErr, grant)
	}
	var resolved sandbox.AccessEvent
	if err := json.Unmarshal(responder.result, &resolved); err != nil || resolved.State != sandbox.AccessStateGranted || resolved.SettingsRevision != 9 {
		t.Fatalf("resolve result = %s, err = %v", responder.result, err)
	}

	responder.result, responder.rpcErr = nil, nil
	handlers.handleResolve(t.Context(), jsonrpcRequest{ID: json.RawMessage(`3`), Params: params})
	if responder.rpcErr == nil || responder.rpcErr.Code != -32021 {
		t.Fatalf("second resolve error = %#v, want -32021", responder.rpcErr)
	}
}

func TestSandboxAccessHandlersListEncodesEmptyArray(t *testing.T) {
	responder := &accessRPCResponder{}
	handlers := sandboxAccessHandlers{inbox: sandbox.NewAccessInbox(nil), r: responder}
	handlers.handleList(t.Context(), jsonrpcRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{}`)})
	if string(responder.result) != `{"events":[],"revision":0,"lost":0}` {
		t.Fatalf("list result = %s, want contract-valid empty array", responder.result)
	}
}

func TestSandboxAccessValidatorsAreClosedAndBounded(t *testing.T) {
	if got := validateSandboxAccessResolveRaw(json.RawMessage(`{"eventId":"0123456789abcdef0123456789abcdef","decision":"always"}`)); got == "" {
		t.Fatal("unknown decision accepted")
	}
	if got := validateSandboxAccessListRaw(json.RawMessage(`{"limit":201}`)); got == "" {
		t.Fatal("oversized page accepted")
	}
	if got := validateSandboxAccessListRaw(json.RawMessage(`{"after":1}`)); got == "" {
		t.Fatal("unknown pagination field accepted")
	}
	if got := validateSandboxAccessResolveRaw(json.RawMessage(`{"eventId":"0123456789abcdef0123456789abcdef","decision":"dismiss"}`)); got != "" {
		t.Fatalf("valid resolve rejected: %s", got)
	}
}

package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	gitregistry "github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/transport/control"
)

type gateAcquisitionLog struct {
	mu     sync.Mutex
	counts map[string]int
}

func (l *gateAcquisitionLog) record(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[name]++
}

func (l *gateAcquisitionLog) count(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[name]
}

type recordingDomainAdmission struct {
	name string
	log  *gateAcquisitionLog
}

func (a *recordingDomainAdmission) Name() string { return a.name }

func (a *recordingDomainAdmission) TryAcquire(context.Context) (control.Permit, *control.Rejection) {
	a.log.record(a.name)
	return recordingDomainPermit{}, nil
}

type recordingDomainPermit struct{}

func (recordingDomainPermit) Release() {}

func TestMethodSpecOperationsAcquireEachDomainGateOnce(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	registry := newRegWithStub(logger)
	groups := profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))
	server := &WSServer{
		log:            logger,
		registry:       registry,
		groups:         groups,
		vaultLifecycle: newFakeVaultLifecycle(),
		contentDB:      content.NewStub(logger),
		apiCollections: apicoll.NewCollections(apiTestPaths(t)),
		git:            gitregistry.New(),
		filesys:        filesystem.New(),
	}

	lane := control.NewSemaphore("test-lane", 32)
	log := &gateAcquisitionLog{counts: make(map[string]int)}
	gates := map[string]*recordingDomainAdmission{}
	for _, name := range []string{
		"config", "vault", "content", "session", "git", "filesystem", "api",
	} {
		gates[name] = &recordingDomainAdmission{name: name, log: log}
	}

	configOp, endpointWired := server.buildConfigOp(lane, gates["config"], gates["vault"])
	cases := []struct {
		name   string
		gate   string
		specs  []methodSpec
		method string
		raw    string
	}{
		{
			name:   "config",
			gate:   "config",
			specs:  server.configSpecs(lane, gates["config"], gates["vault"], configOp, endpointWired, nil, nil),
			method: "groups.list",
			raw:    `{}`,
		},
		{
			name:   "vault",
			gate:   "vault",
			specs:  server.vaultSpecs(lane, gates["config"], gates["vault"]),
			method: "vault.status",
			raw:    `{}`,
		},
		{
			name:   "content",
			gate:   "content",
			specs:  server.contentSpecs(lane, gates["content"], control.ImmediateSubmission{}),
			method: "history.query",
			raw:    `{"scope":"everywhere"}`,
		},
		{
			name:   "session",
			gate:   "session",
			specs:  server.seamSpecs(lane, gates["session"]),
			method: "sessions.status",
			raw:    `{"profileIds":[]}`,
		},
		{
			name:   "git",
			gate:   "git",
			specs:  server.gitSpecs(lane, gates["session"], gates["git"]),
			method: "git.status",
			raw:    `{"bindingId":"0123456789abcdef0123456789abcdef"}`,
		},
		{
			name:   "filesystem",
			gate:   "filesystem",
			specs:  server.filesSpecs(lane, gates["session"], gates["filesystem"]),
			method: "files.list",
			raw:    `{"bindingId":"0123456789abcdef0123456789abcdef","path":"/","limit":1}`,
		},
		{
			name:   "api",
			gate:   "api",
			specs:  server.apiSpecs(lane, gates["api"], gates["vault"]),
			method: "api.collections.list",
			raw:    `{}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spec methodSpec
			for _, candidate := range tc.specs {
				if candidate.method == tc.method {
					spec = candidate
					break
				}
			}
			if spec.method == "" {
				t.Fatalf("methodSpec %q was not registered", tc.method)
			}

			r := &recordingResponder{}
			before := log.count(tc.gate)
			h := validated(spec, spec.build(&wsConn{}, &connState{}, r), r)
			h(context.Background(), jsonrpcRequest{
				ID:     json.RawMessage(`1`),
				Method: tc.method,
				Params: json.RawMessage(tc.raw),
			})

			if got := log.count(tc.gate) - before; got != 1 {
				t.Fatalf("domain gate %q acquired %d times; want exactly once by the operation (responder=%+v)", tc.gate, got, r)
			}
		})
	}
}

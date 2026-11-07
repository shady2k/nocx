package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	gitregistry "github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
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

	log := &gateAcquisitionLog{counts: make(map[string]int)}
	gates := map[string]*recordingDomainAdmission{}
	for _, name := range []string{
		"config", "vault", "content", "session", "filesystem", "api",
	} {
		gates[name] = &recordingDomainAdmission{name: name, log: log}
	}

	// The two stubs stand for the ONE plan production builds for a caller
	// that writes both stores; composing them here costs nothing because they
	// are two distinct objects, and each row below still counts the gate it
	// names.
	configVault := control.NewComposite(gates["config"], gates["vault"])
	configOp, endpointWired := server.buildConfigOp(configVault)
	// wantGate is how many times the domain gate is acquired. It is one
	// everywhere except filesystem, whose whole-domain gate went in stage 4
	// of ADR-8eej3e66: filesystem work is ordered per binding and bounded per
	// failure domain, so a call acquires NO domain gate — the assertion now
	// guards against the gate being reintroduced as much as against a
	// double acquisition.
	cases := []struct {
		name     string
		gate     string
		wantGate int
		specs    []methodSpec
		method   string
		raw      string
	}{
		{
			name:     "config",
			gate:     "config",
			wantGate: 1,
			specs:    server.configSpecs(gates["config"], configVault, configOp, endpointWired, nil, nil),
			method:   "groups.list",
			raw:      `{}`,
		},
		{
			name:     "vault",
			gate:     "vault",
			wantGate: 1,
			specs:    server.vaultSpecs(gates["config"], gates["vault"], capability.NewStoreGuard(catalog.Declarations(), time.Second)),
			method:   "vault.status",
			raw:      `{}`,
		},
		{
			name:     "content",
			gate:     "content",
			wantGate: 1,
			specs:    server.contentSpecs(gates["content"], control.ImmediateSubmission{}),
			method:   "history.query",
			raw:      `{"scope":"everywhere"}`,
		},
		{
			name:     "session",
			gate:     "session",
			wantGate: 1,
			specs:    server.seamSpecs(gates["session"]),
			method:   "sessions.status",
			raw:      `{"profileIds":[]}`,
		},
		{
			// NO GIT ROW: stage 5 removed the git domain gate (per-checkout
			// order and a per-mount bulkhead replaced it), so there is no
			// gate left to count. Filesystem keeps its row at zero, which is
			// stage 4's assertion that the gate was not reintroduced.
			name:     "filesystem",
			gate:     "filesystem",
			wantGate: 0,
			specs:    server.filesSpecs(gates["session"]),
			method:   "files.list",
			raw:      `{"bindingId":"0123456789abcdef0123456789abcdef","path":"/","limit":1}`,
		},
		{
			name:     "api",
			gate:     "api",
			wantGate: 1,
			specs:    server.apiSpecs(gates["api"], gates["vault"]),
			method:   "api.collections.list",
			raw:      `{}`,
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

			if got := log.count(tc.gate) - before; got != tc.wantGate {
				t.Fatalf("domain gate %q acquired %d times; want %d by the operation (responder=%+v)", tc.gate, got, tc.wantGate, r)
			}
		})
	}
}

package session

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestSpawnCarriesLifecycleLaunchWithoutSecretInProcessMetadata(t *testing.T) {
	const capability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	spawner := &captureSpawner{proc: &fakeProcess{shell: "/bin/bash"}}
	svc := New(Options{
		Generation: "gen",
		Spawner:    spawner,
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{1}, nil },
	})
	_, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-1", Domain: "dom-1", Epoch: 7, Capability: capability},
	}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if spawner.req.Lifecycle == nil || spawner.req.Lifecycle.Capability != capability {
		t.Fatalf("lifecycle launch not forwarded: %#v", spawner.req.Lifecycle)
	}
	if strings.Contains(spawner.proc.Shell(), capability) {
		t.Fatal("capability appeared in process shell metadata")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type captureSpawner struct {
	req  SpawnRequest
	proc Process
}

func (s *captureSpawner) Spawn(req SpawnRequest) (Process, error) {
	s.req = req
	return s.proc, nil
}

type fakeProcess struct{ shell string }

func (p *fakeProcess) Read([]byte) (int, error) { return 0, io.EOF }

func (p *fakeProcess) Write(b []byte) (int, error)                                  { return len(b), nil }
func (p *fakeProcess) Close() error                                                 { return nil }
func (p *fakeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *fakeProcess) Done() <-chan struct{}                                        { return make(chan struct{}) }

func (p *fakeProcess) WaitErr() (error, bool) { return nil, false }
func (p *fakeProcess) Pid() int               { return 1 }
func (p *fakeProcess) Shell() string          { return p.shell }

func (p *fakeProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

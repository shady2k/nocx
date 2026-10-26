package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	helperproto "github.com/shady2k/nocx/internal/helper/proto"
	helper "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// Only the reply is damaged: the real service still owns a real shell and
// its reservation. Refusing that reply must close both registries, rather
// than detach a shell that no returned pane can ever close.
type faultyLocalSpawnReply struct {
	*helper.Service
	mutate func(*helperproto.SpawnResult)
}

func (s *faultyLocalSpawnReply) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	result, err := s.Service.Call(ctx, op, params)
	if err != nil || op != helperproto.OpSpawn {
		return result, err
	}
	var p helperproto.SpawnParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.IdempotencyKey == "rejected-pane" {
		spawned, ok := result.(helperproto.SpawnResult)
		if !ok {
			return nil, errors.New("helper spawn returned an unexpected result type")
		}
		// The inventory owns its launch record; change only the wire copy.
		launch := *spawned.Entry.Launch.Local
		spawned.Entry.Launch.Local = &launch
		s.mutate(&spawned)
		result = spawned
	}
	return result, nil
}

func TestRefusedLocalPaneReleasesItsHelperBudgetAndRegistryEntry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*helperproto.SpawnResult)
	}{
		{"missing launch", func(r *helperproto.SpawnResult) { r.Entry.Launch.Local = nil }},
		{"invalid process", func(r *helperproto.SpawnResult) { r.Entry.Launch.Local.Pid = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			logger := discardLogger(t)
			const generation = "1111111111111111aaaaaaaaaaaaaaaa"
			const windowBytes = 2 * 64 << 10
			const rowBytes = 64 << 10
			const reservation = windowBytes + rowBytes
			svc := helper.New(helper.Options{
				Generation: helperproto.GenerationID(generation),
				Spawner:    helper.NewLocalSpawner(logger, helper.Shell{Path: "/bin/sh", Args: []string{"-i"}}, ""),
				Log:        logger,
				Limits: helper.Limits{
					DefaultWindowBytes: windowBytes, MinWindowBytes: windowBytes, MaxWindowBytes: windowBytes,
					DefaultRowBufferBytes: rowBytes, MinRowBufferBytes: rowBytes, MaxRowBufferBytes: rowBytes,
					BudgetBytes: 2 * reservation,
				},
			})
			serverConn, clientConn := net.Pipe()
			peer := helperhost.New(serverConn, serverConn, generation, "instance-a", logger)
			peer.Register(&faultyLocalSpawnReply{Service: svc, mutate: tc.mutate})
			release := svc.Bind(peer)
			served := make(chan error, 1)
			go func() { served <- peer.Serve(context.Background()) }()
			t.Cleanup(func() {
				_ = clientConn.Close()
				release()
				svc.Close()
				if err := <-served; err != nil {
					t.Errorf("helper host: %v", err)
				}
			})
			client, dialErr := helperclient.Dial(ctx, helperclient.Config{
				Exec: helperclient.NewSocketConn(clientConn), ExpectHash: generation,
				SentinelTTL: time.Second, Log: logger,
			})
			if dialErr != nil {
				t.Fatal(dialErr)
			}
			t.Cleanup(func() { _ = client.Close() })
			reg := session.New(log.NewSlogAdapter(logger), nil)
			opener := &localHelperOpener{
				client: client, registry: reg, log: logger, dir: t.TempDir(),
				installed: helperlocal.Installed{Binary: "/unused/nocx-helper", Generation: generation},
			}
			cfg := session.Config{Kind: session.KindLocal, Cwd: "/", Cols: 80, Rows: 24}
			held, selected, heldErr := opener.OpenHosted(ctx, cfg, "held-pane")
			if heldErr != nil || !selected {
				t.Fatalf("open the surviving pane: selected=%v, err=%v", selected, heldErr)
			}
			heldID := held.Session.ID()
			for i := range 3 {
				if i == 1 {
					// A coordinator detach preserves the running shell and its
					// reservation, even while another pane's startup fails.
					if err := reg.Close(heldID); err != nil {
						t.Fatal(err)
					}
				}
				if _, selected, err := opener.OpenHosted(ctx, cfg, "rejected-pane"); err == nil || !selected {
					t.Fatalf("iteration %d: malformed launch was not refused: selected=%v, err=%v", i, selected, err)
				}
				if used := svc.WindowBytesInUse(); used != reservation {
					t.Fatalf("iteration %d: refused pane retained helper budget: got %d, want surviving pane's %d", i, used, reservation)
				}
				wantRegistered := 0
				if i == 0 {
					wantRegistered = 1
				}
				if entries := reg.List(); len(entries) != wantRegistered {
					t.Fatalf("iteration %d: registry retains %d sessions, want %d", i, len(entries), wantRegistered)
				}
				inventory, err := client.Sessions(ctx)
				if err != nil || len(inventory) != 1 || inventory[0].HostSessionID.Session != string(heldID) || inventory[0].Exit != nil {
					t.Fatalf("iteration %d: failed open changed the surviving shell: inventory=%+v, err=%v", i, inventory, err)
				}
			}
			// The slot really is reusable, and the next refusal must still
			// protect both live sessions rather than evicting either one.
			replacement, _, replacementErr := opener.OpenHosted(ctx, cfg, "replacement-pane")
			if replacementErr != nil {
				t.Fatalf("open replacement after failed starts: %v", replacementErr)
			}
			_, _, budgetErr := opener.OpenHosted(ctx, cfg, "over-budget")
			var refusal *helperclient.RefusalError
			if !errors.As(budgetErr, &refusal) || refusal.Code != helperproto.ErrCodeWindowBudget {
				t.Fatalf("full live inventory must refuse another pane: %v", budgetErr)
			}
			if err := reg.EndSession(replacement.Session.ID()); err != nil {
				t.Fatal(err)
			}
			if used := svc.WindowBytesInUse(); used != reservation {
				t.Fatalf("closing replacement left %d bytes, want detached survivor's %d", used, reservation)
			}
			if err := client.CloseSession(ctx, helperclient.HostSessionID{Generation: generation, Session: string(heldID)}); err != nil {
				t.Fatal(err)
			}
			if used := svc.WindowBytesInUse(); used != 0 {
				t.Fatalf("ending the last shell retained %d bytes", used)
			}
			opener.registry = nil
			if _, selected, err := opener.OpenHosted(ctx, cfg, "unowned-pane"); err == nil || !selected {
				t.Fatalf("open without a registry was not refused: selected=%v, err=%v", selected, err)
			}
			inventory, err := client.Sessions(ctx)
			if err != nil || len(inventory) != 0 {
				t.Fatalf("open without a registry left helper sessions: inventory=%+v, err=%v", inventory, err)
			}
		})
	}
}

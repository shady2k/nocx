package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// The answer to pane.reconnect is the moment the client is entitled to press
// again. If the reply is written while the pane still holds its guard, that
// press is refused with "a reconnect is already running", although the work
// it asked for is over. These probes read the guard from inside the answer
// itself, so the ordering is observed rather than raced against.

type reconnectAnswerProbe struct {
	machine *WSServer
	paneID  string

	answers     int
	busy        []bool
	registering []bool
	kind        []string
}

func (p *reconnectAnswerProbe) observe(kind string) {
	p.machine.paneMu.Lock()
	defer p.machine.paneMu.Unlock()
	rec := p.machine.paneRecords[p.paneID]
	p.answers++
	p.kind = append(p.kind, kind)
	p.busy = append(p.busy, rec.busy)
	p.registering = append(p.registering, rec.registering != nil)
}

func (p *reconnectAnswerProbe) TryResult(json.RawMessage, json.RawMessage) error {
	p.observe("result")
	return nil
}

func (p *reconnectAnswerProbe) TryError(json.RawMessage, RPCError) error {
	p.observe("error")
	return nil
}

func (p *reconnectAnswerProbe) TryNotify(string, json.RawMessage) error { return nil }

type reconnectProbePolicy struct{ retryErr error }

func (*reconnectProbePolicy) PaneClosed(string) {}

func (*reconnectProbePolicy) ShellFailed(context.Context, string, uint64, int, string, any) {}

func (p *reconnectProbePolicy) RequestShellRetry(context.Context, string, uint64) error {
	return p.retryErr
}

type reconnectProbeHelper struct{ err error }

func (*reconnectProbeHelper) OpenHosted(context.Context, session.Config, string) (HostedSessionOpen, bool, error) {
	return HostedSessionOpen{}, false, errors.New("not in this test")
}

func (h *reconnectProbeHelper) RegisterReconnect(context.Context, session.Config, uint64) error {
	return h.err
}

type reconnectProbeOpenOp struct{ err error }

func (*reconnectProbeOpenOp) Disposition() capability.Disposition {
	return capability.Direct("test")
}

func (o *reconnectProbeOpenOp) Prepare(ctx context.Context, fn func(context.Context, capability.OpenService) error) error {
	if o.err != nil {
		return o.err
	}
	return fn(ctx, reconnectProbeOpenService{})
}

func (o *reconnectProbeOpenOp) Dial(ctx context.Context, fn func(context.Context, capability.OpenService) error) error {
	return fn(ctx, reconnectProbeOpenService{})
}

func (o *reconnectProbeOpenOp) Run(ctx context.Context, fn func(context.Context, capability.OpenService) error) error {
	return fn(ctx, reconnectProbeOpenService{})
}

type reconnectProbeOpenService struct{}

func (reconnectProbeOpenService) Resolve(string) (string, *ssh.ConnectConfig, error) {
	return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
}

func (reconnectProbeOpenService) Open(context.Context, session.Config) (session.Session, error) {
	return nil, errors.New("not in this test")
}

func (reconnectProbeOpenService) Close(session.ID) error { return nil }

func (reconnectProbeOpenService) EndSession(session.ID) error { return nil }

// TestPaneReconnectReleasesItsGuardBeforeAnswering pins the ordering on every
// branch that takes the guard: a refused shell retry, a refused open and a
// successful registration. The reply must find the pane free, because the
// client may press again as soon as it reads it.
func TestPaneReconnectReleasesItsGuardBeforeAnswering(t *testing.T) {
	const paneID = "pane-reconnect-release"
	const epoch = 7

	for _, tt := range []struct {
		name         string
		shellFailure bool
		retryErr     error
		openErr      error
		helperErr    error
		want         string
	}{
		{
			name:         "refused shell retry",
			shellFailure: true,
			retryErr:     refuse(-32001, "the vault is sealed"),
			want:         "error",
		},
		{
			name:    "refused open",
			openErr: refuse(-32602, "no such profile"),
			want:    "error",
		},
		{
			name: "successful registration",
			want: "result",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			ws := NewWSServer(logger, session.New(logger, nil))
			ws.lifetimeCtx = ctx
			wc := &wsConn{}
			ws.bindPane(paneID, wc, newConnState(), openParams{PaneID: paneID, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24})
			ws.paneMu.Lock()
			rec := ws.paneRecords[paneID]
			rec.offer = true
			rec.shellFailure = tt.shellFailure
			if ws.paneEpochs == nil {
				ws.paneEpochs = make(map[string]uint64)
			}
			ws.paneEpochs[paneID] = epoch
			ws.paneMu.Unlock()
			if tt.shellFailure {
				ws.paneConnectionPolicy = &reconnectProbePolicy{retryErr: tt.retryErr}
			}
			ws.opener = &sessionOpener{
				op: &reconnectProbeOpenOp{err: tt.openErr},
				resolver: &resolverHolder{r: &fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
					return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
				}}},
				helper: &reconnectProbeHelper{err: tt.helperErr},
			}

			probe := &reconnectAnswerProbe{machine: ws, paneID: paneID}
			h := paneReconnectHandlers{machine: ws, r: probe, w: wc}
			h.handle(ctx, jsonrpcRequest{
				ID:     json.RawMessage("1"),
				Params: json.RawMessage(`{"paneId":"pane-reconnect-release","connectionEpoch":7}`),
			})

			if probe.answers != 1 {
				t.Fatalf("answers = %d, want exactly one", probe.answers)
			}
			if probe.kind[0] != tt.want {
				t.Fatalf("answer kind = %s, want %s", probe.kind[0], tt.want)
			}
			if probe.busy[0] || probe.registering[0] {
				t.Fatalf("answer was written while the pane still held its guard (busy=%t registering=%t): the next press would be refused",
					probe.busy[0], probe.registering[0])
			}
		})
	}
}

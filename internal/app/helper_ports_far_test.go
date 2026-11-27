package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	fshost "github.com/shady2k/nocx/internal/filesystem/hostsvc"
	fslocal "github.com/shady2k/nocx/internal/filesystem/local"
	githost "github.com/shady2k/nocx/internal/git/hostsvc"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	gitregistry "github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/portsprobe"
	portshost "github.com/shady2k/nocx/internal/portsprobe/hostsvc"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
)

type portsHostTools struct {
	absent bool
	calls  atomic.Int32
}

func (r *portsHostTools) Run(_ context.Context, _ portsprobe.Command) (*portsprobe.Result, error) {
	r.calls.Add(1)
	if r.absent {
		return nil, portsprobe.ErrCommandAbsent
	}
	return &portsprobe.Result{Stdout: []byte("LISTEN 0 511 0.0.0.0:6768 0.0.0.0:* users:((\"app\",pid=871,fd=81))\n")}, nil
}

type gatedPortsHostTools struct {
	*portsHostTools
	release <-chan struct{}
}

func (r gatedPortsHostTools) Run(ctx context.Context, command portsprobe.Command) (*portsprobe.Result, error) {
	select {
	case <-r.release:
		return r.portsHostTools.Run(ctx, command)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type portsShellOpener struct {
	reg *session.Reg
}

func (o portsShellOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (transport.HostedSessionOpen, bool, error) {
	sess, err := o.reg.Adopt(ctx, cfg, session.ID(strings.ReplaceAll(cfg.PaneID, "-", "")), &s14PinChannel{done: make(chan struct{})})
	return transport.HostedSessionOpen{Session: sess}, true, err
}

type portsFarStand struct {
	t             *testing.T
	ws            *transport.WSServer
	conn          *websocket.Conn
	far           *farhelper.Registry
	panes         *panedriver.Driver
	tools         *portsHostTools
	seams         *gitFarSeams
	root          string
	next          int
	kill          chan func()
	resumeSamples chan struct{}
}

func newPortsFarStand(t *testing.T, mode hostmode.Mode, absent bool) *portsFarStand {
	t.Helper()
	root := t.TempDir()
	initRepo := exec.CommandContext(t.Context(), "git", "init", "-q")
	initRepo.Dir = root
	if out, err := initRepo.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("ports test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fsvc, err := fshost.New(fslocal.New(), fshost.JournalConfig{Root: t.TempDir(), Hostname: "fixture", InstanceID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsvc.Close() })
	tools := &portsHostTools{absent: absent}
	kill := make(chan func(), 2)
	resumeSamples := make(chan struct{})
	var served atomic.Bool
	peer := func(in io.Reader, out io.Writer) int {
		pipe, ok := in.(io.ReadCloser)
		if !ok {
			t.Error("the helper fixture requires a closable input lane")
			return 1
		}
		kill <- func() { _ = pipe.Close() }
		h := host.New(in, out, syntheticArtifactHash, "ports-peer", discardLogger(t))
		var runner portsprobe.Runner = tools
		if served.Swap(true) {
			runner = gatedPortsHostTools{portsHostTools: tools, release: resumeSamples}
		}
		h.Register(portshost.New(runner))
		h.Register(githost.New(gitlocal.NewFactory()))
		h.Register(fsvc)
		_ = h.Serve(context.Background())
		return 0
	}
	far, panes, seams := gitFarStandWithSeams(t, mode, &gitFarSeams{t: t, peer: peer}, true)
	logger := farHelperTestLogger(t)
	reg := session.New(logger, nil)
	sched := discovery.NewScheduler(far, logger, nil)
	t.Cleanup(func() { _ = sched.Close() })
	ws := transport.NewWSServer(logger, reg,
		transport.WithDiscoveryScheduler(sched),
		transport.WithHelperSessionOpener(portsShellOpener{reg: reg}),
		transport.WithProfileResolver(traceResolver{host: "far.example"}),
		transport.WithSSHConfigResolver(refusingOracle{cfg: ssh.HostConfig{HostName: "far.example", User: "tester", Port: 22}}, ""),
		transport.WithGitRegistry(gitregistry.New()),
		transport.WithGitRepoFactory(gitlocal.NewFactory()),
		transport.WithGitHelperFactory(helperGitFactory(far, func() *panedriver.Driver { return panes })),
		transport.WithFilesystemRegistry(filesystem.New()),
		transport.WithFilesystemProviderFactory(filesystemProviderFactory(far, func(pane, _ string) (string, bool) {
			return panes.ConnectionID(pane)
		})),
	)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	conn := dialTraceWSServer(t, ws)
	return &portsFarStand{t: t, ws: ws, conn: conn, far: far, panes: panes, tools: tools, seams: seams, root: root, kill: kill, resumeSamples: resumeSamples}
}

func (s *portsFarStand) call(method string, params map[string]any) *wsRPCResult {
	s.t.Helper()
	s.next++
	if err := s.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		s.t.Fatal(err)
	}
	return callAppWS(s.t, s.conn, method, params, s.next)
}

func (s *portsFarStand) open(pane, profile string) string {
	s.t.Helper()
	p := map[string]any{"kind": "ssh", "paneId": pane, "cols": 80, "rows": 24}
	if profile == "" {
		p["host"] = "far.example"
	} else {
		p["profileId"] = profile
	}
	reply := s.call("open", p)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(reply.Result, &result); reply.Error != nil || err != nil || result.SessionID == "" {
		s.t.Fatalf("opening pane %s: %s, %+v, %v", pane, reply.Result, reply.Error, err)
	}
	s.panes.Bind(pane, "conn")
	status, _ := s.far.Axis("conn")
	s.ws.PublishPaneConnection(transport.PaneConnection{
		PaneID: pane, ConnectionEpoch: 1, ConnectionID: "conn", SessionID: result.SessionID,
		Helper: transport.PaneConnectionHelper{State: string(status.State), Reason: string(status.Reason), Action: string(status.Action)},
	})
	return result.SessionID
}

type portsStatusWire struct {
	PaneID    string `json:"paneId"`
	Host      string `json:"host"`
	Discovery struct {
		State     string              `json:"state"`
		Listeners []portsListenerWire `json:"listeners"`
	} `json:"discovery"`
}

type portsListenerWire struct {
	Port    int `json:"port"`
	Process struct {
		Name string `json:"name"`
	} `json:"process"`
}

func (s *portsFarStand) status(method, pane string) portsStatusWire {
	s.t.Helper()
	reply := s.call(method, map[string]any{"paneId": pane})
	var got portsStatusWire
	if err := json.Unmarshal(reply.Result, &got); reply.Error != nil || err != nil || got.PaneID != pane {
		s.t.Fatalf("%s: %s, %+v, %v", method, reply.Result, reply.Error, err)
	}
	return got
}

func (s *portsFarStand) assertUnavailable(pane string) {
	s.t.Helper()
	reply := s.call("ports.status", map[string]any{"paneId": pane})
	if reply.Error == nil || reply.Error.Code != -32602 || string(reply.Error.Data) != `{"reason":"helper-unavailable"}` {
		s.t.Fatalf("ports.status = %+v, error %+v", reply, reply.Error)
	}
	validateAppContract(s.t, loadAppContractSchema(s.t, "ports.error.schema.json"), reply.Error.Data, "ports unavailable")
}

const (
	portsSavedPane = "0190d3a0-1c2b-7def-8abc-0123456789ab"
	portsAdHocPane = "0190d3a0-1c2b-7def-8abc-0123456789ac"
)

func TestPortsSavedAndAdHocReadOneFarConnection(t *testing.T) {
	s := newPortsFarStand(t, hostmode.Helper, false)
	s.open(portsSavedPane, "saved-profile")
	s.open(portsAdHocPane, "")
	s.status("ports.sample", portsSavedPane)
	saved := s.status("ports.status", portsSavedPane)
	adhoc := s.status("ports.status", portsAdHocPane)
	if saved.Host != "far.example" || adhoc.Host != saved.Host || s.seams.dials.Load() != 1 {
		t.Fatalf("the panes did not sample their shared host: %+v, %+v", saved, adhoc)
	}
	if len(saved.Discovery.Listeners) != 1 || saved.Discovery.Listeners[0].Port != 6768 ||
		saved.Discovery.Listeners[0].Process.Name != "app" || !reflect.DeepEqual(saved.Discovery, adhoc.Discovery) {
		t.Fatalf("different or missing far listeners: %+v, %+v", saved, adhoc)
	}
}

func TestPortsWithoutToolsLeavesGitAndFilesServing(t *testing.T) {
	s := newPortsFarStand(t, hostmode.Helper, true)
	sid := s.open(portsSavedPane, "saved-profile")
	s.status("ports.sample", portsSavedPane)
	got := s.status("ports.status", portsSavedPane)
	if got.Discovery.State != "unavailable" || len(got.Discovery.Listeners) != 0 {
		t.Fatalf("no-tool status = %+v", got)
	}
	git := s.call("git.open", map[string]any{"sessionId": sid, "cwd": s.root})
	var gitResult struct{ State string }
	if err := json.Unmarshal(git.Result, &gitResult); git.Error != nil || err != nil || gitResult.State != "ok" {
		t.Fatalf("git.open: %s, %+v, %v", git.Result, git.Error, err)
	}
	files := s.call("files.open", map[string]any{"sessionId": sid, "rootPath": s.root})
	var binding struct{ BindingID string }
	if err := json.Unmarshal(files.Result, &binding); files.Error != nil || err != nil || binding.BindingID == "" {
		t.Fatalf("files.open: %s, %+v, %v", files.Result, files.Error, err)
	}
	list := s.call("files.list", map[string]any{"bindingId": binding.BindingID, "path": s.root, "limit": 100})
	if list.Error != nil || !strings.Contains(string(list.Result), `"name":"README.md"`) {
		t.Fatalf("files.list: %s, %+v", list.Result, list.Error)
	}
	if axis, known := s.far.Axis("conn"); !known || axis.State != farhelper.StateServing {
		t.Fatalf("missing probe tools changed the helper axis: %+v", axis)
	}
}

func TestPortsModeRawRefusesWithoutSampling(t *testing.T) {
	s := newPortsFarStand(t, hostmode.Raw, false)
	s.open(portsAdHocPane, "")
	s.assertUnavailable(portsAdHocPane)
	forwards := s.call("tunnel.list", map[string]any{})
	if forwards.Error != nil || string(forwards.Result) != `{"forwards":[]}` {
		t.Fatalf("tunnel.list in Raw mode: %+v, %s", forwards.Error, forwards.Result)
	}

	if got := s.tools.calls.Load(); got != 0 || s.seams.dials.Load() != 0 {
		t.Fatalf("raw mode reached the helper: %d probes, %d dials", got, s.seams.dials.Load())
	}
}

func TestPortsResamplesWhenTheFarHelperReturns(t *testing.T) {
	s := newPortsFarStand(t, hostmode.Helper, false)
	s.open(portsAdHocPane, "")
	if got := s.status("ports.sample", portsAdHocPane); got.Discovery.State != "available" {
		t.Fatalf("first sample: %+v", got)
	}
	(<-s.kill)()
	waittest.WaitFor(t, "lost far helper", func() bool {
		axis, _ := s.far.Axis("conn")
		return axis.Reason == farhelper.ReasonHelperLost
	})
	count := s.tools.calls.Load()
	s.assertUnavailable(portsAdHocPane)
	reply := s.call("ports.sample", map[string]any{"paneId": portsAdHocPane})
	if reply.Error == nil || reply.Error.Code != -32602 || s.tools.calls.Load() != count {
		t.Fatalf("sample while helper absent: %+v, count = %d", reply, s.tools.calls.Load())
	}
	if err := s.far.Refresh(t.Context(), "conn"); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "recovered far helper", func() bool {
		axis, _ := s.far.Axis("conn")
		return axis.State == farhelper.StateServing
	})
	if got := s.status("ports.status", portsAdHocPane); got.Discovery.State != "pending" {
		t.Fatalf("status after recovery = %+v, want pending", got)
	}
	close(s.resumeSamples)
	waittest.WaitFor(t, "fresh sample after the settle interval", func() bool {
		return s.status("ports.status", portsAdHocPane).Discovery.State == "available"
	})
	if s.tools.calls.Load() <= count {
		t.Fatal("recovery reused a sample from the old helper")
	}
}

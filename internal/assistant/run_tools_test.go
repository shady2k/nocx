package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

func TestRunTools_AdmissionBindsCompleteContextAndFreshAuthority(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	pane := &struct{ run string }{"run-bound"}
	p := AskParams{Grant: &grant, KnownMaterial: &fakeKnownMaterial{}, RunID: "run-bound", SessionID: "pane-bound", AutomaticSessionItems: []string{"frozen"}, MarkedSessionWindows: []MarkedSessionWindow{{ItemID: "marked", Start: 2, Count: 3}}, FreshGrant: func() *content.Grant { calls++; fresh := grant; fresh.ExpiresAt = int64(100 + calls); return &fresh }, PaneAccessBinder: func(run, paneID string) (any, any, any, any, session.Identity) {
		return pane, pane, pane, pane, session.Identity{InstanceID: "bound-instance", Epoch: 7}
	}}
	tools, err := BindRunTools(cl, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("minted before admission")
	}
	first, err := tools.Admit()
	if err != nil {
		t.Fatal(err)
	}
	second, err := tools.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || first.Grant.ExpiresAt != 101 || second.Grant.ExpiresAt != 102 {
		t.Fatalf("admission grants %d: %+v %+v", calls, first.Grant, second.Grant)
	}
	ctx := first.RunContext
	if ctx.RunID != p.RunID || ctx.Session != p.SessionID || ctx.PaneAccess != pane || ctx.SessionReads != pane || ctx.SessionKeys != pane || ctx.SessionMessages != pane || ctx.ControllerIdentity != (session.Identity{InstanceID: "bound-instance", Epoch: 7}) || !reflect.DeepEqual(ctx.AutomaticSessionItems, []string{"frozen"}) || len(ctx.MarkedSessionWindows) != 1 {
		t.Fatalf("incomplete context: %+v", ctx)
	}
	if first.Dispatcher != tools {
		t.Fatal("admission did not bind assistant dispatcher")
	}
}

func TestRunTools_FrozenAttachmentReadsKeepTheirMarkedWindow(t *testing.T) {
	grant := sessionGrant("pane-a", autonomousMatrix())
	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &recordingRequester{body: liveFrameBodyRows(11)}
	p := AskParams{Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, KnownMaterial: &fakeKnownMaterial{}, AttemptLedger: &fakeLedger{}, RunID: "run-1", SessionID: "pane-a", Attempt: 1, ScreenCapture: capture, AutomaticSessionItems: []string{"att-shell"}, MarkedSessionWindows: []MarkedSessionWindow{{ItemID: "att-shell", Start: 3, Count: 2}}}
	tools, err := BindRunTools(cl, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := tools.Admit()
	if err != nil {
		t.Fatal(err)
	}
	invocation.Context = context.Background()
	invocation.Method = "session.read"
	invocation.RawParams = []byte(`{"id":"att-shell"}`)
	if _, err := invocation.Dispatcher.Dispatch(invocation); err != nil {
		t.Fatal(err)
	}
	calls := capture.calls()
	if len(calls) != 1 || calls[0].region == nil || *calls[0].region != (FrameRegion{Start: 3, End: 5}) {
		t.Fatalf("attachment capture = %+v; want marked rows [3,5)", calls)
	}
}

func TestRunTools_PolicyRefusalIsATypedEndpointRefusal(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	p := AskParams{Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, KnownMaterial: &fakeKnownMaterial{}, AttemptLedger: &fakeLedger{}, RunID: "run-1", SessionID: "pane-a", Attempt: 1}
	tools, err := BindRunTools(cl, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := tools.Admit()
	if err != nil {
		t.Fatal(err)
	}
	invocation.Context = context.Background()
	invocation.Method = "files.read"
	invocation.RawParams = []byte(`{"path":"/outside-run-fence"}`)
	out, err := tools.Dispatch(invocation)
	var refused *DispatchRefusalError
	if !errors.As(err, &refused) || refused.Method != "files.read" || refused.Reason == "" || out != "" {
		t.Fatalf("Dispatch = %q, %v; want typed refusal with its reason", out, err)
	}
}

func TestRunTools_DispatchUsesApprovalAndEgress(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "read.txt")
	secret := "known-secret-value-123"
	writeFile(t, path, secret)
	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	approvals := NewApprovalStore()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	p := askParamsWith("", &grant, &fakeLedger{}, approvals, &knownMatcher{value: secret, name: "token"}, nil)
	p.Questioner = q
	p.RunID = "run-1"
	p.Attempt = 1
	mints := 0
	p.FreshGrant = func() *content.Grant { mints++; return &grant }
	tools, err := BindRunTools(cl, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := tools.Admit()
	if err != nil {
		t.Fatal(err)
	}
	invocation.Context = context.Background()
	invocation.Method = "files.read"
	invocation.RawParams = json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))
	out, err := invocation.Dispatcher.Dispatch(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, secret) || len(q.asked) != 1 || len(q.egressAsked) != 1 || mints < 2 {
		t.Fatalf("out=%q asks=%d screens=%d mints=%d", out, len(q.asked), len(q.egressAsked), mints)
	}
	if _, ok := any(tools).(interface {
		Catalogue(content.Grant) []agenttools.Tool
	}); !ok {
		t.Fatal("missing catalogue")
	}
}

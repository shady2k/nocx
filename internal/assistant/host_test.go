package assistant

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func hostFactoryFor(t *testing.T) RunHostFactory {
	t.Helper()
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunHostFactory(registry, content.Floor{}, nil)
}

func TestRunHostWithoutGrantCannotDispatch(t *testing.T) {
	ctx, _ := logtest.New(t)
	host, err := hostFactoryFor(t).NewRunHost(ctx, AskParams{}, func(ToolCall) error {
		t.Fatal("an ungranted host announced an effect")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(host.Tools()) != 0 {
		t.Fatal("an ungranted host offered tools")
	}
	out, err := host.InvokeClassified(ctx, "files.read", "call-1", `{"path":"/etc/passwd"}`)
	if err != nil || out.Kind != InvocationNocxMessage || !strings.Contains(out.Text, "There is no such tool") {
		t.Fatalf("ungranted invocation = %+v, %v; want a controlled message without execution", out, err)
	}
}

func TestRunHostRequiresKnownMaterialProtection(t *testing.T) {
	ctx, _ := logtest.New(t)
	grant, _ := testDirGrant(t, autonomousMatrix())
	_, err := hostFactoryFor(t).NewRunHost(ctx, AskParams{Grant: &grant}, nil)
	if err == nil || !strings.Contains(err.Error(), "egress vault comparison") {
		t.Fatalf("host creation = %v, want missing material protection to fail closed", err)
	}
}

func TestRunHostClassifiesRawOutputAndRefusal(t *testing.T) {
	ctx, _ := logtest.New(t)
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "read.txt")
	writeFile(t, path, "host-owned read")
	ledger := &fakeLedger{}
	p := askParams("", &grant, ledger, nil)
	p.TurnEntryID = "host-turn"
	calls := 0
	host, err := hostFactoryFor(t).NewRunHost(ctx, p, func(ToolCall) error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	out, err := host.InvokeClassified(ctx, "files.read", "call-1", `{"path":"`+path+`"}`)
	if err != nil || out.Kind != InvocationToolOutput || !strings.Contains(out.Text, "host-owned read") || strings.Contains(out.Text, "<tool_result") {
		t.Fatalf("read = %+v, %v; want raw classified file output", out, err)
	}
	if framed := host.FrameForModel("files.read", out.Text); framed == out.Text {
		t.Fatal("file output did not receive the registry's untrusted frame")
	}
	if calls != 1 || len(ledger.recordedCauses()) != 1 || ledger.recordedCauses()[0].turn != "host-turn" {
		t.Fatalf("calls=%d causes=%+v; want one announced and recorded effect", calls, ledger.recordedCauses())
	}
	refusal, err := host.InvokeClassified(ctx, "files.read", "call-2", `{"path":"/etc/passwd"}`)
	if err != nil || refusal.Kind != InvocationNocxMessage || !strings.Contains(refusal.Text, "files.read") || strings.Contains(refusal.Text, "<tool_result") {
		t.Fatalf("refusal = %+v, %v; want an unframed nocx message", refusal, err)
	}
}

func TestRunHostFreshDriveRetainsApproval(t *testing.T) {
	ctx, _ := logtest.New(t)
	policy := autonomousMatrix()
	policy.Observe.Decision = content.DecisionAsk
	grant, dir := testDirGrant(t, policy)
	path := filepath.Join(dir, "approved.txt")
	writeFile(t, path, "approved once")
	factory := hostFactoryFor(t)
	p := askParams("", &grant, &fakeLedger{}, nil)
	host, err := factory.NewRunHost(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"path":"` + path + `"}`
	_, err = host.InvokeClassified(ctx, "files.read", "call-1", args)
	var asked *ApprovalRequestedError
	if !errors.As(err, &asked) || asked.Request == nil {
		t.Fatalf("first call = %v, want typed approval suspension", err)
	}
	req := asked.Request
	concreteFactory, ok := factory.(*runHostFactory)
	if !ok {
		t.Fatalf("host factory = %T, want *runHostFactory", factory)
	}
	if !concreteFactory.approvals.Approve(Approval{RunID: req.RunID, Attempt: req.Attempt, Tool: req.Tool, CallID: req.CallID, ArgHash: req.ArgHash}) {
		t.Fatal("host's pending approval was not retained")
	}
	resumed, err := factory.NewRunHost(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.InvokeClassified(ctx, "files.read", "call-1", args)
	if err != nil || out.Kind != InvocationToolOutput || !strings.Contains(out.Text, "approved once") {
		t.Fatalf("resumed call = %+v, %v; want the approved effect", out, err)
	}
}

func TestRunHostKeepsHiddenToolsExecutableAndRefreshesVisibleCatalog(t *testing.T) {
	ctx, _ := logtest.New(t)
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "hidden.txt")
	writeFile(t, path, "hidden eligible output")
	p := askParams("", &grant, &fakeLedger{}, nil)
	p.Presentation = &agenttools.PresentationConfig{Lazy: true, Essential: []string{}, SchemaTokenLimit: 1}
	host, err := hostFactoryFor(t).NewRunHost(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	executable := presentationToolNames(host.Tools())
	if !containsToolName(executable, "files.read") || !containsToolName(executable, toolsSearchName) {
		t.Fatalf("executable catalog = %v, want eligible file read and discovery", executable)
	}
	if visible := presentationToolNames(host.ModelTools()); containsToolName(visible, "files.read") {
		t.Fatalf("hidden read was offered before discovery: %v", visible)
	}
	out, err := host.InvokeClassified(ctx, "files.read", "hidden-call", `{"path":"`+path+`"}`)
	if err != nil || out.Kind != InvocationToolOutput || !strings.Contains(out.Text, "hidden eligible output") {
		t.Fatalf("hidden eligible invocation = %+v, %v", out, err)
	}
	if _, err := host.InvokeClassified(ctx, toolsSearchName, "search-call", `{"query":"files.read"}`); err != nil {
		t.Fatal(err)
	}
	if visible := presentationToolNames(host.ModelTools()); !containsToolName(visible, "files.read") {
		t.Fatalf("discovered tool absent from next model request: %v", visible)
	}
}

func TestRunHostMapsOnlyTrustedNamesInModelMessages(t *testing.T) {
	ctx, _ := logtest.New(t)
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "session.read")
	writeFile(t, path, "the user wrote session.read")
	p := askParams("", &grant, &fakeLedger{}, nil)
	p.ModelToolName = func(name string) string { return "encoded_" + strings.ReplaceAll(name, ".", "_") }
	host, err := hostFactoryFor(t).NewRunHost(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	output, err := host.InvokeClassified(ctx, "files.read", "read", `{"path":"`+path+`"}`)
	if err != nil || !strings.Contains(output.Text, "the user wrote session.read") {
		t.Fatalf("file data was translated: %+v, %v", output, err)
	}
	refused, err := host.InvokeClassified(ctx, "files.read", "outside", `{"path":"/outside/session.read"}`)
	if err != nil || !strings.Contains(refused.Text, "encoded_files_read") || strings.Contains(refused.Text, "call to files.read") {
		t.Fatalf("refusal did not render the trusted name: %+v, %v", refused, err)
	}
	unknown, err := host.UnknownTool("unknown session.read", `{}`)
	if err != nil || !strings.Contains(unknown, `"unknown session.read"`) || !strings.Contains(unknown, "encoded_files_read") {
		t.Fatalf("unknown response lost untrusted spelling or mapped catalog: %q, %v", unknown, err)
	}
	if got := refusalResult("encoded_files_read", RefusedFileChanged, "", "the resource session.read changed"); !strings.Contains(got, "the resource session.read changed") {
		t.Fatalf("resource reason was translated: %q", got)
	}
}

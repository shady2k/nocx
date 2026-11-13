package toolendpoint

// What an agent's MCP session is TOLD when it initializes (nocx-xn63t.6.45).
//
// The finding is a coordinator that initialized nocx's stdio MCP server, read
// an answer carrying only serverInfo, and then followed its own global herdr
// skill when asked to spawn a worker: the tools were granted and the sentence
// that would have pointed at them was never written. So the text is checked
// here the way internal/app/worker_preamble_test.go checks the worker's
// preamble — every tool name in it is looked up in the ASSEMBLED registry and
// in what the caller's own grant OFFERS, never copied into a list that can
// drift from the declarations.
//
// THE NEGATIVE HALF IS WHAT MAKES THE POSITIVE HALF EVIDENCE: the same scan is
// run over texts that ARE wrong, in each of the two ways a name can be wrong —
// one nothing declares, and one that is declared and withheld from that
// caller.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// instructionToolName is the shape a tool takes in the text: the namespace and
// the name, exactly as the wire spells them. It is a scanner rather than a list
// of expected names, for worker_preamble_test.go's reason: a list here would be
// the second copy of the vocabulary this check exists to keep honest.
var instructionToolName = regexp.MustCompile(`workers\.[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*`)

// coordinatorGrantForTest is the authority a coordinator's pane is admitted
// with: the coordinator's four calls need session and environment kinds, and
// spawning is what Delegate is (internal/app/worker_auth.go's callerGrant).
func coordinatorGrantForTest() content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{
		Observe:           permit,
		MutateReversible:  permit,
		MutateDestructive: permit,
		Delegate:          permit,
		PrivilegeChange:   refuse,
		Disclose:          refuse,
		CrossBoundary:     refuse,
	}.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-1"},
		{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
		{Kind: content.ResourceWorkspace, ID: agenttools.ParticipantWorkspaceScopeID(contractWorkspace)},
	})
}

// workerGrantForTest is what a worker is admitted with: Observe alone, and a
// workspace scope and nothing else (internal/app/worker_auth.go's
// participantGrant). Delegate is refused, so a worker cannot spawn — and the
// instructions it reads must not tell it to.
func workerGrantForTest() content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{
		Observe:           permit,
		MutateReversible:  refuse,
		MutateDestructive: refuse,
		PrivilegeChange:   refuse,
		Disclose:          refuse,
		CrossBoundary:     refuse,
		Delegate:          refuse,
	}.AsGrant([]content.GrantScope{
		{Kind: content.ResourceWorkspace, ID: agenttools.ParticipantWorkspaceScopeID(contractWorkspace)},
	})
}

// offeredTo reads the names this grant's own catalogue offers, through the
// real dispatcher and the real registry: the same projection tools.catalogue
// answers with, which is what an agent actually sees.
func offeredTo(t *testing.T, grant content.Grant) map[string]bool {
	t.Helper()
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		contractWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	cataloguer, ok := dispatcher.(assistant.ToolCatalogue)
	if !ok {
		t.Fatal("dispatcher does not implement assistant.ToolCatalogue")
	}
	offered := make(map[string]bool)
	for _, tool := range cataloguer.Catalogue(grant) {
		offered[tool.Name] = true
	}
	return offered
}

// instructionToolProblems names every tool the text sends a caller to that the
// registry does not declare, or declares without offering it to that caller.
func instructionToolProblems(t *testing.T, grant content.Grant, text string) []string {
	t.Helper()
	names := instructionToolName.FindAllString(text, -1)
	if len(names) == 0 {
		return []string{"the text names no tool at all, so this check can say nothing about it"}
	}
	offered := offeredTo(t, grant)
	var problems []string
	for _, name := range names {
		if !offered[name] {
			problems = append(problems, name+" is named but this caller's own grant does not offer it")
		}
	}
	return problems
}

func TestACoordinatorIsSentToItsOwnWorkerTools(t *testing.T) {
	text := instructionsFor(coordinatorGrantForTest())
	for _, problem := range instructionToolProblems(t, coordinatorGrantForTest(), text) {
		t.Errorf("%s:\n%s", problem, text)
	}
	if !strings.Contains(text, "workers.spawn") {
		t.Fatalf("the coordinator's instructions do not name workers.spawn, which is the call this finding was filed about:\n%s", text)
	}
}

// The sentence the finding is disproved by: a coordinator must be told to load
// DEFERRED tools before its first call, because a client that defers them
// lists nothing to the model and the model then reaches for a habit.
func TestACoordinatorIsToldToLoadTheDeferredToolFirst(t *testing.T) {
	text := instructionsFor(coordinatorGrantForTest())
	for _, want := range []string{"ToolSearch", "select:mcp__nocx__workers_spawn"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the coordinator's instructions do not contain %q:\n%s", want, text)
		}
	}
}

// And the other half of the finding: the advice must not send a coordinator to
// another worker manager. The words are named so the prohibition is concrete —
// herdr is the one installed here, and the finding was filed from a session
// that followed its skill.
func TestACoordinatorIsNotSentToAnotherMultiplexer(t *testing.T) {
	text := instructionsFor(coordinatorGrantForTest())
	if !strings.Contains(text, "terminal multiplexer") || !strings.Contains(text, "herdr") {
		t.Fatalf("the coordinator's instructions do not rule out another multiplexer:\n%s", text)
	}
	lowered := strings.ToLower(text)
	for _, forbidden := range []string{"use herdr", "herdr to spawn", "start herdr"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("the coordinator's instructions recommend another multiplexer (%q):\n%s", forbidden, text)
		}
	}
}

func TestAWorkerIsSentToItsOwnReportingTools(t *testing.T) {
	grant := workerGrantForTest()
	text := instructionsFor(grant)
	for _, problem := range instructionToolProblems(t, grant, text) {
		t.Errorf("%s:\n%s", problem, text)
	}
	for _, want := range []string{"workers.report", "workers.inbox"} {
		if !strings.Contains(text, want) {
			t.Fatalf("a worker's instructions do not name %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "workers.spawn") {
		t.Fatalf("a worker's instructions name workers.spawn, a call its grant does not offer:\n%s", text)
	}
}

// The negative half: the same scan over texts that ARE wrong. Each case is the
// real text with one name swapped, so what fails is the swap.
func TestTheInstructionToolScanRejectsANameTheCallerCannotCall(t *testing.T) {
	coordinator := instructionsFor(coordinatorGrantForTest())
	worker := instructionsFor(workerGrantForTest())
	for _, tc := range []struct {
		name  string
		grant content.Grant
		text  string
	}{
		{
			name:  "a tool nothing declares",
			grant: coordinatorGrantForTest(),
			text:  strings.Replace(coordinator, "workers.spawn", "workers.declare", 1),
		},
		{
			name:  "a tool a worker is not offered",
			grant: workerGrantForTest(),
			text:  strings.Replace(worker, "workers.report", "workers.spawn", 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := instructionToolProblems(t, tc.grant, tc.text)
			if len(problems) == 0 {
				t.Fatalf("the scan passed a text naming %s:\n%s", tc.name, tc.text)
			}
		})
	}
}

// The endpoint's own wire: the method mcpstdio's initialize calls. The grant is
// the admitted invocation's, never a parameter, and the two roles must get two
// different answers from the same socket.
func TestInstructionsOverTheWireFollowTheAdmittedGrant(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry,
		contractWorkerRecord{},
		content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	for _, tc := range []struct {
		name  string
		grant content.Grant
		want  string
	}{
		{name: "coordinator", grant: coordinatorGrantForTest(), want: "workers.spawn"},
		{name: "worker", grant: workerGrantForTest(), want: "workers.report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &testAuthorizer{inv: assistant.ToolInvocation{
				Context:    context.Background(),
				RunContext: agenttools.RunContext{RunID: "run-1", Session: "session-1", Workspace: contractWorkspace},
				Grant:      tc.grant,
			}}
			endpoint := startEndpoint(t, Config{
				Dir:      t.TempDir(),
				Peers:    testPeers{uid: 1000, pid: 1234},
				Owner:    testOwner{uid: 1000},
				SelfUID:  1000,
				Auth:     auth,
				Dispatch: dispatcher,
				Logger:   testLogger(),
			})
			conn := dialEndpoint(t, endpoint)
			if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"tools.instructions","params":{}}`+"\n"); err != nil {
				t.Fatalf("write instructions request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error != nil {
				t.Fatalf("instructions response error = %+v", response.Error)
			}
			var result struct {
				Instructions string `json:"instructions"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatalf("decode instructions result: %v", err)
			}
			if !strings.Contains(result.Instructions, tc.want) {
				t.Fatalf("%s instructions = %q, want one naming %s", tc.name, result.Instructions, tc.want)
			}
		})
	}
}

// The ToolSearch selector's server name is a fact the STAGED CONFIG owns: the
// shell integration writes mcpServers["<name>"], and Claude composes every MCP
// tool name as mcp__<name>__<tool>. A selector whose server name drifted from
// that key names a tool that does not exist — the silent failure this whole
// finding is about — so the copy is checked against the script that writes it.
func TestTheToolSearchSelectorNamesTheServerTheStagedConfigStages(t *testing.T) {
	script, err := os.ReadFile("../../internal/shellintegration/scripts/nocx.bash")
	if err != nil {
		t.Fatalf("read the shell integration script: %v", err)
	}
	declared := regexp.MustCompile(`\{"mcpServers":\{"([^"]+)":`).FindSubmatch(script)
	if declared == nil {
		t.Fatal("the shell integration no longer stages an mcpServers entry in a shape this check can read, so the selector's server name is unchecked")
	}
	server := string(declared[1])
	text := instructionsFor(coordinatorGrantForTest())
	for _, match := range regexp.MustCompile(`select:mcp__([a-z0-9_-]+)__`).FindAllStringSubmatch(text, -1) {
		if match[1] != server {
			t.Fatalf("the selector names server %q, but the staged config stages %q:\n%s", match[1], server, text)
		}
	}
}

package app

// A PANE THIS MACHINE'S HELPER CARRIES NAMES NO TOOL SURFACE, AND SAYS WHY
// (nocx-e2bws, criterion 2).
//
// The owner's decision of 2026-09-14 is what this test pins at the coordinator:
// agent orchestration works where a nocx helper is INSTALLED, the bridge the
// agent runs IS that helper, and the integration bundle carries no executable —
// so a pane whose shell runs on a host with no helper of its own has no tool
// surface at all, and the request that opens it names no far path.
//
// WHAT IS READ IS THE REQUEST THE DAEMON DECODED, not the params this process
// built: worker_launch_token_route_test.go states why that distinction is the
// one worth asserting, and the same scripted stand serves both.
//
// The reason is asserted as a CODE the far shell renders into a sentence, and
// the endpoint is asserted NON-EMPTY beside it: this backend does run a tool
// endpoint (it is told one below), so the absence is a fact about the far host
// rather than a backend with no tool surface — which is the state the shell's
// own older sentence describes and the one a person can act on.

import (
	"testing"

	"github.com/shady2k/nocx/internal/shellintegration"
)

func TestASpawnSSHPaneOnAHostWithNoHelperNamesNoToolsAndSaysWhy(t *testing.T) {
	opener, _, ep := sshTokenRoute(t)
	// The endpoint this backend runs. Without it the absence below would be
	// explained by a coordinator with no tool surface at all, and this test
	// would pass for the wrong reason.
	opener.setToolSocketPath("/run/user/1000/nocx/tool.sock")

	sshTokenPaneOpened(t, opener, ep)

	asks := ep.spawner.sshSpawns()
	if len(asks) != 1 {
		t.Fatalf("the daemon was asked to open %d ssh session(s), want exactly 1", len(asks))
	}
	req := asks[0]

	if req.AgentToolSocketPath != "" {
		t.Fatalf("the spawn named the far socket path %q, and no host without a helper can serve it",
			req.AgentToolSocketPath)
	}
	if req.AgentHelperPath != "" {
		t.Fatalf("the spawn named the far bridge binary %q, and nothing puts one on a host with no helper",
			req.AgentHelperPath)
	}
	if want := string(shellintegration.AgentToolsNoHelperOnHost); req.AgentToolsAbsent != want {
		t.Fatalf("the spawn named no reason for the absence (%q), so the far shell reports a path it was never given; want %q",
			req.AgentToolsAbsent, want)
	}
	if req.AgentToolEndpoint == "" {
		t.Fatal("the spawn named no tool endpoint: this backend runs one, and the absence tested above is about the FAR host")
	}
}

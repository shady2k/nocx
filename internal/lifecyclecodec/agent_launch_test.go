package lifecyclecodec

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
)

func TestAgentLaunchEventsRoundTripDataFields(t *testing.T) {
	ticket := "0123456789abcdef0123456789abcdef0123456789A"
	cases := []lifecycle.Event{
		{Kind: lifecycle.KindAgentLaunchResolve, AgentLaunchResolve: &lifecycle.AgentLaunchResolve{RequestID: "r-launch-1", Agent: "myagent"}},
		{Kind: lifecycle.KindAgentLaunchResolve, AgentLaunchResolve: &lifecycle.AgentLaunchResolve{RequestID: "r-pane", Agent: "myagent", PanePath: new("/pane/bin:/usr/bin"), PaneCwd: "/pane/work"}},
		{Kind: lifecycle.KindAgentLaunchResolve, AgentLaunchResolve: &lifecycle.AgentLaunchResolve{RequestID: "r-empty-path", Agent: "myagent", PanePath: new(""), PaneCwd: "/pane/work"}},
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-1", Agent: "myagent", Local: true, ToolSurface: true, Ticket: ticket, Payload: "v1\n2\n2f62696e2f6167656e74\n"}},
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-2", Agent: "myagent", Local: false}},
		// The worker-literal classification: no record, and a surface, in one
		// answer (nocx-xn63t.6.49). It is the case that made the two fields
		// necessary, so it is the case the round trip has to carry.
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-3", Agent: "claude", Local: false, ToolSurface: true}},
		// The record's non-launch words (nocx-3an1s.1.3): the shell matches the
		// typed word against this list, so the spelling on the wire is the
		// contract here too.
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-4", Agent: "claude", Local: true, ToolSurface: true, NonLaunchSubcommands: "update update-config"}},
		{Kind: lifecycle.KindAgentLaunchCancel, AgentLaunchCancel: &lifecycle.AgentLaunchCancel{Agent: "myagent", Ticket: ticket}},
		{Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{RequestID: "r-enrol-1", Agent: "myagent", LaunchTicket: ticket, Cols: 80, Rows: 24}},
	}
	for _, event := range cases {
		t.Run(string(event.Kind), func(t *testing.T) {
			input := lifecycle.Envelope{Version: 1, Lane: "lane-1", Domain: "dom-1", Epoch: 2, Sequence: 3, Event: event}
			var frame bytes.Buffer
			if _, err := Encode(&frame, input); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if event.Kind == lifecycle.KindAgentLaunchResolved && !event.AgentLaunchResolved.Local && !bytes.Contains(frame.Bytes(), []byte(`"local":false`)) {
				t.Fatal("remote classification omitted the explicit local=false field")
			}
			// The shell reads this off the raw frame, so the WIRE spelling is
			// the contract: a field renamed here is a worker that silently
			// stops being told it has tools.
			if event.Kind == lifecycle.KindAgentLaunchResolved && event.AgentLaunchResolved.ToolSurface &&
				!bytes.Contains(frame.Bytes(), []byte(`"tool_surface":true`)) {
				t.Fatalf("the tool-surface classification is not on the wire in the spelling the shell reads: %s", frame.Bytes())
			}
			if event.Kind == lifecycle.KindAgentLaunchResolved && event.AgentLaunchResolved.NonLaunchSubcommands != "" &&
				!bytes.Contains(frame.Bytes(), []byte(`"non_launch_subcommands":"`+event.AgentLaunchResolved.NonLaunchSubcommands+`"`)) {
				t.Fatalf("the non-launch words are not on the wire in the spelling the shell reads: %s", frame.Bytes())
			}
			got, err := NewDecoder(&frame, Config{}, nil).ReadFrame()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, input) {
				t.Fatalf("round trip = %#v, want %#v", got, input)
			}
		})
	}
}

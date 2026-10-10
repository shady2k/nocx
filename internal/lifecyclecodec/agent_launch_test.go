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
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-1", Agent: "myagent", Local: true, Ticket: ticket, Payload: "v1\n2\n2f62696e2f6167656e74\n"}},
		{Kind: lifecycle.KindAgentLaunchResolved, AgentLaunchResolved: &lifecycle.AgentLaunchResolved{RequestID: "r-launch-2", Agent: "myagent", Local: false}},
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

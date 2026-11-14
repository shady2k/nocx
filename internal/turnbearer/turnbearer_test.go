package turnbearer

import (
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"

	"github.com/shady2k/nocx/internal/toolendpoint"
)

type testEndpoint struct{}

func (testEndpoint) SocketPath() string                                       { return "/test.sock" }
func (testEndpoint) RetireAdmissions(string, toolendpoint.AdmissionEpoch) int { return 0 }

func TestRetireClearsDiscardedAuthority(t *testing.T) {
	m := NewMinter(testEndpoint{}, "helper")
	_, retire, err := m.Mint(Authority{Tools: testRunTools{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := m.turns[:cap(m.turns)]
	retire()
	if backing[0].token != "" || backing[0].authority.Tools != nil {
		t.Fatal("retirement retains secret and authority in discarded slice tail")
	}
}

func TestLookupCannotPublishAfterRetire(t *testing.T) {
	m := NewMinter(testEndpoint{}, "helper")
	bridge, retire, err := m.Mint(Authority{Tools: testRunTools{}})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.SplitN(bridge.Env[0], "=", 2)[1]
	authority, ok := m.Lookup(token)
	if !ok {
		t.Fatal("live turn missing")
	}
	retire()
	published := false
	if authority.Publish(func() bool { published = true; return true }) || published {
		t.Fatal("a late publication survived retirement")
	}
}

type testRunTools struct{}

func (testRunTools) RunContext() agenttools.RunContext {
	return agenttools.RunContext{RunID: "run", Session: "pane"}
}

func (testRunTools) Admit() (assistant.ToolInvocation, error) { return assistant.ToolInvocation{}, nil }

func TestRetireSerializesWithPublication(t *testing.T) {
	m := NewMinter(testEndpoint{}, "helper")
	bridge, retire, err := m.Mint(Authority{Tools: testRunTools{}})
	if err != nil {
		t.Fatal(err)
	}
	authority, ok := m.Lookup(strings.SplitN(bridge.Env[0], "=", 2)[1])
	if !ok {
		t.Fatal("live turn missing")
	}
	publishing, finish := make(chan struct{}), make(chan struct{})
	published := make(chan bool, 1)
	go func() { published <- authority.Publish(func() bool { close(publishing); <-finish; return true }) }()
	<-publishing
	retired := make(chan struct{})
	go func() { retire(); close(retired) }()
	close(finish)
	select {
	case ok := <-published:
		if !ok {
			t.Fatal("live publication refused")
		}
	case <-time.After(time.Second):
		t.Fatal("publication stuck")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("retirement stuck")
	}
	if authority.Publish(func() bool { t.Fatal("late callback ran"); return true }) {
		t.Fatal("retired lifetime admitted")
	}
}

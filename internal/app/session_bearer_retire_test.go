package app

import (
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/turnbearer"
)

func TestSessionBearerRetirementKeepsSettlementVisible(t *testing.T) {
	stand := newTurnStand(t, &turnDispatcher{})
	dispatcher := &lateSettlingDispatcher{entered: make(chan assistant.ToolInvocation, 1), cancelled: make(chan struct{}), resume: make(chan struct{}), settled: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(dispatcher.resume) }) })
	tools := &testTurnTools{ctx: agenttools.RunContext{RunID: "retiring", Session: string(stand.sess.ID())}, grant: turnGrantForTest(), dispatcher: dispatcher}
	bridge, retire, err := mintTurn(t, stand, turnbearer.Authority{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	client := newS14MCPClientWithToken(t, stand.endpoint.SocketPath(), envToken(t, bridge.Env))
	result := client.startCall(t, "alpha.hold", map[string]any{})
	<-dispatcher.entered
	retired := make(chan struct{})
	go func() { retire(); close(retired) }()
	awaitLateSignal(t, dispatcher.cancelled, "retiring dispatch cancellation")
	if !stand.minter.Pending("retiring") {
		t.Fatal("retirement hid pending dispatch")
	}
	if _, ok := stand.minter.Lookup(envToken(t, bridge.Env)); ok {
		t.Fatal("retiring bearer admitted")
	}
	if err := stand.minter.BeginTurn("retiring", turnbearer.Authority{Tools: tools}); err == nil {
		t.Fatal("retiring session began turn")
	}
	if _, _, err := stand.minter.Mint("retiring"); err == nil {
		t.Fatal("retiring session replaced before settlement")
	}
	joined := make(chan struct{})
	started := make(chan struct{})
	go func() { close(started); stand.minter.EndTurn("retiring"); close(joined) }()
	<-started
	select {
	case <-joined:
		t.Fatal("EndTurn did not join retiring dispatch")
	default:
	}
	select {
	case <-retired:
		t.Fatal("retirement did not wait for dispatch")
	default:
	}
	release.Do(func() { close(dispatcher.resume) })
	awaitLateSignal(t, retired, "retirement completion")
	awaitLateSignal(t, joined, "joined EndTurn completion")
	if stand.minter.Pending("retiring") {
		t.Fatal("retired session still pending")
	}
	if resp := s14Await(t, result); resp.failed == "" {
		t.Fatal("cancelled dispatch succeeded")
	}
}

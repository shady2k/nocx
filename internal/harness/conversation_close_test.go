package harness

import (
	"context"
	"errors"
	"testing"
)

func TestCloseConversationKeepsUnsettledActorRegistered(t *testing.T) {
	s := newSession(Base{}, nil)
	m := NewSessionManager(Base{}, nil)
	m.sessions["pane"] = s
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.CloseConversation(ctx, "pane"); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	if m.sessions["pane"] != s {
		t.Fatal("unsettled actor removed; a replacement could reuse its bearer incarnation")
	}
	close(s.done)
	if err := m.CloseConversation(context.Background(), "pane"); err != nil {
		t.Fatal(err)
	}
	if m.sessions["pane"] != nil {
		t.Fatal("settled actor retained")
	}
}

func TestCloseConversationSerializesReplacementUntilSettlement(t *testing.T) {
	s := newSession(Base{}, nil)
	m := NewSessionManager(Base{}, nil)
	m.sessions["pane"] = s
	accepted := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		<-s.in
		close(accepted)
		<-release
		close(s.done)
	}()
	go func() { finished <- m.CloseConversation(context.Background(), "pane") }()
	<-accepted
	unlocked := m.mu.TryLock()
	if unlocked {
		m.mu.Unlock()
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if unlocked {
		t.Fatal("replacement could start before the closing actor settled")
	}
}

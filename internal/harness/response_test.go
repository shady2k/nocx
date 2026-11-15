package harness

import (
	"context"
	"strings"
	"testing"
	"time"
)

type responseEventProtocol struct{ shellProtocol }

func (p responseEventProtocol) NewCodec(SessionInfo, Launch) Codec { return p }
func (responseEventProtocol) Decode(line []byte) (Decoded, error) {
	return Decoded{Response: &Response{ID: strings.TrimPrefix(string(line), "response ")}, Events: []Event{{Kind: EventEnd, Outcome: &Outcome{Model: "unsupported"}}}}, nil
}

func TestResponseCannotCarryTurnTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := NewSessionManager(Base{}, responseEventProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); err == nil || !strings.Contains(err.Error(), "response envelope") {
		t.Fatalf("unsupported response terminal: %v", err)
	}
}

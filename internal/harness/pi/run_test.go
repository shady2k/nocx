package pi

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestRunReportsSessionProtocolUnimplemented(t *testing.T) {
	c := New(t.TempDir(), nil)
	turn, err := c.Run(context.Background(), harness.TurnSpec{ConversationID: "conversation", Text: "question", Dir: t.TempDir()})
	if turn != nil {
		_ = turn.Cancel(context.Background())
		_, _ = turn.Wait(context.Background())
	}
	if turn != nil || err == nil || !strings.Contains(err.Error(), "unimplemented") {
		t.Fatalf("Run = %v, %v; want explicit unimplemented error and no turn", turn, err)
	}
}

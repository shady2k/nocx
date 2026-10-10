package session

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

// A re-adopted session is THE SAME session (nocx-zg3k3.5.3, REVIEW-1): its
// opened-at is the moment its pipe was really opened, carried across the
// coordinator's restart -- not the adoption instant, which would floor every
// pane-scoped read of its pre-restart blocks at nothing. A session adopted
// with no carried moment opens now, exactly as before.
func TestAdoptKeepsTheOpenedAtThePipeReallyOpened(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	opened := time.Now().Add(-time.Hour)
	sess, err := reg.Adopt(context.Background(), Config{
		Kind:     KindLocal,
		Cols:     80,
		Rows:     24,
		OpenedAt: opened,
	}, ID("0123456789abcdef0123456789abcdef"), &reasonChannel{})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := sess.OpenedAt(); !got.Equal(opened) {
		t.Fatalf("adopted session openedAt = %v, want the pipe's real open %v", got, opened)
	}
}

func TestAdoptWithoutACarriedOpenedAtOpensNow(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	before := time.Now()
	sess, err := reg.Adopt(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	}, ID("0123456789abcdef0123456789abcdea"), &reasonChannel{})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := sess.OpenedAt(); got.Before(before) {
		t.Fatalf("adopted session openedAt = %v, want an instant at or after the adoption began %v", got, before)
	}
}

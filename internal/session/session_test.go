package session

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestStub_ImplementsRegistry(t *testing.T) {
	var _ Registry = (*Stub)(nil)
}

func TestStub_OpenAndClose(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	reg := NewStub(logger, pt, sshClient)

	ctx := context.Background()
	sess, err := reg.Open(ctx, Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	if sess.ID() == "" {
		t.Fatal("session ID is empty")
	}
	if sess.Kind() != KindLocal {
		t.Fatalf("expected KindLocal, got %d", sess.Kind())
	}

	// Verify it's in the list
	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 session, got %d", len(reg.List()))
	}

	// Close it
	if err := reg.Close(sess.ID()); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	// Verify it's removed
	if len(reg.List()) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(reg.List()))
	}
}

func TestStub_Get_NotFound(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	reg := NewStub(logger, pt, sshClient)

	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

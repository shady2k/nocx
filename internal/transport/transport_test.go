package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestStub_ImplementsTransport(t *testing.T) {
	var _ Transport = (*Stub)(nil)
}

func TestStub_StartAndStop(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	reg := session.NewStub(logger, pt, sshClient)
	tp := NewStub(logger, reg)

	ctx := context.Background()
	if err := tp.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if err := tp.Stop(ctx); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}
}

func TestStub_StartTwice_ReturnsError(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	reg := session.NewStub(logger, pt, sshClient)
	tp := NewStub(logger, reg)

	ctx := context.Background()
	if err := tp.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if err := tp.Start(ctx); err == nil {
		t.Error("expected error on second Start()")
	}
}

func TestStub_StopWithoutStart(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	reg := session.NewStub(logger, pt, sshClient)
	tp := NewStub(logger, reg)

	if err := tp.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}
}

package app

import (
	"context"
	"testing"
)

func TestNew(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if a == nil {
		t.Fatal("New() returned nil app")
	}
}

func TestNew_AllModulesInjected(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	if a.Logger == nil {
		t.Error("Logger is nil")
	}
	if a.Pty == nil {
		t.Error("Pty is nil")
	}
	if a.SSH == nil {
		t.Error("SSH is nil")
	}
	if a.Session == nil {
		t.Error("Session is nil")
	}
	if a.Transport == nil {
		t.Error("Transport is nil")
	}
	if a.Config == nil {
		t.Error("Config is nil")
	}
	if a.ShellIntegration == nil {
		t.Error("ShellIntegration is nil")
	}
}

func TestStartShutdown(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	a.Shutdown(ctx)
}

func TestStartTwice_ReturnsError(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	if err := a.Start(ctx); err == nil {
		t.Error("expected error on second Start()")
	}
}

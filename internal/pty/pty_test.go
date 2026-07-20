package pty

import (
	"context"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestStub_ImplementsInterface(t *testing.T) {
	var _ Pty = (*Stub)(nil)
}

func TestStub_Write(t *testing.T) {
	p := NewStub(log.NewSlogAdapter(nil))
	n, err := p.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}
}

func TestStub_Read_ReturnsEOF(t *testing.T) {
	p := NewStub(log.NewSlogAdapter(nil))
	buf := make([]byte, 1024)
	n, err := p.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes, got %d", n)
	}
}

func TestStub_Close(t *testing.T) {
	p := NewStub(log.NewSlogAdapter(nil))
	if err := p.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStub_Resize(t *testing.T) {
	p := NewStub(log.NewSlogAdapter(nil))
	if err := p.Resize(context.Background(), 80, 24, 800, 600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

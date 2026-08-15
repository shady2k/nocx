package app

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/ssh"
)

type recordingDiscoveryProvider struct {
	host string
	cfg  ssh.ConnectConfig
	conn ssh.DiscoveryConn
}

func (p *recordingDiscoveryProvider) DiscoveryConn(_ context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error) {
	p.host = host
	for _, opt := range opts {
		opt(&p.cfg)
	}
	return p.conn, nil
}

type stubDiscoveryConn struct {
	done chan struct{}
}

func (c *stubDiscoveryConn) Exec(context.Context, string) (*ssh.ExecResult, error) {
	return &ssh.ExecResult{}, nil
}
func (c *stubDiscoveryConn) Done() <-chan struct{} { return c.done }
func (c *stubDiscoveryConn) LostErr() error        { return nil }
func (c *stubDiscoveryConn) Close() error          { return nil }

// TestSSHExecConnProviderForwardsSessionRoute proves the composition-root
// adapter cannot silently turn a jump-routed completion into a direct dial.
func TestSSHExecConnProviderForwardsSessionRoute(t *testing.T) {
	provider := &recordingDiscoveryProvider{conn: &stubDiscoveryConn{done: make(chan struct{})}}
	open := sshExecConnProvider(
		provider,
		ssh.WithUser("alice"),
		ssh.WithPort(2222),
		ssh.WithJumpHost("bastion.example", 2200, "jumper", "password"),
	)

	conn, err := open(context.Background(), "target.example")
	if err != nil {
		t.Fatalf("open completion lease: %v", err)
	}
	if conn == nil {
		t.Fatal("open completion lease returned nil")
	}
	if provider.host != "target.example" {
		t.Fatalf("host = %q, want target.example", provider.host)
	}
	if provider.cfg.User != "alice" || provider.cfg.Port != 2222 {
		t.Fatalf("target options lost: %+v", provider.cfg)
	}
	if provider.cfg.JumpHost != "bastion.example" || provider.cfg.JumpPort != 2200 || provider.cfg.JumpUser != "jumper" {
		t.Fatalf("jump route lost: %+v", provider.cfg)
	}
}

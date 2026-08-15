package ssh

// The pty-less exec lane (design D19): RealClient.HelperConn opens ONE
// long-lived exec session WITHOUT a pty-req — a pty would apply line
// discipline and corrupt the helper's binary frames — with pipes, and the
// lease owns its own pooled reference like DiscoveryConn does. The no-pty
// assertion is the point of these tests: the test server records pty-req
// requests, and the lane must never send one.

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestHelperConn_OpensPtyLessLane proves the lane end to end against the
// in-process server: Start reaches the server as an exec with the given
// command, NO pty-req precedes it (D19), the pipes carry the wire bytes
// byte-identically, and Wait reports the remote exit status.
func TestHelperConn_OpensPtyLessLane(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		if cmd != "/opt/nocx-helper" {
			t.Errorf("exec command = %q, want %q", cmd, "/opt/nocx-helper")
		}
		return "OUT\n", "ERR\n", 0
	})
	client := tunnelTestClient(t, srv)
	hc, err := client.HelperConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("HelperConn: %v", err)
	}
	defer func() { _ = hc.Close() }()

	if err = hc.Start("/opt/nocx-helper"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case cmd := <-srv.execCommands:
		if cmd != "/opt/nocx-helper" {
			t.Fatalf("exec command = %q, want %q", cmd, "/opt/nocx-helper")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exec request never reached the server")
	}

	// D19: the channel was opened without pty-req. The server records pty
	// dimensions only when a pty-req arrives; the lane must not send one.
	srv.mu.Lock()
	ptySeen := srv.ptyCols != 0 || srv.ptyRows != 0
	srv.mu.Unlock()
	if ptySeen {
		t.Fatal("the helper lane requested a pty (D19)")
	}

	// The pipes carry the wire bytes byte-identically: no line discipline.
	stdout, err := io.ReadAll(hc.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := string(stdout); got != "OUT\n" {
		t.Errorf("stdout = %q, want %q", got, "OUT\n")
	}
	stderr, err := io.ReadAll(hc.Stderr())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if got := string(stderr); got != "ERR\n" {
		t.Errorf("stderr = %q, want %q", got, "ERR\n")
	}
	code, err := hc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit status = %d, want 0", code)
	}
}

// TestHelperConn_ReportsExitStatus proves the lane surfaces a nonzero remote
// exit status — the version-mismatch signal the client classifies (D5).
func TestHelperConn_ReportsExitStatus(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(_ string) (string, string, int) { return "", "", 42 })
	client := tunnelTestClient(t, srv)
	hc, err := client.HelperConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("HelperConn: %v", err)
	}
	defer func() { _ = hc.Close() }()

	if err = hc.Start("/opt/nocx-helper"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	code, err := hc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 42 {
		t.Errorf("exit status = %d, want 42", code)
	}
}

// TestHelperConn_DoneOnConnectionLoss proves the lease reports transport
// loss: when the underlying connection dies, Done closes and the lease
// releases its pooled reference (the same watcher DiscoveryConn uses).
func TestHelperConn_DoneOnConnectionLoss(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	hc, err := client.HelperConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("HelperConn: %v", err)
	}
	defer func() { _ = hc.Close() }()

	srv.liveMu.Lock()
	for c := range srv.liveConns {
		_ = c.Close()
	}
	srv.liveMu.Unlock()

	select {
	case <-hc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close on connection loss")
	}
}

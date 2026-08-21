package ssh

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// DiscoveryConn — the owned connection lease for port discovery (spec §3)
// ---------------------------------------------------------------------------

func TestDiscoveryConn_ImplementsInterface(t *testing.T) {
	var _ DiscoveryConn = (*discoveryConn)(nil)
}

// readWithTimeout reads from ch with a bounded wait, so a regression that
// breaks the echo hangs the test instead of the whole suite.
func readWithTimeout(t *testing.T, ch Channel) string {
	t.Helper()
	type readOut struct {
		n   int
		err error
	}
	buf := make([]byte, 128)
	done := make(chan readOut, 1)
	go func() {
		n, err := ch.Read(buf)
		done <- readOut{n, err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("channel read: %v", out.err)
		}
		return string(buf[:out.n])
	case <-time.After(5 * time.Second):
		t.Fatal("channel read timed out")
		return ""
	}
}

func TestDiscoveryConn_Exec_ReturnsOutputAndExitStatus(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		if cmd != "probe-command" {
			t.Errorf("exec command = %q, want %q", cmd, "probe-command")
		}
		return "OUT\n", "ERR\n", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	res, err := dc.Exec(context.Background(), "probe-command")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := string(res.Stdout); got != "OUT\n" {
		t.Errorf("stdout = %q, want %q", got, "OUT\n")
	}
	if got := string(res.Stderr); got != "ERR\n" {
		t.Errorf("stderr = %q, want %q", got, "ERR\n")
	}
	if res.ExitStatus != 0 {
		t.Errorf("exit status = %d, want 0", res.ExitStatus)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestDiscoveryConn_Exec_NonzeroExitStatus(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		return "partial\n", "boom\n", 42
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	res, err := dc.Exec(context.Background(), "probe-command")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitStatus != 42 {
		t.Errorf("exit status = %d, want 42", res.ExitStatus)
	}
	// Output is still fully delivered alongside the nonzero status.
	if got := string(res.Stdout); got != "partial\n" {
		t.Errorf("stdout = %q, want %q", got, "partial\n")
	}
	if got := string(res.Stderr); got != "boom\n" {
		t.Errorf("stderr = %q, want %q", got, "boom\n")
	}
}

// TestDiscoveryConn_Exec_SessionRefused_MaxSessions proves the MaxSessions-1
// case over a real channel open: the interactive shell holds the only
// session channel, Exec's NewSession is rejected with ResourceShortage, and
// the shell stays fully usable. This is the fact behind the "additional
// sessions refused" discovery state — never "no ports".
func TestDiscoveryConn_Exec_SessionRefused_MaxSessions(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setMaxSessions(1)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()

	dc, err := client.DiscoveryConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	_, err = dc.Exec(context.Background(), "probe-command")
	if !errors.Is(err, ErrExecSessionRefused) {
		t.Fatalf("Exec error = %v, want ErrExecSessionRefused", err)
	}

	// The interactive session survived the refusal.
	if _, err := tab.Write([]byte("hi")); err != nil {
		t.Fatalf("tab write after refusal: %v", err)
	}
	if got := readWithTimeout(t, tab); got != "echo:hi" {
		t.Errorf("tab echo = %q, want %q", got, "echo:hi")
	}
}

// TestDiscoveryConn_Exec_Cancel_NoGoroutineNoLeak proves the cancellation
// contract against a server that never answers: canceling the context while
// an exec is in flight closes the auxiliary session (the only thing that
// stops the remote exec), Exec returns the context error promptly, no
// goroutine outlives it, the connection stays healthy for the next exec,
// and releasing the lease reclaims the pooled connection.
func TestDiscoveryConn_Exec_Cancel_NoGoroutineNoLeak(t *testing.T) {
	srv := startTestSSHServer(t)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		close(entered)
		<-unblock
		return "", "", 0
	})
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	type execOut struct {
		res *ExecResult
		err error
	}
	execCh := make(chan execOut, 1)
	go func() {
		res, execErr := dc.Exec(ctx, "blocking-probe")
		execCh <- execOut{res, execErr}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("server never started the exec")
	}

	cancel()
	select {
	case out := <-execCh:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("Exec error = %v, want context.Canceled", out.err)
		}
		if out.res != nil {
			t.Error("Exec returned a result on cancel, want nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return after cancel — the session close did not stop it")
	}

	// Unblock the server handler so its goroutines wind down, then confirm
	// the connection is healthy: a fresh session on the SAME lease works.
	close(unblock)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		return "OK\n", "", 0
	})
	res, err := dc.Exec(context.Background(), "next-probe")
	if err != nil {
		t.Fatalf("Exec after cancel: %v (connection damaged by cancel?)", err)
	}
	if got := string(res.Stdout); got != "OK\n" {
		t.Errorf("stdout = %q, want %q", got, "OK\n")
	}

	// No goroutine outlives the canceled exec (server handler included).
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline+1 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d (leak after cancel)", runtime.NumGoroutine(), baseline+1)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// No retained client: releasing the lease (and the tab) reclaims the
	// pooled connection.
	_ = dc.Close()
	_ = tab.Close()
	deadline = time.Now().Add(5 * time.Second)
	for client.pool.Count() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pool count after close = %d, want 0", client.pool.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiscoveryConn_Close_MidExec_StopsRemoteExec proves tab-death
// semantics: closing the lease while an exec is in flight closes the
// auxiliary session — the only thing that stops the remote exec — and Exec
// returns ErrExecClosed promptly instead of leaving the probe running.
func TestDiscoveryConn_Close_MidExec_StopsRemoteExec(t *testing.T) {
	srv := startTestSSHServer(t)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		close(entered)
		<-unblock
		return "", "", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}

	execCh := make(chan error, 1)
	go func() {
		_, err := dc.Exec(context.Background(), "blocking-probe")
		execCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("server never started the exec")
	}

	if err := dc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-execCh:
		if !errors.Is(err, ErrExecClosed) {
			t.Fatalf("Exec error after Close = %v, want ErrExecClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return after Close — the in-flight exec was not stopped")
	}
	close(unblock)
}

// TestDiscoveryConn_Exec_OutputCap_BoundsAndStops proves the output bound is
// real at the writer boundary: a remote that spews more than the cap is cut
// off, the exec reports Truncated, and it returns promptly instead of
// hanging on a full channel buffer.
func TestDiscoveryConn_Exec_OutputCap_BoundsAndStops(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		return strings.Repeat("x", execOutputCap*2), "", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	type execOut struct {
		res *ExecResult
		err error
	}
	execCh := make(chan execOut, 1)
	go func() {
		res, err := dc.Exec(context.Background(), "huge-probe")
		execCh <- execOut{res, err}
	}()
	select {
	case out := <-execCh:
		if out.err != nil {
			t.Fatalf("Exec: %v", out.err)
		}
		if !out.res.Truncated {
			t.Error("Truncated = false, want true (output exceeded the bound)")
		}
		if len(out.res.Stdout) > execOutputCap {
			t.Errorf("captured stdout = %d bytes, want <= %d", len(out.res.Stdout), execOutputCap)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after the output cap fired")
	}
}

func TestDiscoveryConn_Exec_AfterClose_Fails(t *testing.T) {
	srv := startTestSSHServer(t)
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		return "", "", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	if err := dc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := dc.Exec(context.Background(), "probe-command"); !errors.Is(err, ErrExecClosed) {
		t.Fatalf("Exec after Close = %v, want ErrExecClosed", err)
	}
}

// TestDiscoveryConn_Close_ReleasesReference proves Close drops the lease's
// own pooled reference; the shared connection survives for the tab.
func TestDiscoveryConn_Close_ReleasesReference(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}

	// Tab + lease on one shared connection.
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("pool count = %d, want 1", got)
	}
	// Closing the lease leaves the connection up for the tab.
	if err := dc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tab.Write([]byte("still-alive")); err != nil {
		t.Fatalf("tab write after lease close: %v", err)
	}
	if got := readWithTimeout(t, tab); got != "echo:still-alive" {
		t.Errorf("tab echo = %q, want %q", got, "echo:still-alive")
	}
	if got := client.pool.Count(); got != 1 {
		t.Errorf("pool count after lease close = %d, want 1 (tab still holds it)", got)
	}
	_ = tab.Close()
	deadline := time.Now().Add(5 * time.Second)
	for client.pool.Count() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pool count after tab close = %d, want 0", client.pool.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiscoveryConn_Loss_ClosesDoneAndReclaimsPool proves the loss path:
// transport death closes the lease's Done and releases its own reference, so
// the dead entry is reclaimed and a subsequent Exec reports ErrExecLost.
func TestDiscoveryConn_Loss_ClosesDoneAndReclaimsPool(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tab.Close() }()
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}

	srv.killConns()

	select {
	case <-dc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after connection loss")
	}
	if dc.LostErr() == nil {
		t.Fatal("LostErr = nil after connection loss, want the transport error")
	}
	if _, err := dc.Exec(context.Background(), "probe-command"); !errors.Is(err, ErrExecLost) {
		t.Fatalf("Exec after loss = %v, want ErrExecLost", err)
	}

	// Both references release on loss: the lease watcher and the tab's
	// session watcher. Nothing lingers.
	deadline := time.Now().Add(5 * time.Second)
	for client.pool.Count() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pool count after loss = %d, want 0 (dead entry reclaimed)", client.pool.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The bound is at the seam, not at the builder (nocx-e4ir3).
//
// Discovery's probes are short and ours, which is exactly the argument that
// was true of the integration command until it was 92 KiB. The bound is
// enforced on the way out so that stays true of whatever probe is added
// next, and so the caller gets OUR named refusal rather than the far side's
// opaque one — on Linux an over-long command dies in the far execve at
// MAX_ARG_STRLEN, which reports nothing a person can act on.
func TestDiscoveryConn_Exec_ACommandOverTheBoundIsRefusedAndNeverRun(t *testing.T) {
	srv := startTestSSHServer(t)
	var ran atomic.Bool
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		ran.Store(true)
		return "", "", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	res, err := dc.Exec(context.Background(), strings.Repeat("x", MaxRemoteCommandLen))
	if !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("Exec returned (%v, %v), want ErrCommandTooLong", res, err)
	}
	if res != nil {
		t.Error("a refused Exec returned a result; there is no result for a command that never ran")
	}
	if ran.Load() {
		t.Error("the far side ran the command: it reached the wire before it was refused")
	}
}

// The paired success: one byte under the bound still runs, whole.
func TestDiscoveryConn_Exec_TheLongestAdmissibleCommandRuns(t *testing.T) {
	srv := startTestSSHServer(t)
	want := strings.Repeat("x", MaxRemoteCommandLen-1)
	var got atomic.Value
	srv.setExecHandler(func(cmd string) (stdout, stderr string, exit int) {
		got.Store(cmd)
		return "OUT\n", "", 0
	})
	client := tunnelTestClient(t, srv)
	dc, err := client.DiscoveryConn(context.Background(), srv.addr, tunnelConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("DiscoveryConn: %v", err)
	}
	defer func() { _ = dc.Close() }()

	res, err := dc.Exec(context.Background(), want)
	if err != nil {
		t.Fatalf("Exec one byte under the bound: %v", err)
	}
	if string(res.Stdout) != "OUT\n" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "OUT\n")
	}
	if s, _ := got.Load().(string); s != want {
		t.Errorf("the far side saw a %d-byte command, want the %d bytes submitted", len(s), len(want))
	}
}

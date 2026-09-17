package completion_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/completion"
)

func TestLocalCompleter_EmptyText(t *testing.T) {
	c := completion.NewLocal()
	resp, err := c.Complete(context.Background(), completion.Request{
		Cwd:   "/tmp",
		Line:  "",
		Pos:   0,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("empty line: expected 0 candidates, got %d", len(resp.Candidates))
	}
}

func TestLocalCompleter_AbsolutePath(t *testing.T) {
	c := completion.NewLocal()
	// Create a temp directory with known entries.
	dir := t.TempDir()
	f1 := filepath.Join(dir, "alpha.txt")
	f2 := filepath.Join(dir, "alpha_sub")
	if err := os.WriteFile(f1, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f2, 0o750); err != nil {
		t.Fatal(err)
	}

	resp, err := c.Complete(context.Background(), completion.Request{
		Cwd:   "/",
		Line:  "ls " + dir + "/alp",
		Pos:   len("ls " + dir + "/alp"),
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("expected 2 candidates for %s/alp*, got %d: %+v", dir, len(resp.Candidates), resp.Candidates)
	}
	names := make(map[string]bool)
	for _, c := range resp.Candidates {
		names[c.Name] = true
		if c.Source != "path" {
			t.Errorf("expected source=path, got %q for %s", c.Source, c.Name)
		}
	}
	if !names["alpha.txt"] {
		t.Error("missing alpha.txt")
	}
	if !names["alpha_sub"] {
		t.Error("missing alpha_sub")
	}
	// alpha_sub is a directory.
	for _, c := range resp.Candidates {
		if c.Name == "alpha_sub" && !c.IsDir {
			t.Error("alpha_sub should be marked as directory")
		}
	}
}

func TestLocalCompleter_RelativePathWithCwd(t *testing.T) {
	c := completion.NewLocal()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := c.Complete(context.Background(), completion.Request{
		Cwd:   dir,
		Line:  "cat hel",
		Pos:   len("cat hel"),
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Name != "hello.txt" {
		t.Errorf("expected hello.txt, got %q", resp.Candidates[0].Name)
	}
}

func TestLocalCompleter_RelativeNoCwd(t *testing.T) {
	c := completion.NewLocal()
	resp, err := c.Complete(context.Background(), completion.Request{
		Cwd:   "", // no cwd — relative paths cannot resolve
		Line:  "cat hel",
		Pos:   len("cat hel"),
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("no cwd: expected 0 candidates, got %d", len(resp.Candidates))
	}
}

func TestLocalCompleter_CancelledContext(t *testing.T) {
	c := completion.NewLocal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := c.Complete(ctx, completion.Request{
		Cwd:   "/tmp",
		Line:  "ls /t",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reason != "cancelled" {
		t.Errorf("expected reason=cancelled, got %q", resp.Reason)
	}
	if len(resp.Candidates) != 0 {
		t.Error("cancelled context: expected 0 candidates")
	}
}

func TestLocalCompleter_CommandPosition(t *testing.T) {
	// In command position (first word), the local completer does path
	// completion when the word looks like a path.
	c := completion.NewLocal()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := c.Complete(context.Background(), completion.Request{
		Cwd:   dir,
		Line:  "./run",
		Pos:   len("./run"),
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate for ./run*, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Name != "run.sh" {
		t.Errorf("expected run.sh, got %q", resp.Candidates[0].Name)
	}
}

func TestTokenAt(t *testing.T) {
	// Directly test the unexported tokenAt via the local completer's
	// behaviour: the Complete method extracts the token at Pos.
	// We test implicitly through the results.

	// Token at start of line.
	c := completion.NewLocal()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "mycmd"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// "bi" at position 3+1 → token is "bi"
	resp, _ := c.Complete(context.Background(), completion.Request{
		Cwd:   dir,
		Line:  "ls bi",
		Pos:   len("ls bi"),
		Limit: 20,
	})
	// The token is "bi" relative to cwd=dir, which lists dir's contents
	// starting with "bi". We created "bin" directory.
	if len(resp.Candidates) != 1 || resp.Candidates[0].Name != "bin" {
		t.Errorf("expected [bin], got %+v", resp.Candidates)
	}
}

func TestLocalCompleter_HiddenFiles(t *testing.T) {
	c := completion.NewLocal()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Bare prefix: hidden entries are NOT listed.
	resp, _ := c.Complete(context.Background(), completion.Request{
		Cwd:   dir,
		Line:  "cat v",
		Pos:   len("cat v"),
		Limit: 20,
	})
	if len(resp.Candidates) != 1 || resp.Candidates[0].Name != "visible" {
		t.Errorf("bare prefix: expected [visible], got %+v", resp.Candidates)
	}

	// Dot prefix: hidden entries ARE listed when the prefix starts with dot.
	resp, _ = c.Complete(context.Background(), completion.Request{
		Cwd:   dir,
		Line:  "cat .h",
		Pos:   len("cat .h"),
		Limit: 20,
	})
	found := false
	for _, c := range resp.Candidates {
		if c.Name == ".hidden" {
			found = true
		}
	}
	if !found {
		t.Errorf("dot prefix: expected .hidden in results, got %+v", resp.Candidates)
	}
}

// ── SSH completer tests ─────────────────────────────────────────────────

type fakeExecConn struct {
	execResult *completion.ExecResult
	execErr    error
	lastProbe  completion.CompletionProbe
	closed     bool
}

func (f *fakeExecConn) Complete(_ context.Context, probe completion.CompletionProbe) (*completion.ExecResult, error) {
	f.lastProbe = probe
	return f.execResult, f.execErr
}

func (f *fakeExecConn) Close() error {
	f.closed = true
	return nil
}

func fixedRand(nonce string) func() (string, error) {
	return func() (string, error) { return nonce, nil }
}

func TestSSHCompleter_Success(t *testing.T) {
	nonce := "abcd1234"
	stdout := "NONCE:" + nonce + ":START\n" +
		"path\tfile.txt\t/home/user/file.txt\t0\n" +
		"path\tsubdir\t/home/user/subdir\t1\n" +
		"function\tcheckout\n" +
		"NONCE:" + nonce + ":END\n"

	conn := &fakeExecConn{execResult: &completion.ExecResult{Stdout: []byte(stdout)}}
	provider := func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}
	c := completion.NewSSHWithRand(provider, fixedRand(nonce))

	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "git ch",
		Pos:   6,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(resp.Candidates), resp.Candidates)
	}
	if resp.Candidates[0].Name != "file.txt" || resp.Candidates[0].Source != "path" || resp.Candidates[0].Path != "/home/user/file.txt" {
		t.Errorf("unexpected path candidate: %+v", resp.Candidates[0])
	}
	if resp.Candidates[1].Name != "subdir" || !resp.Candidates[1].IsDir {
		t.Errorf("unexpected dir candidate: %+v", resp.Candidates[1])
	}
	if resp.Candidates[2].Name != "checkout" || resp.Candidates[2].Source != "function" {
		t.Errorf("unexpected function candidate: %+v", resp.Candidates[2])
	}
	if !conn.closed {
		t.Error("ExecConn was not closed")
	}
}

func TestSSHCompleter_BannerPollution(t *testing.T) {
	nonce := "abcd1234"
	stdout := "Welcome to example.com!\n" +
		"Last login: Mon Aug  4 12:00:00 2026\n" +
		"NONCE:" + nonce + ":START\n" +
		"path\tfile.txt\t/home/user/file.txt\t0\n" +
		"NONCE:" + nonce + ":END\n"

	conn := &fakeExecConn{execResult: &completion.ExecResult{Stdout: []byte(stdout)}}
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}, fixedRand(nonce))

	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate from framed payload, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Name != "file.txt" {
		t.Errorf("expected file.txt, got %q", resp.Candidates[0].Name)
	}
}

func TestSSHCompleter_MissingEndMarker(t *testing.T) {
	nonce := "abcd1234"
	stdout := "NONCE:" + nonce + ":START\n" +
		"path\tfile.txt\t/home/user/file.txt\t0\n"

	conn := &fakeExecConn{execResult: &completion.ExecResult{Stdout: []byte(stdout)}}
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}, fixedRand(nonce))

	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	if !resp.Truncated {
		t.Error("expected Truncated=true when END marker missing")
	}
}

func TestSSHCompleter_NoStartMarker(t *testing.T) {
	nonce := "abcd1234"
	stdout := "some error output\n" +
		"NONCE:" + nonce + ":END\n"

	conn := &fakeExecConn{execResult: &completion.ExecResult{Stdout: []byte(stdout)}}
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}, fixedRand(nonce))

	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("expected 0 candidates without START marker, got %d", len(resp.Candidates))
	}
}

func TestSSHCompleter_ExecError(t *testing.T) {
	conn := &fakeExecConn{execErr: context.Canceled}
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}, fixedRand("abcd"))

	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reason != "cancelled" {
		t.Errorf("expected reason=cancelled, got %q", resp.Reason)
	}
}

func TestSSHCompleter_LeaseError(t *testing.T) {
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return nil, context.DeadlineExceeded
	}, fixedRand("abcd"))

	_, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err == nil {
		t.Fatal("expected error from failed lease, got nil")
	}
}

func TestSSHCompleter_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		called = true
		return nil, nil
	}, fixedRand("abcd"))

	resp, err := c.Complete(ctx, completion.Request{
		Host:  "example.com",
		Cwd:   "/home/user",
		Line:  "ls fi",
		Pos:   5,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reason != "cancelled" {
		t.Errorf("expected reason=cancelled, got %q", resp.Reason)
	}
	if called {
		t.Error("provider should not be called when ctx is cancelled")
	}
}

func TestParseCompletionOutput_Empty(t *testing.T) {
	resp := parseViaSSH(t, "", "abcd1234")
	if len(resp.Candidates) != 0 {
		t.Errorf("expected 0 candidates for empty output, got %d", len(resp.Candidates))
	}
}

func TestParseCompletionOutput_CommandCandidate(t *testing.T) {
	resp := parseViaSSH(t, "NONCE:abcd1234:START\ncommand\tgit\nNONCE:abcd1234:END\n", "abcd1234")
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Name != "git" || resp.Candidates[0].Source != "command" {
		t.Errorf("unexpected candidate: %+v", resp.Candidates[0])
	}
}

func parseViaSSH(t *testing.T, stdout, nonce string) *completion.Response {
	t.Helper()
	conn := &fakeExecConn{execResult: &completion.ExecResult{Stdout: []byte(stdout)}}
	c := completion.NewSSHWithRand(func(_ context.Context, _ string) (completion.ProbeConn, error) {
		return conn, nil
	}, fixedRand(nonce))
	resp, err := c.Complete(context.Background(), completion.Request{
		Host:  "example.com",
		Cwd:   "/tmp",
		Line:  "x",
		Pos:   1,
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

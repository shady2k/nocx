package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnumerateHostPatterns_BasicConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
Host myserver
    HostName 192.168.1.1

Host devbox
    HostName dev.local
    User developer

Host *
    User default
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
	if patterns[0] != "myserver" {
		t.Errorf("expected 'myserver', got %q", patterns[0])
	}
	if patterns[1] != "devbox" {
		t.Errorf("expected 'devbox', got %q", patterns[1])
	}
}

func TestEnumerateHostPatterns_MultiplePatternsOnOneLine(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
Host server1 server2 server3
    HostName example.com

Host single
    HostName other.com
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(patterns) != 4 {
		t.Fatalf("expected 4 patterns, got %d: %v", len(patterns), patterns)
	}
	expected := []string{"server1", "server2", "server3", "single"}
	for i, e := range expected {
		if patterns[i] != e {
			t.Errorf("patterns[%d] = %q, want %q", i, patterns[i], e)
		}
	}
}

func TestEnumerateHostPatterns_WildcardsExcluded(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
Host *
    User default

Host prod-*
    ProxyJump bastion

Host ?-special
    HostName special.example.com

Host !backup
    HostName backup.example.com

Host concrete1 concrete2
    HostName example.com
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns (concrete1, concrete2), got %d: %v", len(patterns), patterns)
	}
}

func TestEnumerateHostPatterns_FileNotExist(t *testing.T) {
	patterns, err := EnumerateHostPatterns("/nonexistent/path/config")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if patterns != nil {
		t.Fatalf("expected nil patterns, got %v", patterns)
	}
}

func TestEnumerateHostPatterns_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patterns) != 0 {
		t.Fatalf("expected 0 patterns, got %d: %v", len(patterns), patterns)
	}
}

func TestEnumerateHostPatterns_CommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
# This is a comment
# Host somealias  (commented out — should not appear)

Host goodalias
    HostName real.example.com

# Another comment
Host anotherealias
    HostName also.example.com
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
}

func TestEnumerateHostPatterns_NoHostLines(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
HostName default.example.com
User test
Port 2222
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patterns) != 0 {
		t.Fatalf("expected 0 patterns, got %d: %v", len(patterns), patterns)
	}
}

func TestEnumerateHostPatterns_LowercaseHost(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(`
host loweralias
    HostName lower.example.com

Host NormalAlias
    HostName normal.example.com
`), 0o600); err != nil {
		t.Fatal(err)
	}

	patterns, err := EnumerateHostPatterns(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
	if patterns[0] != "loweralias" {
		t.Errorf("expected 'loweralias', got %q", patterns[0])
	}
}

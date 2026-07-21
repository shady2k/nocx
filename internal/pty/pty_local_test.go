package pty

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestLocalPty_ImplementsInterface(t *testing.T) {
	var _ Pty = (*LocalPty)(nil)
}

func TestLocalPty_SpawnAndWrite(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	n, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n == 0 {
		t.Fatal("Write returned 0 bytes")
	}
}

func TestLocalPty_ReadReturnsOutput(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	_, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 4096)
	n, err := lp.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if n == 0 {
		t.Fatal("expected output, got 0 bytes")
	}
	if !strings.Contains(string(buf[:n]), "hello") {
		t.Fatalf("expected output to contain 'hello', got: %q", string(buf[:n]))
	}
}

func TestLocalPty_Resize(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	err := lp.Resize(context.Background(), 132, 43, 0, 0)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

func TestLocalPty_CloseTwice(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	if err := lp.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lp.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func mustSpawn(t testing.TB, cols, rows uint16) *LocalPty {
	t.Helper()
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return lp
}

// A macOS .app launched from Finder inherits no locale at all. The child shell
// then computes a non-UTF-8 stdout encoding and every Python/Rich/prompt_toolkit
// TUI silently downgrades non-ASCII output to '?' — which looks exactly like a
// font bug in the renderer. Fill the gap, but never override a deliberate choice.
func TestWithUTF8Locale(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string // expected LANG entry, "" = must not be added
	}{
		{
			name: "adds LANG when the environment carries no locale at all (Finder launch)",
			env:  []string{"PATH=/usr/bin", "TERM=xterm-256color"},
			want: "LANG=en_US.UTF-8",
		},
		{
			name: "keeps an inherited LANG untouched",
			env:  []string{"LANG=ru_RU.UTF-8"},
			want: "",
		},
		{
			name: "respects a deliberate non-UTF-8 LANG",
			env:  []string{"LANG=C"},
			want: "",
		},
		{
			name: "LC_ALL alone is enough — do not add LANG",
			env:  []string{"LC_ALL=en_GB.UTF-8"},
			want: "",
		},
		{
			name: "LC_CTYPE alone is enough — do not add LANG",
			env:  []string{"LC_CTYPE=en_US.UTF-8"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withUTF8Locale(tt.env)

			var added []string
			for _, kv := range got {
				if !contains(tt.env, kv) {
					added = append(added, kv)
				}
			}

			if tt.want == "" {
				if len(added) != 0 {
					t.Fatalf("expected no additions, got %v", added)
				}
				return
			}
			if len(added) != 1 || added[0] != tt.want {
				t.Fatalf("expected exactly %q to be added, got %v", tt.want, added)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

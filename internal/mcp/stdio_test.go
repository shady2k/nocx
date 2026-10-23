package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBoundedStderrRetainsSanitizedRedactedTail(t *testing.T) {
	stderr := &boundedStderr{max: 24}
	_, _ = stderr.Write([]byte("discarded prefix; "))
	_, _ = stderr.Write([]byte("failure: SECRET\x00\xffmarker"))

	got := stderr.safeTail([]string{"SECRET"})
	if strings.Contains(got, "SECRET") || strings.IndexByte(got, 0) >= 0 || strings.IndexByte(got, 0xff) >= 0 {
		t.Fatalf("stderr tail exposed secret or unsafe bytes: %q", got)
	}
	if !strings.Contains(got, "[redacted]") || !strings.Contains(got, "failure") {
		t.Fatalf("stderr tail = %q, want sanitized diagnostic with redaction", got)
	}
	if len(got) > stderrBound {
		t.Fatalf("stderr tail length = %d, exceeds bound", len(got))
	}
}

func TestStderrDiagnosticIsAddedOnlyAfterSanitization(t *testing.T) {
	stderr := &boundedStderr{max: stderrBound}
	_, _ = stderr.Write([]byte("dependency failed: SECRET\x1b[31m"))
	err := safeOperationError("server activation", errors.New("stdio connect failed"), []string{"SECRET"}, stderr)
	if err == nil || !strings.Contains(err.Error(), "dependency failed") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("activation error = %v, want sanitized diagnostic", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("activation error contains secret or unsafe bytes: %v", err)
	}
}

func TestBoundedStderrRetainsOnlyConfiguredTail(t *testing.T) {
	stderr := &boundedStderr{max: 8}
	_, _ = stderr.Write([]byte("0123456789"))
	if got := stderr.safeTail(nil); got != "23456789" {
		t.Fatalf("stderr tail = %q, want last 8 bytes", got)
	}
}

func TestBoundedStderrRedactsPartialSecretsAtRetentionBoundaries(t *testing.T) {
	t.Run("discarded prefix", func(t *testing.T) {
		stderr := &boundedStderr{max: 8}
		_, _ = stderr.Write([]byte("xxxxABCDEFzzz"))
		got := stderr.safeTail([]string{"ABCDEF"})
		if strings.Contains(got, "BCDEF") {
			t.Fatalf("stderr tail exposed secret suffix: %q", got)
		}
	})

	t.Run("unfinished suffix", func(t *testing.T) {
		stderr := &boundedStderr{max: 32}
		_, _ = stderr.Write([]byte("connection failed: TOKEN"))
		got := stderr.safeTail([]string{"TOKENVALUE"})
		if strings.Contains(got, "TOKEN") {
			t.Fatalf("stderr tail exposed secret prefix: %q", got)
		}
	})

	t.Run("token exceeds retention", func(t *testing.T) {
		stderr := &boundedStderr{max: 8}
		_, _ = stderr.Write([]byte("ABCDEFGHI"))
		if got := stderr.safeTail([]string{"ABCDEFGHI"}); got != "" {
			t.Fatalf("stderr tail = %q, want no unprovable fragment", got)
		}
	})
}

func TestWholeFrameBoundAcrossReaderChunks(t *testing.T) {
	for _, size := range []int{(64 << 10) + 17, maxFrameBytes - 1, maxFrameBytes} {
		data := append(bytes.Repeat([]byte("x"), size), '\n')
		r := &ownedFrameReader{reader: bufio.NewReaderSize(bytes.NewReader(data), 64<<10), maxFrame: maxFrameBytes, maxTotal: maxSessionBytes}
		if len(data) > maxFrameBytes {
			one := make([]byte, 1)
			n, err := r.Read(one)
			if n != 0 || !errors.Is(err, ErrFrameTooLarge) {
				t.Fatalf("oversized frame exposed %d bytes: %v", n, err)
			}
		} else {
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("chunked frame: got %d bytes, %v", len(got), err)
			}
		}
	}
}

func TestTotalOutputAndTruncatedFrameBounds(t *testing.T) {
	r := &ownedFrameReader{reader: bufio.NewReaderSize(strings.NewReader("one\ntwo\n"), 16), maxFrame: 8, maxTotal: 7}
	got, err := io.ReadAll(r)
	if string(got) != "one\n" || !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("aggregate bound exposed %q: %v", got, err)
	}
	r = &ownedFrameReader{reader: bufio.NewReaderSize(strings.NewReader("unterminated"), 16), maxFrame: 100, maxTotal: 100}
	got, err = io.ReadAll(r)
	if len(got) != 0 || !errors.Is(err, ErrProtocol) {
		t.Fatalf("incomplete frame exposed %q: %v", got, err)
	}
}

func TestOnlySecretBindingsEnterCatalogAndResultScreening(t *testing.T) {
	for _, transport := range []string{"stdio", "http"} {
		t.Run(transport, func(t *testing.T) {
			flag, material := "1", "vault-only-material"
			bindings := []Binding{{Name: "MCP_MODE", Literal: &flag}, {Name: "MCP_TOKEN", SecretRef: "credential"}}
			resolver := &testResolver{values: map[string]string{"credential": material}}
			var sensitive []string
			if transport == "stdio" {
				env, values, err := resolveEnvironment(t.Context(), bindings, resolver)
				if err != nil {
					t.Fatal(err)
				}
				joined := "\n" + strings.Join(env, "\n") + "\n"
				if !strings.Contains(joined, "\nMCP_MODE=1\n") || !strings.Contains(joined, "\nMCP_TOKEN="+material+"\n") {
					t.Fatal("bindings were not forwarded to the process")
				}
				sensitive = values
			} else {
				headers, values, err := resolveHeaders(t.Context(), bindings, resolver)
				if err != nil {
					t.Fatal(err)
				}
				if len(headers) != 2 || headers[0].value != flag || headers[1].value != material {
					t.Fatal("bindings were not forwarded to HTTP")
				}
				sensitive = values
			}
			if len(sensitive) != 1 || sensitive[0] != material || resolver.calls != 1 {
				t.Fatal("literal configuration was classified as credential material")
			}
			catalog := Catalog{ServerName: "fixture", ServerVersion: flag}
			if catalogContainsSecret(catalog, sensitive) {
				t.Fatal("ordinary version metadata collided with a literal configuration flag")
			}
			catalog.ServerName = material
			if !catalogContainsSecret(catalog, sensitive) {
				t.Fatal("credential echoed in metadata escaped screening")
			}
			result := redactKnown(flag+" "+material, sensitive)
			if !strings.HasPrefix(result, flag+" ") || strings.Contains(result, material) {
				t.Fatal("result screening altered literal data or exposed credential material")
			}
		})
	}
}

package mcp

import (
	"errors"
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

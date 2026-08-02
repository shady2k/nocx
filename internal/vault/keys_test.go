package vault

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

// loweredCost reduces the argon2 cost for the calling goroutine. Use in any
// test that does not specifically test with production parameters.
func loweredCost(t *testing.T) {
	t.Helper()
	oldMem, oldTime, oldThr := argon2Memory, argon2Time, argon2Threads
	argon2Memory = 1 // 1 KiB — fast
	argon2Time = 1
	argon2Threads = 1
	t.Cleanup(func() {
		argon2Memory = oldMem
		argon2Time = oldTime
		argon2Threads = oldThr
	})
}

func TestNewRootKey(t *testing.T) {
	loweredCost(t)
	k1, err := newRootKey()
	if err != nil {
		t.Fatalf("newRootKey: %v", err)
	}
	if len(k1) != 32 {
		t.Fatalf("len = %d, want 32", len(k1))
	}
	k2, err := newRootKey()
	if err != nil {
		t.Fatalf("newRootKey: %v", err)
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("two calls produced identical keys")
	}
}

func TestRootKeyEnvelopeRoundTrip(t *testing.T) {
	loweredCost(t)
	root, err := newRootKey()
	if err != nil {
		t.Fatal(err)
	}
	e, err := wrapWithPassphrase(root, "hunter2")
	if err != nil {
		t.Fatalf("wrapWithPassphrase: %v", err)
	}
	got, err := unwrapWithPassphrase(e, "hunter2")
	if err != nil {
		t.Fatalf("unwrapWithPassphrase: %v", err)
	}
	if !bytes.Equal(got, root) {
		t.Fatal("unwrapped key differs from original")
	}
}

func TestEnvelopeWrongPassphrase(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "correct")
	if _, err := unwrapWithPassphrase(e, "wrong"); err != ErrUnsealFailed {
		t.Fatalf("unwrap with wrong passphrase = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeTamperedCiphertext(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "hunter2")
	e.Ciphertext[15] ^= 0xFF // flip a byte in the payload
	if _, err := unwrapWithPassphrase(e, "hunter2"); err != ErrUnsealFailed {
		t.Fatalf("unwrap tampered ciphertext = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeTamperedSalt(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "hunter2")
	e.Salt[0] ^= 0xFF
	if _, err := unwrapWithPassphrase(e, "hunter2"); err != ErrUnsealFailed {
		t.Fatalf("unwrap tampered salt = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeTamperedMemory(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "hunter2")
	// Set to a different value than was used at wrap time (1).
	e.Memory = 42
	if _, err := unwrapWithPassphrase(e, "hunter2"); err != ErrUnsealFailed {
		t.Fatalf("unwrap tampered Memory = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeTamperedTime(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "hunter2")
	e.Time = 2
	if _, err := unwrapWithPassphrase(e, "hunter2"); err != ErrUnsealFailed {
		t.Fatalf("unwrap tampered Time = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeTamperedThreads(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "hunter2")
	e.Threads = 2
	if _, err := unwrapWithPassphrase(e, "hunter2"); err != ErrUnsealFailed {
		t.Fatalf("unwrap tampered Threads = %v, want ErrUnsealFailed", err)
	}
}

// TestEnvelopeMixedParameters proves the KDF parameters live in the Envelope,
// not in a package constant: write an envelope with one Memory and another
// with a different Memory, then unwrap both.
func TestEnvelopeMixedParameters(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()

	// Wrap with Memory=1
	argon2Memory = 1
	e1, err := wrapWithPassphrase(root, "pass")
	if err != nil {
		t.Fatal(err)
	}

	// Wrap the same root with Memory=2
	argon2Memory = 2
	e2, err := wrapWithPassphrase(root, "pass")
	if err != nil {
		t.Fatal(err)
	}

	// Both must unwrap — the envelope carries its own params
	got1, err := unwrapWithPassphrase(e1, "pass")
	if err != nil {
		t.Fatalf("unwrap e1 (Memory=%d): %v", e1.Memory, err)
	}
	got2, err := unwrapWithPassphrase(e2, "pass")
	if err != nil {
		t.Fatalf("unwrap e2 (Memory=%d): %v", e2.Memory, err)
	}
	if !bytes.Equal(got1, root) {
		t.Fatal("e1 produced wrong key")
	}
	if !bytes.Equal(got2, root) {
		t.Fatal("e2 produced wrong key")
	}
}

func TestEnvelopeZeroMemoryRejected(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "pass")
	e.Memory = 0
	if _, err := unwrapWithPassphrase(e, "pass"); err != ErrUnsealFailed {
		t.Fatalf("unwrap with Memory=0 = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeInvalidSaltRejected(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "pass")
	e.Salt = []byte("short") // < 16 bytes
	if _, err := unwrapWithPassphrase(e, "pass"); err != ErrUnsealFailed {
		t.Fatalf("unwrap with short salt = %v, want ErrUnsealFailed", err)
	}
}

func TestEnvelopeEmptyCiphertextRejected(t *testing.T) {
	loweredCost(t)
	root, _ := newRootKey()
	e, _ := wrapWithPassphrase(root, "pass")
	e.Ciphertext = nil
	if _, err := unwrapWithPassphrase(e, "pass"); err != ErrUnsealFailed {
		t.Fatalf("unwrap with nil ciphertext = %v, want ErrUnsealFailed", err)
	}
}

// --- Recovery code ---

// crockfordPattern matches grouped Crockford base32: groups of 4 or fewer
// chars separated by hyphens, e.g. "abcd-efgh-jkmn-pqrs-tvwx-0123-45".
var crockfordPattern = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]+(-[0-9a-hjkmnp-tv-z]+)*$`)

func TestNewRecoveryCodeFormat(t *testing.T) {
	loweredCost(t)
	code, e, err := newRecoveryCode()
	if err != nil {
		t.Fatalf("newRecoveryCode: %v", err)
	}
	if code == "" {
		t.Fatal("recovery code is empty")
	}
	// Crockford base32 with grouping
	if !crockfordPattern.MatchString(code) {
		t.Fatalf("code %q does not match Crockford pattern", code)
	}
	// No ambiguous chars
	if strings.ContainsAny(code, "iIlLoU") {
		t.Fatalf("code %q contains ambiguous characters", code)
	}
	// At least 128 bits → 26 Crockford chars
	if len(strings.ReplaceAll(code, "-", "")) < 26 {
		t.Fatalf("code too short: %q", code)
	}
	// Envelope must be unwrappable with the code
	root, err := unwrapWithPassphrase(e, code)
	if err != nil {
		t.Fatalf("unwrap recovery envelope: %v", err)
	}
	if len(root) != 32 {
		t.Fatalf("recovery root key len = %d, want 32", len(root))
	}
}

func TestNewRecoveryCodeUnique(t *testing.T) {
	loweredCost(t)
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		code, _, err := newRecoveryCode()
		if err != nil {
			t.Fatalf("newRecoveryCode: %v", err)
		}
		if seen[code] {
			t.Fatalf("collision at iteration %d: %q", i, code)
		}
		seen[code] = true
	}
}

func TestRecoveryCodeWrongCode(t *testing.T) {
	loweredCost(t)
	code, e, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	// Slightly modify the code
	b := []byte(code)
	b[len(b)-1] ^= 1
	wrong := string(b)
	if wrong == code {
		wrong = wrong[:len(wrong)-1] + "x"
	}
	if _, err := unwrapWithPassphrase(e, wrong); err != ErrUnsealFailed {
		t.Fatalf("unwrap with wrong code = %v, want ErrUnsealFailed", err)
	}
}

// TestRecoveryCodeEntropy ensures the raw entropy is at least 128 bits.
func TestRecoveryCodeEntropy(t *testing.T) {
	loweredCost(t)
	code, _, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	clean := strings.ReplaceAll(code, "-", "")
	// 26 Crockford chars = 26 * 5 = 130 bits, but ceiling(128/5) = 26
	if len(clean) < 26 {
		t.Fatalf("recovery code has %d chars, need at least 26 for 128 bits", len(clean))
	}
}

// --- Production timing measurement ---
//
// This test uses the real production parameters and measures wall-clock time.
// It is deliberately kept out of the lowered-cost helper to produce an honest
// measurement for the spec (open item: does 64 MiB × t=3 land near 100-200 ms?).

func TestProductionKeyCost(t *testing.T) {
	// Reset to production values explicitly
	argon2Memory = 64 * 1024
	argon2Time = 3
	argon2Threads = 4

	root, err := newRootKey()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	e, err := wrapWithPassphrase(root, "production-benchmark")
	cost := time.Since(start)
	if err != nil {
		t.Fatalf("wrapWithPassphrase (production): %v", err)
	}

	got, err := unwrapWithPassphrase(e, "production-benchmark")
	if err != nil {
		t.Fatalf("unwrapWithPassphrase (production): %v", err)
	}
	if !bytes.Equal(got, root) {
		t.Fatal("unwrapped key differs")
	}

	t.Logf("production argon2id wrapWithPassphrase (m=%d KiB, t=%d, p=%d): %v",
		argon2Memory, argon2Time, argon2Threads, cost)

	// The spec expects ~100-200 ms. If this is wildly off we should know,
	// but we do not fail CI for a slow machine.
	if cost > 5*time.Second {
		t.Fatalf("production wrap took %v, far above expected range", cost)
	}
}

// regexp import is used by id_test.go patterns — make sure it's available.
// (The regexp import is needed for the pattern var above.)

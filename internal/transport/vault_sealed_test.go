package transport

// The dispatcher's vault seam (ADR-0032), tested as a seam: a failure that
// is a sealed-vault failure is normalized to the canonical shape — code
// -32001, reason "vault-sealed" — by the wrapper every handler is
// constructed with, so the renderer's dispatcher raises the unlock and
// re-sends the request. These tests pin the normalization and, at the
// bottom, the boundary that makes a READ that reports never prompt.
import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// spyResponder records what a handler wrote, the stand-in for the real
// connection's Responder.
type spyResponder struct {
	results []json.RawMessage
	errors  []RPCError
	notifs  []string
}

func (s *spyResponder) TryResult(_ json.RawMessage, result json.RawMessage) error {
	s.results = append(s.results, result)
	return nil
}

func (s *spyResponder) TryError(_ json.RawMessage, rpcErr RPCError) error {
	s.errors = append(s.errors, rpcErr)
	return nil
}

func (s *spyResponder) TryNotify(method string, _ json.RawMessage) error {
	s.notifs = append(s.notifs, method)
	return nil
}

// TestSealedNormalizer_CanonicalSealedErrorPassesThroughUnchanged: the
// canonical shape is what the renderer's dispatcher keys on; normalization
// must not rewrite what is already canonical (it carries the same code and
// reason, so this pins the idempotence).
func TestSealedNormalizer_CanonicalSealedErrorPassesThroughUnchanged(t *testing.T) {
	real := &spyResponder{}
	norm := newSealedNormalizer(real)
	err := RPCError{Code: vaultSealedCode, Message: "vault is sealed", Data: &vaultErrorData{Reason: "vault-sealed"}}

	_ = norm.TryError(json.RawMessage(`1`), err)

	if len(real.errors) != 1 {
		t.Fatalf("got %d errors, want 1", len(real.errors))
	}
	got := real.errors[0]
	if got.Code != vaultSealedCode || rpcErrorReason(&got) != "vault-sealed" {
		t.Fatalf("canonical error was rewritten: %+v", got)
	}
}

// TestSealedNormalizer_BareSealedMessageIsNormalized: a handler that
// described the sealed vault without the shape — the bare -32603 with the
// vault's words, or a wrapped "…: vault is sealed" — is a bug the wrapper
// hides: it must reach the renderer as the canonical sealed error, so the
// renderer's seam fires for it.
func TestSealedNormalizer_BareSealedMessageIsNormalized(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
	}{
		{"bare", "vault is sealed"},
		{"wrapped", "probe config: vault is sealed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := &spyResponder{}
			norm := newSealedNormalizer(real)
			_ = norm.TryError(json.RawMessage(`1`), RPCError{Code: -32603, Message: tc.message})

			if len(real.errors) != 1 {
				t.Fatalf("got %d errors, want 1", len(real.errors))
			}
			got := real.errors[0]
			if got.Code != vaultSealedCode {
				t.Fatalf("code = %d, want %d (canonical sealed)", got.Code, vaultSealedCode)
			}
			if reason := rpcErrorReason(&got); reason != "vault-sealed" {
				t.Fatalf("reason = %q, want %q", reason, "vault-sealed")
			}
			if got.Message != tc.message {
				t.Fatalf("message changed: %q, want %q", got.Message, tc.message)
			}
		})
	}
}

// TestSealedNormalizer_FallbackRecognizesTheActualVaultError pins the
// message fallback to the vault package's OWN wording: sealedVaultReason
// recognizes vault.ErrVaultSealed by its text, so a reword of
// ErrVaultSealed in internal/vault/errors.go breaks this test instead of
// silently stopping the production fallback. The string and the error are
// one fact; this test is the pin that keeps them one.
func TestSealedNormalizer_FallbackRecognizesTheActualVaultError(t *testing.T) {
	err := RPCError{Code: -32603, Message: "probe config: " + vault.ErrVaultSealed.Error()}
	if reason := sealedVaultReason(err); reason != "vault-sealed" {
		t.Fatalf("sealedVaultReason = %q, want %q for the real %q",
			reason, "vault-sealed", vault.ErrVaultSealed.Error())
	}
}

// TestSealedNormalizer_NonSealedErrorPassesThrough: only the sealed
// condition is this seam's business. Any other failure goes to the caller
// exactly as the handler wrote it.
func TestSealedNormalizer_NonSealedErrorPassesThrough(t *testing.T) {
	real := &spyResponder{}
	norm := newSealedNormalizer(real)
	err := RPCError{Code: -32603, Message: "dial tcp: connection refused"}

	_ = norm.TryError(json.RawMessage(`1`), err)

	if len(real.errors) != 1 {
		t.Fatalf("got %d errors, want 1", len(real.errors))
	}
	if got := real.errors[0]; got.Code != -32603 || got.Message != "dial tcp: connection refused" {
		t.Fatalf("non-sealed error changed: %+v", got)
	}
}

// TestSealedNormalizer_NilVaultDataDoesNotPanic: rpcErrorFor sets
// Data: reasonForError(err), which is a NIL *vaultErrorData for a
// non-vault error — exactly what dialog.openFile's failure path writes.
// The normalizer must pass such an error through, not dereference the nil.
func TestSealedNormalizer_NilVaultDataDoesNotPanic(t *testing.T) {
	real := &spyResponder{}
	norm := newSealedNormalizer(real)
	err := RPCError{Code: -32603, Message: "dialog.openFile: no file chosen", Data: (*vaultErrorData)(nil)}

	_ = norm.TryError(json.RawMessage(`1`), err)

	if len(real.errors) != 1 || real.errors[0].Code != -32603 {
		t.Fatalf("error not passed through: %+v", real.errors)
	}
}

// TestSealedNormalizer_ResultAndNotifyPassThrough: only errors are this
// seam's business; results and notifications must be untouched.
func TestSealedNormalizer_ResultAndNotifyPassThrough(t *testing.T) {
	real := &spyResponder{}
	norm := newSealedNormalizer(real)

	_ = norm.TryResult(json.RawMessage(`1`), json.RawMessage(`{"ok":true}`))
	_ = norm.TryNotify("some.event", json.RawMessage(`{}`))

	if len(real.results) != 1 || len(real.notifs) != 1 {
		t.Fatalf("result/notify not passed through: results=%d notifs=%d", len(real.results), len(real.notifs))
	}
}

// TestConnMethods_InstallsTheNormalizer: the seam is installed where handlers
// are built — the responder a builder receives IS the sealedNormalizer, so a
// handler cannot write a sealed failure past it. The handler in this test
// knows nothing about the seam.
func TestConnMethods_InstallsTheNormalizer(t *testing.T) {
	var injected Responder
	m := connMethods(map[string]methodSpec{
		"test.capture": {
			method:     "test.capture",
			submission: control.ImmediateSubmission{},
			build: func(w *wsConn, state *connState, r Responder) handlerFunc {
				injected = r
				return func(ctx context.Context, req jsonrpcRequest) {}
			},
			validate: noParams(),
		},
	}, &wsConn{}, nil)

	m["test.capture"].handle(context.Background(),
		jsonrpcRequest{ID: json.RawMessage(`1`), Method: "test.capture"})

	if _, ok := injected.(*sealedNormalizer); !ok {
		t.Fatalf("handler responder = %T, want *sealedNormalizer", injected)
	}
}

// assertNoPendingAsk proves a call raised NO unlock prompt: the ask broker
// holds nothing. A future refactor that turns a sealed-status read into a
// passphrase demand fails here.
func assertNoPendingAsk(t *testing.T, ws *WSServer) {
	t.Helper()
	ws.asks.mu.Lock()
	defer ws.asks.mu.Unlock()
	if n := len(ws.asks.pending); n != 0 {
		t.Fatalf("%d pending asks — a read that reports raised the unlock prompt", n)
	}
}

// rpcErrorReason extracts the machine-readable reason from an RPCError's
// data, the way the renderer reads it.
func rpcErrorReason(e *RPCError) string {
	switch d := e.Data.(type) {
	case *vaultErrorData:
		return d.Reason
	case vaultErrorData:
		return d.Reason
	}
	return ""
}

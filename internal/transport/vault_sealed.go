package transport

// The vault raises its own unlock — ADR-0032, and the seam this file owns.
//
// A call that fails because the vault is sealed must reach the user as the
// unlock prompt, and the original call must complete once the vault answers.
// Two single owners make that true, and neither is per call site:
//
//  1. THIS seam, the backend dispatcher: every control handler is
//     constructed with a sealedNormalizer (connMethods), so a failure that
//     is a sealed-vault failure — recognized by the canonical shape
//     (code -32001, reason "vault-sealed") or by the vault's own words in
//     the message — is rewritten to the canonical shape before it reaches
//     the wire. One wrapper, every method, including the ones not written
//     yet. A handler that says something else about a sealed vault is a bug
//     this wrapper hides from the user.
//
//  2. The renderer's dispatcher (frontend/src/dispatcher.ts): on seeing the
//     canonical reason it raises the unlock dialog — the vault layer owns
//     the prompt, one dialog coalescing concurrent sealed calls — and
//     re-sends the request verbatim. The re-sent request is a FRESH
//     submission, so the operation's gates and lane permit, released when
//     the failed attempt returned, are free for it: the call completes.
//
// WHY THE REPLAY IS NOT ON THE BACKEND. ADR-0032's first draft put the
// replay here, in the dispatcher: catch the sealed error, call RequestUnlock,
// block for the resolution, re-run the handler. It cannot work, and the
// reason is the admission model, not the read loop. A handler emits its
// sealed error from INSIDE the capability operation's callback
// (h.op.Run(ctx, cb), where cb writes h.r.TryError) — the error is the
// callback's answer, not its return. op.Run holds the operation's composite
// admission (the conflict gates and the lane permit) for the whole callback,
// so a synchronous re-run of the handler inside the unwinding TryError
// re-acquires an admission the first attempt still holds: measured as
// "Control plane busy" on vault.inventory, vault.resolveLine and
// profiles.importTabby. Re-submitting the replay through the method's own
// submission works only for the ordered submissions; a bounded-lane method
// refuses the second task while the first still holds its permit (the lane
// is non-waiting by design, ADR-0026). The renderer's re-send has neither
// problem because it is a genuinely new request with no first attempt
// holding anything. So the replay owner is the renderer's dispatcher, and
// this seam's only job is the normalization that makes the renderer's seam
// fire for every sealed failure.
//
// A call that deliberately swallows the sealed condition to REPORT a fact
// (agent.status asking whether a credential resolves) never produces the
// canonical error and therefore never triggers a prompt — that is the line
// between a read that reports and a read that needs the secret. It is
// asserted by test (vault_sealed_test.go).
//
// The closed set of ingress-critical methods (ack, vault.unlockResolved,
// connections.passwordResolved) runs INLINE on the read loop and gets the
// normalizer like every other method — normalization is a pure rewrite, no
// blocking, no ask, so the read-loop rule is untouched: a resolution still
// never waits behind the lane, and nothing new can block the loop.

import (
	"encoding/json"
	"strings"
)

// vaultSealedCode is the JSON-RPC code for a sealed vault, the code the
// renderer reads as "offer the unlock". It mirrors the code vaultErrorCode
// assigns to vault.ErrVaultSealed in ws_vault.go; the constant is the single
// spelling both halves of the seam share.
const vaultSealedCode = -32001

// sealedNormalizer is the Responder every control handler is constructed
// with. It forwards every write unchanged except one: a failure that is a
// sealed-vault failure is rewritten to the canonical sealed shape (code
// -32001, reason "vault-sealed") before it reaches the wire, so the
// renderer's dispatcher raises the unlock for it — whatever the handler
// wrote it as.
type sealedNormalizer struct {
	real Responder
}

func newSealedNormalizer(real Responder) *sealedNormalizer {
	return &sealedNormalizer{real: real}
}

func (s *sealedNormalizer) TryResult(id json.RawMessage, result json.RawMessage) error {
	return s.real.TryResult(id, result)
}

func (s *sealedNormalizer) TryNotify(method string, params json.RawMessage) error {
	return s.real.TryNotify(method, params)
}

func (s *sealedNormalizer) TryError(id json.RawMessage, rpcErr RPCError) error {
	if sealedVaultReason(rpcErr) != "" {
		return s.real.TryError(id, RPCError{
			Code:    vaultSealedCode,
			Message: rpcErr.Message,
			Data:    &vaultErrorData{Reason: "vault-sealed"},
		})
	}
	return s.real.TryError(id, rpcErr)
}

// sealedVaultReason reports whether rpcErr is a sealed-vault failure. Two
// fingerprints, so a handler does not have to remember which one it used:
//
//   - the canonical shape rpcErrorFor produces for vault.ErrVaultSealed —
//     code -32001 with the reason "vault-sealed" — what the vault's own
//     handlers emit;
func sealedVaultReason(rpcErr RPCError) string {
	switch d := rpcErr.Data.(type) {
	case *vaultErrorData:
		if d != nil && d.Reason == "vault-sealed" {
			return "vault-sealed"
		}
	case vaultErrorData:
		if d.Reason == "vault-sealed" {
			return "vault-sealed"
		}
	}
	if strings.Contains(rpcErr.Message, "vault is sealed") {
		return "vault-sealed"
	}
	return ""
}

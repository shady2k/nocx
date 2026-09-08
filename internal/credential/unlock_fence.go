package credential

// THE FENCE THAT MAKES THE RULE ABOVE STRUCTURAL (nocx-0b1n2).
//
// resolver.go says it in words: an operation-stance read waits for a person
// to answer the vault's unlock, and the answer is vault.unseal, which needs
// the vault gate. A caller still holding that gate is showing somebody a
// dialog whose only working button is Cancel.
//
// It was said in words twice and broken twice — nocx-o3606 (secrets.save-
// KeyPassphrase, vault.resolveLine) and nocx-9fzkk (skills.audit, the skill
// draft), the second on a handler written after the first was fixed. A rule
// that lives in a comment is one a new handler does not have to satisfy, so
// it lives here instead: the operation that holds such a gate says so on the
// context it hands its callback, and an operation-stance read that sees the
// mark fails immediately and loudly rather than deadlocking a person.
//
// WHICH GATES CARRY THE MARK IS DECLARED, NEVER DETECTED. Only the gate
// vault.unseal itself acquires qualifies, and the composition root says which
// one that is (internal/capability.UnlockAnsweringGate, wired in
// transport.domainGates). It is deliberately NOT every admission: the ssh
// dial resolves a key passphrase inside an admission holding only the
// execution lane, which vault.unseal shares with capacity to spare — a
// separate bound, tracked as nocx-0dvf2, and not this rule.
//
// The mark travels on the context, which is the only thing that reaches from
// the operation's callback to the resolve, and it does not survive the
// operation: material resolved before or after Run uses the outer context,
// which carries nothing.

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnlockUnanswerable is what an operation-stance read gets instead of a
// secret when it runs inside a gate the unlock's own answer would need.
//
// It is a PROGRAMMING error and reads as one: the alternative outcome is the
// product showing a person "Unlock the vault to …" and then refusing their
// Unlock with "Control plane busy", which is indistinguishable from the app
// being broken and costs them the whole action. Failing here fails the one
// request instead, in a sentence that names what to do about it, and any test
// that exercises the path fails with it.
var ErrUnlockUnanswerable = errors.New(
	"credential: an operation-stance read ran while holding a gate that the answer to " +
		"the unlock must itself acquire, so the unlock it raises could not be answered — " +
		"resolve material before or after the operation, never inside it")

// unlockAnswerGateKey is the context key carrying the name of the gate. The
// unexported struct type is the standard way to keep the key private to this
// package: nothing outside can set the mark except through the function
// below, so the fence cannot be forged off or on by a caller with a string.
type unlockAnswerGateKey struct{}

// WithUnlockAnswerGate marks ctx as running while the named gate is held.
// gate is the domain gate's own name and travels only into the error, so a
// developer who trips the fence is told which gate they were inside.
//
// Called by internal/capability's operation Run, which is the one place that
// knows both that a gate is held and for how long. It is exported for that
// caller and for tests; nothing else has the fact to declare.
func WithUnlockAnswerGate(ctx context.Context, gate string) context.Context {
	return context.WithValue(ctx, unlockAnswerGateKey{}, gate)
}

// unlockAnswerGate reports the gate the context is running inside, if any.
func unlockAnswerGate(ctx context.Context) (string, bool) {
	gate, ok := ctx.Value(unlockAnswerGateKey{}).(string)
	return gate, ok
}

// fencedUnlock is the check Resolve makes before it waits for a person. It
// returns the error to answer with, or nil when the read is free to block.
func fencedUnlock(ctx context.Context) error {
	gate, held := unlockAnswerGate(ctx)
	if !held {
		return nil
	}
	return fmt.Errorf("%w (gate %q)", ErrUnlockUnanswerable, gate)
}

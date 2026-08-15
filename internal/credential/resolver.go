package credential

// WHO MAY RAISE THE UNLOCK — the declaration this file exists to force
// (nocx-k41yv, ADR-0032 as amended).
//
// A sealed vault reaches the person as the unlock prompt through one seam:
// the failure travels out as an error on a control REQUEST, the transport's
// normalizer rewrites it to the canonical shape, and the renderer's
// dispatcher raises the dialog and re-sends the request. Nothing about that
// mechanism can tell whether the caller WANTED it: an ask needs the secret
// and must raise, while a settings page listing which endpoints have a key
// must not — it is describing state, and a modal thrown at somebody who was
// only reading is the regression that keeps coming back.
//
// Three times the fix was applied at a call site, and the fourth escape
// arrived through a path nobody had edited. The cause was always the same:
// whether the prompt appeared was decided by WHICH PIPE the error happened
// to travel down. A pipe is not an intent.
//
// So the intent is a required argument. A resolution declares why it is
// asking, here, at the moment of asking:
//
//   - ForOperation — the person asked for something that cannot happen
//     without this secret. A sealed vault is returned verbatim, so the seam
//     recognizes it and the unlock appears.
//   - ToReport — the resolution's whole purpose is to answer a question
//     about state. A sealed vault is a FACT here, so it comes back as
//     ErrSealedQuiet: a distinct error that deliberately does not wrap the
//     vault's sealed error and does not carry its words, which is what
//     makes it unrecognizable to the seam. A read cannot raise a prompt
//     even if it wants to.
//
// There is no third option and no default. Stance has no valid zero value:
// a resolution that names nothing gets ErrStanceUndeclared instead of a
// secret, and the compiler already refuses the call that omits the
// argument entirely.

import (
	"context"
	"errors"
)

// Stance is why a secret is being resolved. It is a required argument of
// every resolution — see the package comment above.
type Stance int

const (
	// StanceUndeclared is the zero value and is never valid. It exists so
	// the zero value cannot silently mean one of the real stances: a
	// resolution that names nothing is a defect, and it is answered as one.
	StanceUndeclared Stance = iota

	// ForOperation: the person asked for something this secret is needed
	// for — the ask, a probe, connecting a profile. A sealed vault becomes
	// the unlock prompt and the caller's request completes once the vault
	// answers.
	ForOperation

	// ToReport: the resolution answers a question about state — which
	// endpoints have a resolvable key, what a credential badge says,
	// agent.status. A sealed vault is reported, never raised.
	ToReport
)

// ErrStanceUndeclared is what a resolution that names no stance gets
// instead of a secret. It is a programming error, surfaced rather than
// guessed at: guessing is how the stance stopped being declared last time.
var ErrStanceUndeclared = errors.New("credential: resolution names no stance")

// ErrSealedQuiet is a sealed vault reported as a fact by a ToReport
// resolution. It deliberately neither wraps the vault's own sealed error
// nor repeats its words: the transport's normalizer recognizes a sealed
// failure by exactly those two fingerprints, so this error cannot become
// the canonical shape and therefore cannot raise a prompt. Callers that
// need to say "sealed" in a status field test for this.
var ErrSealedQuiet = errors.New("the vault cannot answer right now")

// Resolver is the stanced read seam over a SecretStore. Every consumer that
// resolves material on behalf of a person holds THIS, never the store: the
// store's Get has no stance to give, so a holder of the store can bypass
// the declaration, and a seam that can be bypassed is the one that was.
type Resolver interface {
	Resolve(ctx context.Context, id SecretID, why Stance) (Secret, error)
}

// NewResolver wraps a store. sealed reports whether an error from the store
// is the sealed-vault condition; it is injected because this package must
// not import the vault (the vault imports this one), and because it is the
// composition root's job to say which implementation's sealed error is in
// play. A nil sealed predicate makes every error ordinary — the quiet
// translation simply never fires, which fails towards reporting the
// store's own error rather than towards a prompt nobody asked for.
func NewResolver(store SecretStore, sealed func(error) bool) Resolver {
	return resolver{store: store, sealed: sealed}
}

type resolver struct {
	store  SecretStore
	sealed func(error) bool
}

func (r resolver) Resolve(ctx context.Context, id SecretID, why Stance) (Secret, error) {
	switch why {
	case ForOperation:
		// Verbatim, including the sealed error: this path is exactly the
		// one whose failure must reach the seam intact.
		return r.store.Get(ctx, id)
	case ToReport:
		s, err := r.store.Get(ctx, id)
		if err != nil && r.sealed != nil && r.sealed(err) {
			return Secret{}, ErrSealedQuiet
		}
		return s, err
	default:
		return Secret{}, ErrStanceUndeclared
	}
}

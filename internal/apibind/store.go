package apibind

import "context"

// Store is the binding document. It is the only thing in this feature that
// holds an identifier for stored credential material: a collection file
// names a variable, and this is what turns that name into a value (design
// §8).
//
// The interface is declared by the coordinator ahead of its implementation
// so the transport layer and the resolver could be written against it in
// parallel. Two properties are load-bearing and belong here rather than in a
// comment on the implementation:
//
//   - Lookup takes a Key, never a bare identifier. There is no method that
//     accepts an id from outside, so a file — or a renderer — that spells one
//     has nothing to hand it to.
//   - UnbindCollection is the CLOSING EVENT of §12.2's invariant. Without it
//     the interval "a value exists from before its binding until the
//     collection is deleted" has no end, which is the defect the first draft
//     of the design shipped: an invariant named only at its start buys a test
//     that guards only its start.
type Store interface {
	// Lookup resolves a variable to the stored value's identifier. The
	// second return is false when nothing is bound — which is a normal
	// state, not an error: it is how an unresolved variable blocks a send
	// (§6.5) rather than sending an empty string.
	Lookup(k Key) (id string, found bool, err error)

	// Bind stores value and records the binding. The value is written
	// before the binding, so an interruption leaves an unreachable value
	// rather than a binding that points at nothing.
	Bind(ctx context.Context, k Key, value []byte) error

	// Unbind removes one binding and the value it alone referenced.
	Unbind(ctx context.Context, k Key) error

	// UnbindCollection removes every binding a collection owns, and the
	// values only those bindings referenced.
	UnbindCollection(ctx context.Context, collection string) error
}

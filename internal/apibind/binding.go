// Package apibind holds the binding from a collection's variable name to a
// stored secret value.
//
// It exists so that internal/apicoll does not have to. A collection file
// names a variable; this package is the ONLY place that knows which stored
// value a variable resolves to, and the only place an identifier for
// credential material appears for this feature. It never reaches the
// renderer (ADR-0011) and it never enters a file under the collection root
// (design §8).
package apibind

// Key addresses one binding. Collection is the collection's canonical root,
// so two collections with a variable of the same name do not share a value —
// the reason the key is a triple rather than a variable name.
type Key struct {
	Collection  string
	Environment string
	Variable    string
}

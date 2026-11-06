// Package scopes holds the type-based check that no provider call in the
// handler layer escapes an operation's scope.
//
// ADR-8eej3e66 §4 says provider adapters take the scope, and a static check
// refuses direct provider calls outside them. A check that reads text cannot
// answer "is this a provider call": an alias (`p := s.filesys; p.Close(...)`)
// or another receiver (`server.filesys.Close(...)`) is the same call with a
// different spelling. This package answers it from go/types: a call is a
// provider call when the method it resolves to belongs to the method set of a
// provider field's type, whatever the receiver is named.
package scopes

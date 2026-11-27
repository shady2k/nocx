// Package sessionid names a session and nothing else.
//
// It exists because a session's identity is not the session: internal/session
// owns the session lifecycle and therefore reaches the ssh client, the pty and
// the deployed-helper machinery, so a package that only needs to spell
// "which session owns this" pulled the whole connection stack behind it. That
// is how internal/filesystem came to import golang.org/x/crypto/ssh through a
// single type — the leaf was named, and the name cost the world (design
// C30/C24).
//
// So the type lives here, in a package whose whole dependency graph is this
// file, and internal/session re-exports it as an alias: `session.ID` and
// `sessionid.ID` are one type, not two, and every existing call site keeps
// compiling without a conversion.
package sessionid

// ID names one session. It is opaque: the backend mints it (AD-7), a caller
// only carries it, and nothing about its contents may be derived from it.
type ID string

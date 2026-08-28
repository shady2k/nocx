// Package apiimport converts a Postman v2.1 export or a single curl command
// line into the collection model of internal/apicoll, and writes the result
// to disk as one atomic arrival.
//
// Both entrances are HOSTILE INPUT. internal/importer's Tabby importer is
// the precedent (nocx-52b: an imported config was step 1 of a
// renderer-takeover chain), and the two rules it bought hold here: parsing
// happens backend-side, and every bound is stated as a number rather than
// hoped for.
//
// Two rules shape everything in this package:
//
//   - An imported document never yields a secret. Credential-shaped values and
//     `{{secret:…}}` references are dropped and itemised; the person supplies
//     the value afterwards through the ordinary collection editor. FromCurl
//     is deliberately different because it writes no file: a curl line's
//     own Authorization header stays on the request.
//
//   - CURL IS PARSED, NEVER EXECUTED (design §10). The quoting and
//     continuation rules in curl_lex.go are our own. There is no shell, so
//     $(...) and backticks are literal text rather than a substitution that
//     has to be defended against. TestPackageNeverExecs asserts the absence
//     of the exec rather than the absence of damage.
//
// AN IMPORT NEVER FIRES A REQUEST (design §10, §13). Nothing in this
// package opens a network connection, and nothing in it reads a file named
// by the input: a curl -d @body.json becomes a request whose body NAMES
// body.json, and the reading happens later, on send, under the collection
// handle's path rules (§13.1).
package apiimport

import (
	"io/fs"
	"os"
)

// Unsupported is one thing the import did not carry over, itemised for the
// user. AGENTS.md: "a soft degrade must be visible in the product, not only
// in a log" — so this is a return value, never a slog line.
//
// What names the feature (a curl flag in its long form, a Postman field, an
// item path). It NEVER carries an argument's value: a refused
// --oauth2-bearer would otherwise itemise the token it refused.
type Unsupported struct{ What, Why string }

// FS is every filesystem touch ImportInto makes, injected so that "the
// forty-first file fails" (design §12.2) is a test rather than a hope.
//
// MkdirAll and Lstat are additions to the surface the brief named; the five
// others are exactly it. Lstat is what refuses an existing destination, and
// MkdirAll is what makes a Postman folder a real directory (design §6.2) —
// both are filesystem touches, so routing them anywhere but through here
// would leave two of ImportInto's failure paths untestable, which is the
// one thing this interface exists to prevent.
type FS interface {
	MkdirTemp(dir, pattern string) (string, error)
	MkdirAll(path string, perm os.FileMode) error
	Lstat(name string) (fs.FileInfo, error)
	WriteFile(name string, b []byte, perm os.FileMode) error
	Sync(name string) error
	Rename(oldpath, newpath string) error
	RemoveAll(path string) error
}

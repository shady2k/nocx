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
//   - A COLLECTION FILE NAMES A VARIABLE, NEVER A SECRET (design §8). A
//     Postman variable of "type": "secret", a curl line's
//     Authorization header, a -u password: the NAME goes into the
//     environment file, the VALUE goes to the BindWriter, and no identifier
//     for it is written anywhere under the collection root. ImportInto is
//     the only thing here that writes a file, so it is the only thing here
//     that the rule binds, and it is the only one holding a BindWriter to
//     hand a value to.
//
//     FromCurl writes NO FILE. It converts one line for the request form
//     and hands the result straight back to the person who pasted it, so a
//     credential on that line stays where the line put it — see FromCurl,
//     and nocx-14exx for what minting an unbindable variable there cost.
//     The two answers are one argument to one converter (parseCurl's
//     credentials), not two converters that agree until they do not.
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
	"context"
	"io/fs"
	"os"

	"github.com/shady2k/nocx/internal/apibind"
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

// BindWriter takes a secret VALUE out of the import and into the app's
// binding document, which is the only place an identifier for credential
// material exists for this feature (design §8.1). Declared here as a narrow
// consumer contract: this package needs one method, and naming the whole of
// apibind would make the importer depend on a lifecycle it has no part in.
type BindWriter interface {
	Bind(ctx context.Context, k apibind.Key, value []byte) error
}

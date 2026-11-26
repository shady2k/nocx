package proto

// The `fs` service: the READ half of how a coordinator reads files on a host
// whose ssh connection the far helper holds (ADR-727g63tr, design §5.10).
//
// # Why a service and not SFTP
//
// Files used to reach an ssh host by opening SFTP from the coordinator. The
// owner's invariant of 2026-09-13 is that this helper makes every ssh
// connection, and SFTP is a subsystem on that connection, so the transport for
// a file question moved with the connection. What crosses here is a NAME and a
// BOUND: a path, an offset, a length — never a file descriptor, a command or a
// byte the coordinator could have chosen to write.
//
// # Only the read ops exist in this generation
//
// `root`, `stat`, `list`, `canonical` and `read` are the whole closed set this
// generation answers. The write half — a write session, its `commit` with an
// expected version, `abort`, `inventory` — belongs to the leaves that add it,
// and a service name per direction would be two names for one answer (AD-8).
// An older helper answers `unknown_op` to an op it does not have, which is the
// reading a coordinator needs: this machine's helper is older than this app,
// not this host refusing files.
//
// # Why the shapes are here and not in the service package
//
// Both ends of this socket are the same Go package, and `contracts/helper/fs.*`
// freezes each shape. Keeping the DTOs beside the op constants is what lets
// the contract test marshal the EXACT struct the service returns, rather than a
// second, hand-written copy that agrees until one of them is edited.

import filesystem "github.com/shady2k/nocx/internal/filesystem"

// ServiceFS is the name of the helper service that reads this machine's
// filesystem for a coordinator.
const ServiceFS = "fs"

// The read ops. They are five because each answers a different question and
// each has its own params type: a new FIELD on a frozen op is a break
// (`additionalProperties: false`), while a new op is one an older helper
// answers `unknown_op` to — and that refusal is exactly what a coordinator
// must be able to read.
const (
	// OpRoot answers the navigation root: the verified working directory when
	// the caller supplied one that is still usable, the account's home
	// directory inferred and labelled otherwise.
	OpRoot = "root"
	// OpStat classifies one path and reports the metadata a row needs, without
	// enumerating its parent.
	OpStat = "stat"
	// OpList answers one page of a complete, deterministically ordered
	// directory listing.
	OpList = "list"
	// OpCanonical resolves a path to the provider-canonical identity.
	OpCanonical = "canonical"
	// OpRead reads one file: the whole of it with a version, or a range with
	// the object's identity.
	OpRead = "read"
)

// FSVersion identifies the bytes a whole-file read returned. It is the fence a
// later conditional write compares against: size and mtime are the cheap
// fields, and sha256 is the one that cannot be fooled by a rewrite of the same
// length inside one filesystem timestamp granule. It is returned ONLY by a
// whole-file read, because a range's version is not the file's version and
// reporting one would be a claim about bytes the caller never saw.
type FSVersion struct {
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtimeNs"`
	SHA256  string `json:"sha256"`
}

// FSRootParams is fs.root's params. Path is the caller's VERIFIED working
// directory — the value an OSC 7 report was checked into — and is optional:
// with no path the provider this service was registered with answers, which is
// the account's home directory unless the composition root supplied an
// override. A path is re-checked at call time exactly as local.Provider.Root
// re-checks its own override, so a directory that is gone by the time the
// panel opens falls back to the same labelled home rather than to a root that
// does not exist.
type FSRootParams struct {
	Path string `json:"path,omitempty"`
}

// FSRootResult is fs.root's answer: the root's real path, the abbreviated form
// a header shows, and whether it is an inference — with the reason, because a
// root the product guessed must say so rather than look verified.
type FSRootResult struct {
	Path           string `json:"path"`
	Display        string `json:"display"`
	Inferred       bool   `json:"inferred"`
	InferredReason string `json:"inferredReason"`
}

// FSPathParams is the params of fs.stat and fs.canonical: one path. The two
// ops take the same shape because they take the same question — which object
// is this path — and one declaration keeps them from drifting apart.
type FSPathParams struct {
	Path string `json:"path"`
}

// FSStatResult is fs.stat's answer: the kind a row links by, and the three
// metadata fields a listing row carries. A symlink is followed, so Kind never
// spells `symlink` here — it describes the object the link would open or
// expand, which is what a caller deciding whether to descend needs.
type FSStatResult struct {
	Kind    filesystem.Kind `json:"kind"`
	Size    int64           `json:"size"`
	MtimeNs int64           `json:"mtimeNs"`
	Mode    uint32          `json:"mode"`
}

// FSListParams addresses one page of one directory. Offset and Limit are the
// caller's page; an offset past the directory's total is answered empty with
// the real Total and Rev, the same answer local.List gives for that page —
// deleting an entry between two pages produces it, and the Total the caller
// already holds is what resolves the race.
type FSListParams struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// FSListEntry is one row of a listing. Path is the LEXICAL path — the absolute
// spelling the caller asked about, symlinks unresolved — so the tree a client
// draws keeps the shape it requested; the listing's Canonical is the identity
// of the directory actually read.
type FSListEntry struct {
	Name    string          `json:"name"`
	Path    string          `json:"path"`
	Kind    filesystem.Kind `json:"kind"`
	Size    int64           `json:"size"`
	MtimeNs int64           `json:"mtimeNs"`
	Mode    uint32          `json:"mode"`
	// LinkTarget and LinkKind are present exactly for a symlink: where it
	// points, and what that target is. LinkKind is `unreadable` when the
	// target could not be classified and `other` when it is genuinely broken,
	// because those are different facts a person acts on differently.
	LinkTarget string          `json:"linkTarget,omitempty"`
	LinkKind   filesystem.Kind `json:"linkKind,omitempty"`
}

// FSListResult is one page of one directory. Entries is never nil: an empty
// directory is [], never null, because a decoder must not have to tell "no
// rows" from "no answer". Rev is filesystem.ComputeRev over the WHOLE,
// deterministically ordered directory — not this page — so the client can
// compare two polls without re-reading every row.
type FSListResult struct {
	Path      string        `json:"path"`
	Canonical string        `json:"canonical"`
	Entries   []FSListEntry `json:"entries"`
	Offset    int           `json:"offset"`
	Total     int           `json:"total"`
	HasMore   bool          `json:"hasMore"`
	Rev       string        `json:"rev"`
}

// FSCanonicalResult is fs.canonical's answer: the provider-canonical identity
// of the path.
type FSCanonicalResult struct {
	Canonical string `json:"canonical"`
}

// FSReadParams addresses one read. Whole asks for the ENTIRE file and its
// version, and requires Offset 0; Length bounds either kind of read, and is
// never more than the files.read ceiling plus one (the extra byte lets a
// caller ask for "the ceiling and a bit" without inventing a second bound).
type FSReadParams struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	Whole  bool   `json:"whole"`
}

// FSReadResult is one read. Canonical is the identity of the object actually
// opened; Identity is dev:ino on a unix build, so a caller can tell a file an
// append grew from a file that was replaced between two reads — an append
// keeps the inode, a replacement changes it. Data is the bytes returned, base64
// on the wire (Go's []byte marshals that way). Size is the number of bytes in
// Data. Version is present ONLY for a whole-file read, where it describes
// exactly those bytes; a range's version would be a claim about the file that
// the caller's window did not see.
type FSReadResult struct {
	Canonical string     `json:"canonical"`
	Identity  string     `json:"identity"`
	Data      []byte     `json:"data"`
	Size      int64      `json:"size"`
	MtimeNs   int64      `json:"mtimeNs"`
	Changed   bool       `json:"changed"`
	Version   *FSVersion `json:"version,omitempty"`
}

// The write ops, and the session they are a conversation in.
//
// # One session, four ops, and a state the caller cannot see
//
// A write is not one request. The bytes arrive in chunks that each fit a
// frame, the target's version has to be checked at the instant of the commit,
// and an answer that never arrives must not become a second write. So the
// caller opens a session with an id it chose, streams chunks into a temporary
// file beside the target, and ends it either by committing — which installs
// the temporary file only if the target still matches what the caller expects
// — or by aborting. The id is the caller's because the caller is the party
// that has to retry: after a dropped reply it asks again with the same id and
// learns the first outcome instead of writing twice.
//
// # Why the session belongs to a connection
//
// The temporary file is the helper's, not the host's: a caller that goes away
// without committing has left a file nobody will ever finish. A session's
// connection is therefore recorded at `write-open`, and its closure aborts
// every session still open on it. A commit already installing is NOT aborted —
// the rename is the point of no return, and its answer is what the next
// connection asks for.
const (
	// OpWriteOpen starts a session: it resolves the target once, refuses an
	// object that is not a regular file, and creates the temporary.
	OpWriteOpen = "write-open"
	// OpWrite appends one chunk to the session's temporary file.
	OpWrite = "write"
	// OpWriteCommit installs the temporary file over the target, if and only
	// if the target still matches the caller's expectation.
	OpWriteCommit = "write-commit"
	// OpWriteAbort discards the session's temporary file.
	OpWriteAbort = "write-abort"
)

// The three outcomes a commit can answer with. `committed` is the only one
// that means the bytes are in place; `aborted` means this daemon knows they
// are not; `unknown-outcome` means the daemon does not know, because the
// session was never its own — the caller must compare `current` with what it
// wrote and decide for itself.
const (
	FSOutcomeCommitted = "committed"
	FSOutcomeAborted   = "aborted"
	FSOutcomeUnknown   = "unknown-outcome"
)

// FSWriteOpenParams opens a write session. ID is the caller's 128-bit
// identifier, 32 lower-case hex characters: hex and only hex, because it is
// both a wire identity and a filename component (`.nocx-write-<id>`), and a
// value that can spell `..` or a separator is a path-traversal waiting for the
// first caller that is not this one. Path is the target as the caller knows
// it; the helper resolves it ONCE and answers where the write will actually
// land.
type FSWriteOpenParams struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// FSWriteOpenResult is where the write will land: the resolved target path,
// with every symlink on it — including a final component that is one —
// followed exactly once, at open time. A caller that commits to another
// spelling in the same directory installs there instead; the resolution is
// what the panel reports and what a symlinked target keeps intact.
type FSWriteOpenResult struct {
	Resolved string `json:"resolved"`
}

// FSWriteParams appends one chunk. Offset must equal the number of bytes
// already written — the session is a stream, not a sparse file — and Data is
// base64 on the wire because that is how Go's []byte marshals. A chunk larger
// than the frame's own room is refused: the bound is the transport's, not a
// product decision, and a caller that ignores it would be answered `too-large`
// by the frame encoder after the fact.
type FSWriteParams struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
}

// FSWriteResult is what a chunk leaves behind: the number of bytes the session
// now holds. It is the value the next chunk's Offset must equal, so a caller
// that keeps it needs no second call to learn its own position.
type FSWriteResult struct {
	Size int64 `json:"size"`
}

// FSWriteExpect is the condition the commit checks. The two members are
// alternatives, never a conjunction: Version says "this file, still exactly as
// I read it", Absent says "nothing is at this name". A request carrying both
// is refused `bad_params` — a caller that asked for two conditions has not
// decided what it wants, and guessing would pick one silently.
type FSWriteExpect struct {
	// Version is what a whole-file read answered: size, mtime and a digest.
	// The commit refuses `changed-since-read` unless all three still hold.
	Version *FSVersion `json:"version,omitempty"`
	// Absent, when true, refuses to overwrite: a name that exists answers
	// `exists` and nothing is replaced. False is not a third condition and is
	// refused, because "must not be absent" is a sentence that says nothing.
	Absent *bool `json:"absent,omitempty"`
}

// FSWriteCommitParams installs a session's temporary file at Path. Path must
// remain in the directory `write-open` resolved — the NAME may differ, which
// is what lets a caller answer a KeepBoth collision by committing under a
// suffixed name without sending the bytes again — and anything that escapes
// that directory is `invalid-path`. Size is the number of bytes the caller
// believes it wrote; a session holding another count answers `size-mismatch`
// and stays open, which is the cheap check that catches a chunk the caller
// believed delivered.
type FSWriteCommitParams struct {
	ID     string         `json:"id"`
	Path   string         `json:"path"`
	Size   int64          `json:"size"`
	Expect *FSWriteExpect `json:"expect,omitempty"`
}

// FSWriteCommitResult is the one outcome a commit had. The three are mutually
// exclusive by construction, and `outcome` is the discriminator a caller
// switches on:
//
//   - `committed`: the bytes are in place at Path, and Version describes the
//     file now at that path. Asking again with the same id answers THIS answer
//     again, without touching the file — the inode does not move.
//   - `aborted`: this daemon knows the session wrote nothing (it was aborted,
//     or its connection went away before the commit).
//   - `unknown-outcome`: the daemon never held this session — it restarted, or
//     another generation answered the open. Current is the target as it is
//     NOW, so the caller can compare the digest with the one it intended; the
//     digest is present only when the size matches, because hashing a file of
//     another size answers a question the caller did not ask. Current is
//     omitted when there is no target at all.
type FSWriteCommitResult struct {
	Outcome string          `json:"outcome"`
	Path    string          `json:"path"`
	Version *FSVersion      `json:"version,omitempty"`
	Current *FSWriteCurrent `json:"current,omitempty"`
}

// FSWriteCurrent is what is in the way when the outcome is unknown: the
// target's size and mtime always, its digest only when the size is the one the
// caller stated — which is the only case in which comparing digests means
// anything.
type FSWriteCurrent struct {
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtimeNs"`
	SHA256  string `json:"sha256,omitempty"`
}

// FSWriteAbortParams ends one session. There is nothing else to say: the id
// identifies it, and the answer is what happened to it.
type FSWriteAbortParams struct {
	ID string `json:"id"`
}

// FSWriteAbortResult reports the session's outcome after the abort: `aborted`
// when the temporary file is gone and the target untouched, or `committed`
// when the session had already installed its file — the abort of a finished
// session is not an error, it is a fact the caller wanted and already has. An
// abort naming an id this daemon never held answers `aborted` too: there is no
// temporary file of ours, which is what the caller asked for.
type FSWriteAbortResult struct {
	Outcome string `json:"outcome"`
}

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
// caller's page; a page beyond the directory's total is refused rather than
// answered empty, because "there is more" and "you asked past the end" are
// different facts and an empty page cannot tell them apart.
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

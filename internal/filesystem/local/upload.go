package local

// The write half of the local view (upload design D7, as corrected).
//
// # Why this exists at all, when D7 first said it must not
//
// D7 withheld this seam because a local `Write` had no caller: D9 said a
// drop on a local tab inserts the dropped file's path, so nothing ever
// asked the backend to copy a file onto its own disk, and a write path
// with no caller is the nocx-rtg0 failure exactly.
//
// D9 was reasoned from the desktop build alone, where the renderer and the
// tab's shell are provably one machine. In a browser the premise is false:
// a "local" tab is a shell on the BACKEND's machine and the dropped file
// is on the BROWSER's, and the two coincide only under `make dev-web`.
// The rule is now about what the gesture yields rather than about which
// machine anything is on — whoever has the path inserts it, whoever has
// only the bytes uploads them — so a browser drop on a local tab uploads,
// and this seam has a caller.
//
// R1 is unchanged. It forbids sending a file to the WRONG host, and the
// backend's own machine is exactly the host a local tab's shell is on.
//
// # The capability this adds, said plainly
//
// The backend now writes a file to its own filesystem at the renderer's
// request, at a caller-supplied destination. That is new and worth
// reviewing. It is NOT an escalation: the same client can already type
// `cat > file` into that same tab, and the transport reaches this only
// through a binding whose session it owns (D15) with a destination it
// validated. The renderer still cannot name a SOURCE on this disk (R2).

import (
	"fmt"
	"os"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/transfer"
)

// newFileMode is what an uploaded file is created with. The process umask
// then applies, so the result is what a shell redirect in that same tab
// would have produced — which is the design's "the uploaded file gets the
// destination's default" (§4, Out: the source's mode is not carried,
// because the stream source has none to carry).
const newFileMode = 0o666

// Sink returns the write half of this provider's view — the optional
// filesystem.Uploader seam (design D7).
//
// It is never nil: a provider that implements Uploader can write. The sink
// is built per call because it holds no state of its own; `os` is the
// whole of its dependency.
//
// internal/transfer is reused verbatim rather than paralleled. Its
// RemoteFS is already transport-agnostic — Create, PosixRename, Rename,
// Remove — so the temp file, the collision decision, the promote,
// progress, cancellation and the stranded-path accounting are one
// implementation for both providers. A second sink here would be two
// owners of one behaviour (AGENTS.md), and the two would agree until the
// day they did not.
func (p *Provider) Sink() transfer.Sink { return transfer.NewSink(osFS{}, transfer.DefaultChunk) }

// osFS presents this machine's filesystem as the write surface
// internal/transfer declares. It is the local counterpart of the
// composition root's fsTransferLease, and it is far shorter for one reason:
// the two contracts RemoteFS documents and cannot check are properties `os`
// already has.
//
//   - Create's O_WRONLY|O_CREATE|O_EXCL is spelled here, so a concurrent
//     transfer's temp file cannot be truncated (D5).
//   - A refusal arrives as *fs.PathError wrapping the errno, so
//     errors.Is against fs.ErrNotExist, fs.ErrPermission and fs.ErrInvalid
//     already holds. That is what keeps the sink's KeepBoth search from
//     spending its 32 attempts on a read-only directory and then reporting
//     "no free name", which would be false rather than merely vague. EEXIST
//     is deliberately NOT in that set: a taken name is the one refusal that
//     trying the next suffix does answer.
//
// Paths are joined by internal/transfer with `path`, not `path/filepath`.
// On the platforms nocx targets those are the same function; the provider
// still owns syntax, and checkPath is what the read half applies.
type osFS struct{}

func (osFS) Create(path string) (transfer.RemoteFile, error) {
	// #nosec G304 — a caller-supplied destination is the feature, not the
	// oversight. `path` is the sink's join of an Upload the transport
	// already validated (§5.3: destDir absolute, clean and bounded; name
	// exactly one path component), reached only through a binding whose
	// session the connection owns (D15). The write lands wherever that name
	// resolved at commit time, which §5.3 states as the guarantee and is
	// the one scp gives.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, newFileMode) //nolint:gosec // see above
	if err != nil {
		// The concrete type must not be returned as a nil interface: a
		// non-nil transfer.RemoteFile holding a nil *os.File would pass
		// the sink's error check and then panic on the first Write.
		return nil, err
	}
	return f, nil
}

// PosixRename replaces the destination atomically. os.Rename IS
// rename(2) on the platforms nocx targets, so it replaces an existing
// destination and never reports ErrPosixRenameUnsupported — which means
// the sink's two-rename fallback, and the window where the destination
// holds nothing, are unreachable on this path rather than merely unlikely.
func (osFS) PosixRename(old, new string) error { return os.Rename(old, new) }

// Rename is the non-clobbering rename RemoteFS asks for, which os.Rename
// is not: rename(2) replaces silently, and the sink's fallback relies on
// this call REFUSING an existing destination (nocx-340t).
//
// link(2) is that call — it fails with EEXIST on a taken name — and the
// unlink completes the move. The residue is named rather than hidden: if
// the unlink fails after the link succeeded, both names exist and the
// error says so, which is the same "say what is where" the sink's stranded
// list is for.
//
// Reachable only through the fallback PosixRename never selects here. It
// is implemented rather than stubbed because RemoteFS states a contract
// and a method that cannot keep it is worse than one nobody calls.
func (osFS) Rename(old, new string) error {
	if err := os.Link(old, new); err != nil {
		return err
	}
	if err := os.Remove(old); err != nil {
		return fmt.Errorf("filesystem local: %s is now also at %s: %w", old, new, err)
	}
	return nil
}

// Remove deletes a path the sink itself created. A path that is already
// gone reports fs.ErrNotExist, which the sink reads as "removed" rather
// than as a stranded file.
func (osFS) Remove(path string) error { return os.Remove(path) }

// Compile-time proof that the two halves are what the design says they
// are. The composition root asserts the same thing at the wiring, and both
// are wanted: this one fails the day a signature drifts, that one fails the
// day the factory stops returning a writable provider.
var (
	_ filesystem.Provider = (*Provider)(nil)
	_ filesystem.Uploader = (*Provider)(nil)
	_ transfer.RemoteFS   = osFS{}
)

package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

const (
	// DefaultChunk is 256 KiB, matching tabby's read buffer and comfortably
	// inside the lease's 30 s lane timeout on any link that is alive at
	// all. Each chunk is ONE lane call (D2), so a long upload is many short
	// calls rather than one call that outlives the watchdog.
	DefaultChunk = 256 << 10

	tempSuffix = ".nocx-upload-"
	bakSuffix  = ".nocx-bak-"

	// keepBothAttempts bounds the KeepBoth suffix search (D5).
	keepBothAttempts = 32
)

type sink struct {
	fs    RemoteFS
	chunk int
}

// chunkOr is the one place a chunk bound is validated, shared by both
// directions because a chunk is the same idea going each way: the most bytes
// one call may carry, which is what keeps a single call inside the lease's
// lane timeout (D2). Values below one fall back rather than failing — a
// caller that computes a bound has no useful answer to give a constructor.
func chunkOr(n int) int {
	if n > 0 {
		return n
	}
	return DefaultChunk
}

// NewSink returns a Sink writing through fs, moving chunk bytes per lane
// call. Pass DefaultChunk unless you have a reason not to.
//
// A PARAMETER, NOT AN OPTION. This was `opts ...Option` over a one-field
// config, and the single option that config ever had — WithChunkSize — had
// no caller outside a test: an options framework built for a test seam, and
// four production call sites that all passed nothing. deadcode reported it
// the first time CI ran the ratchet on this branch, and it was right
// (nocx-9le.5.25). Naming the bound costs each caller one word and leaves
// nothing in the package that production does not reach.
func NewSink(remote RemoteFS, chunk int) Sink {
	return &sink{fs: remote, chunk: chunkOr(chunk)}
}

// Put writes r into u.DestDir under a temp name and promotes it.
//
// Invariant, both ends named. From the moment Put starts until it returns,
// the destination holds either its previous content or the new content —
// except on a server without posix-rename@openssh.com, where between
// rename(dest→bak) and rename(temp→dest) it holds nothing and the previous
// content is at bak. That window is one round trip, cancellation cannot
// open it wider, and every outcome landing inside it names bak.
//
// Invariant, both ends named. The temp exists from a successful Create
// until either a rename consumes it or a Remove SUCCEEDS. Remove is an
// external call and can fail, so the closing event is a successful removal
// and never an attempted one; where it did not happen — a failed Remove, a
// lost lease — the path is reported in Outcome.Stranded and never dropped.
func (s *sink) Put(ctx context.Context, u Upload, r io.Reader, progress func(total int64)) (Outcome, error) {
	if err := u.validate(); err != nil {
		return Outcome{}, err
	}
	if progress == nil {
		progress = func(int64) {}
	}
	// Skip is the person's answer to a collision they were already shown.
	// The sink does not re-check that the destination is still there: the
	// question was asked and answered, and a stat here would only be a
	// second moment to disagree with the first.
	if u.OnExists == Skip {
		return Outcome{State: StateSkipped}, nil
	}

	nonce, err := s.nonce()
	if err != nil {
		return Outcome{}, err
	}

	// KeepBoth resolves the final name BEFORE a byte moves, and the create
	// is the arbiter rather than a stat (D5): the reservation is an empty
	// file held at the chosen name for the length of the transfer, which
	// the promote then replaces. It is ours, so every failure below removes
	// it — leaving an empty file at a name nobody asked for is litter the
	// person did not create.
	name := u.Name
	var reserved string
	if u.OnExists == KeepBoth {
		var stranded []string
		name, stranded, err = s.reserveName(u.DestDir, u.Name)
		if err != nil {
			return Outcome{Stranded: stranded}, err
		}
		reserved = path.Join(u.DestDir, name)
	}

	dest := path.Join(u.DestDir, name)
	temp := dest + tempSuffix + nonce

	f, err := s.fs.Create(temp)
	if err != nil {
		left, cleanupErr := s.tryRemove(reserved)
		return Outcome{Stranded: left}, errors.Join(fmt.Errorf("transfer: create %s: %w", temp, err), cleanupErr)
	}

	copyErr := s.copy(ctx, f, r, u.Size, progress)
	// The handle is closed on every path, including a failed copy: a
	// leaked handle holds a server-side descriptor the lease cannot name.
	closeErr := f.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("transfer: close %s: %w", temp, closeErr)
	}
	if copyErr == nil && closeErr == nil {
		// The last point at which a cancel can be honoured. Past here the
		// promote runs to completion whatever the context says; see
		// promote.
		copyErr = ctx.Err()
	}
	if copyErr != nil || closeErr != nil {
		// §6 splits these two. A copy that failed leaves a temp whose state
		// is known — partial, ours, removable. A Close that failed after
		// every byte was written is precisely the case where the server did
		// NOT confirm the file's final state, and removing a file whose
		// state you do not know is not cleanup, it is a second uncertain
		// write. So that temp stays and is named; the reservation, an empty
		// file the sink alone created, goes either way.
		remove, stranded := []string{temp, reserved}, []string(nil)
		if copyErr == nil {
			remove, stranded = []string{reserved}, []string{temp}
		}
		left, cleanupErr := s.tryRemove(remove...)
		return Outcome{Stranded: append(stranded, left...)}, errors.Join(copyErr, closeErr, cleanupErr)
	}

	out, err := s.promote(dest, temp, nonce)
	if err != nil && reserved != "" {
		// A promote that failed leaves the reservation sitting at a name
		// nobody asked for — an empty file the sink alone created. It goes
		// the way the temp does, and if it cannot go it is named. On the
		// fallback path the reservation has already become bak by the time
		// a rename fails, so this finds nothing and reports nothing.
		left, cleanupErr := s.tryRemove(reserved)
		out.Stranded = append(out.Stranded, left...)
		err = errors.Join(err, cleanupErr)
	}
	return out, err
}

// copy streams r into w one chunk at a time.
//
// Cancellation is checked BETWEEN chunks and never inside one: a chunk is a
// single lane call on the lease, and abandoning it mid-flight is the shape
// D2 rejects. A chunk is bounded, so the wait is bounded too.
//
// The Read is the exception, and it is the caller's to bound rather than
// this loop's: an io.Reader has no context, so the check below cannot cover
// a Read that has already blocked. Sink.Put states the contract that
// follows — the caller closes the reader on cancellation — and this loop
// keeps its half by unwinding on whatever error the reader then returns.
func (s *sink) copy(ctx context.Context, w RemoteFile, r io.Reader, size int64, progress func(int64)) error {
	return copyChunks(ctx, w, r, size, s.chunk, progress, copyLabels{read: "read source", write: "write"})
}

// copyLabels name the two ends of a copy, so one loop can say which end
// failed in the vocabulary of the direction it is serving: a sink reads a
// source and writes to the server, a source reads the server and sends to
// the client.
type copyLabels struct{ read, write string }

// copyChunks streams exactly size bytes from src to dst, chunk at a time,
// and is the ONE place either direction decides how many bytes cross per
// call, when cancellation is observed, and when progress is reported.
//
// Cancellation is checked BETWEEN chunks and never inside one: a chunk is a
// single lane call on the lease, and abandoning it mid-flight is the shape
// D2 rejects. A chunk is bounded, so the wait is bounded too.
//
// The blocking call is the exception, and it is the CALLER's to bound
// rather than this loop's, in whichever direction it lies. An io.Reader has
// no context and neither has an io.Writer, so the check below cannot cover
// a call that has already blocked: Sink.Put and Source.Get each state the
// contract that follows, and this loop keeps its half by unwinding on
// whatever error the blocked end eventually returns.
//
// The size is enforced in BOTH directions and both are failures. Too few
// bytes is a truncated file; too many is a source that lied, and the excess
// is never passed on — a reader that can keep going must not be able to
// fill the server's disk, and a body that can keep going must not be able
// to overrun the length its own framing already declared.
func copyChunks(ctx context.Context, dst io.Writer, src io.Reader, size int64, chunk int, progress func(int64), label copyLabels) error {
	buf := make([]byte, chunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if total+int64(n) > size {
				return &SizeMismatchError{Declared: size, Got: total + int64(n), AtLeast: true}
			}
			nw, err := dst.Write(buf[:n])
			if err == nil && nw != n {
				err = io.ErrShortWrite
			}
			if err != nil {
				return fmt.Errorf("transfer: %s: %w", label.write, err)
			}
			total += int64(n)
			progress(total)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("transfer: %s: %w", label.read, readErr)
		}
	}
	if total != size {
		return &SizeMismatchError{Declared: size, Got: total}
	}
	return nil
}

// promote moves the finished temp onto the destination.
//
// It takes no context, and that is the design rather than an omission
// (§6, last row): cancellation is REFUSED inside the fallback's two-rename
// window. A cancel landing there would be the one path that deliberately
// leaves a person with no file at all, and "I pressed cancel" must never be
// how the destination goes missing.
func (s *sink) promote(dest, temp, nonce string) (Outcome, error) {
	switch err := s.fs.PosixRename(temp, dest); {
	case err == nil:
		return Outcome{State: StateWritten, FinalName: path.Base(dest)}, nil
	case !errors.Is(err, ErrPosixRenameUnsupported):
		left, cleanupErr := s.tryRemove(temp)
		return Outcome{Stranded: left}, errors.Join(fmt.Errorf("transfer: promote %s: %w", dest, err), cleanupErr)
	}

	// SFTP v3 rename refuses an existing destination (nocx-340t), so the
	// old file moves ASIDE rather than away: for the whole window its
	// content is on disk under a named path, which is what an unlink-first
	// fallback does not give you.
	bak := dest + bakSuffix + nonce
	if err := s.fs.Rename(dest, bak); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			left, cleanupErr := s.tryRemove(temp)
			return Outcome{Stranded: left}, errors.Join(fmt.Errorf("transfer: back up %s: %w", dest, err), cleanupErr)
		}
		// Nothing was there to back up. Not a collision, not a failure.
		bak = ""
	}

	if err := s.fs.Rename(temp, dest); err != nil {
		// Neither path is removed. The destination is missing, so bak holds
		// the only copy of the old content and temp the only copy of the
		// new; the design chose to NAME both over an automatic rollback,
		// which is another rename that can fail in turn and would leave
		// nobody able to say what is where.
		return Outcome{Stranded: present(bak, temp)}, fmt.Errorf("transfer: promote %s: %w", dest, err)
	}
	if bak == "" {
		return Outcome{State: StateWritten, FinalName: path.Base(dest)}, nil
	}
	if err := s.fs.Remove(bak); err != nil {
		// A success that still left something behind. The new content is in
		// place and the old file is still on disk under a name only this
		// Outcome knows; reporting it is the honest result, and swallowing
		// it would be how a stray .nocx-bak- outlives everyone who could
		// explain it.
		return Outcome{State: StateWritten, FinalName: path.Base(dest), Stranded: []string{bak}}, nil
	}
	return Outcome{State: StateWritten, FinalName: path.Base(dest)}, nil
}

// reserveName claims a free KeepBoth name with O_EXCL and returns it, plus
// any path it created and could not clean up.
//
// An UNCLASSIFIED create refusal advances the suffix, because SFTP v3
// answers EEXIST as a generic SSH_FX_FAILURE (the protocol fact
// internal/shellintegration/install_remote.go:33 pays for) and a lost race
// is then indistinguishable on the wire from any other refusal. That
// ambiguity is real and the bound is what keeps it cheap; the last refusal
// travels in the error so the reason is still reported.
//
// A refusal the adapter DID classify stops the search at once and is
// returned as itself. A read-only directory, a quota, a vanished directory
// or a dead lease cannot become a free name by trying 31 more of them, and
// calling that outcome "no free name" is not a weak diagnosis but a false
// one. See notCollision and RemoteFS.Create's contract.
func (s *sink) reserveName(destDir, name string) (string, []string, error) {
	var lastErr error
	for i := 1; i <= keepBothAttempts; i++ {
		candidate := suffixName(name, i)
		p := path.Join(destDir, candidate)
		f, err := s.fs.Create(p)
		if err != nil {
			if notCollision(err) {
				return "", nil, fmt.Errorf("transfer: reserve %s: %w", p, err)
			}
			lastErr = err
			continue
		}
		if err := f.Close(); err != nil {
			left, cleanupErr := s.tryRemove(p)
			return "", left, errors.Join(fmt.Errorf("transfer: close reservation %s: %w", p, err), cleanupErr)
		}
		return candidate, nil, nil
	}
	return "", nil, &NameExhaustedError{Name: name, Attempts: keepBothAttempts, Err: lastErr}
}

// notCollision reports whether a Create refusal is one the sink can tell
// apart from a lost O_EXCL race. These are the sentinels RemoteFS.Create's
// contract obliges the adapter to preserve; anything outside the set is
// treated as the ambiguous SSH_FX_FAILURE it may well be.
//
// The rule for admitting one: a refusal belongs here when trying the next
// suffix cannot possibly change the answer. Permission and quota refusals
// are a property of the directory, ErrNotExist and ErrInvalid of the
// directory or the name shape, ErrClosed and a cancelled context of the
// lease — none of them are a property of the name being taken.
func notCollision(err error) bool {
	switch {
	case errors.Is(err, fs.ErrPermission),
		errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrInvalid),
		errors.Is(err, fs.ErrClosed),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return false
}

// tryRemove removes paths the sink itself created. It returns those it did
// not manage to remove — the closing half of the temp invariant — AND why
// each removal failed. A path that is already gone is removed, not
// stranded.
//
// The reason is returned rather than discarded because §6's row says both:
// a person shown "disk full" and a stranded path never learns that removing
// it was refused for permissions, or that the lease had gone by then, which
// is the only thing that tells them whether the path is theirs to clean up.
// Callers join it BEHIND the primary failure, which stays primary.
func (s *sink) tryRemove(paths ...string) ([]string, error) {
	var stranded []string
	var reasons []error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := s.fs.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			stranded = append(stranded, p)
			reasons = append(reasons, fmt.Errorf("transfer: clean up %s: %w", p, err))
		}
	}
	return stranded, errors.Join(reasons...)
}

// nonce is the random suffix shared by the temp and its backup. They share
// it deliberately (D6): two concurrent fallbacks in one directory cannot
// then collide on the backup name.
func (s *sink) nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("transfer: nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// suffixName turns "a.txt" into "a (1).txt". A name that is all extension —
// a dotfile such as ".bashrc" — has no stem to number, so the whole name
// becomes the stem and the suffix goes on the end.
func suffixName(name string, n int) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// present drops the empty entries, so a fallback that had no destination to
// back up reports one stranded path rather than one path and a blank.
func present(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validate refuses an Upload the sink cannot express. It is not a second
// answer to path policy — the transport validates DestDir and the provider
// owns syntax (§5.3) — it is this function refusing to join a Name whose
// meaning it would have to interpret.
func (u Upload) validate() error {
	if u.DestDir == "" {
		return fmt.Errorf("%w: destination directory is empty", ErrInvalidUpload)
	}
	if u.Size < 0 {
		return fmt.Errorf("%w: negative size %d", ErrInvalidUpload, u.Size)
	}
	switch u.OnExists {
	case Overwrite, KeepBoth, Skip:
	default:
		return fmt.Errorf("%w: unknown collision decision %q", ErrInvalidUpload, u.OnExists)
	}
	if u.Name == "" || u.Name == "." || u.Name == ".." || strings.ContainsAny(u.Name, `/\`) {
		return fmt.Errorf("%w: name %q is not one path component", ErrInvalidUpload, u.Name)
	}
	return nil
}

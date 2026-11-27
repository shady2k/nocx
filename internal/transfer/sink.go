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
	// DefaultChunk is the maximum number of bytes per transfer chunk.
	DefaultChunk = 256 << 10

	tempSuffix = ".nocx-upload-"

	// keepBothAttempts bounds the KeepBoth suffix search (D5).
	keepBothAttempts = 32
)

type sink struct {
	fs    RemoteFS
	chunk int
}

// chunkOr is the one place a chunk bound is validated, shared by both
// directions. A value below one selects DefaultChunk.
func chunkOr(n int) int {
	if n > 0 {
		return n
	}
	return DefaultChunk
}

// NewSink returns a Sink writing through fs, moving chunk bytes per
// write. Pass DefaultChunk unless you have a reason not to.
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
// From the start of Put through its return, an existing destination holds
// either the previous content or the new content. The temp exists from
// successful Create until Rename consumes it or Remove succeeds. A temp
// left behind is reported in Outcome.Stranded.
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
	// leaked handle holds a filesystem descriptor.
	closeErr := f.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("transfer: close %s: %w", temp, closeErr)
	}
	if copyErr == nil && closeErr == nil {
		// Check cancellation once more before the atomic rename. Once it
		// starts, report the filesystem's result.
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

	out, err := s.promote(dest, temp)
	if err != nil && reserved != "" {
		// A promote that failed leaves the reservation sitting at a name
		// nobody asked for — an empty file the sink alone created. It goes
		// the way the temp does, and if it cannot go it is named.
		left, cleanupErr := s.tryRemove(reserved)
		out.Stranded = append(out.Stranded, left...)
		err = errors.Join(err, cleanupErr)
	}
	return out, err
}

// copy streams r into w one chunk at a time.
//
// Cancellation is checked between chunks. A filesystem write in progress
// is allowed to finish.
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
// Cancellation is checked between chunks. A filesystem write in progress
// is allowed to finish.
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

// promote moves the finished temp onto the destination in one rename.
func (s *sink) promote(dest, temp string) (Outcome, error) {
	if err := s.fs.Rename(temp, dest); err != nil {
		left, cleanupErr := s.tryRemove(temp)
		return Outcome{Stranded: left}, errors.Join(fmt.Errorf("transfer: promote %s: %w", dest, err), cleanupErr)
	}
	return Outcome{State: StateWritten, FinalName: path.Base(dest)}, nil
}

// reserveName claims a free KeepBoth name with O_EXCL. Only a confirmed
// name collision advances to the next suffix.
func (s *sink) reserveName(destDir, name string) (string, []string, error) {
	var lastErr error
	for i := 1; i <= keepBothAttempts; i++ {
		candidate := suffixName(name, i)
		p := path.Join(destDir, candidate)
		f, err := s.fs.Create(p)
		if err != nil {
			if !errors.Is(err, fs.ErrExist) {
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

// nonce gives each upload its own temp name.
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

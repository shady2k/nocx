package filesystem

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
)

// ComputeRev is the cheap digest of a complete, ordered listing (spec §5.1):
// every entry's name, size, mtime, mode, kind, LinkTarget and LinkKind, plus
// the canonical identity of the directory itself.
//
// The last inclusion is deliberately beyond the spec's enumeration. A
// symlinked parent retargeted to a directory with identical children changes
// the canonical but not a single entry field, and the client's D9 cycle check
// compares canonicals — so the digest must move too, or a re-list that would
// have revealed the new identity never happens.
//
// The digest is computed over the provider's deterministic order (directories
// first, then UTF-8 byte order of the name) and is stable across calls for an
// unchanged directory. That stability is what makes pagination safe and is
// what the SFTP watcher compares (spec §5.1, D5). When it changes, the
// directory is re-listed in one call and every displayed row is replaced
// atomically.
//
// Canonical and LinkTarget are length-prefixed so no pair of values can
// collide by concatenation.
func ComputeRev(canonical string, entries []Entry) string {
	h := sha256.New()
	writeString(h, canonical)
	for _, e := range entries {
		writeString(h, e.Name)
		var b [8]byte
		//nolint:gosec // a digest hashes bits: a negative size or pre-epoch mtime is still a different digest
		binary.BigEndian.PutUint64(b[:], uint64(e.Size)) // #nosec G115 -- value is range-bounded by the protocol or preceding validation
		h.Write(b[:])
		//nolint:gosec // a digest hashes bits: a negative size or pre-epoch mtime is still a different digest
		binary.BigEndian.PutUint64(b[:], uint64(e.ModTime.UnixNano()))
		h.Write(b[:])
		binary.BigEndian.PutUint32(b[:4], e.Mode)
		h.Write(b[:4])
		h.Write([]byte(e.Kind))
		h.Write([]byte(e.LinkKind))
		writeString(h, e.LinkTarget)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeString feeds one length-prefixed value into the digest. Its only
// caller passes the sha256 hash, whose Write cannot fail; the errors are
// discarded for that reason, not in ignorance.
func writeString(w io.Writer, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	_, _ = w.Write(b[:])
	_, _ = io.WriteString(w, s)
}

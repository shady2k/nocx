// Package hashline provides strict, line-addressed edits against byte-exact
// file snapshots. A revision is tied to the canonical file identity and the
// complete bytes observed by Read; Apply refuses any intervening change.
//
// The package is PURE BY BYTES: it neither reads nor writes a file, and it
// names no path of its own beyond the canonical identity it is handed. Reading
// the whole file, deciding whether it is too large, and installing the bytes
// back are the provider's job (filesystem.VersionedFiles), because those three
// questions have different answers on this machine and on a host behind the
// far helper — and they are the host's to answer at the rename, not ours at the
// patch.
package hashline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrStaleRevision    = errors.New("hashline: file changed since it was read")
	ErrLineNotDisplayed = errors.New("hashline: requested line was not displayed by the read")
	ErrInvalidPatch     = errors.New("hashline: invalid patch")
	ErrBinaryFile       = errors.New("hashline: binary or invalid UTF-8 file")
)

type Snapshot struct {
	Path      string
	Canonical string
	Revision  string
	Total     int64
	WindowEnd int64
	SeenStart int
	SeenEnd   int
	Text      string
	Binary    bool
}

type Result struct {
	Path      string
	Canonical string
	Revision  string
}

type revision struct {
	Canonical string
	Digest    string
	SeenStart int
	SeenEnd   int
	Identity  string
}

type fileLine struct {
	body   []byte
	ending []byte
}

type operation struct {
	kind     byte
	start    int
	end      int
	position int
	before   bool
	body     []string
}

var (
	operationHeader = regexp.MustCompile(`^PUT (?:(\d+)\.=(\d+)|<(\d+)|>(\d+)):$`)
	cutHeader       = regexp.MustCompile(`^CUT (\d+)\.=(\d+)$`)
)

// Read describes the bytes data — the COMPLETE content of the file whose
// canonical identity is canonical — as a snapshot: the numbered window that
// fits in maxBytes, and the revision that names these bytes and the lines the
// window displayed. It performs no I/O, so the caller decides how the bytes
// were obtained and whether they were too large to hold.
func Read(canonical string, data []byte, maxBytes int64) Snapshot {
	r := revision{Canonical: canonical, Digest: digestFor(canonical, data)}
	s := Snapshot{Canonical: canonical, Total: int64(len(data))}
	if ValidateText(data) != nil {
		s.Binary = true
		s.Revision = encodeRevision(r)
		return s
	}
	lines := splitLines(data)
	limit := maxBytes
	if limit <= 0 || limit > int64(len(data)) {
		limit = int64(len(data))
	}
	var numbered bytes.Buffer
	var consumed int64
	for i, line := range lines {
		n := int64(len(line.body) + len(line.ending))
		if consumed+n > limit {
			break
		}
		fmt.Fprintf(&numbered, "%d:", i+1)
		numbered.Write(line.body)
		numbered.Write(line.ending)
		consumed += n
		r.SeenEnd = i + 1
	}
	if r.SeenEnd > 0 {
		r.SeenStart = 1
	}
	s.WindowEnd = consumed
	s.SeenStart, s.SeenEnd = r.SeenStart, r.SeenEnd
	s.Text = numbered.String()
	s.Revision = encodeRevision(r)
	return s
}

// Apply parses and applies one file's patch to data only if revisionToken still
// names this canonical path and exactly these bytes. It returns the new content
// for the caller to install; installing it is a conditional write
// (filesystem.VersionedFiles.Commit), so the window between this check and the
// rename is closed on the machine that owns the file.
func Apply(canonical string, data []byte, revisionToken, patch string) ([]byte, Result, error) {
	r, err := decodeRevision(revisionToken)
	if err != nil {
		return nil, Result{}, err
	}
	identity := sha256.Sum256([]byte(canonical))
	if hex.EncodeToString(identity[:]) != r.Identity {
		return nil, Result{}, ErrStaleRevision
	}
	if digestFor(canonical, data) != r.Digest {
		return nil, Result{}, ErrStaleRevision
	}
	if invalid := ValidateText(data); invalid != nil {
		return nil, Result{}, invalid
	}
	lines := splitLines(data)
	ops, err := parsePatch(patch)
	if err != nil {
		return nil, Result{}, err
	}
	for _, op := range ops {
		if op.kind == 'I' {
			if op.before {
				if op.position < 1 || op.position > len(lines)+1 || op.position < r.SeenStart || op.position > r.SeenEnd+1 {
					return nil, Result{}, ErrLineNotDisplayed
				}
			} else if op.position < 0 || op.position > len(lines) || op.position < r.SeenStart-1 || op.position > r.SeenEnd {
				return nil, Result{}, ErrLineNotDisplayed
			}
			continue
		}
		if op.start < 1 || op.end > len(lines) || op.start < r.SeenStart || op.end > r.SeenEnd {
			return nil, Result{}, ErrLineNotDisplayed
		}
	}
	updated := applyOperations(lines, ops)
	newData := joinLines(updated)
	return newData, Result{Canonical: canonical, Revision: encodeRevision(revision{Canonical: canonical, Digest: digestFor(canonical, newData), SeenStart: r.SeenStart, SeenEnd: r.SeenEnd})}, nil
}

// ValidateText refuses content the line-addressed engine cannot address: a NUL
// byte or invalid UTF-8. It is one answer for both directions — a snapshot that
// may not be patched and content that may not be created — because the question
// "is this a text file" has one answer, and two copies of the byte tests agree
// until one of them forgets a case.
func ValidateText(data []byte) error {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ErrBinaryFile
	}
	return nil
}

// Revision names bytes a caller already holds, with no displayed window. It is
// what a generator of new content answers — files.create, whose target was
// never read — so the token still binds the canonical identity to the exact
// bytes that were installed.
func Revision(canonical string, data []byte) string {
	return encodeRevision(revision{Canonical: canonical, Digest: digestFor(canonical, data)})
}

func digestFor(canonical string, data []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(canonical))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func encodeRevision(r revision) string {
	identity := sha256.Sum256([]byte(r.Canonical))
	return fmt.Sprintf("h1-%s-%s-%d-%d", hex.EncodeToString(identity[:]), r.Digest, r.SeenStart, r.SeenEnd)
}

func decodeRevision(token string) (revision, error) {
	parts := strings.Split(token, "-")
	if len(parts) != 5 || parts[0] != "h1" || len(parts[1]) != sha256.Size*2 || len(parts[2]) != sha256.Size*2 {
		return revision{}, errors.New("hashline: invalid revision")
	}
	seenStart, err := strconv.Atoi(parts[3])
	if err != nil {
		return revision{}, errors.New("hashline: invalid revision")
	}
	seenEnd, err := strconv.Atoi(parts[4])
	if err != nil {
		return revision{}, errors.New("hashline: invalid revision")
	}
	return revision{Digest: parts[2], SeenStart: seenStart, SeenEnd: seenEnd, Identity: parts[1]}, nil
}

func splitLines(data []byte) []fileLine {
	if len(data) == 0 {
		return nil
	}
	lines := make([]fileLine, 0, bytes.Count(data, []byte{'\n'})+1)
	start := 0
	for start < len(data) {
		i := bytes.IndexByte(data[start:], '\n')
		if i < 0 {
			lines = append(lines, fileLine{body: append([]byte(nil), data[start:]...)})
			break
		}
		i += start
		bodyEnd := i
		if bodyEnd > start && data[bodyEnd-1] == '\r' {
			bodyEnd--
		}
		lines = append(lines, fileLine{body: append([]byte(nil), data[start:bodyEnd]...), ending: append([]byte(nil), data[bodyEnd:i+1]...)})
		start = i + 1
	}
	return lines
}

func joinLines(lines []fileLine) []byte {
	var out bytes.Buffer
	for _, line := range lines {
		out.Write(line.body)
		out.Write(line.ending)
	}
	return out.Bytes()
}

func parsePatch(patch string) ([]operation, error) {
	if patch == "" {
		return nil, ErrInvalidPatch
	}
	rows := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	if len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	var ops []operation
	for i := 0; i < len(rows); {
		header := rows[i]
		i++
		if m := cutHeader.FindStringSubmatch(header); m != nil {
			start, _ := strconv.Atoi(m[1])
			end, _ := strconv.Atoi(m[2])
			if start > end {
				return nil, fmt.Errorf("%w: reversed range", ErrInvalidPatch)
			}
			ops = append(ops, operation{kind: 'C', start: start, end: end})
			continue
		}
		m := operationHeader.FindStringSubmatch(header)
		if m == nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPatch, header)
		}
		var op operation
		if m[1] != "" {
			op.kind = 'R'
			op.start, _ = strconv.Atoi(m[1])
			op.end, _ = strconv.Atoi(m[2])
			if op.start > op.end {
				return nil, fmt.Errorf("%w: reversed range", ErrInvalidPatch)
			}
		} else {
			op.kind = 'I'
			if m[3] != "" {
				op.position, _ = strconv.Atoi(m[3])
				op.before = true
			} else {
				op.position, _ = strconv.Atoi(m[4])
			}
		}
		for i < len(rows) {
			row := rows[i]
			if strings.HasPrefix(row, "PUT ") || strings.HasPrefix(row, "CUT ") {
				break
			}
			i++
			if row == "" || row[0] != '+' {
				return nil, fmt.Errorf("%w: body row %q lacks + prefix", ErrInvalidPatch, row)
			}
			op.body = append(op.body, row[1:])
		}
		if len(op.body) == 0 {
			return nil, fmt.Errorf("%w: PUT requires at least one body row", ErrInvalidPatch)
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil, ErrInvalidPatch
	}
	return ops, nil
}

func applyOperations(lines []fileLine, ops []operation) []fileLine {
	out := append([]fileLine(nil), lines...)
	ending := []byte("\n")
	for _, line := range lines {
		if len(line.ending) > 0 {
			ending = append([]byte(nil), line.ending...)
			break
		}
	}
	ordered := append([]operation(nil), ops...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return operationAnchor(ordered[i]) > operationAnchor(ordered[j])
	})
	for _, op := range ordered {
		body := make([]fileLine, len(op.body))
		for j, row := range op.body {
			body[j] = fileLine{body: []byte(row), ending: append([]byte(nil), ending...)}
		}
		switch op.kind {
		case 'C':
			out = append(out[:op.start-1], out[op.end:]...)
		case 'I':
			at := op.position
			if op.before {
				at--
			}
			out = append(out[:at:at], append(body, out[at:]...)...)
		default:
			out = append(out[:op.start-1], append(body, out[op.end:]...)...)
		}
	}
	return out
}

func operationAnchor(op operation) int {
	if op.kind == 'I' {
		return op.position
	}
	return op.start
}

// Package hashline provides strict, line-addressed edits against byte-exact
// file snapshots. A revision is tied to the canonical file identity and the
// complete bytes observed by Read; Apply refuses any intervening change.
package hashline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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

var pathMu sync.Mutex

// Read reads the complete file for its byte-exact revision digest, while
// returning only complete UTF-8 lines that fit in maxBytes. Line numbers are
// one-based and returned text is numbered as "N:TEXT". Original line endings
// are preserved in the returned text and on later edits.
func Read(path string, maxBytes int64) (Snapshot, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Snapshot{}, err
	}
	before, err := os.Stat(canonical)
	if err != nil {
		return Snapshot{}, err
	}
	if !before.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("hashline: %s is not a regular file", path)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return Snapshot{}, err
	}
	after, err := os.Stat(canonical)
	if err != nil {
		return Snapshot{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return Snapshot{}, ErrStaleRevision
	}

	r := revision{Canonical: canonical, Digest: digestFor(canonical, data)}
	s := Snapshot{Path: path, Canonical: canonical, Total: int64(len(data))}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		s.Binary = true
		s.Revision = encodeRevision(r)
		return s, nil
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
	return s, nil
}

// Apply parses and applies one file's patch only if revision still names the
// exact canonical path and bytes read earlier. It writes through a same-dir
// temporary file and renames it, so a failed write leaves the original bytes.
func Apply(path, revisionToken, patch string) (Result, error) {
	r, err := decodeRevision(revisionToken)
	if err != nil {
		return Result{}, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Result{}, err
	}
	identity := sha256.Sum256([]byte(canonical))
	if hex.EncodeToString(identity[:]) != r.Identity {
		return Result{}, ErrStaleRevision
	}
	pathMu.Lock()
	defer pathMu.Unlock()
	data, err := os.ReadFile(canonical)
	if err != nil {
		return Result{}, err
	}
	if digestFor(canonical, data) != r.Digest {
		return Result{}, ErrStaleRevision
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return Result{}, ErrBinaryFile
	}
	lines := splitLines(data)
	ops, err := parsePatch(patch)
	if err != nil {
		return Result{}, err
	}
	for _, op := range ops {
		if op.kind == 'I' {
			if op.before {
				if op.position < 1 || op.position > len(lines)+1 || op.position < r.SeenStart || op.position > r.SeenEnd+1 {
					return Result{}, ErrLineNotDisplayed
				}
			} else if op.position < 0 || op.position > len(lines) || op.position < r.SeenStart-1 || op.position > r.SeenEnd {
				return Result{}, ErrLineNotDisplayed
			}
			continue
		}
		if op.start < 1 || op.end > len(lines) || op.start < r.SeenStart || op.end > r.SeenEnd {
			return Result{}, ErrLineNotDisplayed
		}
	}
	updated := applyOperations(lines, ops)
	newData := joinLines(updated)
	if err := atomicReplace(canonical, newData); err != nil {
		return Result{}, err
	}
	return Result{Path: path, Canonical: canonical, Revision: encodeRevision(revision{Canonical: canonical, Digest: digestFor(canonical, newData)})}, nil
}

// Create creates a new regular UTF-8 file and refuses if the path already
// exists. Content is written exactly as supplied; no line-ending conversion.
func Create(path, content string) (Result, error) {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return Result{}, ErrBinaryFile
	}
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return Result{}, err
	}
	// #nosec G304 -- canonical is an absolute, cleaned path selected for this operation.
	f, err := os.OpenFile(canonical, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(canonical)
		}
	}()
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return Result{}, err
	}
	if err := f.Close(); err != nil {
		return Result{}, err
	}
	ok = true
	return Result{Path: path, Canonical: canonical, Revision: encodeRevision(revision{Canonical: canonical, Digest: digestFor(canonical, []byte(content))})}, nil
}

func digestFor(canonical string, data []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, canonical)
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

func atomicReplace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nocx-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	info, err := os.Stat(path)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

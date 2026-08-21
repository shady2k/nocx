package apiimport

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// A Postman name is arbitrary text out of a file, and a file that arrives
// in a pull request is a hostile path as well as hostile content (§13.1).
// The answer here is not to validate the name but to never use it as one:
// the display name is carried through untouched into the JSON, and the PATH
// is MINTED from it, so "../../etc" is a folder called "etc" rather than a
// traversal that has to be caught.
const (
	maxNameRunes    = 64
	maxFolderDepth  = 32
	maxItems        = 20000
	fallbackRequest = "request"
	fallbackFolder  = "folder"
)

// windowsReserved are device names that are not filenames on Windows, at
// any extension. A collection is meant to be shared, so a folder that only
// clones correctly on one OS is a folder that is broken on the others.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// slug turns a display name into one path segment, or "" when nothing
// usable is left. It cannot produce "", ".", ".." or a segment containing a
// separator, which is what makes the traversal unspellable rather than
// caught.
func slug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '_' || r == '-' || r == '.' || r == ' ':
			// A run of punctuation collapses, so "../.." cannot survive as
			// dots at all.
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > maxNameRunes {
		out = strings.Trim(string(r[:maxNameRunes]), "-")
	}
	if out == "" || out == "." || out == ".." {
		return ""
	}
	if windowsReserved[strings.ToLower(out)] {
		out = "_" + out
	}
	return out
}

// pathAllocator hands out paths inside the collection that do not collide.
//
// Collisions are compared CASE-INSENSITIVELY because APFS and NTFS are:
// "Users" and "users" are two names here and one directory there, and a
// collection that loses a request on the reviewer's machine is worse than
// one with an ugly suffix.
type pathAllocator struct{ used map[string]bool }

func newPathAllocator() *pathAllocator { return &pathAllocator{used: map[string]bool{}} }

// take returns dir/<unique segment><ext>.
func (p *pathAllocator) take(dir, name, fallback, ext string) string {
	base := slug(name)
	if base == "" {
		base = fallback
	}
	cand := base
	for i := 2; p.used[key(dir, cand+ext)]; i++ {
		cand = base + "-" + itoa(i)
	}
	p.used[key(dir, cand+ext)] = true
	if dir == "" {
		return cand + ext
	}
	return dir + "/" + cand + ext
}

func key(dir, seg string) string { return strings.ToLower(dir + "/" + seg) }

// requestID mints a stable id from the request's path in the collection.
// Deterministic, so re-importing the same export produces the same file
// twice rather than a diff; derived from a path rather than carried over
// from Postman, so no identifier from the source document is written.
func requestID(relPath string) string {
	sum := sha256.Sum256([]byte("nocx/apiimport/request\x00" + relPath))
	return hex.EncodeToString(sum[:8])
}

// clip bounds a hostile display name before it goes into a report the user
// reads. The name itself is carried into the JSON untouched; this is only
// for the Unsupported list.
func clip(s string) string {
	const max = 120
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	if s == "" {
		return "(unnamed)"
	}
	return s
}

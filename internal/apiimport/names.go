package apiimport

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/shady2k/nocx/internal/pathname"
)

// A Postman name is arbitrary text out of a file, and a file that arrives
// in a pull request is a hostile path as well as hostile content (§13.1).
// The answer here is not to validate the name but to never use it as one:
// the display name is carried through untouched into the JSON, and the PATH
// is MINTED from it, so "../../etc" is a folder called "etc" rather than a
// traversal that has to be caught.
const (
	// maxNameRunes is a READABILITY bound, not a filesystem one: a folder
	// named after a paragraph is unreadable in a tree long before it is
	// illegal. The filesystem's bound is pathname.MaxComponentBytes, and it
	// is in BYTES — sixty-four runes of Japanese are 180 of them, which is
	// how a minter with only a rune bound produced names the store refused.
	maxNameRunes = 64
	// collisionMargin is the room take() leaves for what it appends after
	// the extension: `-2` through `-20000`, six bytes, and two spare.
	collisionMargin = 8
	maxItems        = 20000
	fallbackRequest = "request"
	fallbackFolder  = "folder"
)

// slug turns a display name into one path segment that fits in budget bytes,
// or "" when nothing usable is left. It cannot produce "", ".", ".." or a
// segment containing a separator, which is what makes the traversal
// unspellable rather than caught.
//
// Two rules, and only the first is this package's. The CHARSET pass below is
// the import's own: letters and digits survive, everything else collapses to
// a dash, so "../.." cannot come through as dots at all. What makes the
// result a name a filesystem will take — the Windows device names, a
// trailing dot or space, the byte bound — belongs to internal/pathname,
// which is also what internal/apicoll VALIDATES with. Minting through it is
// what makes "a name the importer mints is a name the store accepts" true by
// construction; this package used to carry its own copy of the device list,
// where no validator could reach it.
func slug(name string, budget int) string {
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
	return pathname.Portable(out, budget)
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
//
// The base is minted with room for what take itself appends — the extension
// and, on a collision, the counter — because the component the filesystem
// has to take is the whole of it, not the base.
func (p *pathAllocator) take(dir, name, fallback, ext string) string {
	base := slug(name, pathname.MaxComponentBytes-len(ext)-collisionMargin)
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

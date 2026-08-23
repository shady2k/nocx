// Package pathname owns one question for the whole product: is this name
// usable as a path component on every platform nocx ships to?
//
// It exists because the answer was in two places and neither was complete.
// internal/apicoll validated a path for what makes it DANGEROUS — a
// traversal, an unclean spelling, something that is not one component — and
// knew nothing about the names Windows simply refuses. internal/apiimport
// knew the device names, but only as a list inside a function that MINTS
// names, where a validator cannot reach it. A collection is meant to be put
// under git and shared (§6.1), so a folder called `con` made on Linux is a
// collection a colleague on Windows cannot check out at all — and both
// packages were green while that was true.
//
// Two entry points and one rule behind them:
//
//   - CheckComponent / CheckRelPath REFUSE. A store never rewrites a name a
//     caller gave it: a name silently changed is a surface reporting success
//     for something it did not do (§13.1, and apicoll's own comments say it
//     three times).
//   - Portable MINTS. An importer reading somebody's Postman export has no
//     user to refuse — its whole job is to turn arbitrary text into a name —
//     so it needs the same rule from the other side. Minting THROUGH this
//     package is what makes the two agree by construction rather than by
//     both being maintained.
//
// The three numbers are stated rather than felt:
//
//   - MaxComponentBytes 128. Every filesystem we ship to allows at least 255
//     bytes per component (ext4, APFS, NTFS); 128 leaves room for the
//     extension and the collision suffix a minter appends, and it is the
//     limit apicoll already published for a collection or folder name.
//   - MaxDepth 32 components. It bounds an import: a Postman export nests
//     folders as deep as it likes, and each level costs a directory on disk.
//   - MaxRelPathBytes 200, for the path INSIDE the collection. Classic
//     Windows stops at 260 characters for the whole path, and git for
//     Windows still does; a checkout root like
//     `C:\Users\somebody\src\team-collections\` is around 40 of those, so
//     200 leaves a real root room and still refuses the path that would
//     arrive unopenable.
package pathname

import (
	"errors"
	"fmt"
	"strings"
)

// The bounds. Changing one of these changes what a collection made by this
// build can be checked out on; the arithmetic behind each is in the package
// comment above.
const (
	MaxComponentBytes = 128
	MaxDepth          = 32
	MaxRelPathBytes   = 200
)

// windowsReserved are device names, not filenames: Windows refuses them for
// a file and for a folder, in any case, and at any extension — `CON`,
// `con.json` and `AUX.tar.gz` are all the device. This is the ONE list; the
// importer used to carry a second copy of it beside its minting code.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// unusableRunes may not appear anywhere in a component. The first two are
// separators — `\` on Windows as much as `/` here, so a name carrying one is
// refused on every platform rather than only on the one that would split it —
// and the rest are the characters NTFS reserves.
const unusableRunes = `/\<>:"|?*`

// trailingUnusable may not END a component: Windows drops a trailing dot or
// space rather than keeping it, so `docs.` and `docs ` both become `docs`
// there. Two names here, one directory on the colleague's machine.
const trailingUnusable = ". "

// CheckComponent reports whether name may be one component of a path on
// every platform we ship to. The error is a sentence a surface can show, and
// it names the name: the remedy is always to choose a different one, so the
// user has to be told which one was refused and why.
func CheckComponent(name string) error {
	switch {
	case name == "":
		return errors.New("a name may not be empty")
	case len(name) > MaxComponentBytes:
		return fmt.Errorf("it is %d bytes, longer than the %d-byte limit", len(name), MaxComponentBytes)
	case name == "." || name == "..":
		return fmt.Errorf("%q names a directory, not a file or folder in one", name)
	}
	for _, r := range name {
		switch {
		case r == 0:
			return errors.New("it contains a NUL byte")
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("%q contains a control character (%#U), which Windows will not take in a name", name, r)
		case strings.ContainsRune(unusableRunes, r):
			return fmt.Errorf("%q contains %q; a name may not hold any of %s", name, r, unusableRunes)
		}
	}
	if last := name[len(name)-1]; strings.IndexByte(trailingUnusable, last) >= 0 {
		return fmt.Errorf("%q ends with %q, which Windows drops rather than keeps — two such names are one folder there",
			name, string(last))
	}
	if reservedStem(name) {
		return fmt.Errorf("%q is a device name on Windows (CON, PRN, AUX, NUL, COM1-COM9 and LPT1-LPT9 are devices at any extension), so it cannot be created there", name)
	}
	return nil
}

// CheckRelPath holds a slash-separated path inside a collection to the same
// rule, component by component, and adds the two bounds a single component
// cannot carry: how deep it goes and how long it is in total.
//
// It splits on `/` only. A Windows-spelled path arrives here as ONE
// component containing a backslash, which CheckComponent refuses — so there
// is no second separator to reason about.
func CheckRelPath(rel string) error {
	if rel == "" {
		return errors.New("the path is empty")
	}
	if len(rel) > MaxRelPathBytes {
		return fmt.Errorf("the path is %d bytes, longer than the %d-byte limit", len(rel), MaxRelPathBytes)
	}
	parts := strings.Split(rel, "/")
	if len(parts) > MaxDepth {
		return fmt.Errorf("the path has %d components, deeper than the %d-component limit", len(parts), MaxDepth)
	}
	for _, p := range parts {
		if err := CheckComponent(p); err != nil {
			return err
		}
	}
	return nil
}

// Portable returns name adjusted as little as possible into something
// CheckComponent accepts and at most budget bytes long, or "" when nothing
// usable is left — the caller's cue to use its own fallback.
//
// budget is the room the finished component has, which is not always the
// whole limit: a minter that is going to append `.json` and a `-2` collision
// suffix passes MaxComponentBytes minus that, so the name it ends up with is
// still one CheckComponent takes.
//
// Adjusted, never guessed at: an unusable character is dropped, a trailing
// dot or space is trimmed, an over-long name is cut on a rune boundary (a
// rune bound is not a byte bound — forty Japanese runes are 120 bytes), and
// a device name is prefixed rather than replaced, so `con` stays findable as
// `_con`. Cutting can CREATE a device name — `console` at three bytes is
// `con` — which is why this loops instead of checking once.
func Portable(name string, budget int) string {
	if budget > MaxComponentBytes {
		budget = MaxComponentBytes
	}
	if budget <= 0 {
		return ""
	}
	s := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(unusableRunes, r) {
			return -1
		}
		return r
	}, name)

	// Two passes are enough — a name prefixed with `_` is not a device name,
	// so the second pass always breaks — and the bound makes that a fact
	// rather than a hope.
	for i := 0; i < 3; i++ {
		s = truncateBytes(strings.TrimRight(s, trailingUnusable), budget)
		s = strings.TrimRight(s, trailingUnusable)
		if s == "" || s == "." || s == ".." {
			return ""
		}
		if !reservedStem(s) {
			break
		}
		s = "_" + s
	}
	if CheckComponent(s) != nil {
		return ""
	}
	return s
}

func reservedStem(s string) bool {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return windowsReserved[strings.ToLower(s)]
}

// truncateBytes cuts s to at most n bytes without splitting a rune. A half
// rune is not a shorter name, it is a broken one.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	return s[:cut]
}

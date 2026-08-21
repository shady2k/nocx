package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// wailsModule is the module path whose version the About page reports. Read
// from the build info rather than restated as a constant: the number in go.mod
// is the truth and a second copy of it here would be wrong the first time
// anybody upgraded.
const wailsModule = "github.com/wailsapp/wails/v3"

// Unknown is what a field says when the build carries no answer for it. It is a
// WORD and never the empty string: a blank row on the About page cannot be told
// apart from a row that has not loaded, and the page exists to be read out into
// a bug report (nocx-8bbp).
//
// Exported with OrUnknown below because the wire DTO applies the same rule at
// the other end of the seam, and "what an absent value reads as" may have one
// owner. A transport that spelled its own default would be the second.
const Unknown = "unknown"

// BuildInfo is everything the running binary can say about itself — what a
// person pastes into a bug report, and the only description of this build the
// product ever shows.
//
// Three of the fields are stamped at link time and three are read from the
// process, which is the whole design: nothing here is a constant somebody has
// to remember to bump. `-X` writes Version, Commit and Date (see version.go for
// the exact paths); Go, Wails and Platform are what the binary was actually
// built from and is actually running on.
type BuildInfo struct {
	// Version is the release number ("0.2.0"), or "dev" when nothing stamped
	// it.
	Version string
	// Commit is the git sha the build came from.
	Commit string
	// Date is when it was built.
	Date string
	// Go is the toolchain that compiled it, as runtime.Version reports it.
	Go string
	// Wails is the desktop shell's module version, from the build's own
	// dependency list.
	Wails string
	// Platform is GOOS/GOARCH — the pair that decides which release artifact
	// this is, and the first thing a bug report needs.
	Platform string
	// Development says this build was never stamped, so Version is a
	// placeholder rather than a release number. The updater keys the same fact
	// off Version == "dev" (version.go); this is that fact named for a surface
	// that has to render it, so no page has to spell the sentinel again.
	Development bool
}

// Info describes the running binary.
//
// Read at the call site rather than cached in a package var: the values behind
// it are link-time constants and process facts, so there is nothing to
// memoise, and a var would be a second place the answer lives.
func Info() BuildInfo {
	return BuildInfo{
		Version:     OrUnknown(Version),
		Commit:      OrUnknown(Commit),
		Date:        OrUnknown(Date),
		Go:          runtime.Version(),
		Wails:       wailsVersion(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Development: Version == "dev" || Version == "",
	}
}

// wailsVersion finds the shell's version in the build's dependency list.
//
// debug.ReadBuildInfo answers from data the linker embedded, so it needs no
// module cache at runtime and works in the shipped app. It CAN fail — a binary
// built in a mode that carries no build info — and the answer then is the word,
// not a blank.
func wailsVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	for _, dep := range bi.Deps {
		if dep == nil {
			continue
		}
		if dep.Path == wailsModule {
			// A replaced module reports its replacement's version; that is the
			// code actually linked in, which is what the reader is asking
			// about.
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return OrUnknown(dep.Version)
		}
	}
	return Unknown
}

// OrUnknown reports s, or the word when s carries nothing.
func OrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return Unknown
	}
	return s
}

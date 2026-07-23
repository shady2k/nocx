// Package version carries build metadata stamped into the binary at link time.
//
// The three vars are set with `-ldflags -X` during a release build
// (.github/workflows/release.yml); a plain `go build` leaves the defaults. The
// defaults are load-bearing rather than cosmetic: the updater treats
// Version == "dev" as "this is a development build, never check for updates"
// (§7.7 of docs/superpowers/specs/2026-07-22-distribution-and-updates-design.md),
// so `wails dev` can never offer to replace itself.
//
// Version holds the bare number ("0.2.0"), matching the release manifest's
// `version` field so the two never need translating.
package version

// Values injected at link time. The defaults mark a non-release build.
//
// The full -X paths the release build must use, kept here so a typo in the
// workflow is easy to check against the source of truth:
//
//	github.com/shady2k/nocx/internal/version.Version
//	github.com/shady2k/nocx/internal/version.Commit
//	github.com/shady2k/nocx/internal/version.Date
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

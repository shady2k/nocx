// Package version carries build metadata stamped into the binary at link time.
//
// The build vars are set with `-ldflags -X` during an artifact build
// (.github/workflows/release-build.yml); a plain `go build` leaves the defaults. The
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
//	github.com/shady2k/nocx/internal/version.Wails
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	// Wails is the desktop shell's module version, stamped by the release build
	// from the pin in go.mod (nocx-1ebrs.11).
	//
	// IT CANNOT BE READ FROM THE RUNNING BINARY, which is why it is a -X value
	// and not another runtime fact: app.about is answered by the coordinator
	// (cmd/nocx-server), built `CGO_ENABLED=0 -tags release` because it has no
	// window — so its own module list does not contain wails at all, and the
	// field the About page reads said "unknown" in every shipped 0.6.x build
	// while the desktop app beside it linked the shell.
	//
	// Empty means "nothing stamped it", and Info falls back to the module
	// list, which is the right answer for a build that does link the shell
	// (wails dev, the packaged app binary).
	Wails = ""
)

// Requested reports whether args — os.Args[1:] — asks only for the build
// metadata.
//
// It is a scan for two exact strings and not a flag parser, which is the
// point rather than an economy. nocx-server deliberately parses no flags at
// all, so that a capability cannot arrive by argv where `ps` shows it to
// every process on the machine (the nocx-server design, §6); a valueless
// spelling honours that, and anything taking a value would not.
//
// It lives here, and not in either main package, because both binaries
// answer this question and a release smoke check compares their answers.
// Two copies of the accepted spellings is exactly the pair that drifts.
func Requested(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			return true
		}
	}
	return false
}

// BuildIdentity distinguishes builds within one release version. BuiltAt is the
// canonical UTC build time, not a manifest's publication or promotion time.
// The signed manifest uses this same shape as the running binary.
type BuildIdentity struct {
	Commit  string `json:"commit"`
	BuiltAt string `json:"builtAt"`
}

// Identity reads the existing link-time stamps; it owns no second copy.
func Identity() BuildIdentity {
	return BuildIdentity{Commit: Commit, BuiltAt: Date}
}

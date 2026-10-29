package version

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// THE POINT OF THE WHOLE DESCRIPTOR: no field is ever empty. The About page
// exists so a person filing a bug has something to read and quote, and a blank
// row tells them nothing while looking exactly like a row that has not loaded
// (nocx-8bbp). Absence is spelled out here, once, so no surface has to decide
// what to draw for a missing value.
func TestInfoNamesEveryFieldEvenWhenNothingWasStamped(t *testing.T) {
	got := Info()
	fields := map[string]string{
		"Version":  got.Version,
		"Commit":   got.Commit,
		"Date":     got.Date,
		"Go":       got.Go,
		"Wails":    got.Wails,
		"Platform": got.Platform,
	}
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s is empty; every field must say something, including that it is unknown", name)
		}
	}
}

// A `go test` binary passes no -ldflags, so this asserts the un-stamped build
// exactly as TestLinkTimeDefaults does for the raw vars — and that the
// descriptor reports it as a development build rather than presenting "dev" as
// though it were a release number.
func TestInfoReportsAnUnstampedBuildAsDevelopment(t *testing.T) {
	got := Info()
	if got.Version != "dev" {
		t.Fatalf("Version = %q, want the unstamped default %q", got.Version, "dev")
	}
	if !got.Development {
		t.Fatal("an unstamped build must report Development, or the page shows 'dev' as a release")
	}
}

// The two the process knows about itself rather than being told: they must be
// read, never restated. A hand-maintained platform string is the constant this
// whole descriptor exists to avoid.
func TestInfoReadsTheRuntimeItIsRunningOn(t *testing.T) {
	got := Info()
	if got.Go != runtime.Version() {
		t.Fatalf("Go = %q, want %q", got.Go, runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; got.Platform != want {
		t.Fatalf("Platform = %q, want %q", got.Platform, want)
	}
}

// A stamped build reports what it was stamped with. Asserted through the same
// vars the linker writes, because that is the only way in — and restored after,
// since they are package state a later test would otherwise inherit.
func TestInfoReportsAStampedBuildAsARelease(t *testing.T) {
	defer func(v, c, d string) { Version, Commit, Date = v, c, d }(Version, Commit, Date)
	Version, Commit, Date = "0.3.1", "abc1234", "2026-08-20T10:00:00Z"

	got := Info()
	if got.Version != "0.3.1" || got.Commit != "abc1234" || got.Date != "2026-08-20T10:00:00Z" {
		t.Fatalf("stamped build reported as %+v", got)
	}
	if got.Development {
		t.Fatal("a stamped build is not a development build")
	}
}

// THE SHIPPED APP'S SHELL VERSION IS STAMPED, NOT READ (nocx-1ebrs.11).
//
// app.about is answered by the coordinator, which is built without cgo and
// without a window, so the module list it was linked from has no wails entry —
// the fallback in wailsVersion cannot answer there, and the About page showed
// "Wails: unknown" in the shipped 0.6.0. The release build stamps the pinned
// version instead, and a stamped value has to win over a module list that will
// never have one.
func TestInfoPrefersTheStampedShellVersion(t *testing.T) {
	defer func(w string) { Wails = w }(Wails)
	Wails = "v3.0.0-beta.9"

	if got := Info().Wails; got != "v3.0.0-beta.9" {
		t.Fatalf("Wails = %q, want the stamped %q", got, "v3.0.0-beta.9")
	}

	// An empty stamp is "nothing stamped it", not a value: the module list is
	// then the answer, and a build that links the shell keeps working.
	Wails = ""
	if got := Info().Wails; strings.TrimSpace(got) == "" {
		t.Fatal("an unstamped build must still say something rather than nothing")
	}
}

// AND THE RELEASE BUILD HAS TO DO THE STAMPING. The value above is only ever
// set by a link flag, and a link flag the workflow does not pass is a field the
// shipped binary cannot know — which is the defect, not a missing test.
//
// Asserted against the workflow itself because that file is the only thing
// that runs the linker for a release: every LDFLAGS chain that stamps Version
// must stamp Wails in the same chain, so a second build step cannot be added
// later with half the stamps.
func TestReleaseBuildStampsTheShellVersion(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release-build.yml")
	raw, err := os.ReadFile(path) //nolint:gosec // this repository's own tracked workflow file
	if err != nil {
		t.Fatalf("the release workflow is the only place a release is linked: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	chains := 0
	for i, line := range lines {
		if !strings.Contains(line, `LDFLAGS="`) || !strings.Contains(line, "internal/version.Version=") {
			continue
		}
		chains++
		stamped := false
		for j := i; j < len(lines) && j < i+5; j++ {
			if strings.Contains(lines[j], "internal/version.Wails=") {
				stamped = true
				break
			}
			// The chain ends where the build begins: a stamp written after
			// `go build` is a stamp that build never saw.
			if strings.Contains(lines[j], "go build") || strings.Contains(lines[j], "go list") {
				break
			}
		}
		if !stamped {
			t.Errorf("release-build.yml line %d builds a release without stamping version.Wails", i+1)
		}
	}
	if chains == 0 {
		t.Fatal("no LDFLAGS chain stamps the version; the check above proved nothing")
	}
}

// THE SHIPPED APP'S ABOUT WINDOW READS THE BUNDLE, AND THE BUNDLE'S PLIST IS
// GENERATED FROM WAILS' OWN DEFAULTS UNLESS EVERY IDENTITY FLAG IS NAMED
// (nocx-1ebrs.43). The app menu's About item is orderFrontStandardAboutPanel:,
// which prints NSHumanReadableCopyright out of the bundle's Info.plist
// verbatim — and the plist's only producer is `wails3 generate build-assets`,
// whose unset copyright default is the template string "© now, My Company".
// That is the line the owner read off the shipped 0.6.1. The identity reaches
// the bundle as flags on that one call, and a flag the workflow stops passing
// is the defect returning — asserted here the same way the Wails stamp is.
func TestReleaseBundleCarriesNocxOwnAboutIdentity(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release-build.yml")
	raw, err := os.ReadFile(path) //nolint:gosec // this repository's own tracked workflow file
	if err != nil {
		t.Fatalf("the release workflow is the only place the bundle plist is generated: %v", err)
	}
	call := buildAssetsCall(string(raw))
	if call == "" || !strings.Contains(call, "-productversion") {
		t.Fatal("no build-assets invocation found; the check below proved nothing")
	}
	if got, ok := flagValue(call, "-productidentifier"); !ok || got != "dev.nocx" {
		t.Errorf("release build-assets identifier = %q, want dev.nocx (ADR-0103)", got)
	}
	for _, flag := range []string{"-productcopyright", "-productcomments"} {
		value, ok := flagValue(call, flag)
		if !ok {
			t.Errorf("the build-assets call passes no %s, so the bundle ships the Wails template default and the About window prints it (nocx-1ebrs.43)", flag)
			continue
		}
		for _, boilerplate := range []string{"My Company", "My Product", "This is a comment"} {
			if strings.Contains(value, boilerplate) {
				t.Errorf("%s value %q carries the Wails template's %q boilerplate", flag, value, boilerplate)
			}
		}
		if flag == "-productcopyright" && !strings.Contains(value, "shady2k") {
			t.Errorf("-productcopyright value %q names no nocx identity; the LICENSE holds the project's own holder", value)
		}
	}
}

// buildAssetsCall joins the workflow's `wails3 generate build-assets`
// invocation into one line, resolving the shell's continuation backslashes so
// a flag can be read no matter where the YAML wrapped it.
func buildAssetsCall(workflow string) string {
	var call strings.Builder
	inCall := false
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		continued := strings.HasSuffix(trimmed, "\\")
		trimmed = strings.TrimSuffix(trimmed, "\\")
		if inCall {
			call.WriteByte(' ')
			call.WriteString(trimmed)
			if !continued {
				return call.String()
			}
			continue
		}
		if strings.HasPrefix(trimmed, "wails3 generate build-assets") {
			inCall = true
			call.WriteString(trimmed)
			if !continued {
				return call.String()
			}
		}
	}
	if inCall {
		return call.String()
	}
	return ""
}

// flagValue reads one flag's argument out of a joined shell command. A quoted
// argument is taken whole — the copyright line and the description both
// contain spaces — and the quotes are stripped, because the value that
// reaches the generator is the shell's, not the YAML's.
func flagValue(call, flag string) (string, bool) {
	i := strings.Index(call, flag)
	if i < 0 {
		return "", false
	}
	rest := strings.TrimLeft(call[i+len(flag):], " ")
	if rest == "" {
		return "", false
	}
	if strings.HasPrefix(rest, `"`) {
		if end := strings.Index(rest[1:], `"`); end >= 0 {
			return rest[1 : 1+end], true
		}
		return "", false
	}
	fields := strings.Fields(rest)
	return strings.Trim(fields[0], "'"), true
}

package version

import (
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

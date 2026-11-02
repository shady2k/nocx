package version

import "testing"

// The build number is one derivation from the signed instant (nocx-vh2hr.1.5):
// the pipeline stamps it into the binary from the same minute the manifest
// signs as build.builtAt, so a reader of either must get the same digits.
func TestBuildNumberOfIsTheSignedInstantsUTCminute(t *testing.T) {
	cases := map[string]string{
		"2026-11-01T22:00:00Z":      "202611012200",
		"2026-11-01T22:00:59Z":      "202611012200",
		"2026-11-01T23:59:00+01:00": "202611012259",
		"unknown":                   "",
		"":                          "",
		"2026-11-01":                "",
	}
	for instant, want := range cases {
		if got := BuildNumberOf(instant); got != want {
			t.Errorf("BuildNumberOf(%q) = %q, want %q", instant, got, want)
		}
	}
}

// Info carries the build beside the version, and an unstamped build says
// "unknown" rather than an empty row the About page cannot tell from a
// value that has not loaded (the contract's own rule).
func TestInfoCarriesTheBuildNumber(t *testing.T) {
	old := Build
	defer func() { Build = old }()
	Build = "202611012200"
	if got := Info().Build; got != "202611012200" {
		t.Fatalf("Info().Build = %q", got)
	}
	Build = ""
	if got := Info().Build; got != "unknown" {
		t.Fatalf("unstamped Info().Build = %q, want unknown", got)
	}
}

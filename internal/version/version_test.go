package version

import "testing"

// A plain `go test`/`go build` passes no -ldflags, so the defaults must stand.
// These exact strings are a contract: the updater keys "development build, do
// not self-update" off Version == "dev", and the release workflow's -X paths
// (documented in version.go) must land on these same symbols or the shipped
// binary would silently report "dev" and refuse every update.
func TestLinkTimeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, got, want string
	}{
		{"Version", Version, "dev"},
		{"Commit", Commit, "none"},
		{"Date", Date, "unknown"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

package version

import "testing"

// The spellings are a contract with whoever types them and with the
// release smoke check, so they are asserted rather than assumed — and the
// negative half matters more than the positive one: a daemon that treated
// an ordinary launch as a version request would print and exit instead of
// serving.
func TestRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"no arguments at all — the ordinary launch", nil, false},
		{"double dash", []string{"--version"}, true},
		{"single dash", []string{"-version"}, true},
		{"among others", []string{"--quiet", "--version"}, true},
		{"an unrelated flag", []string{"--verbose"}, false},
		{"a value-taking spelling is not honoured", []string{"--version=1"}, false},
		{"a bare word", []string{"version"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Requested(tc.args); got != tc.want {
				t.Errorf("Requested(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

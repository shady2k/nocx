package main

import "testing"

// farhelperPath is the whole fence of the Ports exemption, and every way it went
// wrong was in this one comparison: a bare prefix admitted
// internal/farhelperlegacy, a suffix with no module check would admit another
// module's package of the same name, and a suffix with a module check admitted
// any package whose path merely ENDS in internal/farhelper — including a
// nested internal/app/internal/farhelper, which is somebody else's package
// declaring a type of the same name (ALX-750). The owner is a path, so the
// comparison is against the path and nothing else. The fixture tree cannot
// reach any of those neighbours — its packages carry one extra segment under
// internal/ — so the boundary is pinned here as well as in the JS fixtures.
func TestFarhelperPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"github.com/shady2k/nocx/internal/farhelper", true},
		{"github.com/shady2k/nocx/internal/farhelperlegacy", false},
		{"github.com/shady2k/nocx/internal/farhelpers", false},
		{"github.com/shady2k/nocx/internal/farhelper/pane", false},
		{"github.com/shady2k/nocx/internal/app/internal/farhelper", false},
		{"github.com/shady2k/nocx/internal/app/internal/farhelperlegacy", false},
		{"github.com/shady2k/nocx/internal/farhelper/internal/pane", false},
		{"example.com/lib/internal/farhelper", false},
	}
	for _, c := range cases {
		if got := farhelperPath(c.path); got != c.want {
			t.Errorf("farhelperPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

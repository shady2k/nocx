package ssh

import "testing"

// modeAllowsIntegration is the open-time gate: it decides whether a session
// publishes the shell bundle and integrates, or opens a plain shell. It had
// no test at all, and it reads the delivery axis by string — so when
// ADR-0033 made `auto` the hardcoded default, an unhandled `auto` here would
// have silently stopped integrating EVERY connection the user never edited,
// with no compile error and nothing in the product to say why the blocks
// went away.
//
// The table is exhaustive over the axis on purpose: an unknown mode must
// fail closed, and a mode this build ships must never fall into that arm by
// omission.
func TestModeAllowsIntegration(t *testing.T) {
	cases := []struct {
		mode string
		want bool
		why  string
	}{
		{
			mode: "auto", want: true,
			why: "the hardcoded default (ADR-0033): auto wraps and installs the " +
				"scripts exactly as script does — what it adds is that the relay " +
				"may be OFFERED, not that the scripts are withheld",
		},
		{
			mode: "script", want: true,
			why: "the explicit answer: wrap and install automatically (N3)",
		},
		{
			mode: "", want: true,
			why: "the direct-host default — no profile said otherwise",
		},
		{
			mode: "raw", want: false,
			why: "the user's opt-out: nothing is written, a plain shell opens",
		},
		{
			mode: "sometimes", want: false,
			why: "an unknown mode fails closed — it never integrates",
		},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			if got := modeAllowsIntegration(tc.mode); got != tc.want {
				t.Errorf("modeAllowsIntegration(%q) = %v, want %v — %s",
					tc.mode, got, tc.want, tc.why)
			}
		})
	}
}

package ghostty

import "github.com/shady2k/nocx/internal/emulator"

// The output-start mark (nocx-2v80t.3.12): OSC 133 C, ESC ] 1 3 3 ; C BEL,
// with no payload and no variable content — nocx.bash's __nocx_preexec
// writes it unconditionally, before every command a shell with authenticated
// integration hands off, via __nocx_marker's bare form (`printf
// '\e]133;%s\a' "$__kind"` with kind C).
//
// ADR-0024 decision 1 says this sequence carries no lifecycle authority —
// "C and D have no meaning to nocx" — and sighting it here does not change
// that: like the render fence (fence.go), a sighted output mark LOCATES an
// event inside an interval sessionruntime has already authenticated by
// other means (the command's own authenticated start and completion); it
// never opens, closes or authorises an interval on its own, and a hostile
// program cannot forge one that arrives before the shell's own — the real
// preexec fires before the shell even reads the command line, and only the
// FIRST sighting per interval is ever given meaning (sessionruntime's own
// rule, not this scanner's).
//
// The scanner shares scanMarkers' pass with the fence's (terminal.go):
// both patterns share the five-byte prefix ESC ] 1 3 3 and diverge at the
// sixth, so one pass over the bytes keeps each candidate's own position
// exactly as far as the caller actually committed to vt_write, which two
// independent passes over the same slice could not (see scanMarkers).
const outputMarkFixed = "\x1b]133;C\x07"

// outputMarkMatches reports whether b is the idx'th byte of the output mark.
func outputMarkMatches(idx int, b byte) bool {
	return idx < len(outputMarkFixed) && b == outputMarkFixed[idx]
}

// sightOutputMark appends the output-mark effect. Like sightFence, this
// carries no authority and calls nothing in the runtime — it produces the
// effect and returns, and the runtime is what decides what a location means
// (ADR-0024 decision 1).
func (t *terminal) sightOutputMark() {
	t.effects = append(t.effects, emulator.Effect{Kind: emulator.EffectOutputMark})
}

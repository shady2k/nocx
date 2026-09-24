package sessionruntime

// The fence split point (nocx-2v80t.3.12).
//
// A pty read hands Ingest whatever the kernel had buffered, with no boundary
// between what one shell write() produced and what the next one did. The
// shell's own PROMPT_COMMAND writes the render fence and the bytes that
// follow it (OSC 133 D/A, OSC 7, then PS1's own visible text) back to back
// with nothing to flush in between — internal/shellintegration/scripts/nocx.bash's
// __nocx_prompt_command — so an ordinary pty read very often hands Ingest
// ALL of it in one feed. The emulator applies the whole feed before Ingest
// ever asks it for a screen (Session.takeObservationScreenLocked reads
// whatever the library holds AT THE MOMENT OF THE CALL, not at the fence's
// own instant), so the interval's closing screen — and the boundary window
// built from it — ended up holding rows the NEXT command's own prompt had
// already drawn. Measured: internal/sessionruntime/observation_test.go's
// TestAFenceBatchedWithTrailingPromptBytesClosesOnTheFencesOwnScreen fed a
// fence immediately followed by 133;D, 133;A, OSC 7 and a visible PS1 line
// in one Ingest call; before this file existed the closing screen's last row
// read the PS1 text instead of blank.
//
// The fix is mechanical, not a second authority: Ingest locates the fence's
// own end HERE, before it ever reaches the emulator, and feeds up to it in
// its own call — so the screen Session reads immediately afterward is the
// screen exactly as the fence left it, with nothing that followed in the
// SAME feed yet applied. The emulator's own scanner
// (internal/emulator/ghostty/fence.go) is unchanged and unaffected: it is
// still the only thing that can ever produce an [emulator.EffectFence], it
// still sees every byte in the same order, and it re-validates the marker
// on its own account regardless of where Ingest happened to split the
// feed — feeding the same bytes in two calls instead of one is exactly the
// case every ordinary pty read already forces it to handle. A wrong guess
// here (a look-alike sequence that is not actually a fence) costs nothing
// but one harmless extra split; it is never treated as authority on its own
// (ADR-0024 decision 1 — a sighted marker only locates an already
// authenticated event).
//
// The wire shape is fixed and already the contract sessionruntime's own
// nonce-matching depends on (fenceNonceOf, runtime.go): ESC ] 1 3 3 7 ;
// N O C X _ F E N C E ; then exactly 64 lowercase hex characters, then BEL.

const (
	fenceMarkerPrefix = "\x1b]1337;NOCX_FENCE;"
	fenceNonceHexLen  = 64
	fenceMarkerLen    = len(fenceMarkerPrefix) + fenceNonceHexLen + 1 // + BEL
)

// nextFenceSplit answers the index within b one past the first COMPLETE
// fence marker's terminating BEL, and whether one was found at all. b may
// hold no fence, one, several, or the leading fragment of one whose
// remainder has not arrived yet — every one of those is ordinary, and a
// partial match at the end of b answers false rather than guess: the next
// Ingest call, and the emulator's own stateful scanner, complete it exactly
// as they already do when no split ever ran.
func nextFenceSplit(b []byte) (end int, ok bool) {
	for start := 0; start+len(fenceMarkerPrefix) <= len(b); start++ {
		if !hasPrefixAt(b, start, fenceMarkerPrefix) {
			continue
		}
		if start+fenceMarkerLen > len(b) {
			// The prefix matched but the rest has not arrived in this feed.
			// It may yet complete in a later Ingest call; nothing here
			// commits to that being a fence, so the search simply stops
			// rather than manufacture a split point past the end of b.
			return 0, false
		}
		hexPart := b[start+len(fenceMarkerPrefix) : start+fenceMarkerLen-1]
		if !isLowerHex(hexPart) {
			continue
		}
		if b[start+fenceMarkerLen-1] != 0x07 {
			continue
		}
		return start + fenceMarkerLen, true
	}
	return 0, false
}

func hasPrefixAt(b []byte, at int, prefix string) bool {
	if at+len(prefix) > len(b) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if b[at+i] != prefix[i] {
			return false
		}
	}
	return true
}

func isLowerHex(b []byte) bool {
	for _, c := range b {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

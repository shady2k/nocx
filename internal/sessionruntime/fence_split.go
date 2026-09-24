package sessionruntime

// The marker split point (nocx-2v80t.3.12).
//
// A pty read hands Ingest whatever the kernel had buffered, with no boundary
// between what one shell write() produced and what the next one did. Two of
// nocx.bash's own sequences are affected by this in the same way, at the two
// ends of a command's own interval:
//
//   - The render fence: __nocx_prompt_command writes it and then, with
//     nothing to flush in between, OSC 133 D/A, OSC 7 and PS1's own visible
//     text. An ordinary pty read very often hands Ingest all of it in one
//     feed, and the emulator applies the whole feed before Ingest ever asks
//     it for a screen (Session.takeObservationScreenLocked reads whatever
//     the library holds AT THE MOMENT OF THE CALL, not at the fence's own
//     instant) — so the interval's closing screen, and the boundary window
//     built from it, ended up holding rows the NEXT command's own prompt had
//     already drawn. Measured: observation_test.go's
//     TestAFenceBatchedWithTrailingPromptBytesClosesOnTheFencesOwnScreen fed
//     a fence immediately followed by 133;D, 133;A, OSC 7 and a visible PS1
//     line in one Ingest call; before this split existed the closing
//     screen's last row read the PS1 text instead of blank.
//   - The output-start mark: __nocx_preexec writes OSC 133 C and control
//     then returns to bash, which forks the command itself — a SEPARATE
//     process whose first output can arrive in the very same pty read as
//     the shell's own C, with nothing to flush in between either. Reading
//     the screen only after the whole feed applied would make the boundary
//     this mark exists to give sessionruntime (fence_split.go's sibling,
//     observation.go's sightOutputMarkLocked) include output the command
//     had already started printing.
//
// The fix for both is the same and mechanical, never a second authority:
// Ingest locates a marker's own end HERE, before it ever reaches the
// emulator, and feeds up to it in its own call — so the screen Session reads
// immediately afterward is the screen exactly as that marker left it, with
// nothing that followed in the SAME feed yet applied. The emulator's own
// scanner (internal/emulator/ghostty's scanMarkers) is unchanged and
// unaffected: it is still the only thing that can ever produce an
// [emulator.EffectFence] or [emulator.EffectOutputMark], it still sees every
// byte in the same order, and it re-validates each marker on its own account
// regardless of where Ingest happened to split the feed — feeding the same
// bytes in two calls instead of one is exactly the case every ordinary pty
// read already forces it to handle. A wrong guess here (a look-alike
// sequence that is not actually a marker) costs nothing but one harmless
// extra split; it is never treated as authority on its own (ADR-0024
// decision 1 — a sighted marker only locates an already authenticated
// event, and the output mark locates nothing authenticated at all beyond
// the interval it already sits inside).
//
// Both wire shapes are fixed and already the contract sessionruntime's own
// consumers depend on: the fence is ESC ] 1 3 3 7 ; N O C X _ F E N C E ;
// then exactly 64 lowercase hex characters, then BEL (fenceNonceOf,
// runtime.go); the output mark is ESC ] 1 3 3 ; C BEL, with no payload
// (__nocx_marker's bare form in nocx.bash).

const (
	fenceMarkerPrefix = "\x1b]1337;NOCX_FENCE;"
	fenceNonceHexLen  = 64
	fenceMarkerLen    = len(fenceMarkerPrefix) + fenceNonceHexLen + 1 // + BEL

	outputMarkerFixed = "\x1b]133;C\x07"
)

// nextMarkerSplit answers the index within b one past the end of whichever
// of the two markers this package cares about (the fence or the output
// mark) completes FIRST, and whether either was found at all. b may hold
// neither, one of either kind, several, or the leading fragment of one whose
// remainder has not arrived yet — every one of those is ordinary, and
// Ingest's loop calls this again on whatever remains after each split.
func nextMarkerSplit(b []byte) (end int, ok bool) {
	fenceEnd, fenceOK := nextFenceSplit(b)
	markEnd, markOK := nextOutputMarkSplit(b)
	switch {
	case fenceOK && markOK:
		if markEnd < fenceEnd {
			return markEnd, true
		}
		return fenceEnd, true
	case fenceOK:
		return fenceEnd, true
	case markOK:
		return markEnd, true
	default:
		return 0, false
	}
}

// nextOutputMarkSplit answers the index within b one past the first COMPLETE
// output mark, and whether one was found. The mark has no variable payload,
// so — unlike the fence — a prefix match with too few bytes left in b is
// simply not a match yet; the loop's own bound excludes it without needing
// a separate early return.
func nextOutputMarkSplit(b []byte) (end int, ok bool) {
	for start := 0; start+len(outputMarkerFixed) <= len(b); start++ {
		if hasPrefixAt(b, start, outputMarkerFixed) {
			return start + len(outputMarkerFixed), true
		}
	}
	return 0, false
}

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

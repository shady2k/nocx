package hostsvc

import "os"

// kindName spells what a non-regular object IS, for `not-regular`'s details.
// filesystem.KindOf answers the wire's coarse vocabulary (regular, dir,
// symlink, other) because that is what a row links by; a refusal that said
// only "other" would leave a person without the one fact they need — that the
// path is a pipe, a device or a socket — so this is deliberately a second,
// finer spelling rather than a change to the first.
func kindName(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&os.ModeNamedPipe != 0:
		return "fifo"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		return "device" // character or block; both are things to not read this way
	case m&os.ModeSymlink != 0:
		return "symlink" // unreachable after a stat, kept so nothing falls through silently
	default:
		return "other"
	}
}

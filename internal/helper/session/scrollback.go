package session

// DefaultScrollbackLines is the budget a session carries when its spawn
// named none: a coordinator that carries no terminal.scrollbackLines setting
// at all (nocx-zg3k3.10.1). It matches the default declared beside
// terminal.scrollbackLines in internal/settings, and the two move together
// the way DefaultRowBufferBytes and history.helperBufferMB do — this
// package does not import settings, so the constant is the seam between
// them, and a change to one is a change to the other.
const DefaultScrollbackLines uint64 = 10000

// scrollbackOf resolves the budget a spawn carries into the number the
// session's terminal is configured with: the value its coordinator named,
// or the default when it named none. A named ZERO is a value and is kept —
// a session that keeps no history is a session the person asked for.
func scrollbackOf(lines *uint64) uint64 {
	if lines == nil {
		return DefaultScrollbackLines
	}
	return *lines
}

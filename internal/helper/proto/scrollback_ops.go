package proto

// OpSetScrollback sets one running session's scrollback budget: how far the
// session's live terminal scrolls back, in physical lines. It is how the
// person's terminal.scrollbackLines reaches a session that is ALREADY
// running — a spawn carries the budget the session was born with
// (SpawnParams.ScrollbackLines), and this op carries every change after
// that, so the budget a running pane keeps is the one on the screen now.
//
// Zero is a VALUE, not the absence of the limit: a session set to zero
// erases what it has retained and keeps nothing further — the one setting
// value that destroys scrollback outright, which the setting's own screen
// names before it is saved. The helper applies the number beside an
// internal memory ceiling of its own, so a value past the declared range
// costs memory up to the ceiling and nothing more.
//
// A generation that does not know the op answers unknown_op, which a
// coordinator reads as "this machine's helper is older than this app" and
// tolerates: the pane keeps the budget it was spawned with until the helper
// catches up.
const OpSetScrollback = "set-scrollback"

// SetScrollbackParams names the session and the budget in physical lines.
type SetScrollbackParams struct {
	Session HostSessionID `json:"session"`
	// MaxLines is the budget in physical lines. Zero keeps no history at
	// all.
	MaxLines uint64 `json:"maxLines"`
}

// SetScrollbackResult is empty: the op either applied or refused.
type SetScrollbackResult struct{}

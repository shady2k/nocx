package emulator

// ScrollbackBudget is the port's retention-configuration path: how a caller
// sets how far a terminal scrolls back and answers for what the change does
// to the history already retained.
//
// It is a capability beside [Terminal] rather than a method on it, and that
// is deliberate: every terminal a session runtime directs can be read and
// fed, but only the adapter that owns the library's retention can carry a
// budget at all — a caller that needs the capability asserts it at its one
// wiring site (the way internal/helper's spawn reaches localPTY's extras by
// type assertion), and a test double stays honest by simply not claiming it.
//
// maxLines is the budget in physical lines. Zero is a VALUE here, not the
// absence of one: it keeps no history at all, erasing what was retained —
// the one setting value that destroys scrollback outright. The library's
// page-granular pruning means the retained count lands within about one page
// of the ask rather than on it; a page is a few hundred rows at the widths
// this product ships, and the implementation states its own measured bound.
//
// Applying a budget takes effect at once: a lower limit prunes what is
// retained immediately, and rows the prune takes were already reported as
// departed when they left the screen, so the application never surfaces as a
// loss to [Terminal.DepartedRows].
type ScrollbackBudget interface {
	ApplyScrollback(maxLines uint64) error
}

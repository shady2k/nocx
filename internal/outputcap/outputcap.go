// Package outputcap holds the one number that bounds what ONE command's
// output is worth: the per-command output cap the History settings expose
// (settings.HistoryOutputCapKB, named to the code as
// content.DefaultOutputCapBytes) and — the owner's decision on
// nocx-2v80t.2.3 — the scrollback budget the terminal emulator allocates
// with. A capture can never need more than history is willing to keep, so
// one number governs both, and a second spelling of it anywhere would be a
// budget the two ends could disagree about.
//
// The package is a leaf on purpose: the emulator's cgo adapter has to read
// the number without importing the content store's SQLite stack, and the
// content store has no reason to import the emulator. Each side keeps its
// own NAME for the value — content.DefaultOutputCapBytes remains the
// policy-facing spelling — and both names are aliases of the one constant
// defined here.
package outputcap

// PerCommandBytes is what one command's output may be worth: 256 KiB of
// head and tail together, the default of settings.HistoryOutputCapKB
// expressed in the unit the code works in.
const PerCommandBytes = 256 << 10

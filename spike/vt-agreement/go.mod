// Separate module ON PURPOSE (nocx-szb40.1).
//
// This is a measurement harness, not product code. Three candidate VT
// libraries have to be built and run to be compared, and exactly one of
// them — at most — should ever enter the product's go.mod. A nested module
// keeps all three out of `go list ./...`, out of the deadcode ratchet, and
// out of the dependency graph anything ships from.
module nocx/spike/vt-agreement

go 1.24.2

require (
	github.com/charmbracelet/x/vt v0.0.0-20260823001701-96af6d2cb5f6
	github.com/creack/pty v1.1.24
	github.com/hinshun/vt10x v0.0.0-20220301184237-5011da428d02
	github.com/tonistiigi/vt100 v0.0.0-20240514184818-90bafcd6abab
)

require (
	github.com/charmbracelet/colorprofile v0.4.2 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260303162955-0b88c25f3fff // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

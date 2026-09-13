// Package ghostty implements [emulator.Terminal] over libghostty-vt — the C ABI
// of the Ghostty terminal emulator, github.com/ghostty-org/ghostty,
// include/ghostty/vt.h.
//
// It is the only package in the tree that names an upstream type, and ADR-0065
// point 3 is the rule it keeps: upstream values are converted here, by name,
// into nocx's own types; borrowed memory is copied before it escapes; terminal
// access is serialised; and nothing upstream — no enum value, no struct layout,
// no handle — reaches the wire protocol or a durable document. The port itself
// (internal/emulator) cannot even reach this package: it imports nothing, and
// surface_test.go reads its source to keep it that way.
//
// # Why CGo, and what that costs
//
// libghostty-vt is a Zig library with a C ABI, so this adapter is CGo and the
// packages that import it lose CGO_ENABLED=0. That is the price ADR-0065 named
// and accepted on its measured gate: a wasm artifact under wazero would keep
// the pure-Go property at ~4.3 MB RSS per session against ~45 KB native and
// 2.0×–2.5× the time, and it costs Kitty graphics. Nothing in this package
// needs a display, a terminal or root.
//
// # The pinned build, and where the archive lives
//
// The CGo directives in terminal.go point at .vendor/ghostty/zig-out, which is
// GITIGNORED and is reproduced by:
//
//	cd internal/emulator/ghostty/.vendor
//	git init ghostty
//	git -C ghostty remote add origin https://github.com/ghostty-org/ghostty.git
//	git -C ghostty fetch --depth 1 origin e2e53f861482e080bf45054ba49ef471f9849937
//	git -C ghostty checkout -q --detach FETCH_HEAD
//	cd ghostty
//	nix shell nixpkgs#zig -c zig build -Demit-lib-vt=true -Doptimize=ReleaseFast
//
// That is ADR-0065 point 1's pin — source commit, toolchain and flags together:
// ghostty e2e53f861482e080bf45054ba49ef471f9849937 (2026-09-11), Zig 0.16.0,
// which is the minimum_zig_version in that commit's build.zig.zon, and
// ReleaseFast. The archive it produces is
// .vendor/ghostty/zig-out/lib/libghostty-vt.a with its headers beside it at
// .vendor/ghostty/zig-out/include.
//
// THE ARCHIVE IS A LOCAL STOPGAP, NOT THE DECISION. How nocx holds it in CI, in
// the e2e stand and in a release — vendored bytes with a content hash, a
// controlled mirror, a per-target build in the pipeline — is a separate bead's
// decision and is deliberately not made here. The build bead replaces the two
// #cgo lines above and the directory they point at; nothing else in this
// package depends on where the archive came from.
//
// Two facts about the link that the build bead will need, both measured in
// .internal/spikes/buildmatrix/README.md: on Linux the static property is kept
// only by the musl triple, since a glibc archive links dynamically; and macOS
// arms never had it, because Go's own darwin runtime loads libSystem and
// libresolv whatever the helper does. This checkout builds for its own host,
// which is what the verification commands for this bead run on.
//
// # What answers a program, and what does not
//
// The port carries the program's own replies; which queries produce one is
// decided by which callbacks are installed, and every one of them is a claim
// about what nocx's terminal IS. Measured on this binding, and none of it is
// inferred:
//
//   - ANSWERED. DSR (`CSI 5n`, `CSI 6n`), the DEC-private DECRQM form
//     (`CSI ? 6 $p` → `\x1b[?6;2$y`), the Kitty keyboard query (`CSI ? u`),
//     device attributes (`CSI c` → `\x1b[?62;22c`, `CSI > c` → `\x1b[>1;0;0c`),
//     and every size query — XTWINOPS `CSI 14/16/18 t` and mode 2048's in-band
//     reports — from the geometry the terminal actually holds. DA's identity is
//     the one ghostty answers itself (see bridge.c); DA is silent without its
//     callback, which is why it is installed.
//
//   - NOT ANSWERED, deliberately. The ANSI DECRQM form (`CSI 4 $p`) is the
//     debt ADR-0065 records and TestDECRQMOnInsertModeIsUnanswered carries. ENQ
//     (0x05) and the colour-scheme query (`CSI ? 996 n`) answer nothing: the
//     first has no response nocx has decided on, the second needs a theme the
//     terminal does not own. A clipboard READ (`OSC 52` with a `?` payload) is
//     in the same list for a different reason: nocx's clipboard is the
//     runtime's to mediate (design §6.2), so no read callback is installed and
//     a program that asks hears nothing — measured, not assumed.
//
//   - REPORTED BUT NOT ANSWERED. BEL, `OSC 0/2` (title), `OSC 7` (cwd),
//     `OSC 52` clipboard WRITES and `OSC 9`/`OSC 777` (notifications) are
//     installed callbacks, and what they produce crosses the port as
//     [emulator.Effect] values rather than as bytes written back: a program
//     that sets a title is owed nothing and hears nothing. `OSC 9;4` — the
//     progress report — is deliberately NOT installed with them, because it is
//     a hint about a command rather than something the program asked the
//     terminal to do, and a runtime that wants it must ask upstream itself.
//
//   - ANSWERED BY UPSTREAM, NOT BY NOCX. XTVERSION (`CSI > q`) is answered
//     `\x1bP>|libghostty\x1b\\` — the library's own default, because no
//     XTVERSION callback is installed and nocx has not decided what its
//     terminal calls itself. It is a version string rather than an enum value
//     or a memory layout, so ADR-0065 point 3 does not forbid it, but it is the
//     one place a program can observe upstream's identity through this port and
//     it wants a decision when the runtime is wired.
package ghostty

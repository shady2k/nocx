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
// The CGo directives in terminal.go point at build/libghostty-vt/vendor, the
// layout `make vt-archives` materialises from the release
// third_party/libghostty-vt/MANIFEST.json pins:
//
//	build/libghostty-vt/vendor/<target>/libghostty-vt.a
//	build/libghostty-vt/vendor/<target>/include/ghostty/vt.h
//
// It is gitignored build output, so nothing here links until `make vt-archives`
// has verified those bytes against the manifest and put them there. NOTHING
// BUILDS THE ARCHIVE LOCALLY ANY MORE: these directives used to name a
// hand-built checkout of upstream source inside this package, which made the
// linked bytes depend on whoever last ran that build, and a ghostty source
// checkout inside the repository breaks the root eslint run because ghostty's
// own tree contains JavaScript. The pin — source commit, toolchain and flags
// together: ghostty e2e53f861482e080bf45054ba49ef471f9849937, Zig 0.16.0 and
// ReleaseFast — lives in the manifest now, and the bytes that reach the linker
// are the ones published against it.
//
// TWO ARCHIVES FOR EACH LINUX TARGET, chosen by the `vtmusl` build tag. An
// archive's libc is baked into its objects, so which of the pair a build links
// is a statement about the host the binary is for and cannot be inferred from
// the compiler: the shipped helper runs on a machine nobody knows and is
// cross-compiled with the pinned Zig's musl triple by `make helpers`, while
// every ordinary build here — go test, golangci-lint, CI's Linux jobs — is the
// host's own glibc compiler. So glibc is the DEFAULT and `vtmusl` is what the
// helper build passes: the many untagged builds stay on the archive their
// toolchain was configured for, and the one build whose target is not its host
// says so where it is made. Measured on this tree, 2026-09-13: untagged native
// links vendor/linux-amd64-gnu and `make helpers` links vendor/linux-amd64.
// Both directions happen to LINK today, so what the constraint settles is
// which pinned bytes each target is built from — not whether a link succeeds.
//
// On macOS there is one archive per architecture and no static property to
// keep, because Go's own darwin runtime loads libSystem and libresolv whatever
// the helper does.
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

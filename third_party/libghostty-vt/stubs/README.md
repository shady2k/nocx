# The macOS cross-link stubs, and why two empty files are committed here.

These two `.tbd` files declare a library's install name and an **empty** symbol
list. They exist for one reason: a Linux host has no macOS SDK, and Go's own
darwin runtime puts `-lresolv` on every darwin link line
(`internal/syscall/unix/net_darwin.go`, an untagged darwin file in a package
every darwin binary imports) plus `-framework CoreFoundation` on arm64
(`runtime/cgo/cgo.go`). Neither `-tags osusergo,netgo` nor any other build tag
removes those flags — measured, twice, in
`.internal/spikes/buildmatrix/README.md` §2.1 — and Zig's bundled
`libSystem.tbd` re-exports neither library.

A `.tbd` is a promise made to the **linker**, and it is enough: nothing in a Go
darwin binary references a `res_9_*` or a CoreFoundation symbol (the two
CoreFoundation includes in `runtime/cgo/gcc_darwin_arm64.c` are behind
`#if TARGET_OS_IPHONE`, which is false on macOS), so an empty export list
satisfies the lookup and the resulting Mach-O declares
`LC_LOAD_DYLIB /usr/lib/libresolv.9.dylib` — exactly what a native build
declares anyway. The owner ran this on macOS: a stub-linked binary launches
under dyld, loads the same dylibs as the native link, survives ad-hoc signing,
and `lipo` joins both slices into a universal binary that runs
(`.internal/spikes/buildmatrix/mac-check.sh`, C4–C7).

The honest limit, recorded so nobody reads more into it: an empty stub cannot
silently absorb a FUTURE CoreFoundation reference. A later Go or binding change
that did reference CF on macOS would fail at link with undefined symbols, which
is the failure we want.

`vtfetch cc --target darwin/arm64` points `-L` and `-F` at this directory for a
darwin build; `make helpers` and the link probe both go through it.

# linkprobe — does the pinned archive actually link?

A nested Go module (its own `go.mod`, so the repository's `go build ./...`,
`go vet ./...`, `golangci-lint ./...` and `deadcode ./...` never see it) whose
only job is to be a genuine caller of libghostty-vt for one build target at a
time.

It exists because the acceptance of the pin asks for something a file listing
cannot answer. In this worktree **nothing in the product imports the emulator
yet** — the CGo adapter is `internal/emulator/ghostty` and lands in
`nocx-ygxjv.2` — so `make helpers` today links no ghostty code at all, and a
claim about the helper's linkage would be a claim about a build with no CGo in
it. What can be settled today is whether the _published bytes_ are linkable, and
that is what this program settles:

- it links against the archive for its target, with that target's C compiler;
- on Linux it comes out with no `PT_INTERP` and no `DT_NEEDED` when the musl
  archive is used, and dynamic when the glibc one is — the difference the whole
  two-archive split exists for;
- the archive is really _in_ the artifact: a ghostty symbol is defined in the
  built file, which an empty stub could not fake;
- and on Linux it runs: create a terminal, read its geometry back, resize it,
  write to it, free it, and print what happened.

`scripts/verify-link.sh` drives all six targets and asserts exactly those four
things. Each `link_*.go` file carries the `#cgo` lines for one target and the
build tags that select it — the same `${SRCDIR}`-relative contract the
production link files will name, spelled once here so a change to
`build/libghostty-vt/vendor/<target>/` breaks this before it breaks the product.

This is not a binding and must not become one: no lifetime management, no
error taxonomy, no callbacks beyond what the four calls above need.

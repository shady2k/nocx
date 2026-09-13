# The macOS cross-link stubs: what each one promises the linker, and how far.

Three `.tbd` files live here, because a Linux host has no macOS SDK and Go's own
darwin link lines name libraries and frameworks that Zig's bundled `libSystem.tbd`
does not carry. A `.tbd` is a promise made to the **linker** — "this library
exports these names" — and the only question worth recording about each one is
whether the promise is true, and what happens the day it stops being true.

| file                                                     | library                                                                         | exports                           |
| -------------------------------------------------------- | ------------------------------------------------------------------------------- | --------------------------------- |
| `libresolv.tbd`                                          | `/usr/lib/libresolv.9.dylib`                                                    | none, and that is the whole truth |
| `frameworks/CoreFoundation.framework/CoreFoundation.tbd` | `/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation` | 16 symbols                        |
| `frameworks/Security.framework/Security.tbd`             | `/System/Library/Frameworks/Security.framework/Versions/A/Security`             | 8 symbols                         |

`vtfetch cc --target darwin/<arch>` points `-L` and `-F` at this directory for a
darwin build; `make helpers` and the link probe both go through it.

## Why these three, and where each flag comes from

Neither `-tags osusergo,netgo` nor any other build tag removes any of them —
measured, twice, in `.internal/spikes/buildmatrix/README.md` §2.1. Three sites,
one per name, and only the first is on every darwin link line:

| flag                        | site                                                                                                          | on a darwin link line?             |
| --------------------------- | ------------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| `-lresolv`                  | `internal/syscall/unix/net_darwin.go:41` — an untagged darwin file in a package every darwin binary imports   | always                             |
| `-framework CoreFoundation` | `runtime/cgo/cgo.go:15` (`darwin,arm64`), and `crypto/x509/internal/macos/corefoundation.go:23-24` (`darwin`) | yes, from either                   |
| `-framework Security`       | `crypto/x509/internal/macos/security.go:18-19` (`darwin`)                                                     | when `crypto/x509` is in the build |

**The flag and the symbols travel separately, and that is the trap this directory
fell into once.** `//go:cgo_ldflag` is emitted for the _package_, so it reaches the
external linker whatever survives dead-code elimination; the
`//go:cgo_import_dynamic` symbols are only referenced if the code using them
survives. So a binary can name `-framework Security` on its link line while
containing no reference to a single Security symbol:

```
# the darwin/amd64 half of `make helpers`, before nocx-sf1 (2026-09-14)
zig cc -target x86_64-macos … -F…/stubs/frameworks … -lresolv -lpthread \
  -framework CoreFoundation -framework Security
error: unable to find framework 'Security'. searched paths:
  …/stubs/frameworks/Security.framework/Security.tbd
```

The flag alone is enough to stop the build, and it arrived for a binary whose
import chain does not mention Security anywhere:

```
$ GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go list -deps -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/nocx-helper
cmd/nocx-helper -> internal/helper/local -> internal/helper/client ->
  internal/coordinator -> internal/update -> net/http -> crypto/tls ->
  crypto/x509 -> crypto/x509/internal/macos
```

`crypto/x509/internal/macos` is `//go:build darwin` and is imported by
`crypto/x509/root_darwin.go`, which is a filename-gated darwin file with no cgo
constraint — so **any** Go darwin binary that imports `crypto/x509` carries both
framework flags into an external link, which is what `make helpers` has asked for
since nocx-ygxjv.10. It is not the local helper variant and not the ghostty
adapter: the ssh build tag has nothing to do with it. Measured by listing every
package in the darwin closure that carries a `//go:cgo_ldflag`, with and without
`-tags nocx_local_ssh` — the tag adds 53 packages and not one of them can put a
library or framework on the line:

```
$ GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go list -deps -e \
    -f '{{range .GoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' ./cmd/nocx-helper \
  | grep -v '^$' | xargs grep -l go:cgo_ldflag
internal/syscall/unix/net_darwin.go            (-lresolv, always)
crypto/x509/internal/macos/corefoundation.go   (-framework CoreFoundation)
crypto/x509/internal/macos/security.go         (-framework Security)
```

The same command with `-tags nocx_local_ssh` prints the same three files — out of
289 packages in the closure instead of 236, so the comparison is not vacuous: the
tag adds 53 packages, and not one of them carries one of these directives.

## Why `libresolv` is still empty and the two frameworks are not

An empty export list is honest exactly as long as nothing in the binary references
a symbol from that library, and the two cases now measure differently:

- **`libresolv`: nothing tested references it.** Neither the helper nor the probe
  contains a `res_9_*` name, and the resolver CGo files that would use one are
  `!darwin` (§2.1), so the empty list satisfies the lookup and the resulting
  Mach-O declares a load command for `/usr/lib/libresolv.9.dylib` — exactly what
  a native build declares anyway. That is a measurement of these two binaries, not
  a law: a package that did call the resolver would fail the link, loudly.
- **`CoreFoundation` and `Security`: Go's darwin x509 path does.** The two files
  of that package carry the ldflags _and_ 24 `//go:cgo_import_dynamic` directives
  between them, and they are live in any binary that verifies a certificate:

```
$ grep -h cgo_import_dynamic "$(go env GOROOT)"/src/crypto/x509/internal/macos/*.go
… x509_CFArrayAppendValue CFArrayAppendValue "/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"
… x509_SecTrustEvaluateWithError SecTrustEvaluateWithError "/System/Library/Frameworks/Security.framework/Versions/A/Security"
```

On that raw line the directive is the first field, the `x509_`-prefixed local name
the second, and the **third** is the library's own spelling of the symbol — which
is what an export list here must contain. So the list is reproducible rather than
transcribed:

```
$ grep -h cgo_import_dynamic "$(go env GOROOT)"/src/crypto/x509/internal/macos/*.go \
    | sed 's/.*cgo_import_dynamic //' | awk '{print $2}' | sort
… 16 CoreFoundation names, then 8 Security names …
```

16 for CoreFoundation (from `corefoundation.go`) and 8 for Security
(from `security.go`) — the package's only two Go files, each with its assembly
half beside it. Go is the only author of that list; nothing here should be edited
without re-running the command above.

## The measurement that decided it

An empty framework stub is not a small lie, it is a landmine, and this is the
probe that shows it — `cmd/x509probe` in the buildmatrix spike. Its main path
generates a self-signed certificate, parses it, and verifies it, which is the
smallest thing that keeps the platform verifier alive past dead-code
elimination; the parse is not decoration, because `Verify` returns `errNotParsed`
for an unparsed certificate _before_ it looks at the platform at all.

Built the way the helper is — `CGO_ENABLED=1 GOOS=darwin GOARCH=amd64
CC="zig cc -target x86_64-macos -L…/stubs -F…/stubs/frameworks" go build
-ldflags="-s -w -linkmode=external"` — against the **empty** stubs it fails, and
the names it fails on are the two lists above:

```
$ … with empty CoreFoundation.tbd and no Security.tbd at all
error: unable to find framework 'Security'. searched paths: …
$ … with an empty Security.tbd added (the tempting minimal fix)
error: undefined symbol: _CFArrayAppendValue
error: undefined symbol: _CFArrayCreateMutable
… 13 CoreFoundation names …
error: undefined symbol: _SecCertificateCopyData
error: undefined symbol: _SecCertificateCreateWithData
… 7 Security names …
```

Note the second run: the _pre-existing_ CoreFoundation stub failed too. Empty was
never a property of CoreFoundation — it was a property of the probes that had been
pointed at it, none of which imported `crypto/x509`.

With the exports committed here both links succeed, and what the linker produces
names the real frameworks rather than the stub. Two readings, from two different
binaries, and they say different things — the distinction is the limit below:

- **The helper**: the four libraries above, and **no Security or CoreFoundation
  symbol anywhere in it**. Read that with `go tool nm` on a build made without
  `-s` (the shipped `-s -w` leaves no symbol table to read, so an unstripped
  `-ldflags="-w -linkmode=external"` build is what this was measured on): zero
  `_Sec*`/`_CF*` names, and zero functions from `crypto/x509/internal/macos`.
  That is why an empty stub linked for as long as it did, and `vtfetch inspect`
  on the same binary prints the load commands that name both frameworks.
- **The probe** (`cmd/x509probe`, built for both darwin arches with the same
  `vtfetch cc` flags): 76 lazy binds on amd64 and 92 on arm64, of which 7 name a
  `_Sec*` symbol resolved against dylib ordinal 3 —
  `/System/Library/Frameworks/Security.framework/Versions/A/Security` — and 13 a
  `_CF*` symbol against ordinal 2, exactly as a native macOS link binds them.
  On a Mac this is `otool -l` plus `dyld_info -bind <binary>`; the numbers above
  were read on Linux with a throwaway `debug/macho` walk of `LC_DYLD_INFO_ONLY`
  (no committed tool here reads bind tables — `cmd/binaryinfo` reads load
  commands, not the lazy-bind stream), so treat them as a recorded measurement
  and re-derive them on the Mac if they matter to you.

Two details that cost a round each and are worth carrying:

- **A v4 `.tbd` lists symbols WITH the leading underscore** (`_CFDataCreate`), which
  is the spelling Zig's own bundled `libSystem.tbd` uses. Listing them without it
  parses cleanly and then resolves nothing: the link still fails with the same
  undefined symbols, one framework lookup later, and the message looks identical.
- **The names are only needed while something references them, and the reference
  must be a CALL.** The helper's darwin build references none of the 24 today
  (`go tool nm`: zero from `crypto/x509/internal/macos`), so it links either way —
  which is why this defect reached a worker rather than being caught by a build.
  And because Go emits these as **lazy** binds, a stub that lists a name the real
  framework does not export does not fail until the function is first called: a
  program that links against the stub but never reaches the verifier proves
  nothing about the promise, which is why the Mac check below drives a real
  `x509.Certificate.Verify` instead of just starting a binary.

## The honest limit, restated

The export lists are **Go's declared set**, not "what some binary happened to
reference". That has one consequence worth stating plainly: a Go upgrade that adds
a dynamic import to `crypto/x509/internal/macos` — or any new package that
references a symbol in either framework — fails the link with that symbol named,
which is the failure we want, and the fix is to add the name here rather than to
widen the list speculatively. What these stubs cannot do is absorb a symbol
_nobody exported_: the day a name is listed here that the real framework does not
export, the link succeeds and **dyld** refuses the binary instead.

That half is a Mac's to answer, and it is `c10` in
`.internal/spikes/buildmatrix/mac-check.sh`: the same probe, stub-linked, run
under dyld. Everything up to and including the link is measurable here; the load
is not.

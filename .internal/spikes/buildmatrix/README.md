# Spike: the helper's build matrix when `CGO_ENABLED=0` is gone

**Bead:** `bm1-r5t8`. **Worktree:** `/home/dev/.herdr/worktrees/nocx/w-bm1-matrix`,
branch `w/bm1-matrix`. **Machine:** NixOS, kernel 6.18.48, AMD Ryzen 5 8600G (6c/12t).
**Toolchain:** Go 1.26.7 linux/amd64, Zig 0.16.0 (nixpkgs), ghostty
`e2e53f861482e080bf45054ba49ef471f9849937` — the same commit the qualification spike
measured. `readelf` from the system binutils; `file` is not installed.

`ADR-0065` chose `libghostty-vt`, and its "Before this record may be Accepted" list asks for
two things: build, link and run the binding on **macOS arm64**, and "establish a build and
link route for the other committed runtime targets". This spike answers the second. It does
not touch the first, which remains a Mac's job (§5).

## 0. The answer in six lines

1. **6 of 6 archives built** from this Linux machine with nothing but Zig — no SDK, no Mac.
   (Four helper targets; the Linux pair was built twice, see line 6.)
2. **2 of 4 targets link by default** (both Linux). The two darwin targets fail on
   `-lresolv`, and the flag is **Go's own**, not ghostty's.
3. **`-tags osusergo,netgo` does not help**, and that is now a measured fact with a
   `file:line` reason, not a guess.
4. **4 of 4 link with two hand-written `.tbd` stubs** (twenty lines total) that satisfy the
   linker's _lookup_ for `libresolv` and `CoreFoundation`; nothing references a symbol in
   either.
5. **The static-linking property survives where it matters and was never held where it
   does not:** the Linux artifacts built on the musl triple are static (two tools agree),
   and the macOS helper built with `CGO_ENABLED=0` **today** is already dynamically linked —
   carrying `LC_LOAD_DYLIB /usr/lib/libresolv.9.dylib`, the very library that stopped the
   Linux cross-link. See §3; this reframes what "losing the zero" costs.
6. **On Linux the archive target is not a footnote.** Built the brief's literal way
   (`x86_64-linux`, glibc) the probe links and runs but is **dynamic** — `PT_INTERP` plus
   `libc.so.6`, `libpthread.so.0`, `librt.so.1`; the same code built `-musl` is 0 and 0. The
   musl triple is what preserves `Makefile:59`'s property, so the difference must be chosen
   deliberately rather than inherited from the brief's spelling.

## 1. The archives: four helper targets, six builds

```bash
git init -q .vendor/ghostty && git -C .vendor/ghostty remote add origin \
  https://github.com/ghostty-org/ghostty.git
git -C .vendor/ghostty fetch --depth 1 origin e2e53f861482e080bf45054ba49ef471f9849937
git -C .vendor/ghostty checkout -q --detach FETCH_HEAD      # HEAD verified == the pin
cd .vendor/ghostty && zig build -Demit-lib-vt=true -Dtarget=<triple> -Doptimize=ReleaseFast
cp zig-out/lib/libghostty-vt.a ../dist/<name>/
```

| helper target  | `zig -Dtarget`              | wall | archive      |
| -------------- | --------------------------- | ---- | ------------ |
| `linux/amd64`  | `x86_64-linux` (glibc ABI)  | 38 s | 18,415,038 B |
| `linux/arm64`  | `aarch64-linux` (glibc ABI) | 37 s | 18,850,642 B |
| `linux/amd64`  | `x86_64-linux-musl`         | 70 s | 18,415,038 B |
| `linux/arm64`  | `aarch64-linux-musl`        | 76 s | 18,850,642 B |
| `darwin/amd64` | `x86_64-macos`              | 62 s | 11,910,352 B |
| `darwin/arm64` | `aarch64-macos`             | 60 s | 10,981,992 B |

**6 of 6 built, and the build needed nothing beyond Zig.** Zig bundled the macOS libc
headers it needs and fetched ghostty's own Zig dependencies (`build.zig.zon`) into its
global cache — 437 MB of cache after the builds, network needed only to fill it.

Two details worth carrying:

- **Both readings of "the Linux target" were built, because the brief's `x86_64-linux` is
  not the same target as the `x86_64-linux-musl` §3 needs.** They are not equivalent and the
  difference is not cosmetic: the two archives come out the **same size and different
  bytes** (`cmp` differs at offset 48581 for amd64, 42473 for arm64 — the libc ABI is baked
  into the objects), and §2/§3 show the links they produce behave differently. The plain
  glibc targets came out _faster_ here (38/37 s) because they were the second pair built,
  with Zig's cache already holding the dependency build; on a warm cache a re-run prints
  seconds, so the numbers above are cold-cache numbers and `run.sh` will beat them.
- All six builds ran **sequentially**, because every target installs into the same
  `.vendor/ghostty/zig-out`; each archive is copied out before the next build starts.
  Parallelising needs a `--prefix`/build-root per target; that is not measured here.

## 2. The Go links

A minimal caller (`cmd/probe`) — create, write an OSC 0 title and a DSR cursor query, read
the title back, resize, free — built with `CGO_ENABLED=1` and `CC="zig cc -target <triple>"`,
`-trimpath -ldflags="-s -w"` (the Makefile's own flags, `Makefile:92`):

| target                                     | default build                                                                                                | with `-tags stublibs` |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------ | --------------------- |
| `linux/amd64` (`-musl` archive)            | **LINK** 13,879,464 B                                                                                        | n/a                   |
| `linux/arm64` (`-musl` archive)            | **LINK** 13,311,448 B                                                                                        | n/a                   |
| `linux/amd64` (`-tags gnu`, glibc archive) | **LINK** 13,311,600 B                                                                                        | n/a                   |
| `linux/arm64` (`-tags gnu`, glibc archive) | **LINK** 12,649,880 B                                                                                        | n/a                   |
| `darwin/amd64`                             | **FAIL** `unable to find dynamic system library 'resolv' using strategy 'paths_first'. searched paths: none` | **LINK** 3,125,845 B  |
| `darwin/arm64`                             | **FAIL** — same error                                                                                        | **LINK** 2,981,138 B  |

The two `gnu` rows are the brief's literal Linux targets, built with
`CC="zig cc -target {x86_64,aarch64}-linux-gnu"` and `-tags gnu`. That the `gnu` tag selects
_one_ archive and not both is not inferred from the build constraints: `tools/ccwrap` records
the argv Go actually hands the external linker, and the recorded link for the amd64 gnu build
carries exactly one `libghostty-vt.a`, at `dist/linux-amd64-gnu` — the right one. Both link, and both run here
(amd64 natively and under `qemu-x86_64 -L <glibc>`; arm64 under `qemu-aarch64 -L <aarch64
glibc>`) with the same `ok: true`. §3 is where they differ from the musl pair, and that
difference is the point.

### 2.1 Where `-lresolv` comes from, and why no build tag can remove it

`grep -rn lresolv $GOROOT/src` returns three sites, and reading each one's build constraints
says exactly which of them reaches a darwin link:

| site                                                                      | constraint                                                   | on a darwin link line? |
| ------------------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------- |
| `internal/syscall/unix/net_darwin.go:41` — `//go:cgo_ldflag "-lresolv"`   | darwin, **by file name**, no tag                             | **yes, always**        |
| `net/cgo_unix_cgo_res.go:21` — `#cgo !android,!openbsd LDFLAGS: -lresolv` | `cgo && !netgo && (linux \|\| openbsd)`                      | no                     |
| `net/cgo_unix_cgo_resn.go:21` — `#cgo !aix,… LDFLAGS: -lresolv`           | `cgo && !netgo && unix && !(darwin \|\| linux \|\| openbsd)` | no                     |

So `-tags osusergo,netgo` disables two files that were never in a darwin build, and leaves
untouched the one that is: an untagged darwin-only file in `internal/syscall/unix`, a package
every darwin binary imports. **Measured, not reasoned:** both darwin targets were rebuilt
with `-tags osusergo,netgo` and both still failed with the identical error and the identical
remaining flag pair (`-lresolv -lpthread` on amd64, `-lresolv -framework` on arm64). The
brief's first candidate is disproved, loudly.

The other two flags the darwin link carries are also Go's — `runtime/cgo/cgo.go:14`
(`darwin,!arm64`: `-lpthread`) and `:15` (`darwin,arm64`: `-framework CoreFoundation`) — and
neither is fatal:

- **`-lpthread` resolves.** `zig cc -target x86_64-macos -lpthread` exits 0: Zig maps it onto
  its bundled `libSystem.tbd`.
- **`-framework CoreFoundation` is only a name.** `runtime/cgo/gcc_darwin_arm64.c:18-20`
  wraps the two `#include <CoreFoundation/…>` lines in `#if TARGET_OS_IPHONE`, so on macOS
  no CoreFoundation symbol is referenced — which is why an _empty_ stub is sufficient and
  why the missing framework first shows up as a lookup failure, not as undefined symbols.

### 2.2 What Zig has, and what it does not

`$ZIG/lib/zig/libc/darwin/` contains exactly two files: `libSystem.tbd` and
`SDKSettings.json`. Measured consequences:

| probe                                                                            | result                                                                  |
| -------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `zig cc -target {aarch64,x86_64}-macos -lSystem / -lm / -lc / -lpthread`         | exit 0                                                                  |
| `zig cc -target {aarch64,x86_64}-macos -lresolv`                                 | `unable to find dynamic system library 'resolv' … searched paths: none` |
| `zig cc -target {aarch64,x86_64}-macos -framework CoreFoundation`                | `unable to find framework 'CoreFoundation'. searched paths: none`       |
| `zig cc -target aarch64-macos -c` a file including `<CoreFoundation/CFBundle.h>` | `fatal error: 'CoreFoundation/CFBundle.h' file not found`               |

The bundled `libSystem.tbd` re-exports `/usr/lib/system/*.dylib` — and **`libresolv` is not
in that list** — so it cannot stand in for the missing library either.

### 2.3 The stubs, and what they do and do not claim

`stubs/libresolv.tbd` and `stubs/frameworks/CoreFoundation.framework/CoreFoundation.tbd`,
ten lines each, each declaring the library's `install-name` and an **empty** symbol list.
They add a search path and nothing else: `-L${SRCDIR}/../stubs` on amd64, plus
`-F${SRCDIR}/../stubs/frameworks` on arm64. They are behind `-tags stublibs` so that the
default build still reports the real missing library — the failure is a result, so it must
stay reproducible.

The honest reading of what was proven: **the linker accepted these artifacts and produced
binaries with no unresolved ghostty, resolv or CoreFoundation symbols.** Measured on both
darwin targets by reading the symbol table of an unstripped build: undefined ghostty
symbols 0, undefined `res_9_*`/resolv symbols 0, undefined CoreFoundation symbols 0 (91–92
undefined symbols in total, all of them the usual Go and dyld runtime bindings against
libSystem). A tbd asserts "this
symbol will exist at runtime"; the resulting Mach-O therefore _declares_ a dependency on
`/usr/lib/libresolv.9.dylib` (and, on arm64, on CoreFoundation). Both exist on macOS — and
§3 shows the current macOS helper already declares the first — but only a Mac can prove the
binary launches (§5, item 1).

### 2.4 `dsymutil`, which cost a debugging round

The first successful darwin link did **not** look successful:

```
running dsymutil failed: exec: "dsymutil": executable file not found in $PATH
dsymutil -f $WORK/b001/exe/a.out -o /tmp/go-link-…/go.dwarf
```

That message appears only _after_ the external link has succeeded — Go runs `dsymutil` to
write DWARF for a Mach-O it just produced. `dsymutil` is an Xcode tool with no Linux
equivalent. It is avoidable and already avoided: with `-ldflags=-w` (and so with the
Makefile's `-s -w`) no DWARF is generated and `dsymutil` is never invoked. A reader who
stops at the first error message will conclude the cross-link failed when it had worked.

## 3. Static or not — the point of the exercise

`file` is not installed here, so `cmd/binaryinfo` reads the real program headers and load
commands through `debug/elf` (`PT_INTERP`, `DT_NEEDED`) and `debug/macho` (`LC_LOAD_DYLIB`),
and the ELF answers were cross-checked with `readelf` — two independent tools, same verdict.

| artifact                                                  | format | linkage     | what the loader must find                                                   | bytes      |
| --------------------------------------------------------- | ------ | ----------- | --------------------------------------------------------------------------- | ---------- |
| `m-linux-amd64` (probe)                                   | ELF    | **static**  | nothing                                                                     | 13,879,464 |
| `m-linux-arm64` (probe)                                   | ELF    | **static**  | nothing                                                                     | 13,311,448 |
| `g-linux-amd64-gnu` (probe, brief's literal Linux target) | ELF    | **dynamic** | `/lib64/ld-linux-x86-64.so.2`, `libc.so.6`, `libpthread.so.0`, `librt.so.1` | 13,311,600 |
| `g-linux-arm64-gnu` (probe, brief's literal Linux target) | ELF    | **dynamic** | `/lib/ld-linux-aarch64.so.1`, the same three libs                           | 12,649,880 |
| `s-darwin-amd64` (probe)                                  | Mach-O | dynamic     | `/usr/lib/libresolv.9.dylib`, `/usr/lib/libSystem.B.dylib`                  | 3,125,845  |
| `s-darwin-arm64` (probe)                                  | Mach-O | dynamic     | the same two + `CoreFoundation`                                             | 2,981,138  |
| `base-linux-amd64` — **helper today**, `CGO_ENABLED=0`    | ELF    | **static**  | nothing                                                                     | 4,386,978  |
| `base-linux-arm64` — helper today                         | ELF    | **static**  | nothing                                                                     | 4,194,466  |
| `base-darwin-amd64` — helper today                        | Mach-O | dynamic     | `/usr/lib/libSystem.B.dylib`, **`/usr/lib/libresolv.9.dylib`**              | 4,455,232  |
| `base-darwin-arm64` — helper today                        | Mach-O | dynamic     | the same two                                                                | 4,234,130  |

`readelf` agrees with `cmd/binaryinfo` on every Linux artifact it was pointed at — 0/0 on the
four static ones, and a real interpreter and three libraries on the two glibc ones:

| binary              | `readelf -l \| grep -c INTERP` | `readelf -d \| grep -c NEEDED` |
| ------------------- | ------------------------------ | ------------------------------ |
| `m-linux-amd64`     | 0                              | 0                              |
| `m-linux-arm64`     | 0                              | 0                              |
| `base-linux-amd64`  | 0                              | 0                              |
| `base-linux-arm64`  | 0                              | 0                              |
| `g-linux-amd64-gnu` | 1                              | 3                              |
| `g-linux-arm64-gnu` | 1                              | 3                              |

(GNU `readelf` cannot read a Mach-O at all, which is why the two darwin artifacts are
answered by `debug/macho` in `cmd/binaryinfo` and by nothing else.)

**The reading that matters.** `Makefile:59` says the zero is load-bearing because "a static
binary is what a helper on an unknown remote host must be — no remote glibc, no
dynamic-loader surprises". Measured, that property is:

- **kept on Linux, but only by the musl triple.** Built the brief's literal way
  (`-Dtarget=x86_64-linux`, `zig cc -target *-linux-gnu`) the probe is **dynamic**: `PT_INTERP`
  present and three `DT_NEEDED` entries (`libc.so.6`, `libpthread.so.0`, `librt.so.1`). Built
  with `-musl` the same probe is fully static, 0 and 0. Both glibc artifacts were executed
  (amd64 natively, arm64 under `qemu-aarch64 -L <aarch64 glibc>`) and both report `ok: true`. So on Linux the archive target is not a detail -
  musl is what preserves the property the Makefile's comment protects, and glibc spends it;
- **never held on macOS.** The `CGO_ENABLED=0` helper of today is already dynamic, and
  already carries `LC_LOAD_DYLIB /usr/lib/libresolv.9.dylib` — the flag Go puts on every
  darwin link line, which is exactly why the fourth and last flag on the _linker's_ side was
  the same library name. Adopting CGo adds `CoreFoundation` on arm64 to a list that already
  names two system libraries, and adds nothing that is not part of macOS.

**And the artifacts run.** The Linux probe executes and asserts its own behaviour:

```
linux/amd64  native        {"goos":"linux","goarch":"amd64","title":"bm1-buildmatrix",
                            "reply_hex":"1b5b313b3152","reply_bytes":6,"cols":120,"rows":40,"ok":true}
linux/arm64  qemu-aarch64  …identical…
```

`1b5b313b3152` is `ESC [ 1 ; 1 R` — a real cursor report produced by the real emulator and
delivered through the `write_pty` C callback, with the title read back out of the library's
own storage and the geometry changed by a resize. This is not an empty CGo stub: the
library did work. (It is also the run that found the one real bug in this spike's own
binding — cgo includes the _exporting_ file's preamble in `_cgo_export.c`, so an
`extern` declaration of the exported callback must use the non-const `uint8_t *` cgo
generates, and the callback needs a C adapter with ghostty's real
`(terminal, userdata, data, len)` signature.)

## 4. What it would cost the real Makefile

No change is committed — `Makefile` is the coordinator's and the owner's. What follows is
what the target would have to become, and what it buys and costs.

The `helpers` target today (`Makefile:88-95`) is one loop with `CGO_ENABLED=0` and no C
compiler at all. It would become, in shape:

```make
# The 2x2 matrix keeps its names; what changes is that each target now needs its
# own C compiler and its own libghostty-vt archive, so CGO_ENABLED=0 -- which
# Makefile:59 calls load-bearing -- comes off this target entirely.
#
# The two Linux archives must be the -musl ones, and so must HELPER_CC below:
# the archive's libc ABI is baked into its objects (§1), and a glibc-built
# archive or triple yields a dynamically linked helper (§3) -- the exact
# "dynamic-loader surprise" Makefile:59 exists to prevent.
VT_COMMIT  := e2e53f861482e080bf45054ba49ef471f9849937
VT_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
VT_ARCHIVE := $(VT_DIR)/libghostty-vt-$(subst /,-,$(t)).a   # built by a vt-archives target

HELPER_CC_linux_amd64  := zig cc -target x86_64-linux-musl
HELPER_CC_linux_arm64  := zig cc -target aarch64-linux-musl
HELPER_CC_darwin_amd64 := zig cc -target x86_64-macos
HELPER_CC_darwin_arm64 := zig cc -target aarch64-macos

helpers: $(VT_ARCHIVES)
	@mkdir -p $(HELPER_ARTIFACT_DIR)
	@for t in $(HELPER_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  CGO_ENABLED=1 GOOS=$$os GOARCH=$$arch \
	    CC="$(HELPER_CC_$${os}_$${arch})" \
	    $(GO) build -trimpath -ldflags="-s -w" -tags stublibs \
	      -o $(HELPER_ARTIFACT_DIR)/nocx-helper-$$os-$$arch ./cmd/nocx-helper || exit 1; \
	  gzip -9 -f $(HELPER_ARTIFACT_DIR)/nocx-helper-$$os-$$arch || exit 1; \
	done
```

- **What a builder needs installed.** A pinned Zig 0.16.0 (via nixpkgs here), the ghostty
  source at the pinned SHA, and network once for Zig's dependency fetch. On macOS the same
  target with a native `CC` and no `stublibs` tag works as it does today — an Xcode
  toolchain supplies `-lresolv`, CoreFoundation and `dsymutil`.
- **Can `make helpers` still run from any machine?** With the stubs, yes — all four targets
  were linked here with no SDK. Without them, only a Mac (or a machine holding a macOS SDK)
  can produce the two darwin artifacts.
- **What it costs in time.** Six archive builds at 37–76 s each, plus the links: roughly five
  minutes added to any build that needs artifacts (four of the six are the ones a helper
  build actually consumes). That is not a corner of the build any
  more: `Makefile:65-83` records that since ADR-0057 every runnable binary needs the helper
  artifacts, and `e2e/stand.ts` calls this target itself. Caching the archives keyed on
  `(VT_COMMIT, zig version, target)` is what keeps that affordable; without a cache, every
  `make dev`, every container run and every CI job pays it.
- **What it costs in artifact size.** Measured on a program that does nothing but construct
  a terminal, write two sequences and free it (`cmd/hello`, built with the same
  `-s -w`): **1,200,290 B without the library, 13,588,928 B with it — about +12.4 MB per
  Linux helper** (+3.56 MB gzipped: 551,052 → 4,112,127). The same program on darwin/arm64
  went 1,161,906 → 2,732,386 B, about +1.6 MB. Those helpers are embedded in the app
  (`//go:embed all:bin`, `internal/helper/deploy/artifacts/source.go:51`), so this is paid once per helper in the shipped
  binary, and it dwarfs today's 4.2–4.5 MB helpers.
  **One flag interaction is worth measuring before an artifact budget is fixed:** the same
  Linux probe is 3,912,328 B with `-ldflags=-w` and 13,879,464 B with `-ldflags="-s -w"` —
  both execute correctly with identical output, so `-s` is _adding_ ~10 MB, which no
  stripping story explains. I did not chase it, and I would not quote the +12.4 MB above as
  final until the real helper has been built both ways. (§6.)
- **What CI needs.** `make helpers` is called from the e2e stand, so the e2e container needs
  Zig plus the pinned source (or a pre-built cache), and the release path needs the same
  pin — `ADR-0065` "Holding an unstable dependency" point 1 asks for exactly that: source,
  toolchain, flags and target pinned together.
- **A Mac SDK is not an option on a Linux box.** `nix build nixpkgs#apple-sdk_15` refuses
  with "add `{ allowUnsupportedSystem = true; }`" — the Apple SDK derivations are
  darwin-only, and even forced they would need the licensed SDK payload. So on Linux the
  choice is the stubs or a Mac.

**Recommendation, in one line.** For the _release_ path, build the two darwin archives on a
Mac (ADR-0065 explicitly allows native builders, and it is what the qualification gate needs
anyway), and cross-build the two Linux archives from Linux with the musl triple measured
here; keep the stub route only for a Linux-only CI that must produce all four, and only
after a Mac has run a stub-linked artifact once (§5.1).

## 5. What only a Mac can answer

1. **Does a stub-linked darwin binary run?** It declares `LC_LOAD_DYLIB` entries for
   `/usr/lib/libresolv.9.dylib` and CoreFoundation, which exist on macOS and are what a
   native build would use — but a `.tbd` is a promise made to the _linker_, and only dyld on
   a Mac can cash it. This is the one open question the whole stub route hangs on.
2. **Native build vs stub build, compared.** Whether a real-SDK link produces a different
   binary than the stub link (extra load commands, different symbol exports, a working
   `dsymutil`-produced `.dSYM`) can only be seen by doing both, on a Mac.
3. **Running the qualification probes on macOS arm64** — create, ingest, extract cells, a
   reply callback, key encoding from terminal state, resize, destroy — is `ADR-0065`'s first
   acceptance gate and is untouched here. My run proves the _Linux_ artifacts; a Mach-O
   binary cannot be executed on this machine at all — qemu-user translated the Linux ELF for
   `arm64` (I used it for that run), and nothing here runs Mach-O.
4. **Signing and notarization.** `codesign`, the Hardened Runtime and `notarytool` are
   macOS-only, so no part of "the helper as a signed artifact" is measurable here; whether a
   given delivery path even requires it (a file pushed over ssh carries no quarantine bit, a
   downloaded one does — _inference_, untested here) is a Mac-side question.
5. **Universal (fat) binaries.** Ghostty's own `build.zig:192-207` emits its xcframework
   only when `builtin.os.tag.isDarwin()` "since xcodebuild is required", so the upstream
   universal-artifact route is closed to Linux. Two thin archives cross-build fine here
   (both darwin targets did), which means a fat helper could also be produced by `lipo` on a
   Mac — but `lipo` is a Mac tool and the combination is unverified.
6. **Real hardware behaviour.** Whether the arm64 helper runs natively on Apple silicon and
   whether the amd64 one behaves under Rosetta is not answerable from Linux, nor is the
   deployment question `ADR-0065` raised: what a remote macOS host does with the artifact
   once it arrives.
7. **`dsymutil`.** If the darwin helpers are ever wanted _with_ DWARF, that step exists only
   on a machine with Xcode; here the flag pair `-s -w` avoids needing it at all.

## 6. What I did not verify, and what I deliberately left

- **The real helper was not built with the binding** — there is no binding in the product
  yet. `cmd/probe` stands in for it: a link test and a behaviour test, not the helper.
- **`make helpers` was not run and `Makefile` was not edited**, per the brief. The four
  helper artifacts in §3's table were built by hand with the same `go build` invocation
  (`CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"`) into this spike's own `bin/`, to measure
  what today's recipe produces — never into `internal/helper/deploy/artifacts/bin`.
- **The `-s` size anomaly is unexplained.** Same program, same compiler, same flags except
  `-s`: 3,912,328 B (`-w` alone) vs 13,879,464 B (`-s -w`), both producing identical,
  correct output when run. I measured that it is reproducible and that the ghostty code is
  present in both; I did not find the mechanism, and §4's size number should be re-measured
  on the real helper before it is relied on.
- **The darwin binaries were not run.** The four cross-links were built (§2), and §5.1 is
  the boundary: nothing on this machine can execute a Mach-O.
- **No macOS SDK was obtained.** `nixpkgs#apple-sdk_15` is not buildable on x86_64-linux
  (refused as an unsupported system), and forcing it would still need a licensed payload.
- **The stub `.tbd` files declare no symbols.** That is correct for macOS today
  (`TARGET_OS_IPHONE` guard, §2.1), but it also means an empty stub cannot silently absorb a
  _future_ CoreFoundation reference: a later Go or build-mode change that referenced CF on
  macOS would fail at link with undefined symbols, which is the failure mode I would want.
- **Nothing repo-wide was run:** no `make ci`, no `go test ./...` at the root, no containers,
  no e2e, no `br`. The only gates are this module's, in the brief's own words:
  `cd .internal/spikes/buildmatrix && go vet ./... && gofumpt -l .`.
- **The archives were built sequentially** (§1); parallel `make` would need per-target
  prefixes, which is untested here.
- **Everything built is gitignored**: `.vendor/`, `dist/` (six archives, ~92 MB), `bin/`,
  `logs/`.

## 7. Reproducing

```bash
cd .internal/spikes/buildmatrix && ./run.sh
```

Fetches the pinned source (failing closed if the commit is wrong), builds the six archives,
links the probes for all four helper targets in both Linux flavours — **asserting** that the
default darwin link fails on `-lresolv` — then links the darwin pair again with
`-tags stublibs`, prints the linkage table and runs the probes it can run: native amd64, the
musl arm64 under `qemu-aarch64`, and the glibc amd64 under `qemu-x86_64 -L <glibc>`. About
fifteen minutes on this machine, five of them the six archive builds.

Three small programs carry the findings: `cmd/probe` (the caller that proves the archive is
used), `cmd/binaryinfo` (the `file` this machine does not have) and `cmd/hello`, which
isolates the library's own size cost for §4. A fourth, `tools/ccwrap`, is a ten-line CC wrapper
that records the external linker's argv — the only way to read the flags that reached the
linker, since a _successful_ link never prints its own command:

```bash
ZIG=$(nix shell nixpkgs#zig -c sh -c 'command -v zig')      # zig is not on PATH here
ZIGLOG=/tmp/argv.txt ZIG="$ZIG" CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  CC="$PWD/tools/ccwrap -target x86_64-linux-gnu" go build -tags gnu -o /tmp/p ./cmd/probe
```

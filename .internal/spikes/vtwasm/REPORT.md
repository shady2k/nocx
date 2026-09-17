# Spike: `libghostty-vt` as one wasm artifact, under wazero, with CGo gone

- **Date:** 2026-09-12
- **Bead:** `wz1-m2q7`. **Worktree:** `/home/dev/.herdr/worktrees/nocx/w-wz1-wasm`,
  branch `w/wz1-wasm`.
- **Binding documents:** `ADR-0065` (the emulator the runtime talks to), the session-runtime
  design `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md` §6.3,
  §6.11, and the `Makefile`'s `CGO_ENABLED=0` for the helper.
- **Measured against:** `.internal/spikes/emulator/REPORT.md` and its committed
  `results/ghostty.jsonl` — the same ghostty commit, built natively.
- **Machine:** `Linux vm-agents 6.18.48 #1-NixOS SMP PREEMPT_DYNAMIC … x86_64`,
  `go version go1.26.7 linux/amd64`, `github.com/tetratelabs/wazero v1.12.0`.

## 0. The answer in five lines

**It builds, it runs under wazero with `CGO_ENABLED=0`, and it carries everything the
design's cell, style, wrap, render-state, reply and key-encoding needs cross the flat ABI
— but not Kitty graphics, which upstream force-disables on freestanding targets.** On a
continuous stream the wasm build parses **2.0×–2.5× slower** than the native one in the
frozen run (**2.0×–2.8× across every run recorded here**), and an
instance-per-session costs **~4.3 MB RSS against ~45 KB for a native terminal** — about
95×, which is the number that decides it. On the two behavioural datasets that matter,
the wasm and native builds agree exactly: **62 of 62 corpus replays** produce byte-identical
final screens, and of **355** shared probe observations **306–309** are identical — the
difference is seven named observations plus the budgeted probe's host-process timings, never
a cell, a mode, a reply or an encoding (§3).

## 1. How to reproduce

```bash
cd .internal/spikes/vtwasm
./build.sh                 # pins Zig 0.16.0 + ghostty e2e53f86, builds .build/vt.wasm (~45 s)
NATIVE=1 ./build.sh        # also builds the native archive for the comparison (~51 s)
cd nativecmp && go build -o ../.build/nativecmp . && cd ..

CGO_ENABLED=0 go build ./...                       # the assertion the route exists for
CGO_ENABLED=0 go test ./...                        # the ABI contract, as tests
CGO_ENABLED=0 go run ./cmd/probes > results/probes.jsonl
CGO_ENABLED=0 go run ./cmd/compare                 # diffs that file against the native one
CGO_ENABLED=0 go run ./cmd/wasmcmp corpus > results/corpus-wasm.jsonl
.build/nativecmp corpus > results/corpus-native.jsonl
CGO_ENABLED=0 go run ./cmd/wasmcmp throughput -capture bash -reps 5 -xfeed 20
.build/nativecmp throughput -capture bash -reps 5 -xfeed 20
CGO_ENABLED=0 go run ./cmd/wasmcmp instances -n 50 -wasm .build/vt-64k.wasm
.build/nativecmp instances -n 50
```

Commands that worked, verbatim:

```
$ .build/zig/zig version
0.16.0
$ time .build/zig/zig build -Demit-lib-vt=true -Dtarget=wasm32-freestanding -Doptimize=ReleaseFast
real 0m45.056s ; exit 0
$ ls -l .vendor/ghostty/zig-out/bin .vendor/ghostty/zig-out/lib
4657097  ghostty-vt.wasm
6578060  libghostty-vt.a
$ ls -l .build/vt.wasm
4362075  .build/vt.wasm          # shim.c + the archive, 77 exports
$ time zig build -Demit-lib-vt=true -Doptimize=ReleaseFast --prefix zig-out-native
real 0m51.429s ; exit 0
$ ls -l .vendor/ghostty/zig-out-native/lib/libghostty-vt.a
18014474  libghostty-vt.a        # 18015642 in the qualification spike's REPORT.md §0
```

**The pinned commit builds for `wasm32-freestanding` unpatched, first try, in 45 seconds.**
The brief allowed that it might not; it did. `.build/` and `.vendor/` are gitignored, so the
`.wasm` and the fetched sources are not committed — `build.sh` reconciles both to their pins
and fails if the checkout is not the pinned commit.

### What the module is

`shim.c` keeps the nested sized-struct ABI inside the module and exports scalars plus
pointers into its own linear memory — **77 functions**, grouped in the file header.
Everything variable-length (input buffer, encoded keys, dirty-row lists, a hyperlink, a
reply, the title) crosses as an offset the host reads in place; nothing is marshalled.

**The module imports nothing at all** — verified by decoding it (`wazero`'s
`CompiledModule.ImportedFunctions()`, `len == 0`, `ImportedMemories() == 0`), so
instantiation needs no host functions and wazero loads it with a bare module config. The
`env.log` import the nelix spike's `shim.c` notes belongs to a different ghostty commit and
a different build; on this pin there is no import to satisfy. In `internal/vt`, any
declared `env` import is nonetheless satisfied generically from the module's own signatures
rather than a transcribed one, so a future pin that adds one does not become a load failure.

**Ghostty already ships a wasm target of its own.** `build.zig` branches on
`cpu.arch.isWasm()` and `GhosttyLibVt.initWasm` emits `zig-out/bin/ghostty-vt.wasm`: the
full C API, `rdynamic`, `export_table = true` ("so that embedders can insert callback
entries for terminal effects"), and a 128 KB preallocated stack. It is copied out as
`.build/ghostty-vt.wasm` (4.66 MB) and is **a real second route** — a host could call the C
API directly, allocating out-slots with `ghostty_wasm_alloc_opaque` and decoding struct
layouts from `ghostty_type_json()` / `src/terminal/c/types.schema.json`, the "ABI manifest"
`ADR-0065` names. This spike took the shim route because the C compiler owning the struct
ABI is much less work than a Go reimplementation of it, and because the shim's export list
is the capability checklist in executable form. **The existence of the stock module
materially lowers the build-complexity cost of the wasm route**, and it is worth knowing
before anyone re-derives a shim.

`NOCX_HAVE_KITTY` is decided by grepping the archive for the symbol rather than by reading
the header, so the shim compiles against what was actually built.

## 2. Does it carry what the design needs?

Measured, per §6.3 and §6.11, by `internal/vt/vt_test.go` — the assertions are the ABI
contract, and each one fails if the flat boundary loses a fact.

| the design needs                                  | carried across the ABI?                               | evidence                                                                                                                                                           |
| ------------------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| styled cells                                      | **yes**                                               | bold/italic/faint/blink/inverse/invisible/strikethrough/overline as a bitfield, underline style as its own value (`\x1b[4:2m` reads back `underline = 2`)          |
| default / palette / RGB kept apart                | **yes**                                               | `ColorNone` / `ColorPalette` / `ColorRGB` with the value; a palette `1` and an RGB `0x010203` in the same row stay distinct kinds                                  |
| grapheme + authoritative width                    | **yes**                                               | full cluster as UTF-8 (`👨‍👩‍👧‍👦` as one cell), width class narrow/wide/spacer-tail/spacer-head                                                                          |
| logical vs continuation rows                      | **yes**                                               | `wrap`/`wrap_continuation` per row, and the continuation survives a width change                                                                                   |
| the incremental render state a diff encoder needs | **yes**                                               | full → clean → partial, with the dirty row list; the whole state object crosses as one opaque handle plus scalars                                                  |
| key encoding driven from terminal state           | **yes, and this is the one ADR-0065 left unmeasured** | with the terminal in application-cursor mode, the same `Left` encodes `\x1bOD`; in legacy mode `\x1b[D`; Kitty flags give `\x1b[1;5:1D`                            |
| the program's own replies                         | **yes**                                               | the `WRITE_PTY` callback runs _inside_ the module, so a DSR is answered with no host import at all; `write_until_ground` is exposed for safe out-of-band insertion |
| **Kitty graphics payloads**                       | **NO**                                                | `vt_kitty_graphics` returns `-2`; `GHOSTTY_BUILD_INFO_KITTY_GRAPHICS` is false; `grep -c ghostty_kitty_graphics_image libghostty-vt.a` = **0**                     |
| sixel                                             | no, on both targets                                   | the native qualification spike measured the same (§2.5)                                                                                                            |

**The Kitty loss is upstream policy, not a bug in this build.** `src/terminal/build_options.zig`:

```zig
pub fn kittyGraphics(self: Options, target: std.Target) bool {
    if (target.os.tag == .freestanding) return false;   // :275
    return self.features.kitty_graphics;
}
```

— with the reason stated at `:160`: "This requires the ability to get timestamps from the
OS, so it is always disabled on freestanding targets (e.g. wasm32-freestanding) regardless
of this setting." Setting `-Dvt-features=+kitty_graphics` cannot turn it on.

That matters because `ADR-0065` counted exactly this as an advantage over `x/vt`:
"`libghostty-vt` decodes Kitty images, which is more than we have." **On the wasm route the
runtime decodes no images at all** — it does not even report the APC as an unknown sequence
(the probe's kitty case returns no reply and leaves storage empty, against the native
`\e_Gi=42;OK\e\\` and `image_42_decoded = true`). Nothing DISPLAYED is lost, since neither
candidate ever drew an image, but the extension hook `ADR-0065` describes as "upstream's or
a patch's rather than ours" becomes upstream's only, and a future image decision on wasm is
blocked at the target level.

## 3. The nine probes, against the committed native results

`results/probes.jsonl` (370 observations) vs `.internal/spikes/emulator/results/ghostty.jsonl`:

```
shared keys: 355   identical: 306–309   different: 46–49
```

**The span is the point, not sloppiness.** Seven of the differences are stable and named
below; the rest are `7_bounded_rep` wall-clock keys, which move by two or three between runs
because a timing integer occasionally lands on the same value as the committed native run
and occasionally does not. No cell, mode, reply or key encoding ever moves. Every
difference, categorised:

| count | what                                                              | verdict                                                                                                                                                                                                                                             |
| ----- | ----------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 39–42 | `7_bounded_rep` wall time, Go heap and malloc counts              | **not a library difference** — host-process numbers. The terminal state in the same cases (cursor `16,23`, `scrollback_rows = 796`) is identical in every REP case but the capped one: of the 28 shared `7_bounded_rep` behavioural keys, 27 agree. |
| 5     | `11_graphics` kitty rows + `api/graphics/kitty_graphics_compiled` | **a real capability loss**, §2                                                                                                                                                                                                                      |
| 1     | `9_c1_0x9c_in_osc/title_bare_9c/title_events`                     | vocabulary: the native driver reports the _event list_, this one reads the _current title_. Both say "no title"; `title_hex` and `screen_row0` for the same case are identical.                                                                     |
| 1     | `7_bounded_rep/rep_1000000_scrollback100/scrollback_rows`         | **a real behavioural difference**, below                                                                                                                                                                                                            |

Everything else is byte-identical: grapheme assembly across every write boundary (24/24,
7/7, 7/7, 5/5), the combining mark, all modified keys and function keys including F13, IRM,
the DECRQM non-reply, origin-relative CPR, every other REP bound including the 65535 clamp,
resize/reflow, the C1-in-OSC handling, soft-wrap, and the whole incremental render-state
sequence.

### The one behavioural difference: capped scrollback retention

Same driver shape on both sides (`scroll` mode in `cmd/wasmcmp` and `nativecmp`), same
input, 80×24:

| scrollback cap                               | wasm `scrollback_rows` | native `scrollback_rows` |
| -------------------------------------------- | ---------------------- | ------------------------ |
| 1, 10, 50, 100, 150, 200, 250, 300, 400, 500 | **123**                | **218**                  |
| 1000, 2000                                   | 796                    | 796                      |
| no cap                                       | 796                    | 796                      |

The retention is **quantised**: every cap from 1 to 500 keeps the same number of rows, so
the cap is applied in whole pages (`PageList.zig:666` sets `limits.set(.lines, …)`, and
upstream's own test at `:11190` computes expectations as `page_rows / 2`). A page holds at
least 123 rows in the 32-bit freestanding build and at least 218 in the 64-bit native one.

**I did not establish the cause, and I am not going to write down a guess as one.** The
`std.heap.page_size_min` uses I found in `PageList.zig` (`:177`, `:350`, `:753`) are
allocation _alignment_, not capacity, so the obvious "wasm pages are 64 KiB, Linux pages are
4 KiB" story does not follow without more work. What is established: the difference is
reproducible, isolated to this one configuration, present with identical driver code, and
absent everywhere else. If the wasm route is pursued, this is a named item to open upstream
— the scrollback of a capped session is not target-independent, and at a cap of 100 the two
builds disagree by 95 rows.

## 4. The two numbers that decide it

### 4.1 Throughput on a continuous stream

Measured on `bash.jsonl` re-fed 20 times inside one timed run — **1,538,460 bytes**, median
of 5 runs — so the numbers are parsing cost rather than process setup. `results/throughput.txt`.

| chunk                    | wasm MB/s | wasm ns/call | native MB/s | native ns/call | native/wasm |
| ------------------------ | --------- | ------------ | ----------- | -------------- | ----------- |
| 64 B                     | 166       | 386          | 420         | 152            | 2.53×       |
| 256 B                    | 227       | 1128         | 444         | 576            | 1.96×       |
| 1 KiB                    | 263       | 3848         | 591         | 1712           | 2.25×       |
| 4 KiB                    | 274       | 14784        | 598         | 6770           | 2.18×       |
| 16 KiB                   | 277       | 55630        | 582         | 26440          | 2.10×       |
| 64 KiB                   | 276       | 139379       | 618         | 62230          | 2.24×       |
| 1 MiB                    | 278       | 276453       | 606         | 126976         | 2.18×       |
| whole capture (76,923 B) | 275       | 279523       | 540         | 142434         | 1.96×       |

**2.0×–2.5× slower in this run** — 1.96× at 256 B and at the whole-capture end, 2.53× at
64 B — and **the wasm boundary itself costs 219–234 ns per write** (386 − 152 at 64 B in the
first run, 371 − 152 in the second). For a PTY whose reads are typically 4–8 KiB, that is the
whole story: ~275 MB/s against ~600 MB/s, before any of the host's own work.

`results/throughput.txt` carries the same command run a second time immediately afterwards,
for the spread rather than for a second claim: 172 MB/s at 64 B (371 ns/call) and 278 MB/s
at 64 KiB, i.e. within 4% of the numbers above. One run during this session came back ~20%
slower across every chunk size while the machine sat at load average 0.16; three repeats
returned to the figures above, so it is recorded as an outlier of the measurement, not of
the target. **Across every run taken here the ratio stayed between 2.0× and 2.8×, widest at
the smallest chunks where the boundary cost dominates** — the ratio is the robust claim; the
absolute MB/s is indicative.

**A first measurement said the opposite, and it is worth recording why.** With `xfeed=1`
(77 KB total, ~0.3 ms per run the wasm build appeared _faster_: 272 vs 180 MB/s. At that
size the measurement is dominated by per-run setup and by the native run's first-touch of
freshly allocated pages, not by parsing. The continuous-stream measurement is the one the
brief asked for and the one that is true: the single-shot number was not just imprecise, it
had the sign of the effect backwards.

For reference, the qualification spike measured ~43 ms to feed a 1.8 MB alt-screen capture
from Python under wasmtime (~42 MB/s, `REPORT.md` §/README). This run is ~6× faster than
that on a different corpus and a different host language, so the two are not comparable —
the Python/wasmtime boundary, not the wasm target, dominated that number.

### 4.2 Memory per instance

One instance per session, 120×40, 10,000-line scrollback cap, 2 KB of content, render state
open. `results/instances.txt`; `rss` from `/proc/self/statm`.

|                             | wasm, 64 KiB shim buffers | wasm, 1 MiB shim buffers | native                             |
| --------------------------- | ------------------------- | ------------------------ | ---------------------------------- |
| linear memory, one instance | 2,293,760 B               | 4,259,840 B              | —                                  |
| RSS, n = 10                 | 3.80 MB/instance          | 4.64 MB/instance         | 7.96 MB for the whole process      |
| RSS, n = 50                 | 4.29 MB/instance          | 5.31 MB/instance         | 9.93 MB for the whole process      |
| 50 sessions, total          | **214 MB**                | **266 MB**               | **2.2 MB** over the n = 1 baseline |

The native rows are absolute process RSS, and they start at n = 1, not n = 0: 7.72 MB with
one terminal, 7.96 MB with ten, 9.93 MB with fifty. The marginal cost is therefore the
n = 1 → n = 50 difference — 2,207,744 B across 49 extra terminals, **~45 KB per terminal**.

The wasm route with one instance per session is **~4.3 MB per session, roughly 95×**, and
no n = 1 row is reported for it: the RSS delta of a single instance is noise (it came back
negative at 64 KiB) because the Go runtime's own heap moves more than the 2 MB being
measured. n = 10 and n = 50 are the honest samples.

Where an instance's memory goes (64 KiB buffers, measured by successive operations):

```
after_instantiate            1,441,792     module statics + 128 KB stack
after_new(120x40)            2,293,760     the terminal itself   (+0.85 MB)
after 40k lines of output    2,752,512     scrollback            (+0.46 MB)
after RSOpen + RSUpdate      3,145,728     render state          (+0.39 MB)
```

**Almost half of the 1 MiB-buffer instance is this shim's own choice**: `INBUF` and `OUTBUF`
are 1 MiB each, so 1.97 MB of every such instance is scratch that exists to let one `Write`
carry any single capture. They are build-time knobs (`INBUF_KB`/`OUTBUF_KB`) precisely because
they are per-session cost, and `vt_write_n` now **refuses** an oversized write instead of
truncating it, so a small buffer is safe rather than silent.

**The estimate that is not a measurement, and is labelled as one:** if the shim held a table
of terminals in ONE instance instead of one terminal per instance, the marginal cost would
be the measured ~0.85 MB per terminal plus one 1.4 MB floor, i.e. roughly 44 MB for 50
sessions rather than 220 MB — still ~20× native, but a different order of magnitude. I did
not build or measure that shim; it is the obvious next experiment if the wasm route is
pursued for any reason other than per-session isolation.

## 5. The corpus, whole and partitioned

All seven committed captures, replayed through both builds with identical partitioning
(whole, the recorder's own write boundaries, splits into 2/3/5/8/16/32, and one byte per
write for captures ≤ 64 KiB), comparing every cell's grapheme and width class per row:

```
captures replayed: 62 shapes
identical final screens: 62/62
differing: none
```

(`results/corpus-compare.txt`; the full grids are 1.2 MB per driver and are gitignored.)

**On real terminal traffic the wasm target changes nothing.** Every divergence this spike
found is in a synthetic probe, not in a recorded session.

## 6. What this costs the native route, and what it would cost to ship

**Build complexity.** One extra pinned toolchain (Zig 0.16.0, downloaded, no root) and one
`.wasm` artifact, in exchange for: no CGo, no static archive per platform, no SDK, one
artifact for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and
`CGO_ENABLED=0` surviving for the whole helper. `build.sh` is 146 lines and the shim is 703.
That is materially less than `ADR-0065`'s "What this costs" assumed for the CGo route — and
the pinned-commit build worked first time, which the record explicitly listed as an unknown.

**What it does not remove.** Zig stays build-time-only, so a release needs a build machine
with the pinned toolchain and network, and the artifact needs the same "pin source, toolchain
and configuration together" discipline `ADR-0065` already requires (`Holding an unstable
dependency`, point 1). A wasm artifact is not a smaller thing to pin than an archive; it is a
different one.

**Throughput.** 2.1–2.8× slower on the ingest path, ~275 ns of boundary cost per write.

**Memory.** ~95× per session as measured, and the fix for that is a shim that holds many
terminals per instance — more of "we own a terminal adapter" than the design currently
assumes.

## 7. Would I ship it?

**No — not as the runtime's emulator, and not for the reason the brief expected.** The
capability question came back nearly clean: styled cells, colour kept as default/palette/RGB,
per-line wrap continuation, the incremental render state, the program's replies, and key
encoding _driven from terminal state_ all cross the flat ABI, and that last one is the
integration gate `ADR-0065` named as unmeasured — it works, and it is now a test. What kills
it is the combination of two measured numbers against one lost capability: an instance per
session costs ~4.3 MB where a native terminal costs ~45 KB, which for a terminal multiplexer
is the resource that runs out first, and the ingest path is 2–3× slower for a job that sits
directly beside the PTY. On top of that, the freestanding target forces Kitty graphics off
upstream, so the route does not merely cost more — it carries less than the thing it
replaces, and `ADR-0065` had counted that capability as a reason for choosing the library.

The honest counterweight is that the _reason_ the wasm route was attractive — `CGO_ENABLED=0`
for four targets from any machine — is a real problem with a cheaper solution than changing
the emulator's execution model: `ADR-0065` already allows native builders, and a build
matrix that produces a native archive per target buys back both the throughput and the
memory without giving up the capability. I would spend the next hour on that matrix rather
than on a multi-terminal shim. If a future decision does need in-process isolation (untrusted
programs, one bad session unable to corrupt another), then the wasm route's isolation story
is real and these numbers are the price; that trade is not the one on the table today.

## 8. What I did not verify, and what I deliberately left

- **The Kitty conclusion rests on a build-time gate plus a symbol scan plus two runtime
  queries**, not on an attempt to change the gate. `-Dvt-features=+kitty_graphics` cannot
  override `kittyGraphics()`, and I did not patch upstream to prove that forcing it would
  fail to compile on freestanding — the header's stated reason (OS timestamps) makes a
  working patch unlikely, but it is inference.
- **The scrollback divergence has no cause**, only a location (§3). It is the single
  behavioural difference and it is reproducible in four commands.
- **The stock `ghostty-vt.wasm` was built and inspected but not driven.** Its exports,
  `rdynamic`, `export_table` and the `ghostty_wasm_alloc*` helpers were read from
  `build.zig`, `src/build/GhosttyLibVt.zig` and `include/ghostty/vt/wasm.h`; no probe ran
  through it. A host-side route with callbacks in the indirect function table is a claim
  from those sources, not a measurement.
- **One process, one machine, x86_64.** Every number above is from this box; nothing was
  measured on macOS arm64, which is where `ADR-0065`'s acceptance gate lives. The wasm
  artifact is architecture-independent and the native comparison is not, so this spike says
  nothing about the gate itself.
- **The native comparison driver is mine, not the qualification spike's.** It installs the
  same four effect callbacks so the comparison is symmetric, but it is a second driver; the
  probe comparison in §3 is against the spike's _committed output_, which is the stronger
  one.
- **Alternate screen entered/resized/exited as a state, and selection**, are in the corpus
  (vim, less, htop) but were only checked through final-cell equality, not as states.
- **The budgeted REP probe runs on an instance of its own.** On expiry it closes that
  instance — how wazero interrupts a call in flight — and then waits a bounded ten seconds
  for the writer; if the writer has not returned the driver stops rather than letting a
  later case share memory with a possibly-live one, and it emits `state_read: false` because
  a closed instance has no state to read. The budget never fired on this workload (the
  library clamps REP at 65535, so even 10⁹ completes in well under a millisecond), so that
  path is exercised only by construction, not by a real timeout.
- **`nativecmp/` is a nested Go module**, so `go build ./...` and `go vet ./...` in this
  module cannot see it — which is exactly what keeps `CGO_ENABLED=0 go build ./...` honest.
  Its cgo directive points at `.vendor/ghostty/zig-out-native`, which `NATIVE=1 ./build.sh`
  produces.

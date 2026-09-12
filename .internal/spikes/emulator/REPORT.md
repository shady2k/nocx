# Spike: qualifying `libghostty-vt` against `x/vt` for the session runtime's emulator

- **Date:** 2026-09-12
- **Bead:** `nocx-cu50k`. **Worktree:** `/home/dev/.herdr/worktrees/nocx/spike-emulator`,
  branch `spike-emulator`, cut from `feat/agent-orchestration`.
- **Binding documents:** `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md`
  §1 and §1.2; `ADR-0041`; `ADR-0001`.
- **Candidates measured:**
  - `github.com/charmbracelet/x/vt` at `v0.0.0-20260823001701-96af6d2cb5f6`, with
    `github.com/charmbracelet/ultraviolet v0.0.0-20260303162955-0b88c25f3fff` and
    `github.com/charmbracelet/x/ansi v0.11.7` — the versions the product pins today.
  - `libghostty-vt` from `github.com/ghostty-org/ghostty` at
    `e2e53f861482e080bf45054ba49ef471f9849937` (2026-09-11), the public C ABI in
    `include/ghostty/vt.h` and `include/ghostty/vt/*.h`.
- **Reference terminal:** headless `@xterm/headless@5.5.0` + `@xterm/addon-unicode11@0.8.0`,
  i.e. the product's own VT frontend per `ADR-0001`. Added because the two candidates
  disagree with each other on emoji column accounting, and column accounting is exactly
  what `ADR-0041` decided on. Its output is a reference, not a probe subject.

**Scope warning.** This is a measurement of nine probes and five API questions. It is not a
decision. It does not re-run `ADR-0041`'s geometry corpus, and it does not measure
attributes, colour, alternate-screen behaviour, scrollback compression, or performance on
real programs (see §6).

## 0. How to reproduce

```bash
cd .internal/spikes/emulator
./run.sh                 # vendors ghostty, builds libghostty-vt, runs both drivers
```

`run.sh` fetches the pinned ghostty commit into `.vendor/ghostty` and **fails if the
checkout is not that commit**, then runs
`nix shell nixpkgs#zig -c zig build -Demit-lib-vt=true -Doptimize=ReleaseFast` inside it,
then `go run ./cmd/xvt > results/xvt.jsonl` and
`go run ./cmd/ghosttyvt > results/ghostty.jsonl`. Verified end to end from a clean vendor
directory: 1 m 59 s, the same two artifacts by size, and byte-identical output for every
deterministic probe.

Commands that worked, verbatim:

```
$ nix shell nixpkgs#zig -c zig version
0.16.0
$ cd .vendor/ghostty && nix shell nixpkgs#zig -c zig build -Demit-lib-vt=true -Doptimize=ReleaseFast
real    1m25.057s / user 2m59.267s / sys 0m11.809s ; exit 0
$ ls -l zig-out/lib
18015642  libghostty-vt.a
 9916200  libghostty-vt.so.0.1.0
$ ls zig-out/include/ghostty
vt.h  vt/
```

Zig 0.16.0 is exactly ghostty's `build.zig.zon` `minimum_zig_version`, and nixpkgs carries
it. The build needed no other package and no network beyond the clone. The reference
probes are `reference/xtermjs-cells.js` and `reference/xtermjs-zwj-offsets.js`, run with
`npm install @xterm/headless@5.5.0 @xterm/addon-unicode11@0.8.0` beside them; their recorded
output is `results/xtermjs-cases.txt` and `results/xtermjs-zwj.txt`.

Raw driver output is `results/xvt.jsonl` and `results/ghostty.jsonl` — one JSON object per
observation, `{"probe","case","key","value"}`. Every number below is copied from those
files.

## 1. The probes

Both drivers emit the same cases and keys, so the columns are directly comparable. Cell
dumps are `"grapheme"/width` per column, `.` for a cell holding no text.

### 1.1 Grapheme split across writes

Feed the sequence as one `Write`, then split across two `Write` calls at **every** interior
byte offset; the resulting cells must be identical. Counts are
`offsets identical to the single-write result / total interior offsets`.

| sequence | bytes | `x/vt` | `libghostty-vt` | `xterm.js` (reference) |
|---|---|---|---|---|
| `👨‍👩‍👧‍👦` (ZWJ family) | 25 | **3/24**, first divergence at 4 | **24/24** | 21/24 in cell text, **24/24 in geometry** |
| `👍🏽` (skin tone) | 8 | **3/7**, first divergence at 4 | **7/7** | 7/7 |
| `🇷🇺` (flag) | 8 | **3/7**, first divergence at 4 | **7/7** | 7/7 |
| `❤️` (VS16) | 6 | **2/5**, first divergence at 3 | **5/5** | 5/5 |

Single-write result, row 0 of a 20-column terminal, and the cursor column after it:

| sequence | `x/vt` | `libghostty-vt` | `xterm.js` |
|---|---|---|---|
| ZWJ family | `"👨‍👩‍👧‍👦"/2 .` cursor **2** | `"👨‍"/2 . "👩‍"/2 . "👧‍"/2 . "👦"/2 .` cursor **8** | `"👨"/2 . "👩"/2 . "👧"/2 . "👦"/2 .` cursor **8** |
| `👍🏽` | `"👍🏽"/2` cursor **2** | `"👍"/2 . "🏽"/2 .` cursor **4** | `"👍"/2 . "🏽"/2 .` cursor **4** |
| `🇷🇺` | `"🇷🇺"/2` cursor **2** | `"🇷"/2 . "🇺"/2 .` cursor **4** | `"🇷"/1 "🇺"/1` cursor **2** |
| `❤️` | `"❤️"/2` cursor **2** | `"❤️"/1` cursor **1** | `"❤️"/1` cursor **1** |

`x/vt`'s split failure is structural, not random: the offsets that survive are exactly those
that fall **inside** one rune's UTF-8 bytes. `emulator.go:275` flushes the pending grapheme
whenever the written slice ends (`i == len(p)-1`) and `utf8.go:11` prints every ASCII byte
immediately, so any split at a cluster boundary between two complete runes flushes a partial
cluster. At offset 4 of the family — between `👨` and the ZWJ — `x/vt` produces

```
"👨"/2 . "👩‍👧‍👦"/2 . . . . . . . . . . . . . . . . .
```

against `"👨‍👩‍👧‍👦"/2` for the single write. `libghostty-vt` holds an incomplete cluster
across the write and produces byte-identical cells at all 24 offsets.

`xterm.js` is the interesting third case: its **geometry is invariant** (cursor 8, same
widths, at all 24 offsets) but in 3 of 24 offsets — 6, 13, 20, each one splitting a
multi-byte rune — the first cell's *text* loses its trailing ZWJ (`"👨"/2` instead of
`"👨‍"/2`). So the strict requirement "the resulting cells must be identical" is met by
`libghostty-vt` alone; `x/vt` fails it in geometry, `xterm.js` in cell text only.

**Do not read the ZWJ row as "ghostty agrees with the product".** Ghostty and `xterm.js`
agree on *where the columns are* for the ZWJ family and the skin tone, and agree to the
column on `❤️`; they disagree on the regional-indicator flag, where `xterm.js` gives each
indicator width 1 (2 columns total) and ghostty gives each width 2 (4 columns total). On
that row `x/vt`'s 2-column total matches `xterm.js` and its single-cell shape does not.
`ADR-0041` chose `x/vt` **because** its columns matched `xterm.js`; this spike did not
re-run that corpus, so it neither confirms nor overturns the geometry case for ghostty. It
establishes only that the two candidates now disagree with each other on emoji.

### 1.2 Combining mark on an ASCII base

`a` followed by U+0301, one write and two writes:

| | `x/vt` | `libghostty-vt` | `xterm.js` |
|---|---|---|---|
| cells | `"a"/1` `"́"/0` — **2 cells** | `"á"/1` — 1 cell | `"á"/1` — 1 cell |
| cursor | 1 | 1 | 1 |

Same result whether written as one `Write` or two. `x/vt` does not fail because of the write
boundary — it fails even in the single write, because `handlePrint` (`utf8.go:11`) prints an
ASCII base immediately and never buffers it, so the combining mark that follows cannot join
it and lands in its own width-0 cell. Both cells are ours to interpret; a card serialiser
reading cells sees the base character and the combining mark as two separate cells.

### 1.3 Modified keys

Authority: `xterm 410`'s own terminfo as shipped by ncurses 6.6.20251230
(`infocmp -1 xterm-256color`), plus xterm's documented PC-style modifier numbering
(`m = 1 + shift + 2*alt + 4*ctrl`, so Ctrl is 5 and Ctrl+Shift is 6). The live-xterm route
was attempted and abandoned — see §6.

| key event | `x/vt` `SendKey` | `libghostty-vt` legacy | terminfo / documented xterm |
|---|---|---|---|
| Ctrl-Left | **nothing (0 bytes)** | `\x1b[1;5D` | `CSI 1;5D` (`kLFT` is shift-left `\E[1;2D`; m=5 is Ctrl) |
| Shift-Up | **nothing** | `\x1b[1;2A` | `CSI 1;2A` |
| F5 | `\x1b[15~` | `\x1b[15~` | `kf5=\E[15~` ✓ |
| Shift-F5 | **nothing** | `\x1b[15;2~` | `kf17=\E[15;2~` ✓ |
| Ctrl-F5 | **nothing** | `\x1b[15;5~` | `kf29=\E[15;5~` ✓ |
| Ctrl+Shift-Left | **nothing** | `\x1b[1;6D` | `CSI 1;6D` (m=6) |
| Left | `\x1b[D` | `\x1b[D` | `kcub1=\EOD` in application cursor mode, `CSI D` otherwise |

`x/vt` emits nothing at all for a special key with any modifier: `key.go:25`'s switch has no
case for a modified arrow, and the `default` branch appends `string(key.Code)` only
`if key.Mod == 0` (`key.go:294-298`), so `seq` stays empty and nothing is written. This is
silence, not a wrong sequence, which is the worse failure for a keystroke encoder.

With the Kitty keyboard protocol enabled, ghostty additionally emits
`\x1b[1;5:1D`, `\x1b[1;2:1A`, `\x1b[1;1:1D` (event-type suffix, press = `:1`).

### 1.4 F13

| | `x/vt` | `libghostty-vt` |
|---|---|---|
| F13 | `EF BF BD` = **U+FFFD** | `\x1b[25~` |
| F13+Shift | **nothing (0 bytes)** | `\x1b[25;2~` |
| F12 | `\x1b[24~` | `\x1b[24~` |
| F1 | `\x1bOP` | `\x1bOP` |

`x/vt`'s `KeyF13` is `unicode.MaxRune + 15` (ultraviolet's special keys start at
`KeyExtended = unicode.MaxRune + 1`), which is not a valid rune, so `string(key.Code)`
produces U+FFFD.

**Ambiguity, stated rather than resolved:** terminfo cannot name F13 (`kf13` in
`xterm-256color` is **shift-F1**, `\E[1;2P`), so no named authority on this machine fixes
F13's sequence. `CSI 25~` is the conventional xterm-family mapping and is what ghostty
emits; I did not confirm it against a live terminal. This does not weaken the row: U+FFFD is
wrong under every authority, and 0 bytes is wrong under every authority.

### 1.5 Insert mode (IRM)

Setup: `abcd`, `CUP 1;1`, `CSI 4 h`, then print `XY`.

| | `x/vt` | `libghostty-vt` |
|---|---|---|
| row after printing | `XYcd` — **overwrote** | `XYabcd` — **inserted** |
| mode state | callback `EnableMode` fired with mode 4; recorded set | queryable set (`GHOSTTY_TERMINAL_DATA_MODE` on `ANSIMode(4)` → `true`) |
| `CSI 4 $p` (DECRQM) | `\x1b[4;1$y` — replies **"set"** | **no reply** |
| `CSI ? 6 $p`, `CSI ? 25 $p` | — | `\x1b[?6;2$y`, `\x1b[?25;1$y` |

`x/vt` records the mode (`csi_mode.go:71`), answers a DECRQM request saying the mode is set,
and then ignores it: `utf8.go:88` calls `SetCell`, which always overwrites. An emulator that
reports the mode set while not implementing it lies to the program, which is worse for the
authority role than not recognising the mode.

`libghostty-vt` implements IRM, and answers DECRQM for DEC private modes but not for ANSI
modes (`CSI 4 $p` produced nothing while `CSI ? 6 $p` produced `\x1b[?6;2$y`). For a runtime
that is the only emulator, the C query is the path that matters and it works; a program that
polls ANSI modes with DECRQM will not hear back.

### 1.6 Cursor report under origin mode

`DECSTBM 5;10` then `CUP 1;1`, with and without `DECOM`; then `DSR 6`.

| configuration | `x/vt` CPR | `libghostty-vt` CPR |
|---|---|---|
| DECOM off, `CUP 1;1` (absolute row 1) | `\x1b[1;1R` | `\x1b[1;1R` |
| **DECOM on**, `CUP 1;1` (lands on absolute row 5) | `\x1b[5;1R` — **absolute** | `\x1b[1;1R` — **origin-relative** |
| DECOM off, `CUP 5;1` (absolute row 5) | `\x1b[5;1R` | `\x1b[5;1R` |

The discrimination is exact: in `x/vt`, an origin-relative address under DECOM and an
absolute address without it produce the **same** report (`\x1b[5;1R`), so the report cannot
carry the distinction at all. `handlers.go:807` reads `e.scr.CursorPosition()` — the
absolute grid position — while `csi_cursor.go:74` correctly honours DECOM for *addressing*.
`libghostty-vt` answers origin-relative when DECOM is set and absolute otherwise.

Expected value: xterm's documented CPR semantics report the position relative to the origin
(the top-left of the scrolling region when origin mode is set). A live-terminal confirmation
of this row was attempted and **not obtained** (§6); the row above is the two candidates'
measured bytes, and `ADR-0041`-style agreement with `xterm.js` was not available because
headless `xterm.js` does not answer DSR.

### 1.7 Bounded work under REP

`A` then `CSI <n> b`. Both drivers, 80×24, default scrollback.

| n | `x/vt` wall | `x/vt` cumulative alloc / mallocs | `libghostty-vt` wall | ghostty alloc / mallocs |
|---|---|---|---|---|
| 100 000 | 17.0 ms | 12.1 MB / 101 259 | 0.45 ms | 439 B / 11 |
| 1 000 000 | 177.9 ms | 123.1 MB / 1 012 517 | 0.49 ms | 1 960 B / 11 |
| 10 000 000 | 1 821.8 ms | 1.22 GB / 10 125 018 | 0.39 ms | 1 832 B / 7 |
| 1 000 000 000 | **not completed in 30 s** (189 473 616 mallocs done) | 22.9 GB in 30 s | **0.44 ms** | 1 536 B / 16 |

These are the numbers committed in `results/*.jsonl`, from one run. Wall time is the only
value here that drifts, and the probes below were run five times while the drivers were being
edited: `x/vt` gave 16.7–18.5 ms at 10⁵, 172.1–179.9 ms at 10⁶ and 1701.6–1821.8 ms at 10⁷;
ghostty gave 0.36–0.49 ms. The allocation counts, the cell outcomes and every probe in
§1.1–§1.6 and §1.9 are identical across those runs, including a full `run.sh` from a clean
vendor checkout.

`x/vt` is linear with no cap: ~170–182 ns and ~1.013 allocations per repeated character, so
10⁹ repeats is ~170–182 s and ~1.01 × 10⁹ allocations. In 30 seconds it completed **18.9 %**
of the work, consistent with that rate. The allocation is ~121–123 bytes per character —
`utf8.go:44` boxes each printed character with `string(r)` before storing it in a cell.

`libghostty-vt` is flat because it **clamps the count at 65535**. The committed drivers
contain the pair that shows it: `rep_65535` and `rep_70000` leave identical state in ghostty
(cursor `16,23`, 796 scrollback rows, 0.4 ms both) and different state in `x/vt`
(cursor `16,23` / scrollback 796 / 8.4 ms versus cursor `1,23` / scrollback 852 / 9.0 ms).
A scratch run pinned the boundary between 65534 and 65535. So REP is O(min(n, 65535)) in
ghostty and O(n) in `x/vt`.

This probe is the clearest single number in the spike, and it is a **policy** difference as
much as a performance one: clamping REP changes what the program asked for, and the report
records the clamp so the choice is visible rather than discovered later.

### 1.8 Resize

**(a) Height shrink with the cursor on the last row.** 24 rows of `L01`–`L24`, cursor at
row 23, then `Resize(20, 10)`:

| | `x/vt` | `libghostty-vt` |
|---|---|---|
| rows after | `["L01" … "L10"]` | `["L15" … "L24"]` |
| cursor after | `3,9` | `3,9` |
| scrollback after | **0** | **14** |

`x/vt` keeps the **top** ten rows and clamps the cursor onto a row it was not on; the line
the user was editing (`L24`) is destroyed and nothing is preserved — the 14 dropped rows are
not pushed to scrollback. `libghostty-vt` keeps the cursor's row and the rows above it and
pushes the other 14 to scrollback.

**(b) Width change.** 45 characters on a 20-column terminal (rows `01234567890123456789`,
`01234567890123456789`, `ABCDE`), then `Resize(10, 10)`:

| | `x/vt` | `libghostty-vt` |
|---|---|---|
| rows after | `["0123456789" "0123456789" "ABCDE"]` | `["0123456789" ×4 "ABCDE"]` |
| cursor after | `5,2` | `5,4` |
| characters retained | **25 of 45** | 45 of 45 |

`x/vt` **truncates** — columns 10–19 of each row are silently dropped and 20 characters are
lost. `libghostty-vt` **reflows** and loses nothing. Ghostty's documentation says it handles
reflow on resize; the measurement agrees.

**(c) Height grow** (5 → 10 rows) leaves both intact.

Both policies are defensible in the abstract, but only reflow is lossless, and truncation is
not "stated" anywhere in `x/vt`'s API — it is what the code happens to do. For an authority
whose cells a client paints, silently dropping 44 % of a line is a data-loss path.

### 1.9 The 0x9C case

`OSC 0 ; X<E2 9C B3>Y` — U+2733, whose UTF-8 encoding contains the byte 0x9C — terminated by
BEL and by `ESC \`; then a literal 0x9C byte as the terminator; then an ASCII control.

| case | `x/vt` title | `x/vt` grid | `libghostty-vt` title | ghostty grid |
|---|---|---|---|---|
| `…X✳Y BEL` | `"X\xe2"` (58 e2) | **`"YZ"`**, cursor 2 | `X✳Y` (58 e2 9c b3 59) | `"Z"`, cursor 1 |
| `…X✳Y ESC \` | `"X\xe2"` | **`"YZ"`**, cursor 2 | `X✳Y` | `"Z"`, cursor 1 |
| `…X<0x9C>Y BEL` | `"X"` | `"YZ"` | *no title event* | `"Z"` |
| `…X-Y BEL` (control) | `"X-Y"` | `"Z"`, cursor 1 | `X-Y` | `"Z"` |

`x/vt` ends the OSC string at the 0x9C byte inside the UTF-8 encoding, truncating the title
after the first byte of the sequence and printing the rest — `YZ` — onto the grid. This is
exactly the defect `internal/panegrid/c1filter.go` exists to work around, and the workaround
is needed because `x/ansi`'s transition table special-cases 0x9C unconditionally
(`c1filter.go`'s header explains why no supported hook exists). `libghostty-vt` is unaffected
in both terminator forms and leaks nothing onto the grid.

**Real-terminal confirmation.** A real `xterm 410` under Xvfb, given
`printf '\033]0;X\342\234\263Y\007'`, reports the window title `X✳Y` — reproduced in three
separate runs on two displays. So the real terminal's answer matches ghostty's, and `x/vt`'s
row is a genuine divergence from a real terminal, not a difference of convention.

The bare-0x9C row differs in a way worth noting honestly: `x/vt` sets the title to `"X"`,
ghostty fires no title event at all. Nothing reaches the grid in either. I did not determine
which is correct for a raw 0x9C terminator.

## 2. The API answers

### 2.1 Incremental render state for a diff encoder

**`x/vt` — the `Damage` API is dead, and the brief's claim is confirmed.** `damage.go`
declares `Damage`, `CellDamage`, `RectDamage`, `ScreenDamage`, `MoveDamage` and
`ScrollDamage`, and

```
$ grep -rn "CellDamage\|RectDamage\|ScreenDamage\|MoveDamage\|ScrollDamage" x/vt/*.go | grep -v damage.go
$ echo $?
1
```

— nothing outside the declaring file references any of them, including `terminal.go`'s
interface (which names only `Touched()`). `Damage`/`RectDamage` are therefore type
definitions with no emission path, exactly as the brief states. The real mechanism is
ultraviolet's touched-line list reached through `Emulator.Touched() []*uv.LineData`
(`emulator.go:128`, `screen.go:49`), where `LineData` is `{FirstCell, LastCell}` — a column
span per **row**, and nothing finer. Measured for `"hello\r\nworld"` on 20×3:
`[0:{first:0 last:5} 1:{first:0 last:5} 2:nil]`. `Screen.ClearTouched()` (`screen.go:54`) is
the reset, and it is also called internally on alternate-screen entry and resize — so a
consumer that has not read the touched list before a resize loses it.

**`libghostty-vt` — a real render-state API, at row granularity, with its own lifetime.**
`ghostty_render_state_new/update/get/clean/free` over a `GhosttyRenderState`, queried for
`GHOSTTY_RENDER_STATE_DATA_DIRTY` (0 none / 1 partial / 2 full), plus a row iterator
(`…_row_iterator_new`, `…_next_dirty(&y)`) and a cells iterator. Measured by holding one
state across updates:

```
dirty_first_update             = 2 (FULL)   dirty_rows_first_update = [0 1 2]
dirty_after_clean              = 0 (FALSE)  dirty_rows_after_clean  = []
dirty_after_one_line           = 1 (PARTIAL) dirty_rows_after_one_line = [1 2]
```

The last line is the whole answer: after a clean frame, one written line reports **two**
dirty rows at row granularity, which is what a diff encoder needs. Per-row data also carries
`GHOSTTY_RENDER_STATE_ROW_DATA_SELECTION`, so a renderer can skip per-cell selection queries.

The cost is a second object per terminal with its own lifetime: the state is not part of the
terminal, must be updated explicitly, and must be cleaned by the consumer or the next update
reports nothing.

### 2.2 Soft-wrap continuation per line

**`x/vt`: absent, and unrecoverable.** `uv.Line` is `[]Cell` and `uv.LineData` is
`{FirstCell, LastCell}`; `grep -rn "Wrap" x/vt/*.go` matches only the autowrap *mode* and
the phantom/pending-wrap state, never a stored per-line flag. Measured: `0123456789ABCDEFGHIJ`
on 10×4 fills rows 0 and 1 with identical touched spans `{first:0 last:10}`, and a later
`Resize(20, 4)` leaves `row0 = "0123456789"`, `row1 = "ABCDEFGHIJ"` — the wrapped line is not
rejoined, because nothing recorded that it was one logical line. This is the foundation
requirement §1.2 names: the information is not stored while parsing, so it cannot be
recovered afterwards, and the frontend's `isWrapped` path in
`frontend/src/scrollback/serializer.ts:483` has no backend counterpart.

**`libghostty-vt`: stored and readable.** `GHOSTTY_ROW_DATA_WRAP` and
`GHOSTTY_ROW_DATA_WRAP_CONTINUATION` on a `GhosttyRow` obtained from a grid ref. Measured on
the same input: `row0_wrap=true`, `row1_continuation=true`, `row2` both false; and after
`Resize(20, 4)` the row is rejoined to `"0123456789ABCDEFGHIJ"` with wrap false. The
continuation flag survives the resize because it was stored while parsing.

### 2.3 PTY replies

**`x/vt`: replies arrive through an `io.Pipe` and the write blocks until someone reads.**
`Emulator.Read` (`emulator.go:255`) reads the pipe end and `e.pw` is an `io.PipeWriter`, so a
reply-producing write does not return until a reader takes the bytes. Measured directly:

```
no_reader: write_returned_within_2s = false
with_reader: write_ns = 15 640, reply = "\e[1;1R"
```

Yes — `x/vt` must be drained continuously, and the drain must be a separate goroutine, since
the write blocks *inside* whatever goroutine called `Write`. ADR-0041 already records this as
an integration requirement; the probe above is the number behind it.

**`libghostty-vt`: a synchronous callback, no pipe, no reader.** Replies are delivered to
`GHOSTTY_TERMINAL_OPT_WRITE_PTY` during the write; the callback runs on the caller's
goroutine and the parser continues when it returns. There is no drain to forget, and no
deadlock if there is none. The documented constraint is the reverse one: callbacks are
synchronous and must not re-enter `ghostty_terminal_vt_write` on the same terminal, and must
not block (the header says so explicitly: "callbacks must be very careful to not block for
too long"). A runtime that must hand the reply to an SSH carrier has to either be fast or
queue, where `x/vt` lets it be arbitrarily slow as long as something reads.

### 2.4 Non-visual effects

**`x/vt` — callbacks on a `Callbacks` struct**: `Bell`, `Title`, `IconName`, `AltScreen`,
`CursorPosition`, `CursorVisibility`, `CursorStyle`, `CursorColor`, `BackgroundColor`,
`ForegroundColor`, `WorkingDirectory`, `EnableMode`, `DisableMode` (`callbacks.go`). OSC
handlers are registered for 0/1/2 (title), 7 (cwd), 8 (hyperlink), 10/11/12 and 110/111/112
(colours). **There is no OSC 52 clipboard handler**: `RegisterOscHandler` exists as a public
seam (`handlers.go:63`), so a consumer could add one, but nothing is delivered by default.
Note the asymmetry that matters for a runtime: `Callbacks` is a whole struct passed to
`SetCallbacks`, so setting one field replaces the set.

**`libghostty-vt` — a named option per effect**, set with `ghostty_terminal_set`, so each is
independently installed or cleared with NULL: `WRITE_PTY`, `BELL`, `TITLE_CHANGED`,
`PWD_CHANGED`, `ENQUIRY`, `XTVERSION`, `SIZE` (XTWINOPS), `COLOR_SCHEME`,
`DEVICE_ATTRIBUTES`, `CLIPBOARD_WRITE`, `CLIPBOARD_READ`, `DESKTOP_NOTIFICATION`,
`PROGRESS_REPORT` (OSC 9;4), `UNKNOWN_SEQUENCE`. Measured on one terminal fed a title, an
OSC 7 cwd, a BEL, an OSC 52 clipboard write and an OSC 8 hyperlink:

```
bells=1  titles=["T"]  pwds=["file://h/tmp"]  clipboard_writes=[{location:0 length:1}]
```

The clipboard write is delivered as **decoded, binary-safe MIME representations**, not raw
OSC 52 bytes, and the header documents it as a synchronous request with a reply callback
(OSC 5522 semantics) — a caller may deny it. `TITLE` and `PWD` are also readable as terminal
state (`GHOSTTY_TERMINAL_DATA_TITLE`, `…_PWD`), so a runtime does not have to mirror them
itself.

### 2.5 Graphics

**`x/vt`: neither.** `grep -rni "sixel\|kitty" x/vt/*.go` finds no sixel and no Kitty image
code. `handleDcs`/`handleApc`/`handleSos`/`handlePm` (`dcs.go`) forward to user-registered
handlers and log "unhandled sequence" otherwise, so both protocols are dropped unless the
consumer writes a parser.

**`libghostty-vt`: Kitty graphics yes, sixel no.** `include/ghostty/vt/kitty_graphics.h` is
871 lines and the implementation is `src/terminal/kitty/graphics_image.zig` and neighbours;
the linked build reports `GHOSTTY_BUILD_INFO_KITTY_GRAPHICS = true` (measured). Sixel is
**not implemented**: the only occurrence of the word in the whole `src/` tree is
`src/terminal/device_attributes.zig:53`, `sixel = 4`, a flag the embedder may advertise in
DA1 — and the measured default DA1 reply is `\x1b[?62;22c`, which does not advertise it.
So ghostty is not a superset here: it covers Kitty images, and neither candidate parses
sixel. This confirms §1.2's "two real gaps" item 1 as still open for the runtime.

### 2.6 Two capabilities neither candidate question asked about, but the design needs

Both are in `libghostty-vt` only, and both bear on design §6.7 and on card content:

- **`snapshot.h`** — `ghostty_snapshot_encode` / `…_decoder_*`: a CRC-protected record stream
  that restores a terminal "including any unfinished VT parser input", with scrollback pages
  following a READY marker. `x/vt` has nothing comparable; its parser state is unexported
  struct fields. This is the substrate for the recovery problem §6.7 leaves open, and it is
  worth naming before that problem is solved from scratch.
- **`formatter.h`** — format terminal content as plain text, VT, or HTML, over a selection.
  Together with `selection.h` (42 kB of API) this is the extraction path for card bodies.

## 3. What the binding cost

**Measured cost.** One CGo package, 699 lines total across four files
(`binding.go` 391, `callbacks.go` 77, `bridge.c` 178, `bridge.h` 53), written in about two
hours including the build. `bridge.c` exists only because cgo cannot take the address of a
C function with no external linkage, so the effect callbacks are installed from C; the rest
is a Go struct holding the terminal handle plus a registry keyed by a `uintptr_t` userdata so
the `//export` trampolines can find their terminal.

**Build machinery.** Zig 0.16.0 (nixpkgs) plus one command,
`zig build -Demit-lib-vt=true -Doptimize=ReleaseFast`, 85 s wall on this machine, producing
an 18.0 MB static archive and a 9.9 MB shared object with headers. Linking against the
static archive from cgo needed nothing beyond `-lm -lpthread`:
`#cgo LDFLAGS: ${SRCDIR}/../.vendor/ghostty/zig-out/lib/libghostty-vt.a -lm -lpthread`.

**One defect is worth writing down because it will be hit again.** My first binding passed
`&fn` — the address of a stack local holding the callback — to `ghostty_terminal_set`. The
option value **is** the function pointer, cast to `const void *`, as
`example/c-vt-effects/src/main.c:162` shows. The terminal stored my stack address and, on the
first reply-producing write, jumped into a dead stack frame:

```
SIGSEGV: PC=0x7ffedec34528 m=0 sigcode=2 addr=0x7ffedec34528
nocx.internal/spikes/emulator/ghostty._Cfunc_ghostty_terminal_vt_write
```

The header's option table says "Callback Type" and does not spell out that the *value* is the
pointer; the example is the only place the contract is visible. Nothing about the probes
misleads you here — it crashes on the first effect, which is at least loud.

**What a production binding would additionally need**, in the order I would pay for it:

1. **Lifetime and ownership.** The API's rule is "borrowed, valid until the next mutating
   terminal call" — `GhosttyString` for title and pwd, `GhosttyGridRef`, the Kitty image
   storage, and `GhosttySelection`. My binding copies eagerly (`C.GoBytes`) and holds refs
   only inside one call. A production binding needs a stated rule per handle, and either
   copying or a documented borrow window, plus `runtime.Pinner`/`KeepAlive` discipline on
   every pointer that crosses.
2. **Sized structs.** Every struct here carries a leading `size` field and must be built with
   `GHOSTTY_INIT_SIZED`. That is the versioning mechanism, and a Go binding that zero-fills
   a struct literal instead silently sends size 0. My binding paid this by doing struct
   construction in C; a general Go binding has to do it per type and check the result.
3. **Tagged unions.** `GhosttyPoint`, `GhosttyTerminalUnknownSequence`,
   `GhosttyStyleColor`, `GhosttyTerminalScrollViewport` are tagged unions, which cgo exposes
   as opaque byte arrays. Every one of them needs a C shim (I needed one, for
   `GhosttyPoint`), so a real binding is Go + a C shim layer, not pure cgo.
4. **API churn, given the "unstable" warning.** The header's own first paragraph is
   "WARNING: This is an incomplete, work-in-progress API... definitely going to change", and
   it ships a `ghostty_type_json()` ABI manifest plus a schema-validate test in its build for
   exactly that reason. Pinning a commit (as this spike did) is the only stable posture;
   tracking `main` would mean re-reading the header on every bump. `x/vt` has the same
   problem in a different key (`v0.0.0-2026...` pseudo-versions, and the two behavioural
   defects below are the kind of thing a bump changes).
5. **Cross-compilation for the platforms nocx ships.** This is the item I could not measure
   and the one I would budget most for (see §6). `zig build` can cross-compile, and the
   packaging file targets Apple (`GhosttyDist`, an xcframework path in `build.zig`), so the
   machinery exists — for ghostty's own release targets. Whether `libghostty-vt` builds for
   macOS arm64 + amd64 and Windows from this Linux box, and whether the result is what
   nocx's cgo builds need, is unmeasured. `x/vt` is pure Go and costs zero here.

**The thing the binding does not cost** is the `go test ./...` and deadcode problem. The
module is nested (`.internal/spikes/emulator/go.mod`, three direct requirements and their
transitive set, no product dependency), so nothing entered the product's `go.mod`,
`go list ./...`, or the deadcode ratchet — `ADR-0041`'s method section describes doing exactly
this, and it worked: `go.mod` lists only `x/vt`, `ultraviolet`, `x/ansi` and their transitive
set.

## 4. Recommendation, and the cost of the road not taken

**Recommendation: `libghostty-vt` is the cheaper route to the authority role, on eight of
nine probes — and the recommendation is conditional on one measurement this spike did not
make.** The condition is stated in reason 3 and spelled out below, because it is the one
place where the evidence points the other way. Reasons, in order of weight:

1. **`x/vt` fails all nine probes, and four of the failures are silent wrongness rather
   than missing features.** Modified keys emit **nothing** (§1.3); F13 emits U+FFFD (§1.4);
   IRM is reported set and then ignored (§1.5); a CPR under origin mode reports a row it
   cannot distinguish from the absolute one (§1.6). The other five — split clusters,
   combining marks, unbounded REP, destructive resize, and the 0x9C title — are the same
   class of thing arrived at differently. Ghostty answers all nine correctly except one.
2. **Ghostty fails exactly one probe, and it is a deliberate bound, not a defect.**
   `libghostty-vt` clamps REP at 65535 (§1.7); `x/vt` honours the count and takes ~170 s and
   ~1 × 10⁹ allocations for 10⁹ repeats. A runtime that puts a remote program's bytes through
   its authority needs the bound, so this is arguably ghostty's answer to a question `x/vt`
   has not asked. Ghostty's other two misses are outside the nine: no sixel (§2.5), and emoji
   column accounting that disagrees with `xterm.js` on the regional-indicator flag (§1.1).
   Neither is silent wrongness of the same class.
3. **The condition: the argument that chose `x/vt` — column geometry against `xterm.js` — is
   measured on two disjoint bodies of evidence that disagree.** `ADR-0041` scored `x/vt` at
   100/100 on five real captures (`claude`, `bash`, `htop`, `vim`, `less`) containing no
   emoji, and explicitly lists emoji as untested. On the four emoji sequences I measured
   against `xterm.js`, ghostty matches its geometry on **3 of 4** — the ZWJ family (both 8
   columns), the skin tone (both 4), and `❤️` (both 1, width 1) — and differs on one, the
   regional-indicator flag (4 columns against 2); `x/vt` matches on **1 of 4**, and there
   only on the column total, since `xterm.js` gives two 1-column cells where `x/vt` gives one
   2-column cell. So on emoji ghostty's columns are closer to the product's terminal, and on
   everything `ADR-0041` actually measured `x/vt`'s were exact. **This spike cannot rank
   them, because it did not re-run that corpus.** That is the condition on the
   recommendation.
4. **The binding is the smaller risk than five subsystems, and the larger unknown.** 699
   lines and about two hours produced a provisional binding on one platform that ran every
   probe, plus one crash I diagnosed and one C shim layer the ABI forces. Set against five
   subsystems of `x/vt` work, that is the cheap side. Set against an API whose header's first
   paragraph says it is unstable and ships an ABI manifest because of it, and whose
   cross-compilation story is unmeasured (§6), it is not free.

**So the recommendation is a condition, not a verdict:** re-run `ADR-0041`'s corpus
(`internal/agentdriver/testdata/captures/`, five real captures, no emoji) through
`libghostty-vt`, and compare columns against `xterm.js` the way that ADR did. If ghostty's
geometry matches there, the choice that was made on geometry should be revisited on geometry,
and the probe table above supplies the behavioural half of the case. If it does not, the
geometry argument for `x/vt` survives intact and the nine probe failures below become the
price of staying — which is the section that follows.

**The honest cost of the road not taken — what extending or forking `x/vt` commits nocx to
maintaining**, by name:

- **Unicode assembly.** `x/vt` does not assemble a grapheme cluster across a write boundary
  (§1.1) and does not join a combining mark to an ASCII base even within one write (§1.2),
  because `handlePrint` prints ASCII immediately and `flushGrapheme` runs at the end of every
  `Write`. Fixing this means owning the cluster buffer, the flush trigger, and the width
  decision — which is where `ansi.FirstGraphemeCluster` and the `unicode11`/`Ansi`/`Grapheme`
  width-method choice live. Getting it right for ZWJ sequences, flags, skin tones and VS16 is
  the work `xterm.js` does with `addon-unicode11`, and it is the work `ADR-0041` already
  measured once because it decided an entire library choice on it.
- **Input protocols.** `key.go` needs the modifier-aware encodings (`CSI 1;5D`,
  `CSI 15;2~`), then never-ending mode interactions: application cursor keys (`?1`),
  application keypad (`?66`), `modifyOtherKeys`, and the Kitty keyboard protocol with its
  flags, event types and `CSI u` fallbacks. Ghostty's key encoder carries
  `CURSOR_KEY_APPLICATION`, `KEYPAD_KEY_APPLICATION`, `IGNORE_KEYPAD_WITH_NUMLOCK`,
  `ALT_ESC_PREFIX`, `MODIFY_OTHER_KEYS_STATE_2`, `KITTY_FLAGS`, `MACOS_OPTION_AS_ALT` and
  `BACKARROW_KEY_MODE` as separate options; that option list is the size of the job.
- **Mode semantics.** IRM today (§1.5), then the rest of the mode set that is *recorded* but
  not *implemented*: `resetModes` (`mode.go`) registers 18 modes, and the pattern of
  "recognised, reported, not honoured" is the one this spike caught in the cheapest possible
  place. Every mode a program can query is a promise to answer truthfully.
- **Resize policy.** `x/vt` truncates (§1.8) and keeps the top rows rather than the cursor's.
  A policy is needed; reflow is the lossless one, and reflow means re-running cluster
  assembly over the whole buffer — i.e. it depends on the Unicode work above.
- **Resource bounds.** REP is O(n) with ~1 allocation per character (§1.7): 10⁹ repeats is
  ~170 s and ~1.01 × 10⁹ allocations, which is a remote program's denial of service against the
  runtime. Bounding it means choosing a clamp (ghostty chose 65535) and applying it
  consistently across REP, CUP/CHH counts, and DCS/OSC payload sizes. Ghostty has separate
  options for exactly this (`APC_MAX_BYTES`, `KITTY_IMAGE_STORAGE_LIMIT`,
  `SCROLLBACK_MAX_BYTES`, `CONTINUATION_MAX_BYTES`, `CLIPBOARD_WRITE_MAX_BYTES`).
- **And the 0x9C defect is not fixable in `x/vt` at all**, because the byte is consumed by
  `x/ansi`'s package-level transition table before any `x/vt` code sees it —
  `internal/panegrid/c1filter.go`'s header documents this and the filter is the maintained
  consequence. Upstream `charmbracelet/x#976` is open. Staying means either carrying that
  filter as a permanent component of the runtime's ingest path, or fixing `x/ansi` and
  waiting for a release.

Read together: extending `x/vt` to the authority role is not a handful of patches. It is Unicode
assembly, an input-protocol encoder, a mode-semantics audit, a resize policy, and a resource
bound — five subsystems that a from-scratch terminal has already built, and that ghostty has
built and shipped to users.

Set against that, the honest cost of adopting `libghostty-vt` is one CGo package with a
shim layer, a pinned commit, an unmeasured cross-compilation story, and a REP clamp that changes
observable behaviour against a program's request. Those are smaller than five subsystems —
which is why the recommendation is to re-measure geometry and then re-ask the question, not
to close it.

## 5. Where the two candidates agree, for completeness

Worth recording so the next reader does not re-derive it: both parse and store the ordinary
things correctly in this spike. Ordinary ASCII and control sequences, `CSI 15~` for F5 and
`CSI 24~` for F12, application-cursor-mode `\eOD`/`\e[D` for the unmodified arrows, the
`CSI 6n` reply in the plain (non-origin-mode) case, the `CSI ? 6 $p` / `CSI ? 25 $p`
DECRQM replies, a height *grow*, an alternate-screen-free workflow, and cell styles and
colours (consumed, not compared). Neither is a broken emulator; they are two emulators that
differ on exactly the boundaries this brief names.

## 6. What I could not test, and why

- **A live real terminal for the key encodings (§1.3) and the CPR semantics (§1.6).** I
  installed `xterm 410`, `xdotool` and `Xvfb` from nixpkgs and drove a real xterm. It fails
  on this machine in three ways I could not work around inside a reasonable budget: there is
  **no window manager**, so `xdotool windowactivate` aborts and XTEST keystrokes never reach
  the window; synthetic events via `xdotool key --window` are ignored (xterm requires
  `*allowSendEvents: true`, which I enabled, and `XGetInputFocus` still returned
  `PointerRoot`); and xterm is unstable under Xvfb here — `cannot load font
  "-misc-fixed-…-iso10646-1"` on some runs and `fatal IO error 11 … KillClient on X server`
  on others. **The DSR route needs no keystrokes at all, and it still returned zero bytes**
  in every attempt, so I could not confirm §1.6 against a real terminal. One route *did*
  work and is reported above: xterm's **window title** after the 0x9C OSC (§1.9), reproduced
  three times. `infocmp` (ncurses 6.6.20251230) is the named authority for §1.3, and the
  documented xterm CPR semantics for §1.6.
- **Cross-compilation of `libghostty-vt` for nocx's platforms.** Not attempted. zig can
  cross-compile and ghostty's build has Apple targets, but whether nocx's macOS and Windows
  cgo builds can consume it from a Linux CI, and what they would link, is unmeasured — and it
  is the single largest unknown in the cost model of §3.
- **`ADR-0041`'s geometry corpus, re-run against `libghostty-vt`.** Not done, and §4 says so
  plainly: this spike's five emoji sequences are not that corpus, and they found a
  disagreement that the corpus might settle either way. `internal/agentdriver/testdata/captures/`
  is still there and `xterm.js` still answers this question headlessly, so the re-run is
  cheap; it was out of scope for a probe-based spike.
- **Attributes, colour and the truecolor path.** `ADR-0041`'s own "what this does NOT cover"
  list — attributes collected but not scored — applies to this spike unchanged. I read cell
  text, widths and cursor positions; I did not compare styles, and ghostty's style API
  (`style.h`, `GHOSTTY_CELL_DATA_STYLE_ID`) is a different shape from `uv.Cell.Style`.
- **The alternate screen, mouse tracking, focus events, scrollback compression, and
  selection.** All present in both libraries' APIs; none probed. Ghostty's alternate screen
  was exercised only incidentally (nothing in these probes enters it).
- **Real programs.** No capture corpus was replayed. `ADR-0041`'s finding that `x/vt`
  deadlocks without a drain is reproduced here (§2.3), but nothing in this spike says how
  either library behaves on `htop`, `vim` or an agent's spinner under sustained load, and the
  REP number is a synthetic worst case rather than a workload.
- **Ghostty's `UNKNOWN_SEQUENCE` effect.** Left unbound in the binding (its payload is a
  tagged union, and the title/grid evidence already decided probe 9). It would answer
  whether ghostty *reports* what it drops, which is a different question from whether it
  drops it.
- **`libghostty-vt`'s own test suite.** Not run. `build.zig` has `test` and a
  `lib_vt` test step, and a type-schema ABI validation step; running them was not needed for
  any probe here and would have measured the library's opinion of itself rather than the
  nine questions asked.

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

**Scope warning.** This is a measurement of nine probes and five API questions — and, in §7,
added afterwards, of seven recorded programs replayed through all three emulators for column
geometry. It is not a decision. §7 answers §4's geometry question on a corpus recorded fresh
for it (its opening paragraph explains why it cannot be `ADR-0041`'s own bytes). Still
unmeasured anywhere in this report: attributes and colour **values**, alternate-screen
behaviour as a state, scrollback compression, and performance on real programs (see §6).

## 0. How to reproduce

```bash
cd .internal/spikes/emulator
./run.sh                 # pinned ghostty fetch, libghostty-vt build, both drivers
```

`run.sh` is **three automated steps, one npm-conditional automated step, and one documented
manual check**. Automated: it fetches the pinned ghostty commit into `.vendor/ghostty` and
**fails if the checkout is not that commit**; runs
`nix shell nixpkgs#zig -c zig build -Demit-lib-vt=true -Doptimize=ReleaseFast` inside it; then
`go run ./cmd/xvt > results/xvt.jsonl` and
`go run ./cmd/ghosttyvt > results/ghostty.jsonl`, each under a backstop `timeout` that warns
rather than letting a hung driver pass for a complete one. If `npm` is present it additionally
runs the xterm.js reference into `results/xtermjs-*.txt`, installing into a temporary
directory rather than the repository. Not a step: the live-xterm title check of §1.9 is
**manual** — `run.sh` documents it but does not run it, because it is unreliable under Xvfb
here (§6).

Verified end to end from a clean vendor directory with the exit status recorded: `EXIT=0`,
1 m 59 s on the cold run, the same two artifacts by size, and byte-identical output for every
deterministic probe.

**§7 has its own two scripts**, and needs the `libghostty-vt` build above:

```bash
cd .internal/spikes/emulator
./corpus/record.sh       # records the seven captures into corpus/*.jsonl
./geometry.sh            # replays them through all three, scores, writes results/geometry/
```

Both need the `libghostty-vt` build from step 2, and **the fetched checkout cannot be in the
tree while a commit is made**: the pre-commit hook's root eslint walks the filesystem, the root
config ignores `spike/**` but not `.internal/spikes/**`, and ghostty's own sources carry Node
scripts eslint rejects. `run.sh` rebuilds it at the pinned, SHA-verified commit, which is why it
is not committed either.

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

| sequence          | bytes | `x/vt`                          | `libghostty-vt` | `xterm.js` (reference)                    |
| ----------------- | ----- | ------------------------------- | --------------- | ----------------------------------------- |
| `👨‍👩‍👧‍👦` (ZWJ family) | 25    | **3/24**, first divergence at 4 | **24/24**       | 21/24 in cell text, **24/24 in geometry** |
| `👍🏽` (skin tone)  | 8     | **3/7**, first divergence at 4  | **7/7**         | 7/7                                       |
| `🇷🇺` (flag)       | 8     | **3/7**, first divergence at 4  | **7/7**         | 7/7                                       |
| `❤️` (VS16)       | 6     | **2/5**, first divergence at 3  | **5/5**         | 5/5                                       |

Single-write result, row 0 of a 20-column terminal, and the cursor column after it:

| sequence   | `x/vt`                  | `libghostty-vt`                                       | `xterm.js`                                         |
| ---------- | ----------------------- | ----------------------------------------------------- | -------------------------------------------------- |
| ZWJ family | `"👨‍👩‍👧‍👦"/2 .` cursor **2** | `"👨‍"/2 . "👩‍"/2 . "👧‍"/2 . "👦"/2 .` cursor **8** | `"👨"/2 . "👩"/2 . "👧"/2 . "👦"/2 .` cursor **8** |
| `👍🏽`       | `"👍🏽"/2` cursor **2**   | `"👍"/2 . "🏽"/2 .` cursor **4**                      | `"👍"/2 . "🏽"/2 .` cursor **4**                   |
| `🇷🇺`       | `"🇷🇺"/2` cursor **2**   | `"🇷"/2 . "🇺"/2 .` cursor **4**                        | `"🇷"/1 "🇺"/1` cursor **2**                         |
| `❤️`       | `"❤️"/2` cursor **2**   | `"❤️"/1` cursor **1**                                 | `"❤️"/1` cursor **1**                              |

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
multi-byte rune — the first cell's _text_ loses its trailing ZWJ (`"👨"/2` instead of
`"👨‍"/2`). So the strict requirement "the resulting cells must be identical" is met by
`libghostty-vt` alone; `x/vt` fails it in geometry, `xterm.js` in cell text only.

**Do not read the ZWJ row as "ghostty agrees with the product".** Ghostty and `xterm.js`
agree on _where the columns are_ for the ZWJ family and the skin tone, and agree to the
column on `❤️`; they disagree on the regional-indicator flag, where `xterm.js` gives each
indicator width 1 (2 columns total) and ghostty gives each width 2 (4 columns total). On
that row `x/vt`'s 2-column total matches `xterm.js` and its single-cell shape does not.
`ADR-0041` chose `x/vt` **because** its columns matched `xterm.js`; the probe part of this
spike did not re-run that corpus, so on its own evidence it neither confirms nor overturns
the geometry case for ghostty — it establishes only that the two candidates now disagree with
each other on emoji. (**§7, added afterwards, does re-run the geometry comparison**, on a
corpus recorded for it, and finds on a real `emoji` capture exactly this split.)

### 1.2 Combining mark on an ASCII base

`a` followed by U+0301, one write and two writes:

|        | `x/vt`                       | `libghostty-vt`  | `xterm.js`       |
| ------ | ---------------------------- | ---------------- | ---------------- |
| cells  | `"a"/1` `"́"/0` — **2 cells** | `"á"/1` — 1 cell | `"á"/1` — 1 cell |
| cursor | 1                            | 1                | 1                |

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

| key event       | `x/vt` `SendKey`      | `libghostty-vt` legacy | terminfo / documented xterm                                |
| --------------- | --------------------- | ---------------------- | ---------------------------------------------------------- |
| Ctrl-Left       | **nothing (0 bytes)** | `\x1b[1;5D`            | `CSI 1;5D` (`kLFT` is shift-left `\E[1;2D`; m=5 is Ctrl)   |
| Shift-Up        | **nothing**           | `\x1b[1;2A`            | `CSI 1;2A`                                                 |
| F5              | `\x1b[15~`            | `\x1b[15~`             | `kf5=\E[15~` ✓                                             |
| Shift-F5        | **nothing**           | `\x1b[15;2~`           | `kf17=\E[15;2~` ✓                                          |
| Ctrl-F5         | **nothing**           | `\x1b[15;5~`           | `kf29=\E[15;5~` ✓                                          |
| Ctrl+Shift-Left | **nothing**           | `\x1b[1;6D`            | `CSI 1;6D` (m=6)                                           |
| Left            | `\x1b[D`              | `\x1b[D`               | `kcub1=\EOD` in application cursor mode, `CSI D` otherwise |

`x/vt` emits nothing at all for a special key with any modifier: `key.go:25`'s switch has no
case for a modified arrow, and the `default` branch appends `string(key.Code)` only
`if key.Mod == 0` (`key.go:294-298`), so `seq` stays empty and nothing is written. This is
silence, not a wrong sequence, which is the worse failure for a keystroke encoder.

With the Kitty keyboard protocol enabled, ghostty additionally emits
`\x1b[1;5:1D`, `\x1b[1;2:1A`, `\x1b[1;1:1D` (event-type suffix, press = `:1`).

### 1.4 F13

|           | `x/vt`                  | `libghostty-vt` |
| --------- | ----------------------- | --------------- |
| F13       | `EF BF BD` = **U+FFFD** | `\x1b[25~`      |
| F13+Shift | **nothing (0 bytes)**   | `\x1b[25;2~`    |
| F12       | `\x1b[24~`              | `\x1b[24~`      |
| F1        | `\x1bOP`                | `\x1bOP`        |

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

|                             | `x/vt`                                                                                                                                                                 | `libghostty-vt`                                                           |
| --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| row after printing          | `XYcd` — **overwrote**                                                                                                                                                 | `XYabcd` — **inserted**                                                   |
| mode state                  | no public query exists (`isModeSet` is unexported); observed two ways, both from the wire side: the `EnableMode` callback fired with mode 4, and DECRQM answered "set" | public data query: `GHOSTTY_TERMINAL_DATA_MODE` on `ANSIMode(4)` → `true` |
| `CSI 4 $p` (DECRQM)         | replied `\x1b[4;1$y` — **status 1 = set**                                                                                                                              | **no reply**                                                              |
| `CSI ? 6 $p`, `CSI ? 25 $p` | —                                                                                                                                                                      | `\x1b[?6;2$y`, `\x1b[?25;1$y`                                             |

Both "set" observations for `x/vt` come from what the emulator _says_ (a callback and a wire
reply), not from reading a private field — which is what makes the row a defect rather than a
probe limitation: the claim under test is "the emulator tells the program the mode is set",
and it does, twice.

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

| configuration                                     | `x/vt` CPR                 | `libghostty-vt` CPR               |
| ------------------------------------------------- | -------------------------- | --------------------------------- |
| DECOM off, `CUP 1;1` (absolute row 1)             | `\x1b[1;1R`                | `\x1b[1;1R`                       |
| **DECOM on**, `CUP 1;1` (lands on absolute row 5) | `\x1b[5;1R` — **absolute** | `\x1b[1;1R` — **origin-relative** |
| DECOM off, `CUP 5;1` (absolute row 5)             | `\x1b[5;1R`                | `\x1b[5;1R`                       |

The discrimination is exact: in `x/vt`, an origin-relative address under DECOM and an
absolute address without it produce the **same** report (`\x1b[5;1R`), so the report cannot
carry the distinction at all. `handlers.go:807` reads `e.scr.CursorPosition()` — the
absolute grid position — while `csi_cursor.go:74` correctly honours DECOM for _addressing_.
`libghostty-vt` answers origin-relative when DECOM is set and absolute otherwise.

Expected value: xterm's documented CPR semantics report the position relative to the origin
(the top-left of the scrolling region when origin mode is set). A live-terminal confirmation
of this row was attempted and **not obtained** (§6); the row above is the two candidates'
measured bytes, and `ADR-0041`-style agreement with `xterm.js` was not available because
headless `xterm.js` does not answer DSR.

### 1.7 Bounded work under REP

`A` then `CSI <n> b`. Both drivers, 80×24, default scrollback.

| n             | `x/vt` wall                                          | `x/vt` cumulative alloc / mallocs | `libghostty-vt` wall | ghostty alloc / mallocs |
| ------------- | ---------------------------------------------------- | --------------------------------- | -------------------- | ----------------------- |
| 100 000       | 17.2 ms                                              | 12.1 MB / 101 258                 | 0.37 ms              | 1 944 B / 11            |
| 1 000 000     | 171.7 ms                                             | 123.1 MB / 1 012 515              | 0.45 ms              | 1 848 B / 11            |
| 10 000 000    | 1 727.2 ms                                           | 1.22 GB / 10 125 017              | 0.41 ms              | 1 944 B / 11            |
| 1 000 000 000 | **not completed in 30 s** (192 922 025 mallocs done) | 23.3 GB in 30 s                   | **0.47 ms**          | 1 856 B / 17            |

These are the numbers committed in `results/*.jsonl`, produced by the `run.sh` whose exit
status was recorded as 0. **Wall time is the only quantity here that is not reproducible, and
it is not close**: nine runs of the same `x/vt` cases gave 16.4–43.6 ms at 10⁵,
171.7–219.0 ms at 10⁶ and 1701.6–**4090.9** ms at 10⁷; the high sample was taken on a loaded
machine (load average 2.2) and the same cases returned 1701.6–1821.8 ms on every quiet run.
Ghostty's four were 0.32–0.52 ms. **The allocation counts are exact and stable** —
1 012 515 ± 2 mallocs at 10⁶ across every run, and 1.013 mallocs and 122 bytes per repeated
character — so the rate is better read from those than from the clock: at ~1.01 allocations
per character, 10⁹ repeats is ~1.01 × 10⁹ allocations whatever the machine is doing, and the
observed 19.3 %-in-30 s is consistent with that.

Every other probe (§1.1–§1.6, §1.8, §1.9, §1.10 and the API section) reproduced identically
across all of those runs, including a full `run.sh` from a clean vendor checkout.

`x/vt` is linear with no cap: ~170 ns and ~1.013 allocations per repeated character on an
unloaded machine (~409 ns/char at the loaded extreme), so 10⁹ repeats is roughly three
minutes and ~1.01 × 10⁹ allocations. In 30 seconds it completed **19.3 %** of the work
(192 922 025 of 10⁹). The allocation is ~121–123 bytes per character — `utf8.go:44` boxes
each printed character with `string(r)` before storing it in a cell.

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

|                  | `x/vt`            | `libghostty-vt`   |
| ---------------- | ----------------- | ----------------- |
| rows after       | `["L01" … "L10"]` | `["L15" … "L24"]` |
| cursor after     | `3,9`             | `3,9`             |
| scrollback after | **0**             | **14**            |

`x/vt` keeps the **top** ten rows and clamps the cursor onto a row it was not on; the line
the user was editing (`L24`) is destroyed and nothing is preserved — the 14 dropped rows are
not pushed to scrollback. `libghostty-vt` keeps the cursor's row and the rows above it and
pushes the other 14 to scrollback.

**(b) Width change.** 45 characters on a 20-column terminal (rows `01234567890123456789`,
`01234567890123456789`, `ABCDE`), then `Resize(10, 10)`:

|                     | `x/vt`                                | `libghostty-vt`             |
| ------------------- | ------------------------------------- | --------------------------- |
| rows after          | `["0123456789" "0123456789" "ABCDE"]` | `["0123456789" ×4 "ABCDE"]` |
| cursor after        | `5,2`                                 | `5,4`                       |
| characters retained | **25 of 45**                          | 45 of 45                    |

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

| case                 | `x/vt` title      | `x/vt` grid          | `libghostty-vt` title  | ghostty grid    |
| -------------------- | ----------------- | -------------------- | ---------------------- | --------------- |
| `…X✳Y BEL`           | `"X\xe2"` (58 e2) | **`"YZ"`**, cursor 2 | `X✳Y` (58 e2 9c b3 59) | `"Z"`, cursor 1 |
| `…X✳Y ESC \`         | `"X\xe2"`         | **`"YZ"`**, cursor 2 | `X✳Y`                  | `"Z"`, cursor 1 |
| `…X<0x9C>Y BEL`      | `"X"`             | `"YZ"`               | _no title event_       | `"Z"`           |
| `…X-Y BEL` (control) | `"X-Y"`           | `"Z"`, cursor 1      | `X-Y`                  | `"Z"`           |

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

### 1.10 Graphics (probe added by this spike)

Not one of the nine. Added because the graphics answer §2.5 asks for was otherwise derivable
only from reading source, and a grep for the absence of a word is not a measurement. Both
drivers execute a sixel DCS and a Kitty graphics APC and record what the emulator does.

`x/vt` has no grid-side behaviour for either: it logs both as unhandled and, if a consumer
has registered a DCS or APC handler, hands the payload over verbatim. The four log lines it
produced, verbatim from `results/xvt.jsonl` (`log_lines`):

```
unhandled sequence: DCS "q" "\"1;1;2;2#0;2;0;0;0#0~~"
unhandled sequence: ESC "\\"
unhandled sequence: APC "Ga=T,f=24,s=1,v=1,i=42;AAAA"
unhandled sequence: ESC "\\"
```

Note the second and fourth: the `ESC \` string **terminator** is reported as its own
unhandled ESC, once per protocol. That is the parser's shape showing through, and it is
recorded rather than smoothed over.

|                | `x/vt`                                                                                                       | `libghostty-vt`                 |
| -------------- | ------------------------------------------------------------------------------------------------------------ | ------------------------------- |
| sixel DCS      | logged unhandled, no grid change                                                                             | _no report_                     |
| Kitty APC      | logged unhandled, no grid change                                                                             | `image_42_decoded = true`       |
| reply          | `""` — captured once, empty (no reply is sent)                                                               | `\e_Gi=42;OK\e\\`               |
| screen, cursor | unchanged, `0,0`                                                                                             | unchanged, `0,0`                |
| consumer seam  | 1 DCS + 1 APC delivered, payload bytes verbatim: `"1;1;2;2#0;2;0;0;0#0~~"` and `Ga=T,f=24,s=1,v=1,i=42;AAAA` | _no DCS/APC effect to register_ |

So the accurate statement for `x/vt` is **not** "no DCS or APC support": it parses both,
keeps neither, mutates no cell, and delegates both payloads to a handler the embedder
supplies. What it lacks is any _built-in_ interpretation of either payload — which is what a
sixel or Kitty graphics implementation would be. Ghostty has one for Kitty and not for
sixel, where it is silent rather than delegating.

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
`{FirstCell, LastCell}`; `grep -rn "Wrap" x/vt/*.go` matches only the autowrap _mode_ and
the phantom/pending-wrap state, never a stored per-line flag. Measured: `0123456789ABCDEFGHIJ`
on 10×4 fills rows 0 and 1 with identical touched spans `{first:0 last:10}`, and a later
`Resize(20, 4)` leaves `row0 = "0123456789"`, `row1 = "ABCDEFGHIJ"` — the wrapped line is not
rejoined, because nothing recorded that it was one logical line. This is the foundation
requirement §1.2 names: the information is not stored while parsing, so it cannot be
recovered afterwards, and the frontend's `isWrapped` path in
`frontend/src/scrollback/serializer.ts:483` has no backend counterpart.

**`libghostty-vt`: stored and readable.** `GHOSTTY_ROW_DATA_WRAP` and
`GHOSTTY_ROW_DATA_WRAP_CONTINUATION` on a `GhosttyRow` obtained from a grid ref
(`ghostty_grid_ref_row`, `ghostty_row_get`). Measured on the same input: `row0_wrap=true`,
`row1_continuation=true`, `row2` both false; and after `Resize(20, 4)` the row is rejoined to
`"0123456789ABCDEFGHIJ"` with wrap false. The continuation flag survives the resize because
it was stored while parsing.

Two more pieces of the same family, named because a card serializer wants them and neither
exists in `x/vt`: `GHOSTTY_TERMINAL_DATA_CURSOR_PENDING_WRAP` (`= 5`) exposes the phantom
pending-wrap state at the right margin as terminal state, and `ghostty_grid_ref_tracked.h`
provides a reference that survives scrolling and reports when it loses its value — the
substrate for "the marker I recorded is now at row N" without re-scanning.

### 2.3 PTY replies

**`x/vt`: replies arrive through an `io.Pipe` and the write blocks until someone reads.**
`Emulator.Read` (`emulator.go:255`) reads the pipe end and `e.pw` is an `io.PipeWriter`, so a
reply-producing write does not return until a reader takes the bytes. Measured directly:

```
no_reader: write_returned_within_2s = false
with_reader: write_ns = 15 640, reply = "\e[1;1R"
```

Yes — `x/vt` must be drained continuously, and the drain must be a separate goroutine, since
the write blocks _inside_ whatever goroutine called `Write`. ADR-0041 already records this as
an integration requirement; the probe above is the number behind it.

**`libghostty-vt`: a synchronous callback, no pipe, no reader.** Replies are delivered to
`GHOSTTY_TERMINAL_OPT_WRITE_PTY` during the write. The obligation is the opposite of a drain
and it is stricter about _time_: the callback runs **synchronously on the caller's
goroutine**, the parser is blocked until it returns, and the header says so outright —
callbacks "must be very careful to not block for too long or perform expensive operations,
since they are blocking further IO processing", and must not re-enter
`ghostty_terminal_vt_write` on the same terminal. There is nothing to drain and no deadlock
if nothing is registered; what a runtime must not do is hand the reply to a slow carrier
inside the callback. Measured: a DSR 6 produces its reply with no reader goroutine anywhere
in the driver, which is why the ghostty driver has no `drainer` at all and the `x/vt` driver
cannot do without one.

So the two differ in where the back-pressure lands: `x/vt` blocks the _writer_ until someone
reads, ghostty blocks the _parser_ until the callback returns. A runtime that must forward a
reply over SSH has to be fast-or-queue in the second case and may be arbitrarily slow in the
first.

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

Measured by executing the sequences, not by reading the source: a minimal sixel DCS
(`ESC P q "1;1;2;2#0;2;0;0;0#0~~ ESC \`) and a Kitty graphics APC transmitting a 1×1 RGB
image (`ESC _ G a=T,f=24,s=1,v=1,i=42;AAAA ESC \`). Recorded in `results/*.jsonl` under
probe `11_graphics`, which is the one probe here **added by the spike** rather than taken
from the nine (§1.10).

|               | `x/vt`                                                                                                | `libghostty-vt`                                                                                                                                                         |
| ------------- | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| sixel DCS     | logged `unhandled sequence: DCS "q" "\"1;1;2;2#0;2;0;0;0#0~~"`, screen and cursor unchanged, no reply | **no report at all**, screen and cursor unchanged                                                                                                                       |
| Kitty APC     | logged `unhandled sequence: APC "Ga=T,f=24,s=1,v=1,i=42;AAAA"`, screen unchanged                      | **decoded**: `image_42_decoded = true`, reply `\e_Gi=42;OK\e\\`                                                                                                         |
| consumer seam | `dcs_deliveries = 1`, `apc_deliveries = 1` with the payload bytes verbatim — a handler gets it        | not measured; the effect list carries no DCS/APC hook                                                                                                                   |
| compiled in   | n/a (pure Go)                                                                                         | `GHOSTTY_BUILD_INFO_KITTY_GRAPHICS = true`, storage present                                                                                                             |
| control       | n/a                                                                                                   | a deliberately unsupported APC (`ESC _ private-command;payload ESC \`) **is** reported, tag 0 — so the empty list above means "handled", not "nothing is ever reported" |

Neither library mutates a cell for either protocol, which is the right behaviour for an image
overlay and is also what "silently ignored" looks like from the grid. The difference is what
each one _does_ with the payload:

- **`x/vt` parses both and delegates both.** It recognises the DCS and APC framing, produces
  no grid change, and — through `RegisterDcsHandler` / `RegisterApcHandler` — hands the
  payload to the embedder verbatim. So the honest description is not "no graphics support":
  it has the transport and none of the interpretation, and a consumer that wants sixel or
  Kitty must write the decoder itself.
- **`libghostty-vt` interprets Kitty and ignores sixel.** It decodes a Kitty APC into image
  storage and answers `OK` on the wire, with an 871-line `kitty_graphics.h` covering images
  and their placements, and a build-time feature flag. Sixel produces no callback at all —
  its header states that only APC sequences are reported — so a sixel payload is neither
  drawn nor handed back.

A wart from the same log, kept because it is the parser's shape showing through: `x/vt`
reports the `ESC \` string **terminator** as its own `unhandled sequence: ESC "\\"` line, once
per protocol. Verbatim in §1.10.

The comparison with §1.2's "two real gaps" is therefore: gap 1 (graphics) is half closed by
ghostty — Kitty images yes, sixel no — and for `x/vt` it remains a consumer-side decoder plus
a handler registration. Gap 2 (soft wrap) is closed by ghostty and untouched in `x/vt`, as
§2.2 measures.

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

The header's option table says "Callback Type" and does not spell out that the _value_ is the
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

1. **`x/vt` fails all nine specified probes, and four of the failures are silent wrongness
   rather than missing features.** Modified keys emit **nothing** (§1.3); F13 emits U+FFFD
   (§1.4); IRM is reported set by both a callback and a DECRQM reply and then ignored (§1.5);
   a CPR under origin mode reports a row it cannot distinguish from the absolute one (§1.6).
   The other five — split clusters, combining marks, unbounded REP, destructive resize, and
   the 0x9C title — are the same class of thing arrived at differently. Ghostty answers all
   nine correctly except one.
2. **Ghostty fails exactly one of the nine, and it is a deliberate bound, not a defect.**
   `libghostty-vt` clamps REP at 65535 (§1.7); `x/vt` honours the count and takes ~180 s and
   ~1.01 × 10⁹ allocations for 10⁹ repeats. A runtime that puts a remote program's bytes
   through its authority needs the bound, so this is arguably ghostty's answer to a question
   `x/vt` has not asked. Ghostty's other misses are outside the nine: no sixel (§2.5, where the
   added probe §1.10 shows `x/vt` has neither protocol and ghostty decodes Kitty images for
   real), and emoji column accounting that disagrees with `xterm.js` on the
   regional-indicator flag (§1.1). Neither is silent wrongness of the same class.
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
- **Mode semantics.** IRM today (§1.5), then the rest of the mode set that is _recorded_ but
  not _implemented_: `resetModes` (`mode.go`) registers 18 modes, and the pattern of
  "recognised, reported, not honoured" is the one this spike caught in the cheapest possible
  place. Every mode a program can query is a promise to answer truthfully.
- **Resize policy.** `x/vt` truncates (§1.8) and keeps the top rows rather than the cursor's.
  A policy is needed; reflow is the lossless one, and reflow means re-running cluster
  assembly over the whole buffer — i.e. it depends on the Unicode work above.
- **Resource bounds.** REP is O(n) with ~1 allocation per character (§1.7): 10⁹ repeats is
  ~180 s and ~1.01 × 10⁹ allocations, which is a remote program's denial of service against the
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
DECRQM replies, a height _grow_, an alternate-screen-free workflow, and cell styles and
colours (consumed, not compared). Neither is a broken emulator; they are two emulators that
differ on exactly the boundaries this brief names.

## 6. What I could not test, and why

**This section records what §1–§6 could not test at the time they were written.** §7 was added
afterwards on a corpus recorded for it, and it does cover three of the items below: real
programs and the alternate screen, the truecolor path (the bytes, still not their attribute or
colour values), and `ADR-0041`'s geometry question — each bullet that §7 touches says so. The
bullets are otherwise left as written: each was true of the probe table and the recommendation
above it.

- **A live real terminal for the key encodings (§1.3) and the CPR semantics (§1.6).** I
  installed `xterm 410`, `xdotool` and `Xvfb` from nixpkgs and drove a real xterm. It fails
  on this machine in three ways I could not work around inside a reasonable budget: there is
  **no window manager**, so `xdotool windowactivate` aborts and XTEST keystrokes never reach
  the window; synthetic events via `xdotool key --window` are ignored (xterm requires
  `*allowSendEvents: true`, which I enabled, and `XGetInputFocus` still returned
  `PointerRoot`); and xterm is unstable under Xvfb here — `cannot load font
"-misc-fixed-…-iso10646-1"` on some runs and `fatal IO error 11 … KillClient on X server`
  on others. **The DSR route needs no keystrokes at all, and it still returned zero bytes**
  in every attempt, so I could not confirm §1.6 against a real terminal. One route _did_
  work and is reported above: xterm's **window title** after the 0x9C OSC (§1.9), reproduced
  three times. `infocmp` (ncurses 6.6.20251230) is the named authority for §1.3, and the
  documented xterm CPR semantics for §1.6.
- **Cross-compilation of `libghostty-vt` for nocx's platforms.** Not attempted. zig can
  cross-compile and ghostty's build has Apple targets, but whether nocx's macOS and Windows
  cgo builds can consume it from a Linux CI, and what they would link, is unmeasured — and it
  is the single largest unknown in the cost model of §3.
- **`ADR-0041`'s geometry corpus, re-run against `libghostty-vt`.** Not done by the probes
  above, and §4 says so plainly: this spike's five emoji sequences are not that corpus, and
  they found a disagreement that the corpus might settle either way.
  `internal/agentdriver/testdata/captures/` is still there and `xterm.js` still answers this
  question headlessly, so the re-run is cheap; it was out of scope for a probe-based spike.
  **§7 was added afterwards and answers this one**, on a corpus recorded fresh for it:
  `ADR-0041`'s own `frames/` were never committed and are not recoverable, so §7 could not
  replay that ADR's bytes — see §7's opening paragraph for what that does and does not allow
  it to say.
- **Attributes, colour and the truecolor path.** `ADR-0041`'s own "what this does NOT cover"
  list — attributes collected but not scored — applies to this spike unchanged. I read cell
  text, widths and cursor positions; I did not compare styles, and ghostty's style API
  (`style.h`, `GHOSTTY_CELL_DATA_STYLE_ID`) is a different shape from `uv.Cell.Style`.
  **§7 records the truecolor path as bytes** — its `wizard` capture carries 24-bit SGR and no
  256-colour sequence — and still compares no attribute or colour value.
- **The alternate screen, mouse tracking, focus events, scrollback compression, and
  selection.** All present in both libraries' APIs; none probed. Ghostty's alternate screen
  was exercised only incidentally (nothing in these probes enters it). **§7 replays five
  captures that enter the alternate screen and whose final screen is the alternate one**, and
  still does not compare a buffer against `xterm.js`'s.
- **Real programs.** No capture corpus was replayed by the probes above. `ADR-0041`'s finding
  that `x/vt` deadlocks without a drain is reproduced here (§2.3), but nothing in §1–§6 says how
  either library behaves on `htop`, `vim` or an agent's spinner under sustained load, and the
  REP number is a synthetic worst case rather than a workload. **§7 replays seven real-program
  captures through all three emulators**, for column geometry rather than for sustained load
  or performance.
- **Ghostty's `UNKNOWN_SEQUENCE` effect, beyond the tag.** The binding reports _that_ a
  sequence was unsupported and which kind (APC), which is what §1.10 and its control use.
  The payload — the borrowed sequence bytes — is a tagged union of `GhosttyString`s and is
  deliberately not bound, so this spike does not report _what_ was dropped, only that it was
  and that the reporting mechanism works.
- **`libghostty-vt`'s own test suite.** Not run. `build.zig` has `test` and a
  `lib_vt` test step, and a type-schema ABI validation step; running them was not needed for
  any probe here and would have measured the library's opinion of itself rather than the
  nine questions asked.

## 7. The geometry corpus — the measurement §4 made its recommendation conditional on

**This section is the measurement §4 asked for, and §6 listed as not done.** §1–§6 are left as
they were; nothing here restates their probes.

**It is not `ADR-0041`'s corpus, and its scores cannot be compared with that ADR's.** The ADR's
captures were `frames/`, and `spike/vt-agreement/.gitignore` at commit `d3872462` lists
`frames/`: they were never committed and are not recoverable. The tools survive at that commit
(`cmd/capture`, `cmd/diff`, `cmd/render-go`, `js/render-xterm.mjs`) and were read for method.
What the corpus below can do is put the three emulators on **identical bytes** and report which
column each of them puts those bytes in.

### 7.1 The corpus as recorded

| capture  | program (version)            | command                                     | alt screen | bytes | chunks | geometry |
| -------- | ---------------------------- | ------------------------------------------- | ---------- | ----- | ------ | -------- |
| `wizard` | claude 2.1.266 (Claude Code) | `claude`                                    | yes        | 15008 | 131    | 120×40   |
| `bash`   | bash GNU bash 5.3.15(1)      | `bash -i`                                   | no         | 76923 | 283    | 120×40   |
| `htop`   | htop 3.5.3                   | `htop`                                      | yes        | 8398  | 48     | 120×40   |
| `vim`    | vim VIM 9.2                  | `vim -n internal/transport/ws.go`           | yes        | 1617  | 9      | 120×40   |
| `less`   | less 704                     | `env -u LESS less internal/transport/ws.go` | yes        | 1554  | 2      | 120×40   |
| `wide`   | less 704                     | `env -u LESS less -R +26 e2e/ime.spec.ts`   | yes        | 1678  | 3      | 120×40   |
| `emoji`  | bash GNU bash 5.3.15(1)      | `bash corpus/scripts/emoji.sh`              | no         | 162   | 3      | 120×40   |

What those recordings actually contain, counted from the bytes themselves — because a row can
advertise a construct its bytes do not have, and one of these did:

| capture  | alt | CUP | DECSTBM | CHA | SGR (any) | SGR 24-bit | SGR 256 | erase (ED/EL) | bracketed paste |
| -------- | --- | --- | ------- | --- | --------- | ---------- | ------- | ------------- | --------------- |
| `wizard` | 1   | 286 | 1       | 246 | 390       | 214        | 0       | 117           | 3               |
| `bash`   | 0   | 0   | 0       | 0   | 4         | 0          | 0       | 0             | 2               |
| `htop`   | 1   | 178 | 3       | 12  | 367       | 0          | 0       | 1             | 0               |
| `vim`    | 1   | 47  | 1       | 0   | 10        | 0          | 2       | 3             | 1               |
| `less`   | 1   | 1   | 0       | 0   | 41        | 0          | 0       | 1             | 0               |
| `wide`   | 1   | 1   | 0       | 0   | 41        | 0          | 0       | 5             | 0               |
| `emoji`  | 0   | 0   | 0       | 0   | 0         | 0          | 0       | 0             | 0               |

Every capture is `corpus/<name>.jsonl`, recorded by `./corpus/record.sh` through the product's
own `cmd/agent-capture` — bytes only, one JSON object per PTY read. `TERM` is pinned to
`xterm-256color` and `LANG`/`LC_ALL` to `en_US.UTF-8` by the recorder; every capture is 120×40.
Re-record with that script; the versions of everything involved are in
`results/geometry/versions.txt`, written by `./geometry.sh` from the commands themselves.

Three choices in the recording are load-bearing, and each was bought by a capture that was
wrong before it:

- **Nothing is typed that makes a program exit.** htop, vim and less restore the normal screen
  (`?1049l`) on quit, which would record a blank screen in place of the alternate one the row
  exists to measure. Each script ends instead and the recorder kills the program with the
  alternate screen still up. `?1049h` is present in the five captures the table marks `yes`.
- **`LESS` is unset for the two `less` runs.** This shell carries `LESS=FRX`, whose `-X` makes
  less skip `smcup`/`rmcup`: the first recording of the `less` row has **no `?1049h` at all**
  and shows the file on the normal screen. `env -u LESS` is what makes the row the shape it is
  supposed to be.
- **`vim` runs with `-n`.** Without it, vim writes a swap file beside the file it opens; this
  capture ends by killing vim, so the swap can survive, and the next recording of the row would
  then draw vim's "swap file already exists" dialog in place of the file — as well as leaving a
  file outside this spike's directory. `-n` removes the swap and none of the screen.
- **`COLORTERM=truecolor` is set explicitly in the Claude run's env file.** The recorder pins
  `TERM` to `xterm-256color`, whose terminfo advertises no direct-colour capability, and the
  other six captures inherit the caller's environment (which here has `COLORTERM` set too) —
  none of them emits a 24-bit sequence, measured in the table above. The Claude run's env file
  replaces the environment wholesale, so the variable has to be written into it: the first
  recording of the `wizard` row carried 256-colour SGR and not one 24-bit sequence, which is
  not the shape that row advertises. With `COLORTERM` set, the row in `corpus/` carries 24-bit sequences and no
  256-colour ones — the two `SGR` columns of the table above. The other captures carry the
  colours their programs chose under the terminal the recorder pins.
- **The Claude capture is isolated.** A fresh `HOME` and `CLAUDE_CONFIG_DIR` under `/var/tmp`,
  an `-env-file` that makes the recorder refuse to start at all if Claude Code would read
  configuration outside the run, and an endpoint on `127.0.0.1:18999` that accepts the
  connection and never answers — which is what keeps a turn in flight, so the last frame holds
  the spinner rather than the screen Claude restores on exit. `corpus/wizard.meta.json` records
  the resolved binary (2.1.266), its SHA-256 and the environment variable names; nothing of the
  owner's Claude account or configuration is read, and the variable names carry no values.

The shapes are the ones the brief asked for. `wizard`: first run — the theme picker with its
`❯` menu, box drawing, truecolor SGR, the unknown-model warning, then the main UI with a turn
submitted and the spinner up for the rest of the capture — the word stays `Newspapering…` and the
glyph animates: `·` at 18 s, `✢` at 22, 26 and 30 s, `✽` at 31 s. `bash`: a
prompt and `br ready`'s coloured list. `htop`: the alternate screen, 3 `DECSTBM` and 178 `CUP`
sequences, the system bars, and heavy SGR traffic — every count in that sentence is a column of
the table above. `vim` and `less`: alternate screen and a source file. `wide`: `less -R +26
e2e/ime.spec.ts`, whose line 26 is `const MARKER = 'こんにちは'`. `emoji`: a ZWJ family, a
skin-tone sequence, a regional-indicator flag and `❤️`, each between `<` and `>` markers so a
width error moves the markers rather than silently shifting text, with a line of ordinary text
after each.

### 7.2 The measurement

Each capture is replayed from byte zero through all three emulators at the capture's own
geometry, and the final screen is written down column by column — one JSON dump per capture,
emulator and feed shape in `results/geometry/`, which is machine output, about 12 MB, and is
**not committed**: it is regenerated byte-identically by the script below from the corpus's
bytes, which are. `./geometry.sh` runs all of it. `cmd/geom` (Go: x/vt and the existing
`libghostty-vt` binding) and `reference/geometry.mjs` (node: headless xterm.js) emit the same
schema, and `cmd/geom score` compares them into `<capture>.score.json`, from which every table
below is generated rather than typed. Both sides refuse to measure a stream whose decoded bytes
contain a U+FFFD, because the capture format carries bytes as a JSON string and a byte that is
not valid UTF-8 cannot survive that; both were checked to refuse, on a synthetic stream holding
one. **All seven recordings are clean on this check** — 0 replacement characters in every dump —
so the bytes each emulator received are the bytes the program wrote.

Ground truth is the product's own VT frontend (ADR-0001): headless `@xterm/headless` 5.5.0 with
`@xterm/addon-unicode11` 0.8.0 and `unicode.activeVersion = '11'` — the versions that line up
with `frontend/package.json`'s `@xterm/xterm` ^5.5.0 and `@xterm/addon-unicode11` ^0.8.0, and
the setting `frontend/src/renderers/xterm.ts:412` installs. It reads the viewport
(`viewportY + y`), which is the screenful a person sees and the one both candidates report.

Two metrics, both from the same raw dumps:

- **text** — the concatenated characters of the final screen, which is the definition the brief
  gives. A row contributes its cells' characters in column order; a blank column contributes a
  space and the second half of a wide cluster contributes nothing; the row is right-trimmed. The
  count is rows whose text is identical to xterm.js's.
- **geometry** — what each column holds, also the brief's definition: a column's cell is
  classified (blank, narrow, wide, continuation, or a zero-width cell holding a cluster of its
  own) and compared together with the characters it holds. The count is columns where both
  agree, over all 4 800 columns and then restricted to the columns xterm.js does not leave
  blank.

**The one normalisation, and why it is not a fudge.** A zero-width cell holding nothing, and a
cell holding a space where xterm.js holds a wide cluster's tail, both count as a continuation;
a cell holding a printed space counts as blank. Without that, `x/vt`'s wide tails — which hold a
space where `xterm.js` holds nothing — would read as geometry misses that are representation,
not position. `ADR-0041` scored `x/vt` 100/100 on geometry and had to make the same allowance.
The raw cells stay in the dumps, so which representation each emulator used is visible in every
disagreement below.

Not scored: colour and attributes, selection, graphics, performance, and the alternate-screen
state itself (the recorded bytes establish which captures enter it). Cursors are recorded in
every dump and are not part of either score; as an observation, all three emulators' cursors
agreed on all seven captures (wizard 2,37 · bash 56,39 · htop 81,39 · vim 0,0 · less 24,39 ·
wide 15,39 · emoji 0,10).

### 7.3 Scores

| capture  | emulator | text (rows identical) | geometry (columns)  | geometry, content columns | disagreeing columns |
| -------- | -------- | --------------------- | ------------------- | ------------------------- | ------------------- |
| `wizard` | xvt      | 39/40                 | 4790/4800 (99.79%)  | 410/410                   | 10                  |
| `wizard` | ghostty  | 40/40                 | 4800/4800 (100.00%) | 410/410                   | 0                   |
| `bash`   | xvt      | 40/40                 | 4800/4800 (100.00%) | 3459/3459                 | 0                   |
| `bash`   | ghostty  | 40/40                 | 4800/4800 (100.00%) | 3459/3459                 | 0                   |
| `htop`   | xvt      | 40/40                 | 4800/4800 (100.00%) | 2626/2626                 | 0                   |
| `htop`   | ghostty  | 40/40                 | 4800/4800 (100.00%) | 2626/2626                 | 0                   |
| `vim`    | xvt      | 40/40                 | 4800/4800 (100.00%) | 1062/1062                 | 0                   |
| `vim`    | ghostty  | 40/40                 | 4800/4800 (100.00%) | 1062/1062                 | 0                   |
| `less`   | xvt      | 40/40                 | 4800/4800 (100.00%) | 1041/1041                 | 0                   |
| `less`   | ghostty  | 40/40                 | 4800/4800 (100.00%) | 1041/1041                 | 0                   |
| `wide`   | xvt      | 40/40                 | 4800/4800 (100.00%) | 1156/1156                 | 0                   |
| `wide`   | ghostty  | 40/40                 | 4800/4800 (100.00%) | 1156/1156                 | 0                   |
| `emoji`  | xvt      | 40/40                 | 4783/4800 (99.65%)  | 84/100                    | 17                  |
| `emoji`  | ghostty  | 40/40                 | 4795/4800 (99.90%)  | 97/100                    | 5                   |

**Reading it.** On **five of the seven** captures both candidates reproduce xterm.js's screen
exactly: 40/40 rows of text and 4 800/4 800 columns of geometry each for `bash`, `htop`, `vim`,
`less` and `wide`. The CJK row — the one the brief calls the case that matters most — is in that
set, and so is every shape `ADR-0041` measured.

**Two captures are not perfect for at least one candidate.** `wizard` costs `x/vt` ten columns
of row 0, all in one line, and §7.4 shows that is §1.9's defect rather than a geometry policy.
The two candidates differ from each other on exactly two captures, for different reasons. On
`wizard` they differ because of the defect above — 10 columns, `x/vt` alone. Excluding that
defect, `emoji` is the only capture where their screens differ from each other at all: there
`x/vt` matches 84 of the 100 content columns and `libghostty-vt` 97. The text score is 40/40 for both on `emoji` — every
character the program printed is on both screens; what differs is which cell holds it.

**The CJK row, which the brief calls the case that matters most, is a tie for the same reason**: all
three agree cell for cell — each kana a two-column cluster with a continuation beside it, and the
line's closing `'` on the same column in all three:

| line on row 0 of `wide`       | emulator      | columns, from that line's first character                                                                                                                                                                                                |
| ----------------------------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `const MARKER = 'こんにちは'` | xterm.js      | 0:`c`/1 1:`o`/1 2:`n`/1 3:`s`/1 4:`t`/1 5:blank 6:`M`/1 7:`A`/1 8:`R`/1 9:`K`/1 10:`E`/1 11:`R`/1 12:blank 13:`=`/1 14:blank 15:`'`/1 16:`こ`/2 17:cont 18:`ん`/2 19:cont 20:`に`/2 21:cont 22:`ち`/2 23:cont 24:`は`/2 25:cont 26:`'`/1 |
| `const MARKER = 'こんにちは'` | libghostty-vt | 0:`c`/1 1:`o`/1 2:`n`/1 3:`s`/1 4:`t`/1 5:blank 6:`M`/1 7:`A`/1 8:`R`/1 9:`K`/1 10:`E`/1 11:`R`/1 12:blank 13:`=`/1 14:blank 15:`'`/1 16:`こ`/2 17:cont 18:`ん`/2 19:cont 20:`に`/2 21:cont 22:`ち`/2 23:cont 24:`は`/2 25:cont 26:`'`/1 |
| `const MARKER = 'こんにちは'` | x/vt          | 0:`c`/1 1:`o`/1 2:`n`/1 3:`s`/1 4:`t`/1 5:blank 6:`M`/1 7:`A`/1 8:`R`/1 9:`K`/1 10:`E`/1 11:`R`/1 12:blank 13:`=`/1 14:blank 15:`'`/1 16:`こ`/2 17:cont 18:`ん`/2 19:cont 20:`に`/2 21:cont 22:`ち`/2 23:cont 24:`は`/2 25:cont 26:`'`/1 |

That is the line the CJK case turns on, and the widest content in the capture: five kana, each
two columns wide with a continuation beside it, placed identically by all three.

### 7.4 Every disagreement

Every column where either candidate differs from xterm.js, with what all three put there. Columns
are 0-based viewport coordinates — row 0 the top of the screen, column 0 its left edge — and a
cell is written as its characters and column footprint (`/2` wide, `/1` narrow, `/0` not the first
of anything); `blank` is a cell holding nothing, `cont` a wide cluster's second half.

| capture  | row | col | xterm.js holds | libghostty-vt holds | x/vt holds   | kinds                           |
| -------- | --- | --- | -------------- | ------------------- | ------------ | ------------------------------- |
| `wizard` | 0   | 1   | blank          | blank               | `C`/1        | x/vt text+geometry              |
| `wizard` | 0   | 2   | blank          | blank               | `l`/1        | x/vt geometry                   |
| `wizard` | 0   | 3   | blank          | blank               | `a`/1        | x/vt geometry                   |
| `wizard` | 0   | 4   | blank          | blank               | `u`/1        | x/vt geometry                   |
| `wizard` | 0   | 5   | blank          | blank               | `d`/1        | x/vt geometry                   |
| `wizard` | 0   | 6   | blank          | blank               | `e`/1        | x/vt geometry                   |
| `wizard` | 0   | 8   | blank          | blank               | `C`/1        | x/vt geometry                   |
| `wizard` | 0   | 9   | blank          | blank               | `o`/1        | x/vt geometry                   |
| `wizard` | 0   | 10  | blank          | blank               | `d`/1        | x/vt geometry                   |
| `wizard` | 0   | 11  | blank          | blank               | `e`/1        | x/vt geometry                   |
| `emoji`  | 0   | 8   | `👨‍`/2        | `👨‍`/2             | `👨‍👩‍👧‍👦`/2       | x/vt geometry                   |
| `emoji`  | 0   | 10  | `👩‍`/2        | `👩‍`/2             | `>`/1        | x/vt geometry                   |
| `emoji`  | 0   | 11  | continuation   | continuation        | blank        | x/vt geometry                   |
| `emoji`  | 0   | 12  | `👧‍`/2        | `👧‍`/2             | blank        | x/vt geometry                   |
| `emoji`  | 0   | 13  | continuation   | continuation        | blank        | x/vt geometry                   |
| `emoji`  | 0   | 14  | `👦`/2         | `👦`/2              | blank        | x/vt geometry                   |
| `emoji`  | 0   | 15  | continuation   | continuation        | blank        | x/vt geometry                   |
| `emoji`  | 0   | 16  | `>`/1          | `>`/1               | blank        | x/vt geometry                   |
| `emoji`  | 2   | 6   | `👍`/2         | `👍`/2              | `👍🏽`/2       | x/vt geometry                   |
| `emoji`  | 2   | 8   | `🏽`/2         | `🏽`/2              | `>`/1        | x/vt geometry                   |
| `emoji`  | 2   | 9   | continuation   | continuation        | blank        | x/vt geometry                   |
| `emoji`  | 2   | 10  | `>`/1          | `>`/1               | blank        | x/vt geometry                   |
| `emoji`  | 4   | 6   | `🇷`/1          | `🇷`/2               | `🇷🇺`/2       | x/vt geometry, ghostty geometry |
| `emoji`  | 4   | 7   | `🇺`/1          | continuation        | continuation | x/vt geometry, ghostty geometry |
| `emoji`  | 4   | 8   | `>`/1          | `🇺`/2               | `>`/1        | ghostty geometry                |
| `emoji`  | 4   | 9   | blank          | continuation        | blank        | ghostty geometry                |
| `emoji`  | 4   | 10  | blank          | `>`/1               | blank        | ghostty geometry                |
| `emoji`  | 6   | 7   | `❤️`/1         | `❤️`/1              | `❤️`/2       | x/vt geometry                   |
| `emoji`  | 6   | 8   | `>`/1          | `>`/1               | continuation | x/vt geometry                   |
| `emoji`  | 6   | 9   | blank          | blank               | `>`/1        | x/vt geometry                   |

**`wizard` row 0 is §1.9's defect, reproduced by a real program.** `x/vt` holds `Claude Code`
at columns 1–11 where xterm.js and `libghostty-vt` hold nothing. The cause is the byte `0x9C`
being taken as a string terminator inside a UTF-8 sequence: Claude Code's window title is
`✳ Claude Code`, and `✳` is U+2733, `E2 9C B3`. Three sequences reproduce it — the first is the
title a real Claude Code sets, the second the smallest one this spike found, the third the
control — each fed to both emulators by the table generator, which prints the bytes it fed in
hex and refuses to publish a case whose stream does not contain a `0x9C`:

Note what the hex column shows about the case: a capture holds valid UTF-8, so the byte can only
appear inside a multi-byte sequence (`e29cb3` for `✳`, `c29c` for U+009C). A genuinely bare
`0x9C` is an ST and cannot be written into the format at all — which is the same reason the
product's filter distinguishes the two cases rather than dropping the byte.

| sequence fed                                                | length | bytes, hex                                               | x/vt row 0     | libghostty-vt row 0 |
| ----------------------------------------------------------- | ------ | -------------------------------------------------------- | -------------- | ------------------- |
| `ESC [ 2 J ESC [ H ESC ] 0 ; ✳ ␠ C l a u d e ␠ C o d e BEL` | 27     | `1b5b324a1b5b481b5d303be29cb320436c6175646520436f646507` | ` Claude Code` | _(empty)_           |
| `ESC [ 2 J ESC [ H ESC ] 0 ; U + 0 0 9 C B BEL`             | 15     | `1b5b324a1b5b481b5d303bc29c4207`                         | `B`            | _(empty)_           |
| `ESC [ 2 J ESC [ H ESC ] 0 ; p l a i n ␠ t i t l e BEL`     | 23     | `1b5b324a1b5b481b5d303b706c61696e207469746c6507`         | _(empty)_      | _(empty)_           |

The control leaks nothing, so the byte is the whole of the cause. This is
the defect `internal/panegrid/c1filter.go` exists to work around — it rewrites a `0x9C` that is
a UTF-8 continuation byte inside OSC or DCS before `x/vt` sees it, and is wired in front of
every byte at `internal/panegrid/panegrid.go:262`. So this row is evidence about the **raw**
emulator: **it was not re-measured through the product's filter**, because this spike module
deliberately has no dependency on the product module (`§3`), and a filter re-implemented here
would be a measurement of that re-implementation. What the row does establish is that a real
program in the product's own workload class reaches the byte, and that ghostty does not have
the defect.

**`emoji` is the same split §1.1 measured by hand, on a real capture.** The four lines, from
each line's first character (the `<` and `>` markers are the program's, and the `>` is what
shows where a line's end landed):

| line in the capture | emulator      | columns, from that line first character                                                                                                           |
| ------------------- | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `family <👨‍👩‍👧‍👦>`       | xterm.js      | 0:`f`/1 1:`a`/1 2:`m`/1 3:`i`/1 4:`l`/1 5:`y`/1 6:blank 7:`<`/1 8:`👨‍`/2 9:cont 10:`👩‍`/2 11:cont 12:`👧‍`/2 13:cont 14:`👦`/2 15:cont 16:`>`/1 |
| `family <👨‍👩‍👧‍👦>`       | libghostty-vt | 0:`f`/1 1:`a`/1 2:`m`/1 3:`i`/1 4:`l`/1 5:`y`/1 6:blank 7:`<`/1 8:`👨‍`/2 9:cont 10:`👩‍`/2 11:cont 12:`👧‍`/2 13:cont 14:`👦`/2 15:cont 16:`>`/1 |
| `family <👨‍👩‍👧‍👦>`       | x/vt          | 0:`f`/1 1:`a`/1 2:`m`/1 3:`i`/1 4:`l`/1 5:`y`/1 6:blank 7:`<`/1 8:`👨‍👩‍👧‍👦`/2 9:cont 10:`>`/1                                                          |
| `skin <👍🏽>`         | xterm.js      | 0:`s`/1 1:`k`/1 2:`i`/1 3:`n`/1 4:blank 5:`<`/1 6:`👍`/2 7:cont 8:`🏽`/2 9:cont 10:`>`/1                                                          |
| `skin <👍🏽>`         | libghostty-vt | 0:`s`/1 1:`k`/1 2:`i`/1 3:`n`/1 4:blank 5:`<`/1 6:`👍`/2 7:cont 8:`🏽`/2 9:cont 10:`>`/1                                                          |
| `skin <👍🏽>`         | x/vt          | 0:`s`/1 1:`k`/1 2:`i`/1 3:`n`/1 4:blank 5:`<`/1 6:`👍🏽`/2 7:cont 8:`>`/1                                                                           |
| `flag <🇷🇺>`         | xterm.js      | 0:`f`/1 1:`l`/1 2:`a`/1 3:`g`/1 4:blank 5:`<`/1 6:`🇷`/1 7:`🇺`/1 8:`>`/1                                                                           |
| `flag <🇷🇺>`         | libghostty-vt | 0:`f`/1 1:`l`/1 2:`a`/1 3:`g`/1 4:blank 5:`<`/1 6:`🇷`/2 7:cont 8:`🇺`/2 9:cont 10:`>`/1                                                            |
| `flag <🇷🇺>`         | x/vt          | 0:`f`/1 1:`l`/1 2:`a`/1 3:`g`/1 4:blank 5:`<`/1 6:`🇷🇺`/2 7:cont 8:`>`/1                                                                           |
| `heart <❤️>`        | xterm.js      | 0:`h`/1 1:`e`/1 2:`a`/1 3:`r`/1 4:`t`/1 5:blank 6:`<`/1 7:`❤️`/1 8:`>`/1                                                                          |
| `heart <❤️>`        | libghostty-vt | 0:`h`/1 1:`e`/1 2:`a`/1 3:`r`/1 4:`t`/1 5:blank 6:`<`/1 7:`❤️`/1 8:`>`/1                                                                          |
| `heart <❤️>`        | x/vt          | 0:`h`/1 1:`e`/1 2:`a`/1 3:`r`/1 4:`t`/1 5:blank 6:`<`/1 7:`❤️`/2 8:cont 9:`>`/1                                                                   |

Read as behaviour:

- **ZWJ family** — xterm.js and `libghostty-vt` are identical: four 2-column clusters, the `>`
  at column 16. `x/vt` holds the whole sequence in **one** 2-column cell and puts `>` at
  column 10. Six columns of geometry, on one line.
- **Skin tone** — xterm.js and `libghostty-vt`: `👍`/2 and `🏽`/2, `>` at 10. `x/vt`: one `👍🏽`/2
  cell, `>` at 8.
- **Regional-indicator flag** — the only line where `libghostty-vt` differs from xterm.js:
  `🇷`/2 + continuation + `🇺`/2 + continuation, `>` at 10, against xterm.js's two 1-column
  indicators and `>` at 8. `x/vt`'s single `🇷🇺`/2 cell happens to leave `>` at 8, xterm.js's
  column, with a different cell shape underneath it.
- **`❤️` (U+2764 U+FE0F)** — xterm.js and `libghostty-vt` give it one column, `>` at 8;
  `x/vt` gives it two, `>` at 9.

`wizard` and `emoji` are the only two captures on which anything differs at all; on the other
five the three emulators' screens are identical.

### 7.5 Chunked replay

The same captures, split into N writes at even byte offsets, and — for the six captures under
64 KiB — one byte per write. "same" means the final screen is identical to that emulator's own
whole-file screen. `text` counts rows that differ, `geometry` counts columns.

| capture  | mode     | x/vt text | x/vt geometry | ghostty text | ghostty geometry | xterm.js text | xterm.js geometry |
| -------- | -------- | --------- | ------------- | ------------ | ---------------- | ------------- | ----------------- |
| `wizard` | bytewise | same      | same          | same         | same             | diff(1)       | diff(1)           |
| `wizard` | split:2  | same      | same          | same         | same             | same          | same              |
| `wizard` | split:3  | same      | same          | same         | same             | same          | same              |
| `wizard` | split:5  | same      | same          | same         | same             | same          | same              |
| `wizard` | split:8  | same      | same          | same         | same             | same          | same              |
| `wizard` | split:16 | same      | same          | same         | same             | same          | same              |
| `wizard` | split:32 | same      | same          | same         | same             | same          | same              |
| `bash`   | split:2  | same      | same          | same         | same             | same          | same              |
| `bash`   | split:3  | same      | same          | same         | same             | same          | same              |
| `bash`   | split:5  | same      | same          | same         | same             | same          | same              |
| `bash`   | split:8  | same      | same          | same         | same             | same          | same              |
| `bash`   | split:16 | same      | same          | same         | same             | same          | same              |
| `bash`   | split:32 | same      | same          | same         | same             | same          | same              |
| `htop`   | bytewise | same      | same          | same         | same             | same          | same              |
| `htop`   | split:2  | same      | same          | same         | same             | same          | same              |
| `htop`   | split:3  | same      | same          | same         | same             | same          | same              |
| `htop`   | split:5  | same      | same          | same         | same             | same          | same              |
| `htop`   | split:8  | same      | same          | same         | same             | same          | same              |
| `htop`   | split:16 | same      | same          | same         | same             | same          | same              |
| `htop`   | split:32 | same      | same          | same         | same             | same          | same              |
| `vim`    | bytewise | same      | same          | same         | same             | same          | same              |
| `vim`    | split:2  | same      | same          | same         | same             | same          | same              |
| `vim`    | split:3  | same      | same          | same         | same             | same          | same              |
| `vim`    | split:5  | same      | same          | same         | same             | same          | same              |
| `vim`    | split:8  | same      | same          | same         | same             | same          | same              |
| `vim`    | split:16 | same      | same          | same         | same             | same          | same              |
| `vim`    | split:32 | same      | same          | same         | same             | same          | same              |
| `less`   | bytewise | same      | same          | same         | same             | same          | same              |
| `less`   | split:2  | same      | same          | same         | same             | same          | same              |
| `less`   | split:3  | same      | same          | same         | same             | same          | same              |
| `less`   | split:5  | same      | same          | same         | same             | same          | same              |
| `less`   | split:8  | same      | same          | same         | same             | same          | same              |
| `less`   | split:16 | same      | same          | same         | same             | same          | same              |
| `less`   | split:32 | same      | same          | same         | same             | same          | same              |
| `wide`   | bytewise | same      | same          | same         | same             | same          | same              |
| `wide`   | split:2  | same      | same          | same         | same             | same          | same              |
| `wide`   | split:3  | same      | same          | same         | same             | same          | same              |
| `wide`   | split:5  | same      | same          | same         | same             | same          | same              |
| `wide`   | split:8  | same      | same          | same         | same             | same          | same              |
| `wide`   | split:16 | same      | same          | same         | same             | same          | same              |
| `wide`   | split:32 | same      | same          | same         | same             | same          | same              |
| `emoji`  | bytewise | diff(2)   | diff(19)      | same         | same             | diff(1)       | diff(3)           |
| `emoji`  | split:2  | same      | same          | same         | same             | same          | same              |
| `emoji`  | split:3  | same      | same          | same         | same             | same          | same              |
| `emoji`  | split:5  | same      | diff(4)       | same         | same             | same          | same              |
| `emoji`  | split:8  | diff(1)   | diff(8)       | same         | same             | same          | same              |
| `emoji`  | split:16 | diff(1)   | diff(14)      | same         | same             | same          | same              |
| `emoji`  | split:32 | diff(1)   | diff(16)      | same         | same             | same          | same              |

- **`libghostty-vt` holds everywhere**: identical screen at every split of every capture, one
  byte per write included.
- **`x/vt` holds on six of seven**, and fails on `emoji` exactly where §1.1 says it must: at
  every split that falls between two complete runes of a cluster (5, 8, 16, 32 parts, and
  bytewise), a partial cluster is flushed and the line shifts — 4 to 19 columns. Splits at 2
  and 3 land inside a rune and survive.
- **`xterm.js` is not fully chunk-invariant either**: fed one byte at a time it loses a
  character in two captures (`wizard` row 34, an `…`; `emoji` row 0, the family's trailing
  `U+200D`), geometry included in the first case. So byte-boundary sensitivity is not a
  property the product could have inherited by choosing xterm.js: it is the _reference's_ own
  limit, and `libghostty-vt` is the only one of the three that does not have it.

### 7.6 What the numbers support, and what they do not

**Supported.**

1. On the shapes `ADR-0041` measured — bash, htop, vim, less — and on CJK, `x/vt` and
   `libghostty-vt` are both **exactly** xterm.js: 4 800 of 4 800 columns. The geometry argument
   for `x/vt` is reproduced as a property of `x/vt`; it is not reproduced as a **discriminator**
   between the two candidates, because ghostty has the same property on the same bytes.
2. The only capture where the two candidates differ from each other on something other than a
   defect `x/vt`'s own filter already removes is `emoji`, and there `libghostty-vt` is closer to
   the product's terminal (97 of 100 content columns against 84). The emoji case is precisely
   the case `ADR-0041` records as untested.
3. `libghostty-vt`'s final screen is invariant to where the writes fall, up to one byte per
   write, on all seven captures; `x/vt`'s is not, on one, and `xterm.js`'s — the product's own
   frontend — is not either.
4. The `wizard` disagreement is `x/vt`'s known `0x9C` defect, not a geometry policy: ten
   columns, one line, a 15-byte reproduction, and a filter the product already ships.

**Not supported.**

1. **Nothing here compares with `ADR-0041`'s scores.** The bytes are different; a fresh 100/100
   on the same shapes is evidence about this corpus, not a re-run of that ADR's six captures.
   That ADR's exact question is still open on its own bytes, and those bytes are gone.
2. The separation between the candidates rests on **one** capture of ten lines, in which the
   family line alone is 8 of `x/vt`'s 17 disagreeing columns and all four emoji lines together
   are 17. One capture is a sample, not a distribution.
3. The `wizard` row is a first run against an endpoint that never answers: it measures the
   theme picker, the trust dialog, the main UI and the spinner, and no model output.
4. Colour, attributes, selection, graphics and performance are not in this corpus at all
   (§1.10 and §2.5 are the only graphics evidence here), and neither is the alternate screen
   as a state — only the bytes that enter it.
5. The `wide` row exercises CJK through `less` at 120 columns. Both candidates are exact; it
   says nothing about CJK in a narrower or reflowing terminal.

**Verdict: §4's condition is met — and it is met in the direction that weakens `x/vt`.** §4
asked for `ADR-0041`'s non-emoji shapes to be re-measured against `xterm.js` and said that if
ghostty's geometry matches there, the choice made on geometry should be revisited on geometry.
On this corpus's replacements for those shapes, ghostty matches `xterm.js` on every column
`x/vt` matches it on, and on the only shape where the two candidates diverge — emoji, untested
by that ADR — it is the closer one. There is no capture here on which `x/vt`'s columns are the
better ones, so the criterion that chose `x/vt` cannot be quoted against ghostty.

What that leaves is a smaller decision than either document asked for, and it is the owner's:
the geometry criterion is neutral between the two candidates on the workload `ADR-0041`
measured, and on this evidence the behavioural half of §4's case — ghostty failing one of the
nine probes where `x/vt` fails all nine, four of those four silently wrong — is no longer
balanced by a geometry advantage. The
one caveat worth carrying into that decision is that the sample that breaks the tie is a single
emoji capture, and the same corpus says both emulators put the same _characters_ on the screen
there (40/40 rows of text for both).

## Appendix A — the exact probe inputs

Every sequence below is a Go string literal as fed to `Write`/`vt_write`, with its bytes in
hex. `\x1b` is ESC (1b), `\a` is BEL (07). Where a probe drives an API rather than bytes, the
call is named instead.

| probe       | input                                                                                                       | bytes                                                                                  |
| ----------- | ----------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| 1           | `"\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"`                                              | `f09f91a8 e2808d f09f91a9 e2808d f09f91a7 e2808d f09f91a6` (25)                        |
| 1           | `"\U0001F44D\U0001F3FD"`                                                                                    | `f09f918d f09f8fbd` (8)                                                                |
| 1           | `"\U0001F1F7\U0001F1FA"`                                                                                    | `f09f87b7 f09f87ba` (8)                                                                |
| 1           | `"\u2764\uFE0F"`                                                                                            | `e29da4 efb88f` (6)                                                                    |
| 2           | `"a\u0301"`                                                                                                 | `61 cc81` (3)                                                                          |
| 3, 4        | no bytes in: `vt.KeyPressEvent{Code, Mod}` to `SendKey`, or `ghostty.EncodeKey(key, mods, kittyFlags, nil)` | output recorded in §1.3, §1.4                                                          |
| 5           | `"abcd"`, `"\x1b[1;1H"`, `"\x1b[4h"`, `"XY"`, `"\x1b[4$p"`                                                  | `61626364`, `1b5b313b3148`, `1b5b3468`, `5859`, `1b5b342470`                           |
| 6           | `"\x1b[5;10r"`, `"\x1b[?6h"`, `"\x1b[1;1H"`, `"\x1b[6n"`                                                    | `1b5b353b313072`, `1b5b3f3668`, `1b5b313b3148`, `1b5b366e`                             |
| 6 (control) | `"\x1b[5;10r"`, `"\x1b[5;1H"`, `"\x1b[6n"`                                                                  | `1b5b353b313072`, `1b5b353b3148`, `1b5b366e`                                           |
| 7           | `"A"`, `"\x1b[<n>b"`                                                                                        | `41`, `1b5b<n as decimal>62`                                                           |
| 8           | `"L01\r\n" … "L24"`, `"0123456789"×4 + "ABCDE"`, `"hello\r\nworld"`                                         | see §1.8                                                                               |
| 9           | `"\x1b]0;X\u2733Y\a"`, `"\x1b]0;X\u2733Y\x1b\\"`, `"\x1b]0;X\x9cY\a"`, `"\x1b]0;X-Y\a"`, then `"Z"`         | prefixes `1b5d303b58 e29cb3 59`, terminators `07` / `1b5c` / `1b` (`\x9c` bare) / `07` |
| 10          | `"\x1b[6n"`                                                                                                 | `1b5b366e`                                                                             |
| 11          | sixel `"\x1bPq\"1;1;2;2#0;2;0;0;0#0~~\x1b\\"`                                                               | `1b507122...1b5c`                                                                      |
| 11          | kitty `"\x1b_Ga=T,f=24,s=1,v=1,i=42;AAAA\x1b\\"`                                                            | `1b5f47 613d542c663d32342c... 1b5c`                                                    |
| 11          | control APC `"\x1b_private-command;payload\x1b\\"`                                                          | `1b5f 707269766174652d636f6d6d616e643b7061796c6f6164 1b5c`                             |

Terminal geometry, unless a probe says otherwise: 80×24 for probes 3, 4, 6, 7; 20×3 for 1, 2,
5, 11; 40×3 for 9; probe 8 sets its own. Scrollback is the library default except where the
probe passes `SetScrollbackSize`/`SetScrollbackLines`. `TERM` is irrelevant: neither driver
sets it, and neither emulator reads it.

## Appendix B — how each number was taken

- **Cells.** `x/vt`: `Emulator.CellAt(x, y)` on the focused screen, read as
  `{Content, Width}`; a cell with empty content prints `.`. `libghostty-vt`:
  `ghostty_terminal_grid_ref` → `ghostty_grid_ref_cell` → `GHOSTTY_CELL_DATA_HAS_TEXT` /
  `_WIDE`, with the cluster text from `ghostty_grid_ref_graphemes`, mapped to the same width
  vocabulary (narrow 1, wide 2, spacer 0). Rows are `%q`-quoted so a blank row and a row
  holding a space cannot be confused.
- **Replies.** `x/vt`: the driver runs one goroutine reading `Emulator.Read` into a buffer,
  and each probe takes everything accumulated (250 ms budget for the first byte, then 20 ms
  for stragglers). `libghostty-vt`: the `write_pty` effect appends during the write; `Reply()`
  concatenates and clears.
- **Wall time.** `time.Now()` / `time.Since` around the single `Write` call, no warm-up and no
  repetition averaging. This is a one-shot figure and §1.7 reports its observed spread across
  eight runs, including one taken under load, rather than pretending to a precision it does
  not have.
- **Allocation.** `runtime.ReadMemStats` before and after the region, with `runtime.GC()`
  before the first read. `TotalAlloc` and `Mallocs` are reported as deltas; the live heap is
  reported as the two absolute `HeapAlloc` values rather than as a signed difference, because
  a `uint64`→`int64` conversion of a difference is the kind of unchecked cast the repo's
  linter rejects and the delta carries no information the pair does not. Note what these
  numbers do and do not cover: `TotalAlloc` counts **Go** allocations only, so the `x/vt`
  column is the whole cost of its cell model while the ghostty column is only the Go side of
  a call whose real work happens in Zig — which is why the ghostty allocation figures are
  near-constant and small, and why the honest comparison in §1.7 is wall time plus the
  clamp, not bytes.
- **The reference (`xterm.js`).** Headless `@xterm/headless@5.5.0` with
  `@xterm/addon-unicode11@0.8.0` loaded and `unicode.activeVersion = '11'`, matching
  `frontend/package.json` and `frontend/src/renderers/xterm.ts`. Cells read via
  `buffer.active.getLine(y).getCell(x)` → `getChars()` / `getWidth()`. Output committed at
  `results/xtermjs-cases.txt` and `results/xtermjs-zwj.txt`.
- **The real terminal.** `xterm 410` under `Xvfb :94`, title read back with
  `xdotool getwindowname`. Recorded in §1.9 and §6.

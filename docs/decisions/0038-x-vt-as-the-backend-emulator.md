# ADR-0038 — charmbracelet/x/vt is the backend's emulator, chosen by column geometry

- **Status:** accepted (2026-08-25)
- **Serves:** the AD-6 amendment at `docs/architecture.md:151`, which permits a
  live VT grid in the backend for an enrolled pane and grants it exactly two
  powers. Both are POSITIONAL, which is what makes this a measurement about
  columns rather than about text.
- **Reads, does not change:** `AD-6`, [ADR-0001](0001-xterm-js-as-vt-frontend.md)
  (xterm.js owns VT state in the renderer, and is the ground truth here),
  [ADR-0002](0002-native-tabs-no-embedded-multiplexer.md).
- **Related:** `nocx-szb40.1` (the measurement), `nocx-szb40.2` (the grid),
  `nocx-szb40.3` (its first reader).

## Decision

**`github.com/charmbracelet/x/vt`.** It is the only candidate that reproduces
xterm.js's **column geometry** across a double-width character, and a chrome
anchor is a thing at a position.

That sentence is the whole decision. The rest of this document is how it was
measured and what the measurement does not cover, because the gaps are the
grounds on which it could be revisited.

> This ADR was `spike/vt-agreement/REPORT.md` until 2026-08-25. The spike's
> comparison tools were removed once the library choice was closed; the product
> now keeps the deliberately smaller `cmd/agent-capture` tool instead. It runs
> real programs on real PTYs, records the byte-only JSONL corpus format, and
> replays captures through `charmbracelet/x/vt` at the moments a driver needs to
> inspect. The committed corpus remains in
> `internal/agentdriver/testdata/captures/`, and the old spike is still in git
> history at `d3872462`.

## Method

Four tools in a nested Go module, deliberately separate so three candidate libraries stayed
out of the product's `go.mod`, out of `go list ./...` and out of the deadcode ratchet. The
module is gone; the tools are named here because the method is the part worth keeping.

| tool               | what it does                                                                                                               |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| `capture`          | runs a program on a real PTY, pins `TERM`/`LANG`/size, drives it from a script of timed keystrokes, records **bytes only** |
| `features`         | counts what VT constructs a capture exercises, without maintaining a grid                                                  |
| `render-xterm.mjs` | ground truth: `@xterm/headless@5.5.0` + `@xterm/addon-unicode11@0.8.0`                                                     |
| `render-go`        | the same bytes through each candidate, same output shape                                                                   |
| `diff`             | per-column comparison against ground truth, plus chrome-anchor checks                                                      |

**The ground truth is pinned to what the product ships.** `frontend/package.json` resolves
`@xterm/xterm@5.5.0`, and `frontend/src/renderers/xterm.ts:334` loads the unicode11 addon at
runtime. Without that addon the ground truth would account column widths differently from
the product it stands in for — which is the exact class of error this exercise exists to
find.

## Corpus

All real programs on a real PTY. Nothing synthesised.

| capture  | program                              | what it brings                                                                                                                                        |
| -------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `wizard` | `claude` first-run                   | **CHA ×123** (Claude positions per word instead of emitting spaces), truecolor SGR, box drawing, numbered menu with `❯`, animated spinner, input line |
| `bash`   | `bash -i` running `bd ready`         | prompt, bracketed paste, SGR                                                                                                                          |
| `htop`   | `htop`                               | alternate screen, DECSTBM, **CUP ×89**, **SGR ×599**                                                                                                  |
| `vim`    | `vim` on a repo source file          | alternate screen, DECSTBM, CUP                                                                                                                        |
| `less`   | `less` on `internal/transport/ws.go` | alternate screen, paging                                                                                                                              |
| `wide`   | `less -R +26 e2e/ime.spec.ts`        | **CJK**: `const MARKER = 'こんにちは'`                                                                                                                |

## Results

`text` is the concatenated characters; `geometry` is what each **column** holds, where the
second half of a wide character counts as a continuation.

| capture  | `hinshun/vt10x`                           | `tonistiigi/vt100`                         | `charmbracelet/x/vt`                      |
| -------- | ----------------------------------------- | ------------------------------------------ | ----------------------------------------- |
| wizard   | 100.00 / 100.00 · anchors 8/8 · cursor ok | 40.60 / 40.60 · anchors **0/8** · cursor ✗ | 100.00 / 100.00 · anchors 8/8 · cursor ok |
| bash     | 100.00 / 100.00 · anchors 1/1 · cursor ok | 90.42 / 90.42 · anchors 1/1 · cursor ✗     | 100.00 / 100.00 · anchors 1/1 · cursor ok |
| htop     | 100.00 / 100.00 · cursor ok               | 42.88 / 42.88 · cursor ok                  | 100.00 / 100.00 · cursor ok               |
| vim      | 100.00 / 100.00                           | 100.00 / 100.00                            | 100.00 / 100.00                           |
| less     | 100.00 / 100.00 · anchors 3/3             | 76.15 / 76.15 · anchors **1/3**            | 100.00 / 100.00 · anchors 3/3             |
| **wide** | 100.00 / **99.77**                        | 72.77 / 72.67                              | **99.83** / **100.00**                    |

### `tonistiigi/vt100` is out

It misses on every capture that has anything on the screen, misses **8 of 8 chrome anchors**
on the Claude capture including the input line, and puts the cursor off the grid entirely
(row 40 of a 40-row terminal, rows 0–39). Its own package documentation says it handles no
scrolling and misinterprets some control codes; the measurement agrees with the
documentation. It passes `vim` only because that capture's screen is simple enough for
everyone.

### The other two tie everywhere except one line, and that line decides it

Both are perfect on all five narrow captures. On the CJK line they diverge, and this is the
whole finding:

```
xterm.js   'こ'/w2   ''/w0    'ん'/w2   ''/w0     two columns per character
vt10x      'こ'/w1   'ん'/w1  'に'/w1   'ち'/w1   one column — everything after shifts LEFT
x/vt       'こ'/w2   ' '/w0   'ん'/w2   ' '/w0    two columns, continuation is a space
vt100      ' '/w1    ' '/w1   ' '/w1    ' '/w1    did not render the line
```

`vt10x`'s `Glyph` is `{Char rune; Mode int16; FG, BG Color}` — **there is no width field**,
so the information that a character occupied two columns is not merely wrong, it is absent
from the type. Read back as text it looks perfect, which is why the first version of the
diff scored it 100% and was blind to the thing that decides. `x/vt`'s miss is the opposite
shape: its column accounting is exactly xterm's, and it spells the continuation cell as a
space rather than an empty string, which any consumer normalises by skipping `w == 0`.

**One is a representation difference you can normalise. The other is missing information.**

`nocx-szb40.2` gives the grid exactly two powers: may nocx write to this pane, and what the
indicator shows. Both are positional — the driver asks whether the input marker is at a
place — so a renderer whose columns drift left on every wide character is wrong at precisely
the moment it is being trusted. Agent panes carry wide characters routinely: a filename, a
commit message, a user's own prose, an emoji in a tool result.

**In this corpus the disagreement does not itself land on a chrome anchor** — the CJK line
was an ordinary source line, and every anchor row was pure ASCII, so all three anchor counts
above are unaffected by it. That is worth saying plainly rather than implying otherwise: the
argument is not "it broke an anchor here", it is that anchors are addressed by column and
this candidate's columns are the only ones that cannot drift.

**The 0.23% gap is small because one line in one capture had CJK. The significance is
structural, not statistical** — the gap is the entire wide-character class, and it will be as
large as the content is.

## Two harness bugs, kept in the record

Both would have produced a confident wrong answer, and both were mine.

1. **`x/vt` was fed the wrong pipe.** `InputPipe()` is the _keyboard_ side; `Write` is the
   byte stream. With `InputPipe` the emulator returned a **completely empty grid**, which
   reported as-is would have been damning and false.
2. **Four of six captures compared blank screens.** `htop`, `vim` and `less` were captured
   _with_ their quit key, so the stream ended after `?1049l` and the frame under comparison
   was the restored screen — 0 non-blank rows out of 40. Everyone "agreed" on nothing. The
   captures were redone without quitting, so the stream ends inside the alternate screen.

A third is not a bug but worth writing down: `x/vt` **deadlocks** if nothing drains its
`Read` side. It replies upstream — device attributes and friends — and a real terminal
always has a reader. That is an integration requirement for `nocx-szb40.2`, not a defect.

## What this measurement does NOT cover

- **The braille spinner.** No capture contains one. Claude's own spinner in the first-run
  flow is `✽ ✻ ✶ * ✢ ·`, not braille.
- **NBSP before `[Pasted text #N]`.** The task names it from a nelix comment. It needs a
  real paste into an authenticated Claude session; this corpus was captured without one, so
  the hazard is untested rather than cleared.
- **OSC title.** No capture emits one, though live agent panes plainly set titles. The
  `declared-anonymous` row of the mechanism design depends on it.
- **A permission prompt mid-work**, `emoji`, and `scroll up/down` / `insert-delete lines`
  (`S`/`T`/`L`/`M`), which nothing in the corpus emitted.
- **Attributes.** Colour was collected but not scored; the comparison is characters,
  geometry and cursor. Truecolor SGR is exercised heavily (`SGR ×599` in htop) and only
  proves the parser consumed it, not that it stored it identically.

Each gap is a reason the choice could still be revisited, and none of them is a reason to
prefer a library whose type cannot express a column.

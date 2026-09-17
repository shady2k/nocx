# Frame bytes: what a backend-owned screen costs to deliver

Measurement 1 of §8 of
[`2026-09-12-the-backend-holds-the-session-screen-design.md`](../../specs/2026-09-12-the-backend-holds-the-session-screen-design.md),
which is the number §7 says can still move the plan.

The question is narrow and it is a byte question: if the backend owns the terminal emulator and
sends the client the cells that changed instead of the raw PTY bytes, is that cheaper, and by how
much? §7 corrects an earlier revision that claimed it always is — a positional encoder does not
recognise scrolling, so on a screen that scrolls it can cost _more_ than the bytes the program
wrote. That correction was an argument with two numbers in it (≈60 KB/s raw, ≈240 KB/s positional
on a 100×40 screen at 60 fps). This directory is that argument measured, plus the fourth number
§7 asks for: the content no delivered frame carried and a card fetch would have to bring later.

**One sentence about the emulator, so nobody reads it as an oversight:** the frames here are
produced from `github.com/charmbracelet/x/vt`, the emulator in the tree today, because ADR-0065
chooses `libghostty-vt` for the product on grounds of grapheme assembly, input encoding and query
replies — none of which changes how many _cells_ a frame carries, which is the only thing a byte
count depends on. `x/vt` also needs no CGo.

## How to reproduce

From a fresh clone, at the repository root:

```bash
cd .internal/spikes/framebytes
go vet ./... && go test ./...                                            # type check + round-trip proofs
gofumpt -l .                                                             # formatting
go run ./cmd/framebytes -fps "5,15,30,60,120,every" -out results/tables.md
```

The last command writes `results/tables.md` and prints it; **every table below is that file,
verbatim.** The sweep takes about 45 seconds wall and nothing in it is unbounded. The recorded
corpus is read from `../emulator/corpus` (read-only to this spike, `-corpus` to move it), and the
hero capture is generated in-process — the rule is stated below and its SHA-256 is in the tables,
so the same bytes can be regenerated and checked.

Measured on the machine that produced this report:

```
$ uname -a
Linux vm-agents 6.18.48 #1-NixOS SMP PREEMPT_DYNAMIC Fri Aug 28 06:22:54 UTC 2026 x86_64 GNU/Linux
$ go version
go version go1.26.7 linux/amd64
$ nproc
6
CPU: AMD Ryzen 5 8600G w/ Radeon 760M Graphics
```

## The byte accounting

A number whose encoding is not stated is not a measurement. Every byte count below is the length
of a stream built by these rules, and they are also asserted in `accounting_test.go` so that the
prose and the encoder cannot drift apart:

1. **RAW** is the baseline nocx ships today: the sum of the recorded writes, i.e. exactly what the
   renderer receives over the binary data plane. No accounting is applied to it at all.
2. **A frame is a cell diff.** The encoder holds the client's screen, compares it cell by cell
   against the target frame, and emits only the cells that differ. A frame that changes nothing
   emits nothing.
3. **A cell is a grapheme plus a style** — the full `uv.Cell`: content, width, foreground,
   background, underline colour, underline style, attributes. A wide grapheme is emitted once and
   covers both its columns; the tail column is never emitted and never counted against a repaint.
4. **Cursor move**: absolute CUP — `\x1b[H` for the origin, `\x1b[<row>H` when the column is 1,
   otherwise `\x1b[<row>;<col>H`. One CUP precedes each run whose first cell is not already under
   the cursor. **No relative cursor moves are used**, which is a real encoder's next saving and is
   _not_ taken here.
5. **Content**: the grapheme's bytes, and one space per blank cell. **No erase-in-line (`ECH`), no
   repeat (`REP`), no insert/delete** — a run of blanks costs one byte each. All three would
   reduce the positional and scroll-aware numbers below, so this choice inflates frames rather
   than flattering them.
6. **Style**: a terminal's pen. A run whose style differs from the pen emits a reset (`\x1b[m`)
   followed by the full SGR for the new style; a run that continues the pen's style emits nothing.
   Styles are never diffed parameter by parameter — again, an inflation, not a discount. A fresh
   client's pen is the default style; after a screen switch the pen is re-asserted, because that
   is what a terminal's save/restore leaves to the implementation.
7. **Scroll**: one scroll-up, `\x1b[<k>S`, emitted only when the target looks like the baseline
   shifted up by k rows _and_ the resulting stream is shorter than the plain diff. See the
   fidelity notes for why the first condition is a proposal rather than a proof.
8. **Screen**: one `\x1b[?1049h` or `\x1b[?1049l` when the alternate screen is entered or left;
   the entering frame repaints the cleared screen.
9. **Cursor**: at most one final CUP per frame, to leave the caret where the producing terminal has
   it. A caret in the phantom-wrap column is treated as the last column.
10. **No compression layer is counted** — this measures the protocol. A deflate measurement is
    reported separately below, because "the stream compresses well" is a different claim.
11. **No transport framing is counted either**: no length prefix, no JSON envelope, no WebSocket
    header. Frames with no content cost zero here and would cost a header in the product.
12. **OMITTED** is the fourth number: every distinct row content the emulator held at some instant
    that appears in no delivered frame, and which would therefore have to arrive later through a card
    fetch. It is priced as card lines — the line's cells from column 0 with the accounting above,
    then CRLF — and counted once per distinct content, because a card carries a line once rather than
    once per redraw. Blank rows are skipped: there is nothing to recover. It is a **union of two
    sources**, because neither alone sees all of that content: the screen at every write boundary,
    and the scrollback, which catches the lines that scrolled off _inside_ a single write and were
    therefore never a state any frame could be compared against. The tables also carry the **retained**
    part of it — the content that entered retained history, which is what a card actually carries. The
    difference is content overwritten in place, which no terminal retains: a fidelity loss, not a card
    byte. Retained is always a subset of OMITTED, and `TestRetainedIsSubsetOfOmitted` asserts that
    across every capture at three rates.

The encoder is also its own client model, and that is checked the hard way. `TestRoundTripCorpus`
replays all seven recorded captures and the hero, at 30 fps, 60 fps and one frame per write, with
both the positional and the scroll-aware encoder, applies each emitted frame to a **fresh `x/vt`
emulator**, and requires that emulator's screen — every cell, its width, its style, and which of
the two screens it is on — to equal the producing terminal's screen after every delivered frame.
An encoder that emits too few bytes because it is wrong fails there rather than hiding inside the
report.

## The captures

Every recorded capture is the committed corpus at 120×40. The hero case is generated; the rule is
in the fidelity notes.

## The hero case, first

The case §7's argument was made about: a 100×40 terminal, 600 distinct full-width lines a second,
presented at 60 frames a second. There is no recording of it, so it is generated — the rule is in the
fidelity notes, and the digest below is there so a reader can check their generation against this one.

The generated capture: 100×40, 3000 lines at 600 lines/s, one write per line, deterministic xorshift
content. SHA-256 of its byte stream:
`b8b748a2a8ea97922de5f2e73967942ee14dd9f4f687878d1ed5d07807356292`.

Raw output is 306000 bytes over 4.998 s = **61224 B/s**, which is the
document's ≈60 KB/s to three figures.

The document's other figure was ≈240 KB/s for a positional diff rewriting ~4,000 cells per frame.
Measured: **285641 B/s = 4.67× raw output** at 60 fps. The argument
holds, and it was, if anything, generous to the encoder — ≈240 KB/s was an under-estimate by about
19%.

The rate sweep is where the shape is, because coalescing wins when the producer far outruns the
display:

| frame rate  | frames | RAW B  | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW  | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ------ | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ----- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 25     | 306000 | 119032       | 0.39     | 119032         | 0.39        | 212625    | 331657        | 1.08  | 331657           | 1.08 | 0          | 2025         | 212625     | 2025          |
| 15 fps      | 75     | 306000 | 357155       | 1.17     | 357155         | 1.17        | 7875      | 365030        | 1.19  | 365030           | 1.19 | 0          | 75           | 7875       | 75            |
| 30 fps      | 150    | 306000 | 713777       | 2.33     | 316481         | 1.03        | 0         | 713777        | 2.33  | 316481           | 1.03 | 149        | 0            | 0          | 0             |
| 60 fps      | 300    | 306000 | 1427636      | 4.67     | 317960         | 1.04        | 0         | 1427636       | 4.67  | 317960           | 1.04 | 297        | 0            | 0          | 0             |
| 120 fps     | 600    | 306000 | 2840622      | 9.28     | 320329         | 1.05        | 0         | 2840622       | 9.28  | 320329           | 1.05 | 593        | 0            | 0          | 0             |
| every write | 3000   | 306000 | 14162092     | 46.28    | 341641         | 1.12        | 0         | 14162092      | 46.28 | 341641           | 1.12 | 2961       | 0            | 0          | 0             |

### hero, per second

| frame rate  | RAW B/s | POSITIONAL B/s | SCROLL-AWARE B/s | POS+OMITTED B/s | SCROLL+OMITTED B/s |
| ----------- | ------- | -------------- | ---------------- | --------------- | ------------------ |
| 5 fps       | 61224   | 23816          | 23816            | 66358           | 66358              |
| 15 fps      | 61224   | 71460          | 71460            | 73035           | 73035              |
| 30 fps      | 61224   | 142813         | 63322            | 142813          | 63322              |
| 60 fps      | 61224   | 285641         | 63617            | 285641          | 63617              |
| 120 fps     | 61224   | 568352         | 64091            | 568352          | 64091              |
| every write | 61224   | 2833552        | 68356            | 2833552         | 68356              |

_Read this way, four things are true._ A positional encoder is **worse than raw output at 15 fps and
above**, and the multiple grows with the frame rate — 1.17× at 15 fps,
2.33× at 30, 4.67× at 60, 9.28× at 120, and
46.28× at one frame per write (14,162,092 bytes against
306,000). A **scroll-aware encoder is not a byte win either**: wherever it recognises the
scroll it lands at 1.03× raw output at 30 fps, 1.04× at the 60 fps this report is written around, and
1.12× at one frame per write — break-even plus a few percent — and at 15 fps, where it recognises
nothing and is the positional encoder, at 1.17×. The whole sweep is therefore 1.03×–1.17× raw
output, because the rows it must send are exactly the rows the program wrote and each one costs a
cursor move the raw stream did not need. What scroll-awareness buys is _removing the positional penalty_ — a factor of
4.5 at 60 fps,
8.8 at 120 — not beating raw output.

Below 30 fps the scroll-aware encoder emits **no scroll operation at all** (0 and
0 in the `scroll ops` column): forty lines arrive between two frames while the
screen holds thirty-nine rows, so the whole screen is new content and no row of the target matches any
row of the baseline. That is not a loss — a frame with thirty-nine new rows to paint costs the same
either way — but it is why the two encoders are identical there.

The fourth number is what charges frames for what they dropped. At 15 fps, 7875 bytes in
75 rows were drawn and replaced between delivered frames and appear in no frame: one
full line per frame interval, which is exactly the marginal line a 39-row screen cannot hold. At 5 fps
the omission is 212,625 bytes in 2025 rows —
69% of everything the program wrote — and a card
fetch brings the total to **1.08× raw output**, against 0.39× live.
At 30 fps and above nothing is omitted at all, and every omitted row at every rate is retained
(2025 of 2025 rows at 5 fps), so for a pure scrolling producer the
card cost and the omission are the same number.

| capture | geometry | writes | duration | RAW bytes |
| ------- | -------- | ------ | -------- | --------- |
| bash    | 120×40   | 283    | 1591 ms  | 76923     |
| emoji   | 120×40   | 3      | 1 ms     | 162       |
| htop    | 120×40   | 48     | 7629 ms  | 8398      |
| less    | 120×40   | 2      | 2 ms     | 1554      |
| vim     | 120×40   | 9      | 47 ms    | 1617      |
| wide    | 120×40   | 3      | 2 ms     | 1678      |
| wizard  | 120×40   | 131    | 31414 ms | 15008     |
| hero    | 100×40   | 3000   | 4998 ms  | 306000    |

## The recorded corpus

Every capture is a real program on a real PTY at 120×40, recorded by `../emulator/corpus/record.sh`.
Read these against the producer's shape, because that is what decides the comparison, and read the last
two columns against the fourth: **retained is the part of OMITTED a card actually carries.**

**`bash` is the burst case, and it is §7's warning made real.** Its 283 writes span 1.6 seconds. At
60 fps only 96 frames are delivered, they carry 15,396 bytes live —
**0.20× raw output**, a five-fold win on live delivery — and **70,007 bytes in
653 rows were drawn and replaced between frames**, of which 69,956 bytes in
652 rows are retained scrollback a card must carry. Total: **1.11× raw output**
(POSITIONAL) and **1.11×** (SCROLL-AWARE). The frame protocol does not win here; it costs
11% more than the bytes the program wrote. At 5–30 fps
it is the same story with fewer frames and slightly more omitted (0.11× live,
1.08× total, 693 rows).

Its `every write` row is where the union matters most: 1600153 bytes positional against
159668 scroll-aware (a factor of 10), over 283
frames and 265 scroll operations, and 51 bytes in 1 row that no delivered
frame carried — a line that scrolled off inside a single write, which only the scrollback source can
see, since at one frame per write no _state_ can be missed. That is also the largest relative win in
this report, and it is a win against _the positional encoder_, not against raw output.

**`htop` is the incremental TUI, and it is where frames lose on the live stream too.** 1.37×
raw output at 60 fps over 458 frames. Its omitted content is 1,705 bytes in
24 rows and **none of it is retained** (0 bytes): htop runs on the alternate screen
and redraws in place, so what a frame missed is content no terminal kept and **no card can carry**. The
honest total for a card-carrying client is therefore its live number, 1.37×, and the
1.58× in the POS+OMITTED column is the price of a fidelity loss rather than of a fetch. htop
also writes the whole screen itself in cursor-addressed runs: the raw stream is already a diff, and a
tighter one than a cell diff, because htop knows what it changed while the encoder has to rediscover it
cell by cell and pay a style re-assertion on every run. Nothing scrolls (0 scroll
operations), so scroll-awareness changes nothing here.

**`wizard` is Claude Code's first run: the most frames of any capture and the closest thing to
break-even.** 1885 frames at 60 fps over a 31-second capture, delivering 14,928
bytes against 15,008 raw — **0.99× raw output** — and 0 rows omitted. A
spinner and a streaming response repaint a few cells per frame, and a frame that changes nothing costs
nothing. This is the shape a frame protocol is for: no bytes lost, no screen re-sent, and a handful of
cells for the client to apply.

**`vim`, `less`, `wide` and `emoji` are one-frame captures** — the program drew and the capture ended
inside the first frame interval. They are the "attach and see the screen" case: `pos /raw` of
0.87, 0.84, 1.42 and 1.45 respectively. Three are _below_ raw
output, because a full-screen paint in cells can be cheaper than the escape-sequence soup a program
spends to get there; `wide` is above it, which is the wide-grapheme and attribute tax showing up in a
single frame.

**No capture but the burst omits more than a rounding error**: 85 bytes in 3 rows
for `vim`, 1705 bytes in 24 rows for `htop` (unretained — see above), and 0 for the
rest.

## Per-capture tables

Each table below is the generated table for one capture, verbatim, at the six sweep points.

### bash

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW  | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ----- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 8      | 76923 | 8553         | 0.11     | 8553           | 0.11        | 74610     | 83163         | 1.08  | 83163            | 1.08 | 0          | 693          | 74559      | 692           |
| 15 fps      | 24     | 76923 | 8571         | 0.11     | 8571           | 0.11        | 74531     | 83102         | 1.08  | 83102            | 1.08 | 0          | 692          | 74480      | 691           |
| 30 fps      | 48     | 76923 | 8571         | 0.11     | 8571           | 0.11        | 74531     | 83102         | 1.08  | 83102            | 1.08 | 0          | 692          | 74480      | 691           |
| 60 fps      | 96     | 76923 | 15396        | 0.20     | 15396          | 0.20        | 70007     | 85403         | 1.11  | 85403            | 1.11 | 0          | 653          | 69956      | 652           |
| 120 fps     | 191    | 76923 | 15396        | 0.20     | 15396          | 0.20        | 70007     | 85403         | 1.11  | 85403            | 1.11 | 0          | 653          | 69956      | 652           |
| every write | 283    | 76923 | 1600153      | 20.80    | 159668         | 2.08        | 51        | 1600204       | 20.80 | 159719           | 2.08 | 265        | 1            | 51         | 1             |

### htop

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 39     | 8398  | 11536        | 1.37     | 11536          | 1.37        | 1705      | 13241         | 1.58 | 13241            | 1.58 | 0          | 24           | 0          | 0             |
| 15 fps      | 115    | 8398  | 11536        | 1.37     | 11536          | 1.37        | 1705      | 13241         | 1.58 | 13241            | 1.58 | 0          | 24           | 0          | 0             |
| 30 fps      | 229    | 8398  | 11536        | 1.37     | 11536          | 1.37        | 1705      | 13241         | 1.58 | 13241            | 1.58 | 0          | 24           | 0          | 0             |
| 60 fps      | 458    | 8398  | 11536        | 1.37     | 11536          | 1.37        | 1705      | 13241         | 1.58 | 13241            | 1.58 | 0          | 24           | 0          | 0             |
| 120 fps     | 916    | 8398  | 11536        | 1.37     | 11536          | 1.37        | 1705      | 13241         | 1.58 | 13241            | 1.58 | 0          | 24           | 0          | 0             |
| every write | 48     | 8398  | 11748        | 1.40     | 11748          | 1.40        | 0         | 11748         | 1.40 | 11748            | 1.40 | 0          | 0            | 0          | 0             |

### wizard

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 158    | 15008 | 12270        | 0.82     | 12270          | 0.82        | 1663      | 13933         | 0.93 | 13933            | 0.93 | 0          | 16           | 390        | 3             |
| 15 fps      | 472    | 15008 | 14275        | 0.95     | 14275          | 0.95        | 390       | 14665         | 0.98 | 14665            | 0.98 | 0          | 3            | 390        | 3             |
| 30 fps      | 943    | 15008 | 15169        | 1.01     | 14655          | 0.98        | 0         | 15169         | 1.01 | 14655            | 0.98 | 1          | 0            | 0          | 0             |
| 60 fps      | 1885   | 15008 | 15442        | 1.03     | 14928          | 0.99        | 0         | 15442         | 1.03 | 14928            | 0.99 | 1          | 0            | 0          | 0             |
| 120 fps     | 3770   | 15008 | 15442        | 1.03     | 14928          | 0.99        | 0         | 15442         | 1.03 | 14928            | 0.99 | 1          | 0            | 0          | 0             |
| every write | 131    | 15008 | 15442        | 1.03     | 14928          | 0.99        | 0         | 15442         | 1.03 | 14928            | 0.99 | 1          | 0            | 0          | 0             |

### vim

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 1      | 1617  | 1405         | 0.87     | 1405           | 0.87        | 85        | 1490          | 0.92 | 1490             | 0.92 | 0          | 3            | 0          | 0             |
| 15 fps      | 1      | 1617  | 1405         | 0.87     | 1405           | 0.87        | 85        | 1490          | 0.92 | 1490             | 0.92 | 0          | 3            | 0          | 0             |
| 30 fps      | 2      | 1617  | 1405         | 0.87     | 1405           | 0.87        | 85        | 1490          | 0.92 | 1490             | 0.92 | 0          | 3            | 0          | 0             |
| 60 fps      | 3      | 1617  | 1405         | 0.87     | 1405           | 0.87        | 85        | 1490          | 0.92 | 1490             | 0.92 | 0          | 3            | 0          | 0             |
| 120 fps     | 6      | 1617  | 1411         | 0.87     | 1411           | 0.87        | 39        | 1450          | 0.90 | 1450             | 0.90 | 0          | 2            | 0          | 0             |
| every write | 9      | 1617  | 1427         | 0.88     | 1427           | 0.88        | 0         | 1427          | 0.88 | 1427             | 0.88 | 0          | 0            | 0          | 0             |

### less

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 1      | 1554  | 1312         | 0.84     | 1312           | 0.84        | 0         | 1312          | 0.84 | 1312             | 0.84 | 0          | 0            | 0          | 0             |
| 15 fps      | 1      | 1554  | 1312         | 0.84     | 1312           | 0.84        | 0         | 1312          | 0.84 | 1312             | 0.84 | 0          | 0            | 0          | 0             |
| 30 fps      | 1      | 1554  | 1312         | 0.84     | 1312           | 0.84        | 0         | 1312          | 0.84 | 1312             | 0.84 | 0          | 0            | 0          | 0             |
| 60 fps      | 1      | 1554  | 1312         | 0.84     | 1312           | 0.84        | 0         | 1312          | 0.84 | 1312             | 0.84 | 0          | 0            | 0          | 0             |
| 120 fps     | 1      | 1554  | 1312         | 0.84     | 1312           | 0.84        | 0         | 1312          | 0.84 | 1312             | 0.84 | 0          | 0            | 0          | 0             |
| every write | 2      | 1554  | 2009         | 1.29     | 1330           | 0.86        | 0         | 2009          | 1.29 | 1330             | 0.86 | 1          | 0            | 0          | 0             |

### wide

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 1      | 1678  | 2376         | 1.42     | 2376           | 1.42        | 0         | 2376          | 1.42 | 2376             | 1.42 | 0          | 0            | 0          | 0             |
| 15 fps      | 1      | 1678  | 2376         | 1.42     | 2376           | 1.42        | 0         | 2376          | 1.42 | 2376             | 1.42 | 0          | 0            | 0          | 0             |
| 30 fps      | 1      | 1678  | 2376         | 1.42     | 2376           | 1.42        | 0         | 2376          | 1.42 | 2376             | 1.42 | 0          | 0            | 0          | 0             |
| 60 fps      | 1      | 1678  | 2376         | 1.42     | 2376           | 1.42        | 0         | 2376          | 1.42 | 2376             | 1.42 | 0          | 0            | 0          | 0             |
| 120 fps     | 1      | 1678  | 2376         | 1.42     | 2376           | 1.42        | 0         | 2376          | 1.42 | 2376             | 1.42 | 0          | 0            | 0          | 0             |
| every write | 3      | 1678  | 4445         | 2.65     | 2430           | 1.45        | 0         | 4445          | 2.65 | 2430             | 1.45 | 1          | 0            | 0          | 0             |

### emoji

| frame rate  | frames | RAW B | POSITIONAL B | POS /RAW | SCROLL-AWARE B | SCROLL /RAW | OMITTED B | POS+OMITTED B | /RAW | SCROLL+OMITTED B | /RAW | scroll ops | omitted rows | retained B | retained rows |
| ----------- | ------ | ----- | ------------ | -------- | -------------- | ----------- | --------- | ------------- | ---- | ---------------- | ---- | ---------- | ------------ | ---------- | ------------- |
| 5 fps       | 1      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |
| 15 fps      | 1      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |
| 30 fps      | 1      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |
| 60 fps      | 1      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |
| 120 fps     | 1      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |
| every write | 3      | 162   | 235          | 1.45     | 235            | 1.45        | 0         | 235           | 1.45 | 235              | 1.45 | 0          | 0            | 0          | 0             |

## Compression, measured separately

Not part of the accounting and not the number §7 argues about, but the two obvious follow-ups are "is
the positional cost an artefact of an uncompressed stream" and "does compression close the gap", and
both deserve a measurement rather than a guess.

Deflate at its default level over each whole stream, at 60 fps. Not part of the accounting above, and here because of two follow-up questions: whether the positional encoder's cost is an artefact of an uncompressed stream, and whether compression closes the gap between raw output and frames. A deflated byte is a different claim from the one §7 is arguing about, so this table decides neither; it is reported so nobody has to guess.

| capture | RAW B  | RAW deflated  | POSITIONAL deflated | SCROLL-AWARE deflated | deflated POS /RAW | deflated SCROLL /RAW |
| ------- | ------ | ------------- | ------------------- | --------------------- | ----------------- | -------------------- |
| bash    | 76923  | 28913 (0.38)  | 7160 (0.09)         | 7160 (0.09)           | 0.25              | 0.25                 |
| emoji   | 162    | 113 (0.70)    | 171 (1.06)          | 171 (1.06)            | 1.51              | 1.51                 |
| htop    | 8398   | 2744 (0.33)   | 3877 (0.46)         | 3877 (0.46)           | 1.41              | 1.41                 |
| less    | 1554   | 369 (0.24)    | 417 (0.27)          | 417 (0.27)            | 1.13              | 1.13                 |
| vim     | 1617   | 601 (0.37)    | 472 (0.29)          | 472 (0.29)            | 0.79              | 0.79                 |
| wide    | 1678   | 764 (0.46)    | 1174 (0.70)         | 1174 (0.70)           | 1.54              | 1.54                 |
| wizard  | 15008  | 3011 (0.20)   | 3553 (0.24)         | 3508 (0.23)           | 1.18              | 1.17                 |
| hero    | 306000 | 202481 (0.66) | 337334 (1.10)       | 204160 (0.67)         | 1.67              | 1.01                 |

The hero line is the answer. Deflating at 60 fps takes raw output from 306,000
bytes to 202481 (0.66), the positional frame stream from 1,427,636 to
337334 (1.10), and the scroll-aware stream to 204160 (0.67). **Compression does not rescue the
positional encoder**: deflated, it is still 1.67 of deflated raw output, while the
scroll-aware stream lands at 1.01 of deflated raw output. The one case where compression
changes a verdict is `bash`, whose live frames deflate to 0.25 of deflated raw output —
and whose total still pays for the omitted rows.

## The answer §7 asks for

**Is a scroll-aware encoder necessary? Yes — but not because it makes frames cheaper than raw
output.** Without it a full-scroll producer costs 2.33×–46.28×
raw output at 30 fps and above, growing with the frame rate: the hero at 60 fps is
1,427,636 bytes against 306,000. With it, the same case costs
1.04× raw. So the encoder is load-bearing for _not being pathological_, and §7 is
right that it is ours to build and more than herdr's encoder does. It does not make frames cheaper than
raw output; it makes them cost about what raw output costs, plus a cursor move per row and a frame's
own overhead.

**Is a frame protocol cheaper than raw output on this corpus? It depends, and here is what it depends
on.**

1. **Whether the card's retained output is counted.** That is the whole of §7's second correction, and
   it decides `bash`: 0.20× raw live, 1.11× raw once the 70,007 bytes
   no frame carried are fetched — and 69,956 of those bytes are retained scrollback, so
   that total is real card traffic. Counted honestly, at 60 fps the total ranges from
   **0.84× raw output** (less, a single screen) to **1.58×** (htop, of which only
   1.37× is card traffic), with the hero at 1.04× and the burst at
   1.11×. Across the whole sweep the cheapest total is 0.84× (less, 5 fps) and
   the dearest 2.08× (bash, every write).
2. **The producer's shape.** An incremental TUI (`htop`) is already a diff and a cell-level frame
   protocol re-derives it more expensively than the program stated it: 1.37× raw on the live
   stream alone. A burst producer (`bash`) is where live frames look spectacular and total delivery is
   not. A full-scroll producer (`hero`) is where a positional encoder is 4.67× raw and
   a scroll-aware one is break-even. A slow repainter (`wizard`) is where frames are cheapest and
   everything is within a few percent of raw anyway.
3. **The frame rate against the producer's rate.** Below the producer's rate the difference is either
   omitted content or wasted repaints, and the two encoders converge: `hero` at 5 and 15 fps has no
   scroll to recognise, and its total exceeds raw output (1.08× and
   1.19×).

**What this measurement does not support is the sentence "frames cost fewer bytes than raw output".**
Some captures are cheaper — `less` at 0.84×, `vim` at 0.92×, `wizard` at
0.99× — and several are not: a scrolling producer at 60 fps costs 1.04× raw
even scroll-aware, the burst costs 1.11×, and an incremental TUI costs 1.37×, rising
to 1.58× if content no card can carry is counted as if a card carried it. So the honest form
of the sentence is **"it depends, and on this corpus the frame protocol is not systematically cheaper
than raw output"** — with the encoder forced to be scroll-aware, without which a full-scroll producer
is 4.67× raw. The frame protocol's real product is not bandwidth but **the backend
holding the screen**: the caret, the current cells, the state a program's own query is answered from,
and a client whose cost per update is a few cells rather than a stream. Those are the reasons §1 and §2
give for the decision and this measurement does not touch them. What it does say is that §7 should keep
its correction and extend it: the encoder must be scroll-aware, and even then the plan should not
promise a bandwidth win.

## What this did not measure

Named so nobody reads this report as covering them. Measurements 2–5 of §8 are not mine:

- **Backend memory and CPU per session with an emulator, at real tab counts** (measurement 2).
- **First-frame size and latency for an attaching client** (measurement 3). Every number here assumes
  a client that attached at the start of the capture and stayed; an attaching client needs a full
  snapshot plus whatever retained history the card holds, and neither is counted.
- **The client's cost of applying a diff against parsing raw output** (measurement 4). Byte counts say
  nothing about it either way.
- **The backend's cost of capture, storage and per-client diffing** (measurement 5). The encoder here
  is not optimised for speed and it makes two passes over every capture.

Also outside this report:

- **Graphics and sixel.** They are not changed cells, this corpus has none, and nothing here counts
  the separate payload §7 says herdr carries them as.
- **Terminals much wider than 120 columns or deeper than 40 rows.** The corpus is 120×40 and the hero
  is 100×40; a very wide terminal multiplies cell counts and a scrolling one proportionally more, but
  it is not measured.
- **Colour and attribute density as a controlled variable.** The corpus carries what it carries —
  1,217 reversed cells in htop, 1,007 styled cells in vim, 428 foreground and 131 background cells in
  wizard — and the style accounting is exercised by them but not swept.
- **Transport framing.** No length prefix, no JSON envelope, no WebSocket header, no per-frame
  heartbeat: a frame that changes nothing costs 0 bytes here and would cost a header in the product.
  At 60 fps on an idle screen that is the difference between 0 B/s and a few hundred.
- **The card's real wire format** (§6.3), priced here as one rendered line plus CRLF.
- **Compression in the product**, which appears above only as a note.
- **The retention policy.** OMITTED counts content no frame carried; what a card carries depends on
  retention and scrollback policy, and this report assumes it is the retained output complete.
- **Multi-client fan-out**, where one produced frame is delivered to several clients.

## Fidelity notes

**The hero capture is generated, and this is the rule.** 100 columns × 40 rows; one chunk per line; 600
lines a second, so line i arrives at **floor**(i × 1000/600) ms — the integer-millisecond positions of
that rate, so a one-second run's 600 lines span 0…998 ms; each line is 100 characters drawn from a
deterministic xorshift seeded by the line index, then CRLF, so successive lines are full-width
_distinct_, which is what "600 distinct full-width lines per second" names and what stops a positional
encoder being flattered by content that repeats; 102 raw bytes per line, i.e. 61224 B/s,
the ≈60 KB/s the document quotes. `hero_test.go` asserts the geometry, the rate, the full-width rows,
the digest and that no two lines repeat.

**A terminal that scrolled does not hold a grid shifted by k.** The bottom row is the blank row the
cursor sits on after the last newline: the hero's screen holds 39 lines of text and one blank row. A
shift of 10 rows therefore explains 29 of the 30 rows it could, and an encoder that demands an exact
whole-grid shift recognises no scroll at all — which is what the first version of this spike did, and it
reported scroll-aware identical to positional on every capture. The encoder now scores every candidate
shift by how many non-blank rows of the target it explains, takes the best, and then decides on
**bytes**: it emits the scroll only when the scroll stream is shorter than the plain diff. Correctness
never rests on that proposal — whatever k is chosen, the diff that follows is computed against the
client's real screen, and the round-trip tests prove the result.

**A card line is priced by the same rule a terminal uses to keep one.** Trailing padding is trimmed —
but a blank that carries a style is _not_ padding: x/vt's scrollback keeps it, so it is part of the line
and is priced, which is why the styled rows of `htop` and `wizard` cost a little more here than a
content-only trim would say. `TestStyledTrailingBlanksArePriced` holds that rule.

**OMITTED's two sources, and the direction each is wrong in.** The screen at a write boundary cannot
see content that scrolled off inside a single write, which is why the scrollback source is part of the
number: on the `bash` burst at one frame per write that source is the _only_ one that finds anything
(1 row, 51 bytes), and its stream there is 2.08× raw. The scrollback
source in turn keeps only the normal screen, which is why `htop`'s 24 omitted rows are
retained-zero: lines overwritten in place are not retained, and a card cannot carry what a terminal did
not keep. Neither source can be inflated by a line it cannot identify: the scrollback pass counts
pushed lines by index and skips a push whenever the buffer is at its maximum (no capture here comes
near the 10,000-line default, which `TestScrollbackNeverReachesItsMaximum` checks rather than assumes),
and both passes dedupe by content — `TestRetainedDeduplicatesRepeatedLines` pushes the same two lines
sixty times and requires exactly two priced rows.

**Nothing is claimed for retries**, for a client that attaches late, or for a client that scrolls back:
each would add card bytes counted nowhere here.

**Alternate-screen state is carried** (`\x1b[?1049h` / `\x1b[?1049l`, with a repaint of the cleared
screen), because four of the seven captures use it and a frame protocol that cannot express it cannot
show vim. Cursor visibility, the window title, mouse modes and the rest of §6.2's non-visual effects are
**not** carried: they are delivered by another path and cost nothing in these counts.

**The four numbers are exactly the four §7 asks for**, and the two totals beside them are
`POSITIONAL + OMITTED` and `SCROLL-AWARE + OMITTED`. Raw output has no omitted bytes by construction —
every byte the program wrote is delivered — which is why the totals are the honest comparison and the
live columns alone are not. The `omitted rows` column is the number of distinct rows those bytes are
made of, and `retained B` / `retained rows` are the part of them a card actually carries.

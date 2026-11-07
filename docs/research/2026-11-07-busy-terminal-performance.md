# Active-output tabs: CPU and RAM diagnosis

Preparation task: `nocx-3an1s.1.11`. Measurements: 2026-11-07 on
`f49860272193af2bce04745c691b0ad621252092`. Preparation base:
`cc53f3b844468c58320f3aad2d6126ee7faddab3`; the investigated product paths are
unchanged between those revisions. This is evidence, not a claim that fixes shipped.

The owner means tabs **actively producing screen output**, not idle open tabs.
The proposed work and acceptance scenarios are in the
[preparation design](../../.internal/specs/2026-11-07-busy-terminal-performance-design.md).
[Portable measurements](2026-11-07-busy-terminal-measurements.json) include component
results, representative retained-row replay results, native model controls and the
renderer PSS/CPU time series. Backend profile filenames in that data identify the
original archived profiles; the large raw profiles are not checked into this PR.

## What was measured

Native: actual Wails application, production frontend, coordinator, helper and PTYs;
Linux x86_64, Ryzen 5700X, 16 logical CPUs, WebKitGTK 2.54.2, GTK 3.24.52.
Isolated HOME/XDG directories; synthetic output only. CPU 100% means one logical
CPU, not the whole machine. PSS accounts for shared pages; renderer PSS is not total
product memory. No macOS or native trusted-key latency result is claimed.

### Repeated retained-output work

A real coordinator supplied 2708 captured rows. Chromium replay used the existing
parser, painter and settle path, a stable artifact ID, and 60 updates replacing
12 rows each. Instrumentation adds overhead; nested timings are not additive.

| Retained rows | Following | Parse JSONL, ms | Reconcile/paint changed rows, ms | Scroll-to-bottom, ms |
| ------------: | :-------: | --------------: | -------------------------------: | -------------------: |
|           100 |    yes    |             5.5 |                             42.7 |                 33.1 |
|          1000 |    yes    |            34.4 |                             80.1 |                 55.3 |
|          2700 |    yes    |            88.4 |                            162.2 |                 94.7 |
|          2700 |    no     |            81.3 |                            209.1 |                    0 |

Each case added 720 DOM rows, **not** a wholesale retained-DOM rebuild. At 2700
retained rows those additions caused 162000 rows / 15.77 MB to be parsed. Disabling
follow removed scroll work but not full parsing/reconciliation. Native `_glide`
inclusive samples include its mutation callback, not geometry alone.

A hidden-producer control parsed 44 full artifacts but called `paintOutput` zero
times: the landed hidden-paint fix works. It nevertheless caused 88 manager
applications and 44 glides because the unrelated visible pane also accepts pending
rows and settles. Normal append produced no artifact-ID attribute churn.

### Active-screen capture

Real runtime component benchmark, 120x40, primary-screen text, no PTY/socket:

| Bytes per ingest | Time/call, ms | Allocated/call, MB | Allocations/call |
| ---------------: | ------------: | -----------------: | ---------------: |
|              128 |          7.53 |               1.64 |            61655 |
|              800 |          9.03 |               2.11 |            75026 |
|             4096 |          9.22 |               2.68 |            75891 |
|            65536 |         18.61 |              12.82 |            78594 |

Combined profile: active `Row` 65.9% inclusive, publication 35.0%, prefix refresh
34.3%, emulator ingestion 12.2%. These stack percentages overlap. They are **not**
shares of whole-helper CPU. Primary prefix refresh reads the grid before checking
for pins; publication also reads it. Active rows use per-cell CGO; departed rows
already use a bulk bridge. Capture/encoding precedes downstream coalescing.

Two lower-priority controls prevent misattribution:

- Real encrypted ledger append of six rows: 0.790 ms, 51.8 kB and 1048 allocations
  per append; current cap eviction accounts for 52.3% inclusive CPU in that
  benchmark. The earlier fix removed repeated victim rescans, not every retained
  chunk scan. Whole-coordinator attribution remains unmeasured.
- Real request-path logging: DEBUG-to-file adds 9.36 microseconds/request over
  disabled logging. At the earlier observed 366 requests/s this extrapolates to
  about 0.34 percentage points of one CPU, excluding actual session mutation,
  sockets, the second sink and contention. High log volume alone does not make
  logging the main CPU cause. Required request observability stays intact.

### Immutable cell arrays

An isolated GTK/WebKit replay ran the actual `cell-model` source in three fresh
processes: six models, 79x40, nominal 33 ms timer. Only diagnostic bundled copies
differed; no global builtin replacement. Every snapshot's first-cell text was
checked. This excludes DOM, transport and backend work.

| Variant                    | Accepted frames | Model work/60 s, ms | Renderer CPU | Peak PSS, MiB |
| -------------------------- | --------------: | ------------------: | -----------: | ------------: |
| Unmodified                 |           10008 |                7735 |       15.18% |         221.5 |
| Without array freezes      |           10002 |                2733 |        7.96% |         214.6 |
| Without cell-object freeze |           10002 |                6034 |       13.68% |         228.0 |

The array control reduces model work by about 65%, **not whole-application CPU by
65%**. It violates production immutability and is not the proposed implementation.
The replay changes one row per tick; it does not establish a gain when every row
changes. None of these model-only runs reproduced the native RAM ramp.

Native allocation tracing recorded 9.33 million calls over 33.54 s; 116.00 MB of
150.85 MB peak tracked live allocation (76.9%) passed through
`JSC::JSObject::preventExtensions`. That trace used `Malloc=1` and a 250x76 grid:
it attributes allocation paths, not production PSS. Bytes live at trace stop are
not proven leaks. WebKitGTK 2.54.2's
[JSObject implementation](https://raw.githubusercontent.com/WebKit/WebKit/webkitgtk-2.54.2/Source/JavaScriptCore/runtime/JSObject.cpp)
converts dense arrays to dictionary indexing for freeze/preventExtensions;
[its allocator environment](https://raw.githubusercontent.com/WebKit/WebKit/webkitgtk-2.54.2/Source/bmalloc/bmalloc/Environment.cpp)
explains the `Malloc` control. Earlier global-freeze wrapper probes are excluded
from causal RAM claims because they can alter JIT/GC behavior.

### Fresh native RAM controls

Unmodified application, default allocator, no freeze wrapper or allocation
profiler, inspector detached during measured intervals. Actual viewport 1024x768,
79-column grids. A seventh idle tab hides all six producers.

| Condition                         | Duration, s | Initial PSS, MiB | Peak PSS, MiB | Final PSS, MiB |
| --------------------------------- | ----------: | ---------------: | ------------: | -------------: |
| Six redraws, one visible          |         245 |            567.6 |         670.9 |          420.5 |
| Same six, all hidden              |         148 |            592.0 |         763.8 |          422.8 |
| Populate six scrolling histories  |          69 |            549.1 |        1016.6 |         1016.6 |
| Six redraws after those histories |         427 |           1010.4 |        1010.4 |          544.2 |

After history accumulation, 16291 retained-output children were observed. During
subsequent redraw, PSS fell to about 556 MiB by 30 s, then ranged from
514.4 to 642.5 MiB during the remaining 397 seconds of the 427-second phase.
Output continued without explicit GC; 31 samples exceeded 580 MiB.

The earlier run reached 1.5–2.5 GiB. These controls do not erase that observation,
but neither six redraws alone, retained history alone nor the isolated model
reproduces its ramp. Keep `nocx-3an1s.1.9` deferred and unresolved; do not call it a
proven permanent leak or attribute its entire working set to `Object.freeze`.

## Reproducing the workload for a fix comparison

Build both revisions with the same toolchain, native dependencies and helper
configuration. Use the repository's normal helper build and production frontend;
on Linux the measured desktop tags were `release production gtk3`. The headless
coordinator was built from `cmd/nocx-server` without `nocx_login_session`. Build
before changing HOME, then launch with fresh HOME/XDG directories and an isolated
D-Bus session. Never benchmark against personal profiles or publish personal logs.
Use `setsid`, not `nohup`, when detaching a measured process.

Save the following as `producer.cjs` in a disposable directory, then run
`node /absolute/path/producer.cjs unique-tab-id scroll` or `alt` **inside a terminal
pane**. This is the measured generator, not a production helper. Remove old
`stop-ID` files before each run. Ctrl-C or creating that file stops a producer.

```javascript
const fs = require('node:fs')
const root = __dirname,
  id = process.argv[2],
  mode = process.argv[3] || 'scroll'
let tick = 0,
  bytes = 0,
  ack = ''
process.stdin.setRawMode(true)
process.stdin.resume()
if (mode === 'alt') process.stdout.write('\x1b[?1049h')
function stop() {
  clearInterval(timer)
  if (mode === 'alt') process.stdout.write('\x1b[?1049l')
  fs.writeFileSync(
    `${root}/producer-${id}.json`,
    JSON.stringify({ pid: process.pid, tick, bytes, mode, done: true }),
  )
  process.exit(0)
}
process.stdin.on('data', (b) => {
  if (b.includes(3)) stop()
  ack = 'NOCXACK_' + b.toString().replace(/[^a-zA-Z0-9]/g, '')
  process.stdout.write('\r\n' + ack + '\r\n')
})
const timer = setInterval(() => {
  if (fs.existsSync(`${root}/stop-${id}`)) return stop()
  let s = '\x1b[?2026h'
  if (mode === 'alt') s += '\x1b[H\x1b[2J'
  for (let i = 0; i < (mode === 'alt' ? 28 : 8); i++)
    s += `PERF ${id} tick ${tick} row ${i} ${ack} `.padEnd(98, 'x') + '\r\n'
  s += '\x1b[?2026l'
  process.stdout.write(s)
  bytes += Buffer.byteLength(s)
  tick++
  if (tick % 30 === 0)
    fs.writeFileSync(
      `${root}/producer-${id}.json`,
      JSON.stringify({ pid: process.pid, tick, bytes, mode }),
    )
}, 33)
process.on('SIGTERM', stop)
```

This nominally emits about 24 KiB/s scrolling or 83 KiB/s alternate redraw per
producer. Counters establish actual progress; timer cadence is not guaranteed.
The generator uses synchronized-output begin/end markers and allows wrapping at
79 columns; retain those facts when comparing revisions. Its ACK is a visible
marker, not a native input-latency measurement by itself.

For each revision, measure fresh 1/2/4/6-producer profiles, both workloads, one
visible producer and all producers hidden behind an idle tab. Select Notes so a
Files refresh is not mixed into the sample. Verify the **actual** viewport and
accepted grid before timing: a window manager may maximize a requested small
window. Record process CPU deltas, per-process PSS, workload counters, retained
row counts, and parse/delivery volumes separately. Preserve native defaults for
RAM runs and detach the inspector. Include scroll-away, activation catch-up,
command completion, stop and close recovery; use waiting observables, not sleeps
as correctness assertions. The seven-minute history-conditioned phase above is a
useful control, not proof that every longer run is bounded.

Run native before/after comparisons in fresh processes, alternate revision order,
and report spread as well as central values. A saturated renderer with fewer
accepted frames is not an improvement. Native keystroke-to-visible latency must
be measured through a real platform input seam before claiming the milestone's
responsiveness outcome. The existing cross-tab socket test is necessary but does
not prove that UI latency. Six-tab measurements do not satisfy the charter's
sixteen-busy-session acceptance on their own.

## Preparation review

Independent read-only reviews checked the design against its source seams and
the report against the original measurements. The evidence review corrected the
post-settle PSS range from sparse samples to the full series reported above.
Both reviews accepted their assigned scopes after that correction.

Preparation checks resolved all 13 relative document links, recomputed the four
native phases from 931 renderer samples, checked the three model and four
retained replay summaries, and compiled the workload above for JavaScript syntax.
These are document/data checks, not a new native performance run or acceptance
of any proposed implementation.

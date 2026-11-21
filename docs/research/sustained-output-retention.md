# Sustained output at retention boundaries

Measured 2026-11-10 on Linux amd64, Ryzen 7 5700X, Go 1.26.10.
Tasks: nocx-4y82c.1.22 (post-cap eviction scan) and nocx-4y82c.1.23 (wrapped text assembly).
Main was at `1a5edc876` when these were measured.

## Decision

Keep the existing retention policy and ownership. Reduce the work required to
apply it and assemble text linearly. These are two independent defects, not an
invitation to change polling, buffers or deadlines. AD-1, AD-6, AD-8 and
ADR-0078 remain unchanged.

## Which limit means what

| Boundary                         | Default                                     | What exceeding it means                                                                               |
| -------------------------------- | ------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Live emulator history            | 10,000 physical rows and 32 MiB per session | Old live rows become unreachable below a moving floor; allocation is page-granular                    |
| Durable output of one command    | 256 KiB, configurable 16–4096 KiB           | Whole middle chunks are removed while protected head/tail survive; this is not a strict byte ceiling  |
| Durable chunk target             | 16 KiB                                      | Whole rows are retained; chunk sizes and final summary affect cap overshoot                           |
| Helper / coordinator row buffers | 20 MiB each                                 | In-flight protection, not a transcript budget; overflow is incomplete capture, not ordinary retention |
| Helper raw buffer                | 4 MiB                                       | Separate raw-data backpressure boundary                                                               |
| Helper aggregate reservation     | 512 MiB                                     | Not a bound on total application RSS                                                                  |
| Global retained content          | 4096 MiB                                    | Separate eviction policy; neither the per-command cap nor this value bounds SQLite WAL/RSS            |

A no-newline logical line still becomes many physical rows as the terminal wraps.
Removing old output must preserve its missing-output marker, not present survivors
as a complete command. Unloaded retained pages are different: they reserve space
and must not display a loss marker.

## 1. The post-cap append cliff

`internal/content/ledger_block_rows.go:evictBlockRowsToCap` previously allocated and
scanned the complete ordered `(seq, length(body))` list for each over-cap append.
The earlier accepted nocx-n275f optimization already removed repeated scans per
victim. This change extends that implementation: stop after the protected head
and the required victims, deriving the protected tail from authoritative held
bytes. It does not introduce another counter or change the schema.

An encrypted SQLite probe appended 4,000 separate 80-cell rows, measuring each
1,000-row phase. The same retained bytes/chunks and dropped-row count survived.

| Scenario, 1,000 appends             |     Before |      After | Before allocated | After allocated |
| ----------------------------------- | ---------: | ---------: | ---------------: | --------------: |
| 256 KiB cap, first phase below cap  |  183.59 ms |  184.32 ms |     25,732,432 B |    25,749,264 B |
| 256 KiB cap, fourth phase above cap | 1992.28 ms | 1288.26 ms |    220,873,888 B |    63,084,096 B |
| 16 KiB cap, fourth phase above cap  |  371.09 ms |  331.17 ms |     40,378,808 B |    29,816,152 B |

At the default cap the final state stayed 262,314 bytes, 2,352 chunks and 1,648
dropped rows. The 4 MiB control did not reach its cap: all 4,000 rows survived.
These are single paired samples, not confidence intervals. A build overlapped
the after probe; elapsed-time improvement is indicative. Allocation is cumulative
Go allocation, not peak RSS. SQLite/encryption and scanning the protected head
still cost work: post-cap append is not now constant-time.

The retained benchmark isolates the same small-delivery shape:

```sh
go test -run '^$' -bench '^BenchmarkBlockRowsRetention$' -benchtime=1000x -count=1 ./internal/content
```

On the assembled repair it measured 186,841 ns/op and 25,984 B/op fresh, versus
1,134,951 ns/op and 63,109 B/op with 3,000 rows prefilled. The fixed iteration
count keeps the fresh case below the default cap.

## 2. One long wrapped line

`BlockRowsTextAfter` joined each continuation onto the accumulated string,
repeatedly copying its growing prefix. It also concatenated the encoded chunks
before JSON decoding. It now decodes a reader chain and writes text once through
a builder. Empty rows, Unicode, continuation across arbitrary chunk boundaries,
summary records, malformed-input errors and the skip of rows below the recorded
output start (nocx-4y82c.1.11) keep their meaning.

| Input text                |     Before |     After |  Before allocated | After allocated |
| ------------------------- | ---------: | --------: | ----------------: | --------------: |
| 256 KiB, separate lines   |   12.27 ms |  13.06 ms |      14,337,824 B |    13,333,424 B |
| 256 KiB, one wrapped line |   48.14 ms |  13.05 ms |     455,414,128 B |    13,327,912 B |
| 4 MiB, separate lines     |  167.51 ms | 169.87 ms |     232,083,536 B |   215,318,296 B |
| 4 MiB, one wrapped line   | 7718.16 ms | 169.64 ms | 110,386,476,760 B |   215,318,264 B |

```sh
go test -run '^TestBlockRows' -bench '^BenchmarkBlockRowsText$' -benchtime=1x -count=1 ./internal/content
```

These are production-function microbenchmarks, one iteration each, not a claim
that the whole application became 45 times faster. The remaining roughly 215 MB
of allocation for 4 MiB of text includes row/cell decoding and still matters.

## Actual application checks

Used the real headless coordinator, freshly built helper, Vite frontend and
Chromium, with an isolated HOME, no login-session keystore and synthetic output.
This binary included the eviction repair; the later text-only repair was checked
with the assembled package tests and a successful backend build, not claimed as
part of this browser run.

A paced producer printed four phases of 1,000 numbered 78-character lines, pausing
between phases so the real ledger and DOM could be inspected:

| Emitted rows | Stored row bytes | Chunks | Stored DOM rows | Gap notices |
| ------------ | ---------------: | -----: | --------------: | ----------: |
| 1,000        |          104,094 |     43 |             956 |           0 |
| 2,000        |          214,096 |     87 |           1,957 |           0 |
| 3,000        |          263,312 |    105 |           2,405 |           1 |
| 4,000        |          263,312 |    106 |           2,405 |           1 |

Rows still on the live screen are not yet in the stored-row count. At close the
artifact was sealed, `truncated=cap`, 264,721 bytes, 107 chunks, with 1,586 dropped
rows; the final marker and successful completion were visible. No observed long
task over 50 ms occurred in the instrumented later bursts; this is a short paced
smoke, not a saturated-load latency guarantee. JS heap fluctuated rather than
proving a memory bound; it was not GC-normalized.

The database grew from 4,096 to 761,856 bytes while WAL reached 4,132,864 bytes,
then both plateaued through the later paused phases. A 256 KiB artifact limit is
therefore plainly not a 256 KiB disk limit.

A second producer printed 15,000 numbered lines at 50 lines per 20 ms. It completed
successfully with 2,428 retained DOM rows, 263,375 artifact bytes and 12,573 dropped
rows; its final marker and one gap remained visible. The totals reconcile:
2,428 retained + 12,573 dropped = 15,001 output rows, including the final marker.

The real `session.historyPage` result after both commands reported newest interval
`[18949,19013)`, retained floor `8840`, and a 105-column, 46-row screen. Asking for
`before=1` returned the empty interval `[1,1)`, the same floor and `more=false`.
Thus old live history really became unavailable; it was not merely hidden by DOM
virtualization. The live bound is page-granular, not exactly 10,000 rows including
the current screen.

The second workload is reproducible in the terminal:

```python
import sys
import time
for batch in range(300):
    start = batch * 50
    sys.stdout.write(''.join(f'OVERFLOW-{i:06d} ' + 'y' * 60 + '\n'
                             for i in range(start, start + 50)))
    sys.stdout.flush()
    time.sleep(0.02)
print('OVERFLOW-DONE', flush=True)
```

## Verification and boundaries

- Focused content regressions passed, including exact eviction victims, sparse
  sequences, protected ends, prepend, SQL failure rollback and cancellation.
- Assembled backend regressions passed with `-race`:
  `go test -race -run 'Test(BlockRows|AppendBlockRows|AMultiChunkPrepend|APrependThatPasses)' -count=1 ./internal/content`.
- Both backend changes received independent, same-model read-only review: no
  blockers; no claim of cross-model review or mutation testing.
- Backend pre-commit passed with `GOTOOLCHAIN=go1.26.5`, the patch pinned in the
  repository's Linux CI image. The installed golangci-lint 1.64.8 under Go 1.26.10
  first reported widespread SA5011 warnings after `t.Fatal`; changing only the
  Go patch made the gate pass. No assertion or linter rule was weakened.
- No full integration gate, macOS WKWebView run, native CPU/PSS comparison,
  multi-hour soak or global-budget saturation was performed. Docker had no
  available daemon; these are host/headless checks, not container-suite results.
- Global eviction is separate: append/close do not invoke the same retention
  sweep that submission does (nocx-lp371.1). This investigation does not
  establish a strict global disk ceiling during a long active command or between
  sweeps.
- The automatic document-readiness gate is not installed in this repository;
  documentation is reviewed, not reported as automatically accepted.

## Current-main follow-up, 2026-11-14

Task: `nocx-5xlys`. Baseline: `03cd697a0555f7a3a1610d9e6bd1fda5c9f5ee2a`,
Linux amd64, Ryzen 7 5700X, Go 1.26.10. The earlier two repairs are already
on this baseline; this investigation does not claim them again.

### Remaining text reconstruction

`blockRowPlainText` still expanded stored text into a rune slice and one
`storedCell` per cell before immediately joining the graphemes again. It now
walks UTF-8 spans directly, sorting the decoder-owned sparse marks in place.
The unmarked case only trims trailing ASCII spaces. Combining graphemes,
wide/spacer cells, blank cells, reordered marks, malformed marks and overflow
remain covered through the public text-reading path.

Identical retained benchmarks, three samples each:

| Scenario                        | Before median | After median | Before B/op | After B/op | Before allocations/op | After allocations/op |
| ------------------------------- | ------------: | -----------: | ----------: | ---------: | --------------------: | -------------------: |
| 4 MiB, separate lines           |    240.332 ms |    72.240 ms | 216,157,160 | 28,671,320 |             4,666,323 |              105,016 |
| 4 MiB, wrapped line             |    240.216 ms |    75.720 ms | 216,157,176 | 28,671,320 |             4,666,324 |              105,016 |
| Retention, 3,000 prefilled rows |      1.605 ms |     1.070 ms |      63,237 |     36,320 |                 3,465 |                1,199 |

Elapsed times are diagnostic: machine load differed and browser diagnostics
overlapped some measurements. In particular, fresh append also fell from
0.287 ms to 0.193 ms, leaving the prefilled/fresh ratio around 5.6 in both
runs. Do not read the retention timings as proof that its append cliff is gone.
The allocation reduction is the supported improvement; these are cumulative
allocations, not retained memory or RSS.

### Retention: keep the policy, remove conversion overhead

The final repair keeps the original ordered query and victim walk. Scan
destinations live outside the loop, and sequence numbers stay `int64`, the
SQLite driver's native representation, instead of passing through
`database/sql`'s format/parse conversion into `int`.

A candidate locating the head boundary with a SQL window sum was rejected:
prefilled median time rose to 2.800 ms despite reducing allocations. No cache,
schema, payload field, cap, batching or confirmation policy was introduced.
The protected head is still traversed on every over-cap append.

### Browser evidence and limits

The current-main diagnostic matrix passed all 16 cases: three unprofiled
repetitions plus one separately profiled repetition for hidden tabs, history,
16 KiB output and 256 KiB output. Chromium 149, 1280 by 800, real headless
coordinator and helper, disposable HOME, dev debug logging, no login-session
keystore. The workload invokes explicit Bash and Node script files rather
than assuming the user's login shell is Bash.

With 0, 1 and 6 hidden output producers, foreground command-to-visible-marker
medians were 60.55, 75.15 and 171.15 ms respectively (24 samples per condition).
Keyboard next-task measurements had all 60 samples per condition, but are not
visible-echo latency. Binary-message counters include all message types and
must not be called screen-frame rates.

At 50, 250 and 500 history blocks, DOM nodes grew from 6,257 through 31,057 to
62,057. Scroll-frame p95 remained about 16.7–16.8 ms; this Chromium workload
does not demonstrate a scrolling slowdown or establish a memory bound.
Its profile still attributes application work to `_loadVisibleBlockRows`
and `loadedRowRanges`; that alone is not a native-WebKit performance result.

The baseline's short 4,000-row producer crossed 16 KiB but not 256 KiB. The
post-change visual smoke therefore used longer rows and explicitly required
both caps to evict. Both checks passed: at 16 KiB, 174 retained plus 3,826
dropped rows; at 256 KiB, 2,443 retained plus 1,557 dropped rows. Stored ranges,
DOM counts, the single truthful gap marker, sealing, and visible first/last
numbered rows agreed. This is fidelity evidence, not a paired latency comparison.

The fresh ingest profile still puts 48.61% cumulative CPU and 88.15% allocation
space in Ghostty `historyRow`. The prefix-capture skip and immutable frontend
row reuse are already present; the reverted bulk active-row read is not
restored here. Its durable-confirmation/row-pool acceptance remains separate.

`go test ./internal/content` passed on the assembled repair. The first
package run exposed a test-harness defect: the disk-full test's process-wide
64 KiB `RLIMIT_FSIZE` damaged Go's test-cache log, causing failure after
all assertions passed. That scenario now runs in a child test process,
preserving failed-write and recovery assertions without restricting the
parent suite's files or signal handling.

No full integration gate, native macOS/WebKit check, raw-session-recording
repair, or claim of constant-time capped appends is included.

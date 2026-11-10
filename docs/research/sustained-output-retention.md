# Sustained output at retention boundaries

Measured 2026-11-10 on Linux amd64, Ryzen 7 5700X, Go 1.26.10.
Tasks: nocx-4y82c.1.22, nocx-4y82c.1.23 and nocx-4y82c.1.24.
Main was fast-forwarded from `f49860272` to `1a5edc876` before investigation.
No existing pull request was merged.

## Decision

Keep the existing retention policy and ownership. Reduce the work required to
apply it, assemble text linearly, and make the paged renderer discard cached rows
that its accepted retention geometry says no longer exist. These are three
independent defects, not an invitation to change polling, buffers or deadlines.
AD-1, AD-6, AD-8 and ADR-0078 remain unchanged.

The backend changes are based directly on main. The renderer correction is based
on PR #789 at `33f48cd885a9ae95b2bc7cad9cde1f77f00934de`, because its paged merge
is not on main. Do not transplant that correction as though main already paged.

## Open pull requests inspected

Check states below are the inspection snapshot, not a promise about later CI.

| PR                                               | Relevance                                    | Observed check state / recommendation                                                                                |
| ------------------------------------------------ | -------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| [#793](https://github.com/shady2k/nocx/pull/793) | One gutter for live and stored output        | Multiple failures; geometry is relevant, but not a performance proof                                                 |
| [#792](https://github.com/shady2k/nocx/pull/792) | Refused input and focus ownership            | Chromium failure, some checks pending; separate from retention                                                       |
| [#790](https://github.com/shady2k/nocx/pull/790) | Submit framing and readiness                 | Multiple failures; separate from retained output                                                                     |
| [#789](https://github.com/shady2k/nocx/pull/789) | Lazy paged output and bounded response queue | Multiple failures; directly relevant, plus reproduced cache-retention defect corrected on its own branch             |
| [#786](https://github.com/shady2k/nocx/pull/786) | Fresh rows held until output-start           | Multiple failures; avoid treating duplicated/early rows as a retention optimization                                  |
| [#743](https://github.com/shady2k/nocx/pull/743) | Busy-terminal CPU and allocation reductions  | All 13 checks successful at `f0b6e11e760120aa676a5c4a0c1872aff5c44b06`; reuse its work rather than reimplementing it |
| [#732](https://github.com/shady2k/nocx/pull/732) | External harness decision                    | Documentation checks green, product checks skipped; unrelated                                                        |
| [#91](https://github.com/shady2k/nocx/pull/91)   | Old sandbox work                             | Latest displayed checks skipped; historical body evidence is not current validation                                  |

PR #743 already covers prefix capture, bulk emulator reads, immutable cell-model
sharing, hidden-block settlement and asynchronous durable recording. Its bounded,
revision-consistent incremental-read work is a design/prototype, not shipped
paging. Its historical native CPU/PSS figures are not measurements made here.

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

`BlockRowsText` joined each continuation onto the accumulated string, repeatedly
copying its growing prefix. It also concatenated the encoded chunks before JSON
decoding. It now decodes a reader chain and writes text once through a builder.
Empty rows, Unicode, continuation across arbitrary chunk boundaries, summary
records and malformed-input errors keep their meaning.

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

## 3. PR #789's cache outlives retention

`mergeStoredBlockRows` previously unioned every accepted page forever. New geometry
could say rows were evicted while the cache, DOM and selection still held them.
The repair filters the ordered union through the accepted next retained ranges.
An explicit empty range set means full eviction; absent geometry retains the
existing fallback. Unloaded retained ranges remain spacers, not loss.

A real-module/jsdom probe delivered 500 batches of 100 rows with 400 retained:

| Metric                  | Pinned PR baseline | Corrected merge |
| ----------------------- | -----------------: | --------------: |
| Final cached / DOM rows |             50,000 |             400 |
| Total merge time        |         671.117 ms |       17.419 ms |
| Final paint time        |        2272.517 ms |       43.248 ms |
| Missing-output notices  |                  0 |               1 |

This is a renderer-component measurement, not native application frame timing.
A real Chromium surface importing the actual parser, merge and painter also
passed: 200 batches, 400 stream rows, eight retained; maximum DOM/cache/selection
all eight; exact surviving indices `0,1,2,3,396,397,398,399`; one gap marker.
A separate partially loaded retained interval kept an eight-row spacer and no gap.

**Remaining protocol risk:** the caller must supply current `next` geometry.
`artifactVersion` is an artifact identity, not a mutation revision. Separate page
and refresh requests can compete; this repair does not establish freshness.
Revision-consistent incremental reads remain the subject of nocx-3an1s.1.16.
A delayed-old-page / new-refresh experiment is not proved by these merge tests.

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
- The paging module passed all 66 tests with `npx vitest run src/scrollback/block-rows.test.ts`.
- Both repairs received independent, same-model read-only review: no blockers;
  no claim of cross-model review or mutation testing.
- Backend pre-commit passed with `GOTOOLCHAIN=go1.26.5`, the patch pinned in the
  repository's Linux CI image. The installed golangci-lint 1.64.8 under Go 1.26.10
  first reported widespread SA5011 warnings after `t.Fatal`; changing only the
  Go patch made the gate pass. No assertion or linter rule was weakened.
- No full integration gate, macOS WKWebView run, native CPU/PSS comparison,
  multi-hour soak or global-budget saturation was performed. Docker had no
  available daemon; these are host/headless checks, not container-suite results.
- Global eviction is separate: append/close do not invoke the same retention
  sweep that submission does. This investigation does not establish a strict
  global disk ceiling during a long active command or between sweeps.
- The automatic document-readiness gate is not installed in this repository;
  documentation is reviewed, not reported as automatically accepted.

Integrate the backend fixes independently after their PR checks. Keep the paged
correction tied to PR #789, resolve its required-check failures and establish
read freshness before treating that larger change as accepted. Neither existing
performance PR is authorized to merge by this investigation.

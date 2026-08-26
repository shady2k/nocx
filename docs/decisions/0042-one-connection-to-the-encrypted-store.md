# ADR-0042 — One connection to the encrypted store, because the cipher is wider than the frame

- **Status:** Accepted
- **Date:** 2026-08-26
- **Related:** [ADR-0011](0011-persistence-storage-capabilities-and-secret-references.md)
  (one `content.db`, WAL, force-quit survival), [ADR-0018](0018-contentdb-engine-and-encryption-at-rest.md)
  (encryption at rest through the `adiantum` VFS; its multi-process paragraph),
  AD-8 (one owner per behaviour)
- **Qualifies:** ADR-0018's claim that "WAL plus SQLite's file locking make `content.db`
  safe to share across processes". WAL and SQLite's locking are not by themselves
  sufficient once an encrypting VFS sits underneath them. See Consequences.

## Context

`internal/content` is an encrypted SQLite store: one `content.db`, WAL, opened through
the `adiantum` VFS, with a pool of sixteen connections so that readers run alongside the
single writer goroutine.

A reader would occasionally fail with

    sqlite3: database disk image is malformed

The database was not damaged. Every failing round re-read cleanly afterwards, with the
exact expected row count and `integrity_check = ok`. Only the read failed, and only
sometimes: roughly once in fifty runs of the test that happened to have the right shape,
which is often enough to be red in CI a few times a year and never often enough to be
diagnosed. It was first seen on 2026-08-26 in a `backend-linux` run and passed on the
re-run.

**The cause is an alignment mismatch between the cipher and the log.** The `adiantum`
VFS enciphers whole 4096-byte blocks with a wide-block, length-preserving construction.
SQLite's write-ahead log is a 32-byte header followed by frames of `24 + page_size`
bytes — 4120 with the default page size — so a WAL frame boundary never coincides with a
cipher block boundary. Appending a frame therefore performs a read-modify-write of the
block that holds the **tail of the frame before it**, and that earlier frame is committed
and inside a concurrent reader's snapshot.

SQLite's locking is not violated by this, and that is the point worth keeping. WAL
promises a reader that the FRAMES it may read do not change under it. It never promised
that the surrounding BYTES would not, because for plain SQLite that difference does not
exist: a torn read of unused padding is harmless. A wide-block cipher makes the
difference fatal. One byte read mid-rewrite garbles the whole 4096-byte plaintext,
including the committed frame the reader actually wanted, and SQLite reports the only
thing it can — that the image is malformed.

**It is the library's defect, not ours.** It reproduces on the raw `ncruces/go-sqlite3`
driver with no nocx code, on v0.35.2 and on v0.35.3.

### What was measured

Three arms of one experiment, identical but for the named variable:

| arm                                 | connections | rounds | rounds with a failure |
| ----------------------------------- | ----------- | ------ | --------------------- |
| encrypted (`vfs=adiantum`) + WAL    | 16          | 16     | 3                     |
| plaintext + WAL                     | 16          | 16     | 0                     |
| encrypted + `journal_mode=TRUNCATE` | 16          | 16     | 0                     |
| encrypted + WAL                     | 1           | 12     | 0                     |

Removing any one of the three ingredients — the cipher, the write-ahead log, or the
concurrent handles — removes the failure.

## Decision

**The pool is the exclusion: `maxOpenConns = 1`.** One connection means SQLite never
holds two file handles on this database at once, so there are no concurrent VFS calls to
interleave and the race is unreachable rather than unlikely.

WAL stays. Two things follow from that and are part of this decision:

1. **Every read drains its cursor before going back to the pool.** `ownArtifactsFor`
   already did this and said why; `executionsFor` and `artifactsFor` did not, and on a
   one-connection pool every caller of `Entry` hung until the package timeout killed it.
   This is not a workaround for the pool size — it is what makes a read independent of
   it. `TestEntryDoesNotDeadlockOnASingleConnection` guards it with a deadline, because
   a deadlock reports itself as a ten-minute silence otherwise.
2. **The defect is reported upstream**, with the reproducer above. This ADR is
   containment, not the end state.

**Two tests, because one cannot do both jobs.** The stress test that found this is
probabilistic — red in 3 runs of 6 at the profile it can afford — and a profile that
caught it 6 of 6 ran 9m12s under `-race` in the CI container and was killed by the
10-minute package timeout, which guards nothing at all. So the stress test is the net,
and `TestThePoolIsOneConnection` is the gate: it states the invariant, costs
milliseconds, and fails by name the moment somebody raises the pool back. A reader who
finds only the stress test would reasonably conclude the check is flaky and weaken it.

## Rationale

The obvious alternative was to keep the pool and leave WAL: a rollback journal also
excludes readers from a writer, and is also crash-safe. That was the first choice, and
measurement reversed it.

A rollback-journal commit writes the journal, syncs, writes pages in place, syncs, and
finalises the journal, and through this VFS several of those are partial-block
read-modify-writes. A WAL commit is an append.

| journal mode | ms per commit, through `vfs=adiantum` |
| ------------ | ------------------------------------- |
| WAL          | 0.03                                  |
| TRUNCATE     | 5.38                                  |

That number lands on the worst possible path. `AppendChunk` (`ledger_sqlite.go`) opens
**one transaction per chunk**, and its callers are the assistant's streaming deltas — a
commit per delta, continuously, while a model streams. 5.38 ms per commit is a ceiling
of about 185 deltas a second on a path that must keep up with a model.

On the store's real workload — 5000 `RecordCompleted` calls interleaved with sixteen
goroutines running real `QueryEntries` — the three candidates came out:

| configuration                | wall time | race    |
| ---------------------------- | --------- | ------- |
| WAL, 16 connections (before) | ~10 s     | present |
| WAL, 1 connection            | ~34 s     | closed  |
| TRUNCATE, 16 connections     | ~50–63 s  | closed  |

So of the two safe options, one connection is both the faster and the one that keeps
commits cheap. A rollback journal would also have carried a blast radius this does not:
backup semantics documented in terms of `db + -wal + -shm`, the two-number disk budget,
the file-mode sweep over `-wal`/`-shm`, tests that assert a live WAL, and user-facing
settings copy.

Two options were considered and rejected. **Retrying the failed read** papers over a
defect and is forbidden by our engineering rules. **A smaller pool** — two, four — is not
a middle ground: one reader and one writer already reproduce it. There is no pool size
between "racy" and "one".

## Consequences

**Reads serialize with each other, not only with the writer.** Measured at 3.4x on a
profile with sixteen threads running heavy ledger queries flat out. The product never
does that — its readers are user-initiated recall search and block restore — but the
cost is real and it is head-of-line: a long read delays a streamed append behind it.

**`Backup` holds the only connection for its whole duration.** It takes a connection
from the same pool and hands it to SQLite's online backup API, so during a backup every
read and every streamed append waits. This was acceptable before because fifteen other
connections were free. Its tail latency on a large ledger is not yet measured.

**ADR-0018's multi-process claim is now unproven rather than false.** `maxOpenConns = 1`
guarantees one connection per `content.Open`, which is not the same as one handle on the
file across the machine, and nothing stops two backends reaching the same application
data directory (`internal/storage/appdir.go` says so in as many words for the dev
profile). Whether two processes reproduce this defect depends on whether the unsafe
state is per-handle Go memory or the bytes on disk, and that has not been established.
The claim is not withdrawn here; it is flagged as needing its own test.

**The end state is not this.** The technically correct fix keeps both WAL and reader
concurrency by putting a shared I/O lock _outside_ the encrypting VFS — a mutex per
physical file, held across the whole of an encrypted `ReadAt` or `WriteAt` rather than
around the OS calls inside it. That is correctness-sensitive code in a layer ADR-0018
deliberately declined to hand-write, and it does not solve the cross-process case
either. It belongs upstream, or in a deliberate piece of work of its own, not in a
containment patch.

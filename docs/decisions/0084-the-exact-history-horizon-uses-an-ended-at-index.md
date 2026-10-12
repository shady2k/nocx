# ADR-0084 — The exact history horizon uses an `ended_at` index

- **Status:** Accepted
- **Date:** 2026-10-12
- **Beads:** `nocx-q8yjf.25`, `nocx-zg3k3.14.16`
- **Scope:** The cold, no-evictions coverage path of `LedgerRepository.QueryEntries`; no API or wire change.

## Context

`QueryEntries` reads its page, `HasRows`, retention watermark, and coverage in one read transaction. When `retention_watermark.evicted_count == 0`, the surviving entries are the entire store and the exact storewide coverage is `SELECT MIN(ended_at) FROM entries`. SQLite's `MIN` ignores NULL, so entries that have not ended contribute no horizon. Once any eviction has occurred, the retention watermark owns the answer and the minimum query is skipped; this decision keeps a partially retained store from claiming full history.

The exact MIN expression had no ordered access path and scanned `entries` on each no-evictions query. On an encrypted Adiantum scratch store with 2,800 rows and 100 measured ordinary `QueryEntries(ScopeEverywhere, Limit: 10)` calls after five warm-ups, adding a scratch `entries_by_ended_at(ended_at)` index changed the plan from `SEARCH entries` to `SEARCH entries USING COVERING INDEX entries_by_ended_at`. The full calls took 427.279785 ms before and 105.544046 ms after; the complete returned `LedgerPage` values were equal. The scratch population explicitly had 2,800 non-NULL `ended_at` values. This bounded observation supports the access path; it is not a production latency guarantee and does not measure index build, write, or storage cost.

`content.db` uses one `user_version` and an ordered migration ladder. `Open` runs `migrateSchema` and then applies `schemaV1` without a surrounding transaction. Each migration edge's `applyStep` is the owner of its DDL, foreign-key check, version stamp, and commit.

## Decision

Schema 22 owns one covering B-tree, `entries_by_ended_at ON entries(ended_at)`. Keep `SELECT MIN(ended_at) FROM entries`, the no-evictions condition, the watermark branch, and the shared read-transaction snapshot unchanged. Do not substitute a page-derived, approximate, or differently ordered horizon, and do not add a fallback scan for a missing index: the normal schema-shape guard refuses a malformed stamped file.

Define the index DDL once as `schema22EntriesByEndedAtIndexDDL`. Append that constant to fresh `schemaV1` with compile-time string concatenation and execute the same constant inside the schema 21→22 migration transaction. The migration itself creates the index before stamping 22; a stamp-only rung is not safe because the later `schemaV1` call is outside that transaction and a failure between the two could leave an incomplete v22 file. `IF NOT EXISTS` makes the subsequent schema application idempotent after a successful edge.

Freeze the actual schema-21 DDL from base `f28f066623773b8d207cc4303e5ded83d09f0d17` in `testdata/schema_v21.sql`. Pin its normalized catalog digest and historical object names when 21 ceases to be current, and pin the complete schema-22 shape on the final migration rung. Fresh v22 and migrated v21 files must have the same catalog, including the index. The 21→22 edge must preserve every existing row and value.

## Alternatives rejected

- **Keep the full scan.** It avoids index storage, migration time, and write maintenance, but repeats the measured avoidable O(N) scan while the store has never evicted. Rejected for the selected performance repair.
- **Rewrite MIN as `ORDER BY ended_at LIMIT 1`.** Without an ordered index it still scans; without an explicit non-NULL condition it can also return NULL before any real end time. The exact existing aggregate is simpler and already defines the contract.
- **Maintain a separate cached minimum.** This adds another durable owner that must stay correct across insertion, close/update, explicit deletion, retention, and recovery. The ordered index lets SQLite maintain the access path without duplicating horizon state.
- **Use a partial or compound index.** The unchanged global MIN query needs a proven globally ordered covering path. No plan evidence justified adding a predicate or leading key to the access path.

## Migration, refusal, and rollback

The 21→22 index DDL and `user_version=22` commit together through the existing `applyStep` transaction. A failure or cancellation before commit must leave version 21, no new index, and every seeded row unchanged; the next ordinary `Open` retries the edge. A successful migrated v22 store reopens with the same catalog and query result. A malformed stamped-v21 catalog and any version newer than 22 are refused without data loss or restamping. Existing released-schema acceptance remains in force.

Creating the index performs one ordered build over existing rows and requires encrypted-file/WAL disk headroom. Thereafter inserts, `ended_at` updates, and deletes maintain the B-tree. Its physical pages are not content-retention bytes: logical retention accounting is unchanged. A v21 binary refuses a v22 store. Rolling the application back therefore requires restoring a pre-upgrade schema-21 backup or installing a v22-capable build; lowering `user_version` or silently dropping the index is not supported.

## Consequences

The coverage answer remains exact, storewide, NULL-aware, retention-aware, and read from the same snapshot as the page. Repeated no-evictions queries can use the one-column covering index; after an eviction, coverage continues to come from the watermark. Startup pays a one-time index build, and entry mutations pay index maintenance. The migration does not rewrite or discard user rows, change WAL encryption, change the one-connection/one-writer design, or alter accepted ADR-0011, ADR-0018, ADR-0043, or ADR-0055.

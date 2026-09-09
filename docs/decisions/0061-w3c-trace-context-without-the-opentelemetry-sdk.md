# ADR-0061 — W3C Trace Context identifiers, without the OpenTelemetry SDK

- **Status:** Accepted
- **Date:** 2026-09-10
- **Related:** AGENTS.md "Engineering rules" (structured logging via `log/slog` behind
  the logging interface — the rule this extends from _how_ to log to _what a log line
  must be able to be joined on_); AD-8 (one owner per behaviour — the reason the ids
  are minted in one package and nowhere else).
- **Beads:** `nocx-4l2a5` (the epic), `nocx-4l2a5.1` (the mechanism), `nocx-4l2a5.2`
  (the level and the sink), `nocx-4l2a5.3` (the process boundary), `nocx-4l2a5.4`
  (the spawn's steps), `nocx-ndfqe` (the failure that bought all of it),
  `nocx-halpn` (the sink that was `/dev/null`), `nocx-1w3my` (the diagnostic that
  landed in it).
- **Consulted:** the owner, 2026-09-10, who asked for OpenTelemetry and chose this
  reading of it when the fork was put to them.

## Context

On 2026-09-09 a coordinator's `workers.spawn` answered `internal error` in the
running product. Diagnosing it cost an evening, and every minute of that cost came
from the log rather than from the code.

The evidence was in two files. `internal/app` and everything under it wrote to
`nocx.log`; `cmd/nocx-server`'s own logger wrote to stderr, which
`internal/coordinator/spawn.go` sets to `/dev/null` for the daemon it launches. The
one line that named the cause — added a day earlier for exactly this case — was
therefore discarded in the shipped product and survived a dev run only because
`scripts/dev-web.sh` redirects into a `mktemp` file nobody is told about.

What was in the files could not be joined. The exchange crosses three processes: the
agent's MCP call reaches `nocx-helper`, which asks the backend's tool socket, which
dispatches inside the backend. Each wrote its own lines under its own vocabulary,
and the only thing they had in common was a timestamp. The seam for a chain existed
— `internal/log`'s `WithContext` read a trace id out of the context — but its ids
were free-form strings, its only producer minted `run-349`, and there was no span
and no parent at all.

And between "worker participant spawned" and "enrolment never arrived" there were
thirty seconds and no lines whatsoever, so the log could say that a registration had
failed and never which of its six steps was waiting or for how long.

## Decision

**Take the W3C Trace Context identifiers and their `traceparent` spelling. Do not
take the OpenTelemetry SDK.**

`internal/log` mints a 32-hex `trace_id` naming one exchange, a 16-hex `span_id`
naming one frame of it, and a `parent_span_id` naming the frame that asked for that
one — the spec's shapes exactly, never the reserved all-zero value. They travel in
`context.Context`, they are written onto every record by `WithContext`, and they
cross a process boundary as a `traceparent` member on the tool endpoint's request.

A dev build logs at debug without being asked, a release build at info, and one sink
holds every line the process writes.

## Why this rather than the obvious alternative

The obvious alternative is the SDK: `go.opentelemetry.io/otel` with real spans,
durations, attributes, statuses and an OTLP exporter. It was rejected for one
reason, and it is not the dependency.

**There is nothing to export to.** nocx is a local-first desktop application. A
person running it has no collector, and asking a developer to raise a Jaeger beside
their terminal to read why a spawn failed replaces one unreadable log with one more
process to remember. What the machine has is a log file, and the file is what has to
become joinable.

Taking the FORMAT costs a hundred lines and no dependency, and it buys the property
that mattered: `grep <trace_id> nocx.log` returns the whole of one failure, in order,
across every module that touched it. It also keeps the door open in the honest
direction — an exporter added later reads the ids that are already there, so it is a
question of where they go rather than of rewriting every call site. The reverse
choice does not have that property: an SDK adopted now would have to be threaded
through the same call sites anyway, and would additionally have to be configured,
shipped and explained.

A third option was considered and rejected: the SDK's span model with a
log-file exporter of our own. It buys the vocabulary without the infrastructure, and
pays a dependency for a model we would then be using at a tenth of its surface.

## What the next person inherits

- **One package mints ids.** `internal/log/span.go` is the only producer. A second
  derivation of "which exchange is this" is the defect AD-8 is about, and here it
  would be silent: two ids that agree everywhere anyone looks and disagree on the one
  failure being diagnosed.
- **An exchange whose identity already exists derives its trace rather than storing
  one.** An assistant run spans several JSON-RPC frames with nowhere between them to
  keep a minted id, so `DeterministicTraceID` hashes the run id into the spec's
  shape. Anything else with that property should do the same rather than growing a
  table.
- **A missing or malformed `traceparent` is never a refusal.** The call runs, under a
  trace of its own. An observability mechanism that can refuse service is not one,
  and a far side that is one version ahead of us must not lose its answer over it.
- **Debug is the dev default, so a debug line is a line somebody will read.** It is
  no longer a line that needs an environment variable and a restart to reach, which
  means the bar for writing one is lower — and the bar for writing a USELESS one is
  correspondingly higher.
- **The instrumented-call shape is `log.Start`**: an entry with its arguments, an
  exit, a duration and an outcome, all under one span. Facts a caller needs on the
  failure record are BOUND onto the logger rather than passed as start-line
  arguments, because the start line is debug and a release build does not write it.
- **This is not metrics.** Nothing here aggregates, and a counter that wants a home
  does not have one yet.

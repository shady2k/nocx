# ADR-sr71cdyt — The harness keeps process identity until group signals finish

- **Status:** accepted
- **Date:** 2026-11-14
- **Task:** nocx-a62cl.14.1
- **Refines:** ADR-anntnxdj decisions 1 and 5

## Context

A turn owns an agent process group. Reaping its leader before sending the last
signal releases the identity used to address that group. Polling group existence
alone can also wait forever for orphan zombies in a container whose init does not
reap them. RPC agents can finish a turn while still waiting for another request.

## Decision

Observe the leader's exit without reaping it, serialize group signals with Cancel,
and reap only after the final group signal. Linux uses waitid with WNOWAIT; macOS
uses an exit notification through kqueue. No signals are sent after reaping.

A connector explicitly requests graceful shutdown by closing stdin, directly or
through Launch.CompleteOnEnd. A clean exit and an end response are both required
for success. Stdin closure starts a bounded shutdown; an agent that will not exit
is killed and reported as a failure, not silently accepted as successful.

The harness does not set PR_SET_CHILD_SUBREAPER on nocx-server. Existing
internal/proc uses it only in tests. Orphan zombies have terminated and count as
gone; their OS parent owns reaping. Cleanup checks for running group members and
reports failure if they remain after its bound. This does not promise containment
of descendants that deliberately escape the process group.

Run's context owns the turn; cancelling a Wait observer's context does not.
Cancellation cannot be held open by a disconnected event consumer. It may discard
a queued delta to preserve the final failure in the bounded event channel.

## Consequences

The process identity remains owned throughout signalling. Wait reports cleanup
failure rather than leaving an unbounded cleanup loop. Linux and macOS use native
process-state observations, and each connector still owes the parent-death suite
through its own real launch path. No process-wide reaping policy is added.

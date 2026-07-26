# T9: Cancellation-safe dial and resize (nocx-e4g)

## Summary

Made SSH dial and channel resize operations honour context cancellation. Dial handshakes now return `context.Canceled` promptly instead of blocking until the network gives up. Pool waiters on an in-flight dial can cancel without inheriting a broken connection. Resize returns a distinguishable `*ErrDisconnected` after disconnect instead of blocking. Repeated and concurrent `Close` is idempotent (already fixed by 9d411ec; verified with tests).

## Changes

### 1. `dialDirect` — context watchdog for `gossh.NewClientConn` (`ssh_dial.go`)

`gossh.NewClientConn` has no context-aware form in x/crypto v0.54.0. A watchdog goroutine watches `ctx.Done()` and closes the underlying `net.Conn` to unblock the handshake. The goroutine is drain-safe: a buffered channel (size 1) guarantees the send always succeeds regardless of which path the caller takes. The caller sees `ctx.Err()`, not the incidental "use of closed network connection".

**Design decision:** The goroutine cannot leak. On the success path, the select receives the handshake result. On cancellation, `netConn.Close()` unblocks `NewClientConn`, the goroutine sends to the buffered channel, and the caller drains it. Because the channel is buffered, the goroutine never blocks on send even if no one reads.

### 2. `dialViaJumpHost` — `DialContext` + watchdog (`ssh_dial.go`)

Jump-target dialing now uses `gossh.Client.DialContext(ctx, "tcp", targetAddr)`, available since x/crypto v0.54.0. The prior `Dial` call had no context form and could block indefinitely on a dead bastion. `DialContext` returns promptly with `ctx.Err()` on cancellation.

The subsequent `gossh.NewClientConn` handshake uses the same watchdog pattern as `dialDirect`: closing the channel unblocks the handshake.

On every error path, the jump handle is released via `pool.Release(jumpHandle)` — no leaked bastion entries.

### 3. Pool `acquire` — context-aware waiters (`pool.go`)

Waiters on an in-flight dial now select on both `ctx.Done()` and `dialing.done`. A cancelled waiter returns `ctx.Err()` without touching pool state. The `Acquire` and `AcquireDial` signatures gained a `context.Context` parameter; all callers (production and tests) were updated.

**What happens to a pooled entry when a dial is cancelled:** The first dialer's context cancellation propagates through `dialDirect`/`dialViaJumpHost`, the dial factory returns an error, `acquire` cleans up the `dialInProgress` slot, and waiters are woken to retry. A waiter whose context also cancelled gets `ctx.Err()` — it never inherits a broken connection.

### 4. `RealChannel.Resize` — context + disconnect detection (`ssh_channel.go`)

`Resize` now checks the channel's `done` signal before any work. After disconnect, it returns `*ErrDisconnected` immediately rather than blocking on the dead transport. A cancelled context returns `ctx.Err()` promptly via the same goroutine watchdog pattern.

The upfront `done` check is intentionally redundant with the final select: it avoids spawning a goroutine on a known-dead channel, and in the race where `SendRequest` returns before the select observes the closed `done`, the upfront check ensures the call always produces `*ErrDisconnected`, not a raw transport error.

### 5. `ErrDisconnected` — new error type (`errors.go`)

A distinguisable error so callers can differentiate a permanently dead channel from a transient failure: `errors.As(err, &ErrDisconnected{})`.

## Inherited fix: `closeOnce` already present

Commit 9d411ec (the connection pool) already wrapped `RealChannel.Close`'s body in `closeOnce.Do`, which includes `closeCb` and `releasePoolRef`. The bug described in the bead (`closeCb runs outside closeOnce`) was fixed before this task. Tests for repeated and concurrent `Close` were written to verify the guard works.

## Mutation check

Each fix was temporarily disabled and the affected tests were run. Failures recorded below.

| Fix                                                  | Test(s)                                                                | Without fix                                                                                                                                                                    |
| ---------------------------------------------------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `dialDirect` watchdog                                | `TestDialCancel_DirectNewClientConn`, `TestDialCancel_DirectHandshake` | Hangs (>10s timeout); the blocking listener accepts TCP but never sends an SSH banner, so `NewClientConn` blocks forever without the watchdog closing the socket on ctx.Done() |
| `dialViaJumpHost` watchdog + `DialContext`           | `TestDialCancel_JumpHandshake`                                         | Hangs (>10s timeout)                                                                                                                                                           |
| Pool waiter `ctx` select                             | `TestPoolAcquire_WaiterCancellation`                                   | Hangs (>2s timeout); the waiter blocks on `&lt;-dialing.done` without ctx awareness                                                                                            |
| Pool waiter `ctx` select (concurrent)                | `TestPoolAcquire_WaiterCancellationConcurrent`                         | Hangs (>10s timeout)                                                                                                                                                           |
| `closeOnce` guard in `RealChannel.Close`             | `TestChannelClose_Repeated`                                            | `panic: close of closed channel`                                                                                                                                               |
| `closeOnce` guard in `RealChannel.Close`             | `TestChannelClose_Concurrent`                                          | `panic: close of closed channel` (data race)                                                                                                                                   |
| Resize disconnect detection (both `<-c.done` checks) | `TestResize_AfterDisconnect`                                           | Returns `"EOF"`, not `*ErrDisconnected`                                                                                                                                        |
| Resize `ctx` check                                   | `TestResize_CancelledContext`                                          | Returns `"EOF"`, not `context.Canceled`                                                                                                                                        |

All 9 tests fail (hang/panic/wrong error) with their corresponding fix disabled. All pass with `-race` with fixes enabled.

## Verification

- `go test -race -count=1 ./internal/ssh/` — all 24 tests pass (existing + new)
- `gofumpt -l .` — no formatting issues (project-wide)
- `golangci-lint run` — clean (repo-wide)
- `git diff HEAD -- internal | grep '^-'` — all removals are intentional replacements (signature changes, old blocking calls → new cancellable versions)

## Compromises

None. T8's deferred waiter-cancellation gap is closed. The `DialContext` discovery (available in x/crypto v0.54.0) eliminated the need for a watchdog on the jump dial itself; only `NewClientConn` (which has no context form) uses watchdog goroutines.

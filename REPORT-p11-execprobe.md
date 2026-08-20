# P11 — the `exec`-refused row, measured (bead nocx-m8jwn.11)

Measurement only. No production file was changed, nothing was committed, the tracker was
not touched.

## The sentence §6.4 can now write

> **`exec` after `pty-req`** — the refusal does not take the channel or the pty already
> granted on it: a `shell` request on that **same** channel succeeds and reaches a working
> interactive prompt on the same connection, with no second authentication.
> `conventional(exec-refused)`.

It is a **conditional** proven outcome, and the condition is observable at the moment it
matters, so the implementer never has to guess:

- the refusal arrives as a request failure and the channel is still open — this row, above;
- or the server tears the channel down as it refuses, and the client sees `io.EOF` instead
  of a plain failure. Then the prompt does **not** survive on that channel. It still costs
  no second authentication (see "recovery", below), so this branch is `session-failed`
  only if the recovery below is also refused.

**Measured on a not-yet-started session channel that already holds a pty:**

| what the client called                    | what it observes                | is the channel alive? |
| ----------------------------------------- | ------------------------------- | --------------------- |
| request refused, channel kept             | `SendRequest` → `(false, nil)`  | yes — `shell` works   |
| request refused, channel torn down        | `SendRequest` → `(false, io.EOF)` | no — `shell` errors   |
| request **accepted** (a forced command)   | `SendRequest` → `(true, nil)`   | consumed, see §3      |

`(false, nil)` versus `(false, io.EOF)` is the whole discriminator. Both were produced and
both were asserted.

## 1. Real OpenSSH cannot be made to refuse `exec` at all

This is the finding that shapes everything else, and it is why the row could not simply be
proven the obvious way.

Five ways of building a restricted account were tried against a real server
(OpenSSH 10.5p1, spawned by the test as the current uid on loopback, key auth only,
fixture-owned `HOME`). In **every** one, the `exec` channel request is **accepted**:

| mechanism                                          | `exec` request |
| -------------------------------------------------- | -------------- |
| unrestricted account                               | accepted       |
| `ForceCommand` in the server config                | accepted       |
| `command="…"` restriction on the authorized key    | accepted       |
| transfer-only account (`ForceCommand internal-sftp`) | accepted     |
| `Match` block for the user with `ForceCommand`     | accepted       |

Every mechanism **substitutes what runs behind the request**; none refuses the request. The
server config language has no option that refuses `exec` — the only session-shaped controls
it offers are `ForceCommand` (substitute), `PermitTTY` (that is the `pty-req` row),
`MaxSessions` (that is the primary-`session` row) and `ChannelTimeout` (an inactivity
teardown, not a refusal). So **the exec-refused row is not reachable on a stock OpenSSH
server**; it belongs to servers and intermediaries that implement the session channel
themselves — which is exactly why §6.4 was right to keep it open and wrong to assume it.

Assertion, in `internal/app/exec_refusal_probe_test.go`: each of the five is asserted
**accepted**, and the failure message says the row must be re-measured. If a future OpenSSH
ever refuses `exec`, that test goes red on the day it happens.

## 2. A refused session request leaves the channel — and its pty — usable

Since `exec` cannot be refused by the real server, the property was measured against it
through the requests on a not-yet-started session channel that a real server **does**
refuse:

- **`subsystem`** with an unregistered name. This is the closest analogue to `exec`: it is
  the other request that asks the server to start a program on the channel, it is dispatched
  from the same "channel has not started yet" branch, and a real server genuinely answers it
  with a request failure.
- **`x11-req`** with X11 forwarding disabled.

For both, on one connection: `session` → `pty-req` (granted) → the request (**refused**) →
`shell` on that **same** channel. Every time:

- `shell` was **accepted**;
- the shell is real and interactive — it executed a line typed into the channel and its
  **output** came back (the marker is written split, so the pty's echo of the typed line
  cannot be mistaken for the shell's answer);
- the pty granted *before* the refusal is still in force — `tty` inside that shell names a
  `/dev/pts/…` device;
- the server accepted **exactly one** authentication for the whole test: two refusals, two
  channels, two shells, one credential use, one connection.

This is a protocol-level property of the refusal path, not of `exec` specifically. What is
proven directly for `exec` is in §4.

## 3. The outcome OpenSSH actually produces, and why it is worse than a refusal

Because a restricted OpenSSH account **accepts** the `exec` request, it produces an outcome
that §6.4 does not currently have a row for. Measured with `ForceCommand`:

- the `exec` request is accepted;
- the forced command runs instead of ours and reports its exit status on the wire — the
  channel is **consumed**;
- `shell` on that same channel then fails with `io.EOF`: the session has already started, so
  the channel is gone;
- a fresh session channel on the **same connection** opens with no second authentication,
  `pty-req` is granted — and its `shell` runs the forced command too. **There is no native
  prompt anywhere on that connection.**

So the design's row depends on the *request* being refused, exactly as the brief suspected,
and the distinction is load-bearing: **refused** is recoverable to a native prompt on the
same channel; **accepted-and-substituted** is not recoverable at all, on any channel of that
connection. The two must not be collapsed. This one is a `session-failed(…)` shape in D7's
terms, and it needs its own named reason — it is not a variant of the row above.

## 4. The in-process fixture — the exact sequence, both branches

`internal/ssh/exec_refusal_probe_test.go` stands up a `golang.org/x/crypto/ssh` server that
refuses `exec`, which is the only way to drive the literal sequence §6.4 names.

**Branch A — refuse and keep the channel.** `session` → `pty-req` (granted) →
`exec` (**refused**, `(false, nil)`) → `shell` on the same channel (**accepted**), bytes flow
both ways afterwards, and the pty granted before the refusal is still the one the server
holds (its recorded column count is asserted). One connection, one authentication, one
session channel.

**The same thing through the API an implementer holds:** `gossh.Session.Start` marks the
session started only *after* the request succeeds, so a `Start` whose `exec` was refused
leaves the `Session` unstarted and `Shell()` on that **same** `Session` — and therefore that
same channel — works. The client-observable error from the refused `Start` is
`ssh: command <cmd> failed`, with no distinguishing type: **the request result, not the
error, is what an implementer must branch on.**

**Branch B — refuse and close the channel.** `shell` on the dead channel returns
`(false, io.EOF)`. A replacement `session` channel on the **same connection** then opens,
takes a pty and reaches a shell — with the server still recording exactly **one**
authentication and **one** connection. Recovering from the fatal branch therefore costs no
second credential use, which is the test D3 sets. It does consume a second session channel,
so it is available only where the server's session cap allows one (the stock cap is 10; a
cap of 1 is the primary-`session` row).

## 5. Do the real server and our fixture agree?

**On the property, yes. On what they can represent, no — and that divergence has to be
written down.**

- Where both can be measured — a request refused on a not-yet-started session channel — they
  agree exactly: the channel and its pty survive, `shell` afterwards succeeds, no second
  authentication, and the client-side signature is identical (`(false, nil)` alive,
  `(false, io.EOF)` dead). The client library is the same on both halves, which is why the
  signatures match.
- They diverge in reach: **our fixture can produce an `exec` refusal and the real server
  cannot.** So a test that exercises the exec-refused path is exercising a server behaviour
  no stock OpenSSH will ever show us. That is not a reason to drop the path — real
  intermediaries do refuse `exec` — but it does mean the fixture is the *only* place this
  row is testable, and it can never be corroborated against OpenSSH. The corroboration this
  probe offers instead is §2: the same channel-survives-refusal property, proven on the real
  server through the requests it *does* refuse.

## 6. What the two waiting packages need from this

- **The wrapper:** branch on the request *result*, never on the error text. `(false, nil)`
  → send `shell` on the same channel and report `conventional(exec-refused)`. `(false, EOF)`
  or any error → the channel is gone; open a replacement session channel on the same
  connection (never a new connection, never a second authentication), and report
  `session-failed(exec-refused)` only if that is refused too.
- **The superseding ADR:** §6.4 needs a **sixth** row for accepted-and-substituted `exec`
  (§3) — the outcome a restricted real server actually produces, in which no native prompt
  exists on any channel of the connection. Its absence is the gap this measurement found.

## 7. How to re-run

```
go vet ./internal/ssh ./internal/app
go test -count=1 -run TestExecRefusalProbe ./internal/ssh/... ./internal/app/...
```

Both were run in this worktree, and again with `-race`; all seven tests pass. The real-server
half skips cleanly where no `sshd` binary exists (and where the uid has no passwd entry, since
a non-root server can serve only the uid it runs as); the in-process half needs nothing.

Files added, both new, nothing restructured:

- `internal/app/exec_refusal_probe_test.go` — the real-server half (§1, §2, §3).
- `internal/ssh/exec_refusal_probe_test.go` — the in-process half (§4).

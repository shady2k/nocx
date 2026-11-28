# ADR-amhn5ecr — A reconnect that could not ask keeps waiting, and the route it asks about is recorded

- **Status:** accepted (2026-11-28) — `nocx-oalo1.7.2` review round; design §5.4, C16

## The question

[ADR-ydcx00yd](ydcx00yd-a-reconnect-asks-the-helper-that-holds-the-shell-through-the-carrier.md)
decided where the reconnect's route comes from (the in-process carrier) and why the
ask is three-valued. It named three things still open, and the review of that change
found four more on the paths it touched. This record decides the three that have more
than one plausible answer.

## Decision 1: an unaskable inventory is a WAIT, and the automatic loop keeps asking

The reconnect's refusal already reached the renderer as `pane.connection.shellFailure`.
`panedriver` had one shape for it: a failure, so the automatic attempt loop stopped and
a person had to press. That is right for a route that no longer leads where it led and
for a helper that answered and does not hold the shell — a retry cannot change either.
It is wrong for the third answer, "nobody could be asked": a helper that is restarting,
an inventory that timed out, a daemon that would not answer. The shell may be alive and
accepting keystrokes, and C16 exists precisely so a slow helper does not cost a person a
second shell — a rule that buys nothing if the cost is that the pane sits still until
somebody presses a button.

So the failure carries `retryable`, the reattacher answers the unaskable cases with a
typed `transport.PaneShellWait`, and the driver runs the retry as a loop of its own:
its own backoff (1 s doubling to the supervisor's 15 s cap), stopped by the pane
closing, by a shell being adopted, by the connection leaving `connected`, or by a
refusal that stops being a wait. `retryable` is on the wire because a surface must be
able to say "waiting for the host to answer" instead of "shell failed" over a shell
that may still be running.

**The loop's wait is a field, not a constant**, so a test makes it zero and asserts the
decision rather than the timer. Nothing in the suite sleeps to find out whether a retry
happened.

## Decision 2: the binding records the complete route, and a typed route is re-adopted from it

ADR-ydcx00yd left this open because the in-process case does not need it. The cold case
does: `readoptPass.Readopt` demanded a `ProfileID`, so a session a person opened by
typing `ssh dev@host` was unreachable to the next coordinator, and its pane opened a
second shell over a live one.

The `sessions` binding's payload now carries `route` — canonical host, effective port,
account, the first jump hop, and the profile id when there was one — written by the open
that resolved it. A cold re-adoption with no profile rebuilds its connect configuration
from those facts and asks the same host again. Nothing secret is in it: a saved route
names the profile that holds the key, and a typed route names the host `ssh` itself
resolves, whose configuration files and agent are the reference.

The same route makes the refusal exact on both paths. A saved connection edited to
another host, account or port resolves to a route that differs from the recorded one,
and the answer is "the old shell is out of reach; nothing was opened" — before any
inventory is queried and before any spawn at the new destination. The live path checks
this against the carrier's own options; the cold path against the binding's row.

## Decision 3: a re-attachment delivers from its own resume point, with one recorder

The transport half of a re-attachment (`ReadoptHostedSession`) builds the receiver at
the offset this machine's recording ends at, starts the read pump and starts the
recorder. The open that then adopts the same session used to run its fresh-open tail as
well: a second recorder over the same recording, and a delivery pump at offset 0 against
a ring whose base is the recording's end — which needs a reset the ring never gives, so
the subscriber's stream stopped before its first byte while the pane looked alive.

So `pane.opened` now carries `replayFrom`: the offset that stream continues at, which
the transport takes from the ring's own base and the renderer adopts as its cursor. A
re-attached open skips the fresh-open tail (pump, recorder, monitor) and starts delivery
there. One session has one recorder, one read pump and one teardown owner.

The teardown owner is also fenced by incarnation: a re-attachment gives the same session
id a new epoch, and the previous incarnation's `monitorExit` can reach its receiver
removal after the new one has installed its own. The receiver is the claim every
id-keyed teardown hangs off — the ring, the subscriber slot, the lanes, the files and git
bindings — so it is removed only by the incarnation that owns it, and an older monitor
that finds a newer receiver there closes its own ring and touches nothing else.

## Consequences

- `pane.opened` requires `newShell` and `replayFrom`, and `shellFailure` requires
  `retryable`: protocol 48.
- An unaccepted RE-ATTACHMENT is rolled back by detaching, never by ending: the shell
  predates the open, and a later reconnect can still take it back. The rollback of a
  newly spawned shell keeps #888's rule — it is ended, because nothing else ever names
  it.
- A pane hosted by THIS machine's helper re-attaches through the same question as a far
  one, which it could not before: it has no far carrier, so its route is the binding's
  and its carrier is the local route.
- **Still open:** the unresponsive-helper deadline, an answer lost in flight, and
  coordinator crash recovery keep their existing designs, and a re-attachment whose
  write lease another coordinator holds is a refusal a person acts on, not a wait.

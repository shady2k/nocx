# ADR-ydcx00yd — A reconnect asks the helper that already holds the shell, and asks it through the carrier

- **Status:** accepted (2026-11-28) — `nocx-oalo1.7.2`; design §5.4, C16

## The question

A transport loss kills the coordinator's channel, not the far helper's shell. Today
`WSServer.ReconnectPane` ends the pane's session and runs the open again, so the person
gets a second shell beside one that is still running — whose `make build` is now
unattended while the tab shows a prompt. The design says: ask the helper's inventory
first, reattach when it holds the shell, and open a replacement only on a confirmed
absence.

Two things had to be decided that §5.4 states in one line each, and both have more than
one plausible answer.

## Decision 1: the transport asks before it ends anything, and the answer is three-valued

`ReconnectPane` used to end the pane's sessions and then open. Ending a helper-held
session asks its helper to **end the shell** (`session.RegisterHostedSession`'s channel
`EndSession`), so end-then-open could only ever produce a replacement.

- A pane with **nothing to reattach** (no helper-held shell: a local PTY, a plain shell,
  a pane that never adopted one) keeps the old order — end, then open. Nothing can be
  preserved, and the old order is what its lifetime rules were written against.
- A pane that **may still have a shell** asks first, and ends what it held only after
  the open answered, skipping the session the open answered with.

The answer is three values rather than a boolean because C16 turns on telling them
apart: _taken back_, _answered and does not hold it_ (the only case that justifies
another shell), and _nobody could be asked_ (a wait, never a shell). A boolean would
make "the helper is slow" and "the shell is gone" one value, and the first would spawn
a second shell beside a live one.

## Decision 2: the route comes from the in-memory carrier, not from a persisted route

§5.4 says a pane row persists its complete route (host, port, account, identity
reference, jump route, profile when there is one) so an unsaved route can reattach. That
is still what a **cold start** needs, and it is still missing there: `readoptPass.Readopt`
requires a `ProfileID`, so a typed host that is no longer saved cannot be taken back
across a coordinator restart.

A transport loss is a different case, and it does not need the row: the coordinator that
lost the connection is the one that opened it, and it still holds the **hosted carrier**
that opened it — `helperRegistry.hosts[sid]`, which carrier loss does not forget (D1).
That carrier holds the host, the account, the host-key pin and the connection _options_,
which are the complete route, in the exact form the open dialled with.

- Re-deriving the route from the profile store would ask a store that can be edited
  between the open and the reconnect, and a route resolved differently is a different
  machine (the refusal `nocx-k6p18.15` exists for).
- Persisting port/identity/jump on the pane row for this case would be a second copy of
  a fact the carrier already holds, with no reader: the cold-start path is the reader,
  and it is not this path.

So the reattachment passes `carrier.f.opts` as the session's options — that is what the
new `session.Config.Options` field is for, and why `Reg.Adopt` consults it before the
`Remote` config — and the reattacher binds the pane's restored connection with those same
options rather than re-resolving a destination.

## Consequences

- A `pane.opened` now carries `newShell`. It is the divider's own fact and is not
  derivable from the session id: both a reattach and a replacement can answer with an id
  the renderer has not seen, and only this says whether the old shell may still be alive.
- A pane whose shell the helper **reported ended** forgets it (`forgetHostedShell`), so a
  reconnect after an ordinary shell exit opens a replacement instead of asking a question
  the helper already answered.
- A reattached shell resumes its lifecycle stream at the cursor the dead attachment
  stored (`LedgerRepository.LifecycleCursor`), not at the helper's window head; replaying
  a window this coordinator already applied would record its commands twice.
- **Still open:** taking an unsaved route back after a coordinator RESTART. It needs the
  persisted route §5.4 describes, and that is a separate change to the content schema and
  to `readoptPass.Readopt`'s route requirement.
- **Still open:** a reconnect for a pane whose carrier the process no longer holds waits
  and says so. Nothing can be asked, so nothing is opened; a person presses Reconnect
  again, or closes the tab.

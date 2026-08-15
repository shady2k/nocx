# ADR-0032 — The vault raises its own unlock

**Status:** accepted
**Date:** 2026-08-15
**Bead:** `nocx-y7fg` (filed 2026-07-31, P0, still open)

## Context

A sealed vault cannot answer for a secret. Something has to ask the person to
unlock it, and today that something is **the caller**.

`RequestUnlock` exists on the transport, and `vault.unlockResolved` is in the
closed ingress-critical set precisely so an unlock resolution can never queue
behind the lane. But the requester is threaded into exactly one call site:

```go
// internal/app/app.go
resolver := connection.NewResolver(
    profileStore, profileStore, v,
    connection.WithUnlockRequester(tp.RequestUnlock),
    ...
)
```

So opening an SSH connection raises the unlock dialog because somebody wired it
there by hand. `secrets.Get(ctx, id)` from anywhere else returns an error, and
that caller is left to describe the obstacle instead of clearing it.

The AI endpoint path arrived without it, exactly as any fourth caller would.
Its Test button reported _"the endpoint's credential is unavailable — unlock the
vault"_ — telling a person to do the thing the product already knows how to
offer. A dead end with good manners.

**This was already diagnosed, correctly, on 2026-07-31.** `nocx-y7fg` says it
in one line — _"Needing the vault is a property of the call, not of the call
site"_ — and then predicts precisely what happened next: _"Doing it per call
site guarantees the next new method reintroduces the bug."_ It was raised P0
and never finished, so the mechanism stayed per-call-site, and two weeks later
`endpoints.probe` became the next new method. `nocx-25k9.22` had fixed an
earlier instance of the same shape before that.

This ADR does not decide anything new. It writes down a decision that was made
once, acted on twice, and recorded nowhere a later reader would look — which is
why it kept having to be rediscovered. The owner, on being shown the third
instance: _"Да, сам vault, когда понимает, что запрос требует хранилище,
должен сам запускать разблокировку. Мы не должны это делать во всех местах, где
нужен vault."_

## Decision

**The secret-access layer raises the unlock, and callers do not.**

A caller asks the vault for a secret. If the vault is sealed, the vault raises
the unlock, waits for the resolution, and answers the original request. The
caller sees a secret or a refusal; it never sees "sealed" as a state it has to
know what to do about.

Threading `RequestUnlock` into the assistant path as well was the obvious fix
and is rejected: it would make a **third** owner of one behaviour, and the
fourth caller would arrive without it for the same reason this one did. This is
AD-8 applied to a behaviour rather than a package — one owner, and the owner is
whoever already has the vault.

## Consequences

- The seam is the DISPATCHER, as `nocx-y7fg` named it: a call that fails for
  want of an unsealed vault raises the prompt and is replayed. It is the same
  one place every control request already passes through — `connMethods` in
  `internal/transport/registration.go`, where the params middleware landed
  (`3b47ae3`). The wrapper normalizes any sealed-vault failure into the
  canonical shape (code `-32001`, reason `vault-sealed`) — recognized by the
  canonical shape or by the vault's own words in the message, so a handler
  never has to remember which one it used. A method written next year that
  returns a bare sealed error still fires the seam.
- The REPLAY OWNER is the renderer's dispatcher
  (`frontend/src/dispatcher.ts`): on seeing the canonical reason it raises
  the unlock dialog — the vault layer owns the prompt, one dialog coalescing
  concurrent sealed calls — and re-sends the request verbatim. The re-sent
  request is a fresh submission, so the operation's gates and lane permit,
  released when the failed attempt returned, are free for it: the call
  completes. Two single owners, nothing per call site.
- **Why the backend cannot be the replay owner.** The first draft put the
  replay here, in the dispatcher: catch the sealed error, call
  `RequestUnlock`, block for the resolution, re-run the handler. It cannot
  work, and the reason is the admission model, not the read loop. A handler
  emits its sealed error from INSIDE the capability operation's callback
  (`h.op.Run(ctx, cb)`, where the callback writes `h.r.TryError`) — the error
  is the callback's answer, not its return. `op.Run` holds the operation's
  composite admission (the conflict gates and the lane permit) for the whole
  callback, so a synchronous re-run of the handler inside the unwinding
  `TryError` re-acquires an admission the first attempt still holds: measured
  as "Control plane busy" on `vault.inventory`, `vault.resolveLine` and
  `profiles.importTabby`. Re-submitting the replay through the method's own
  submission works only for the ordered submissions; a bounded-lane method
  refuses the second task while the first still holds its permit (the lane is
  non-waiting by design, ADR-0026). The renderer's re-send has neither
  problem because it is a genuinely new request with no first attempt holding
  anything. The decision was made deliberately, not discovered in review:
  "Stop and ask me if the deadlock constraint and the replay requirement turn
  out to conflict" — they did not conflict with the read loop, they conflicted
  with the admission model, and the owner chose the renderer as the replay
  owner.
- `connection.WithUnlockRequester` comes off the resolver. A sealed vault is a
  sealed-vault failure that propagates to the handler that surfaced it; there
  is no per-consumer prompting left anywhere in the backend.
- Cancellation is part of the contract, not an afterthought: a dismissed
  unlock reaches the caller as a distinct, recognisable refusal — the shape
  `VaultOperationCancelledError` already has on the renderer side — so a
  person who chose not to unlock is never shown a failure they did not cause.
  With the renderer as the replay owner this falls out of the existing dialog
  machinery; nothing on the backend blocks, so the backend never has to
  describe a cancel.
- Re-entrancy is answered by the renderer: several requests may reach a
  sealed vault at once and must not raise several dialogs. One dialog in
  flight, and everyone waiting resumes from the same resolution — the vault
  layer coalesces on one promise.
- Deadlock is answered structurally: the backend never blocks on an unlock —
  the normalization is a pure rewrite, safe on the read loop — and the closed
  ingress-critical set is untouched: `vault.unlockResolved` and
  `connections.passwordResolved` still run inline on the read loop, so a
  resolution never waits behind the lane. The rule is not weakened; nothing
  new can block the read loop.
- "The vault may be locked" stops being a product sentence in most places. It
  survives only where a status is genuinely being _reported_ rather than a
  secret being _fetched_: `agent.status` carries a single `credential` enum —
  `resolvable` / `none` (no reference at all) / `deleted` (the secret is
  gone) / `sealed` (the vault cannot answer right now) / `unavailable` — and
  each fact gets its own sentence in `agentStatusLine`. An endpoint with no
  key is not told to unlock a vault. A status read on a sealed vault never
  raises the prompt: `credentialStateFor` swallows the sealed condition to
  report it, and the no-prompt boundary is asserted by test.

## Alternatives considered

**Thread the requester into each consumer** — what the code does today. It works
until somebody forgets, and the record shows that somebody forgets: the
endpoints path, written after the mechanism existed, did not use it, and nothing
reported anything missing because nothing required it.

**Report the sealed state and let the surface decide.** Honest, and it makes
every surface reimplement the same dialog-and-resume. It also produces exactly
the message this ADR exists to delete.

**Never seal while the app runs.** Rejected on its face: auto-seal is a feature
and the vault's whole point is that it can be shut.

# ADR-0033 — `auto` is the name for "not yet answered", not a fourth delivery mode

- **Status:** Accepted
- **Date:** 2026-08-15
- **Related:** the delivery-modes design
  (`.internal/specs/2026-08-05-nocxify-delivery-modes-design.md`, N1 and N3,
  §3.5 "three axes, never one enum"), the footprint-consent design
  (`.internal/specs/2026-08-10-remote-footprint-consent-design.md`, §4
  resolution order, §5.2, §5.3), the remote-helper design
  (`.internal/specs/2026-08-13-remote-helper-design.md`, D8), AD-5 (two-tier
  shell integration), `nocx-7iisi`, `nocx-f4o70`, `nocx-p15s`.

## Context

The delivery axis answers "what may nocx do with this destination":
`raw` adds nothing, `script` installs the shell tiers we ship, `relay` deploys
the Tier-B helper binary. N1 introduced those three and deleted the older
`ShellIntegrationPolicy = auto | ask | off` outright; §3.5 split what that one
enum had conflated into three independent axes — desired mode, observed
delivery, relay consent.

Three documents then went on naming a mode called `auto`. The footprint-consent
design §5.3 says "the connection form carries `desiredMode`, defaulting to
`auto`", and its whole §4 resolution order is built on that rung — step 4 is
the `auto` branch, "resolve the best available tier; present → honour it
silently; absent → ask, before anything is written." D8, written later, keeps
the vocabulary: "`auto` resolves to `relay` only when a surface on that
connection has asked for the helper", and "a machine at an explicit `script` is
not silently upgraded — `script` is an answer, not a gap."

The code has three values and a fourth one hidden. `profile.DesiredMode` is
`raw | script | relay`; the connection form offers those three; the hardcoded
cascade default is `script` (`internal/profile/profile.go`,
`hardcodedDefaults`). `internal/app/consent.go` declares
`const DesiredAuto profile.DesiredMode = "auto"` — known only inside that
package — and treats an absent mode as `auto` when it decides. So one absent
value resolves two ways: the open ack reports `script`
(`internal/transport/ws.go`, `desiredModeForAck`) while the resolver decides as
`auto` and may raise the consent ask. The product calls a connection `script`
and then offers to upgrade it, which is the exact thing D8 forbids — arrived at
through a gap between two defaults rather than through anyone's decision.

The question that forced this ADR: is `auto` needed at all, or should the
default simply be `script` — or a mode called `ask`?

## Decision

**`auto` is a real, stored value of the delivery axis, and it is the hardcoded
default. It means "the user has not answered", not "a fourth kind of
delivery".** `ask` is not added back as a mode.

The hardcoded cascade default becomes `auto`; `DesiredAuto` stops being a
constant one package knows and becomes a value the profile, the cascade, the
form and the wire all carry. An absent mode resolves to exactly one value
everywhere, so the ack and the resolver cannot disagree.

Consent stays outside the cascade: it is keyed by the destination's host-key
fingerprint (`internal/helper/consent`), because a group cannot answer for a
machine and one machine reached two ways is still one machine.

## Rationale

- **`script` cannot be both a choice and a silence.** D8's rule — an explicit
  `script` is never silently upgraded — needs something to bite on. If `script`
  is also what "I never opened this connection's settings" resolves to, the
  product cannot tell the two apart, and honouring D8 means never offering the
  helper on any connection the user has not hand-edited. The user would have to
  visit every connection just to become askable. A distinct `auto` is what makes
  `script` mean "I want scripts, do not offer me the binary" — an answer that
  can be honoured because it can be distinguished.
- **`ask` as a mode re-collapses the axes §3.5 separated.** It conflates what
  the user wants (raw/script/relay) with when we ask them, which is not a
  property of the destination at all — it is a function of whether a stored
  answer exists for that host key. Asking is what `auto` _does_ when it has no
  answer yet, on the axis that owns answers. It would also immediately owe a
  second question: ask about the scripts or about the binary? §5.2 requires
  those stay two questions, because declining a deployed binary must not also
  decline shell scripts — different risks.
- **`auto` is not a privilege escalation.** It resolves to `relay` only when a
  surface on that connection has actually reached for the helper (D8's
  `requested` condition) and only after the ask is answered. Nothing is written
  to the host before the ask (§4 step 4), and the fail-closed default of the
  resolver is refusal. Shipping a helper binary in the app therefore does not
  opt any machine in, which is the failure D8 exists to prevent.
- **The alternative — deleting `auto` from the designs and keeping `script` as
  the default — was rejected** because it makes the ladder unreachable by
  default. Every user who wants the Git panel on a remote host would edit each
  connection by hand, and the product's own refusal text already tells them to
  switch to a mode that would no longer exist.

## Consequences

- The cascade grows a rung that must be expressible at every level: a profile
  can say `auto` explicitly when its group says `raw`. `validDesiredMode`
  accepts it; the connection form offers it; the wire carries it.
- `desiredModeForAck` and the consent resolver must derive from one answer.
  Two defaults for one absent value is the defect this ADR closes
  (`nocx-7iisi`).
- Every user-facing refusal names only modes the form actually offers. Today
  `refusedHelperReason` tells the user to pick "Auto", which is unreachable.
- **The default is only half the answer to "must I edit every connection".**
  The cascade's global layer exists in the signature
  (`ResolveEffectiveProfile`'s `globalDefaults`) and in the renderer's
  provenance label ("from global defaults"), but every production caller passes
  an empty `SparseSSHOptions{}` — there is no store and no surface. Until that
  is filled, a user who wants "relay everywhere" or "script everywhere, never
  offer the binary" still has no single place to say it. `nocx-p15s` holds the
  contract change that wiring owes; the surface itself is separate work.
- Relay's relationship to `profile.RelayConsent` is deliberately left open here
  and decided in `nocx-f4o70`: today the field is stored, backed up and
  validated but read by nothing that decides, while the form's hint claims a
  rule the code does not apply. This ADR fixes what an unanswered destination
  means; it does not decide who owns the answer about the binary.

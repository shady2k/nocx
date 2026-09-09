# ADR-0057 — On your own machine there is no Tier A fallback

- **Status:** Accepted
- **Date:** 2026-09-04
- **Related:** AD-5 (the two-tier shell-integration substrate), `D11` and `D12` of
  the level-1 helper design, `N3` of the nocxify delivery-modes design,
  [ADR-0034](0034-consent-belongs-to-the-machine-not-the-connection.md) (consent
  belongs to the machine), [ADR-0004](0004-shell-integration-fail-open.md)
  (shell integration fails open).
- **Narrows:** AD-5's rule, for the local machine only. AD-5's text is left
  exactly as it stands; it was right when it was written and it remains right for
  every host that is not the one you are sitting at.
- **Epic:** `nocx-ie23r`.

## Context

AD-5 says Tier A — OSC 7/133 through shell hooks, zero install — "remains the
substrate **wherever no helper is installed**", and names local operation among
the places that holds. When it was written, that was the whole of the local
story: nocx spawned local PTYs itself, and the helper was a thing you deployed
onto somebody else's host.

`D11` of the level-1 design changed the premise on 2026-08-31: the helper runs on
every machine, including yours, and there is no local special case. `nocx-ie23r`
builds that half. From then on, "no helper is installed" locally does not mean a
host we could not reach — it means **this copy of nocx is broken**, because the
helper binary ships inside the application and starts under the account already
running it.

That is the distinction this record exists to make, and it is not a matter of
taste. On a remote host, a helper failure is the environment answering: a policy
that forbids installing, a read-only filesystem, a platform we ship no artifact
for. Degrading to Tier A there is correct and AD-5's rule is why. On the local
machine there is no environment to answer — the artifact is embedded, the account
is ours, and the only reasons the helper does not come up are a failed
extraction, a failed start, or a binary that is not what we installed.

## Decision

**When the local helper cannot be installed, started or reached, nocx does not
open the pane by another route. It refuses, and the refusal names what failed,
why, and what to do about it.**

Tier A is not used as a local fallback. There is no second local PTY owner to
fall back to: `internal/app`'s `localPTYFactory` is deleted by the same epic, and
a source-walking test keeps `pty.NewLocal` constructible in exactly one place.

AD-5 is otherwise untouched. Tier A remains the substrate on every host where no
helper is installed, level 0 is not deprecated, and a host that forbids installing
anything still gets blocks. This narrows one sentence of AD-5's rule to exclude
one machine.

## Rationale

The alternative — keep Tier A behind the local helper — costs more than it looks.
It is a **rarely executed** path: it runs only when the daemon is broken, which is
never during development and never in CI, so it is the one path that is not
exercised by anybody's ordinary day. A path like that diverges silently. The
person then gets a terminal that looks like nocx and behaves differently in ways
nobody chose — a different PTY owner, a different session lifetime, blocks
delivered by a different tier — and finds out weeks later through a bug whose
cause is three steps away from its symptom.

Refusing is worse for one minute and better afterwards, because the failure is
reported where it happened, in the words of what actually broke.

This does not contradict ADR-0004's fail-open stance for shell integration.
ADR-0004 is about the _integration_ failing while the shell runs: nocx must not
refuse to run your program because a feature of nocx's is unavailable. The same
rule is why `__nocx_agent_run` prints "not orchestrated" and still starts the
agent. Here it is not a feature that is unavailable — it is the thing that owns
the PTY, so there is no shell to fail open into.

## Consequences

- **A refusal that cannot name an action is a defect in the refusal.** "What to
  do" is a required third part, not a courtesy; `nocx-ie23r.4` fails its tests
  without it.
- **The blast radius of a broken local helper is total**: no local pane opens.
  That is the price of the decision and it is stated here rather than discovered.
  It raises the bar on `nocx-ie23r.1`'s failure tests, which is the intended
  trade.
- **A platform with no helper artifact has no local helper**, and this record does
  not cover it. Today `deploy.ErrUnsupportedPlatform` names windows, 32-bit and
  the BSDs — none of which the application itself targets — so the sets do not
  intersect. If the app ever targets one, this decision is re-argued, not quietly
  excepted.
- **AD-5's text in `docs/architecture.md` still reads as it did.** A reader of
  that file alone will believe Tier A is the local fallback. That is deliberate:
  old records are kept as they were true. Anyone acting on AD-5 for the local
  machine needs this record beside it.

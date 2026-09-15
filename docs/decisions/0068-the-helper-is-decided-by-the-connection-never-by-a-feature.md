# ADR-0068 — The helper is decided by the connection, never by a feature

- **Status:** Accepted
- **Date:** 2026-09-15
- **Supersedes:** D8 of `.internal/specs/2026-08-13-remote-helper-design.md` ("consent is
  asked when the user reaches for the feature, not when a connection is made"), and the part
  of [ADR-0034](0034-consent-belongs-to-the-machine-not-the-connection.md) that names the Git
  panel's consent card as the surface that writes the grant. ADR-0034's decision — the answer
  is keyed by the destination's host-key fingerprint and lives in `internal/helper/consent` —
  stands. Neither record is edited.
- **Related:** [ADR-0033](0033-auto-is-the-name-for-not-yet-answered.md) (`auto` is "not yet
  answered"), [ADR-0057](0057-on-your-own-machine-there-is-no-tier-a-fallback.md) (every pane
  is helper-hosted). Bead `nocx-y6fh7`.

## Context

D8 put the helper consent at the moment a person reaches for a feature that needs it: the Git
panel on an SSH session answered `consentRequired`, offered an Accept card, and accepting
raised the machine to the helper tier. Its reason was real — a ladder that installs a binary
wherever one exists would ask every user about a feature they never reached for.

Two things stopped that holding. The integration method is a property of the connection: it
decides how the session itself is carried, so it has to be settled before the session exists,
not revised by a panel afterwards. And the Git panel path is broken in exactly the way that
shows it is in the wrong place: a helper-hosted SSH pane carries no host key to the panel, so
the card's Accept is refused with "this session has no host key — consent cannot be granted"
(`e2e/git-remote.spec.ts:248`, `e2e/remote-coordinator-reclaim.spec.ts:425`, measured
2026-09-15). The fingerprint exists at one moment — the connect, when the helper asks the
coordinator to verify the host key (`proto.OpVerifyHostKey`) — and that is where the question
belongs.

## Decision

**The integration method is decided in exactly two places: on the saved connection, or at the
moment of connecting.** A connection whose delivery axis names the method is honoured without
asking. A connection at `auto` (ADR-0033) is asked when it connects, beside the host-key
verification, and the answer is recorded as ADR-0034 records it — by host-key fingerprint.

**No feature surface may initiate, offer or request installing the helper.** The Git panel,
the file browser, completion and anything added later consume whatever the connection
decided. On a session without the helper, such a surface says what it cannot do and names the
connection setting that would change it; it carries no Accept, and no RPC it calls may raise
a machine's tier. `git.open`'s `consentRequired` answer and the panel's consent card are
removed.

## Rationale

- **One owner for one decision** (AD-8). A panel that can raise the tier is a second owner of
  how a connection is carried, and it disagrees with the first the moment the connection was
  set to `script` on purpose.
- **The evidence is at the connect.** The host key the grant is keyed by is observed during
  the handshake; a later surface can only re-derive it, which is the second source of truth
  ADR-0034 was written against.
- D8's worry — asking people about a feature they never reached for — is answered by the
  `auto` ask happening once per machine at connect, not by moving the ask into the feature.

## Consequences

- The Git panel's consent card, `shell.footprint.consent` as a panel-initiated write, and
  `git.open`'s `consentRequired` are removed; their contracts shrink in the same commit.
- `e2e/git-remote.spec.ts` and `e2e/remote-coordinator-reclaim.spec.ts` are rewritten to grant
  the helper where a person now does: on the connection, or at connect.
- Revocation stays on the remote-footprint screen (ADR-0034 §5.3); the missing Deny
  (`nocx-j49sp`) now belongs to the connect-time ask.

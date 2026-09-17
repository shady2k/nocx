# ADR-0034 — Consent to deploy the helper belongs to the machine, not to the saved connection

- **Status:** Accepted
- **Date:** 2026-08-16
- **Related:** [ADR-0033](0033-auto-is-the-name-for-not-yet-answered.md) (the delivery
  axis and its default), the footprint-consent design
  (`.internal/specs/2026-08-10-remote-footprint-consent-design.md`, §3.2, §3.3, §5.2,
  §5.3), the delivery-modes design
  (`.internal/specs/2026-08-05-nocxify-delivery-modes-design.md`, §3.5 "three axes,
  never one enum"), AD-8 (one owner per behaviour), `nocx-f4o70`.

> **Naming, 2026-08-31:** `relay` below is the historical name of the third
> delivery mode and of the deployed remote binary; both are now called the
> **helper**. The argument is left exactly as it was written (`nocx-0xq2s`).

## Context

Deploying the helper binary on a remote host needs the user's permission. Two
places claimed to record that permission.

`profile.RelayConsent` was a three-state field on the saved connection —
unknown / granted / denied — offered in the connection form under a hint reading
"the relay deploys a binary on the destination; that needs explicit consent per
host, and a relay selection without granted consent behaves as raw". It was
persisted, backed up, validated, carried on the `profiles.effective` wire as a
required field, and read by **nothing that decides**: `grep` found it only in
backup serialisation and a bounds validator, and `internal/connection/resolver.go`
did not even copy it onto the `ConnectConfig`.

`internal/helper/consent` is the store that actually decides, keyed by the
destination's host-key fingerprint, written by `shell.footprint.consent` from the
Git panel and revoked from the remote-footprint screen.

The hint also stated a rule the code does not apply: `Resolve` answers an explicit
relay with `DesiredRelay` immediately — "an explicit relay choice is the consent
for the binary" — consulting no `RelayConsent` at all. The frontend's two-owners
lint carried a baselined violation on exactly that line.

The design is not univocal here. §5.2 says relay "consults its own `RelayConsent`,
unchanged", and gives a reason worth keeping: "declining a deployed binary must not
also decline shell scripts — different risks". §4.3 says a first install "always
comes from the trust/ask moment, **the connection form**, or an explicit reconnect".
The code implemented §4.3 silently. Both paragraphs are about _when_ permission is
given; neither asks what permission is _about_.

## Decision

**The answer is keyed by the destination's host-key fingerprint and lives in
`internal/helper/consent`. `profile.RelayConsent` is deleted** — the field, its
constants, its validator, its patch path, its backup projection, and the required
`relayConsent` property on `contracts/profiles.effective.schema.json` — together
with the connection form's control and hint.

What a saved connection can still say about the binary is on the delivery axis:
`relay` means "allow it on this connection without asking".

## Rationale

- **A profile is not a machine.** One machine is commonly reachable through several
  saved connections — a second login user, a route through a bastion, the same host
  under another name. A per-connection answer is asked once per connection and can
  contradict itself, with nothing to say which answer is true of the machine. This
  is the property the deleted field could not have had, and it is now asserted:
  `TestOneMachineOneAnswerAcrossConnections`.
- **A rebuilt server must be asked again, and only the host key knows.** A re-imaged
  machine at the same address presents a new fingerprint, so the old grant does not
  carry over — which is the correct answer to "may nocx deploy a binary here", because
  _here_ is somewhere else now. A profile-keyed answer cannot see that at all
  (`TestARebuiltMachineIsAskedAgain`).
- This is what the consent design's own §3.2 already decided for the store —
  "the answer is keyed by the remote host's public key … never the hostname, the
  profile or the route" — and §5.3 puts revocation on the footprint screen. The
  profile field was the part that never followed.
- **Two owners is the defect, whichever wins.** Wiring the field up instead would
  have satisfied §5.2's letter while re-introducing the contradiction above, and the
  losing surface goes on advertising what it cannot deliver.
- §5.2's _reason_ survives the deletion intact: the two answers stay unmerged,
  because declining the binary has never touched script delivery. ADR-0033 and
  `nocx-7k8ma` put that on the other side too — relay allows the binary and still
  integrates.

## Consequences

- `profiles.effective` no longer carries `relayConsent`. Its schema object is closed,
  so a reintroduction fails the contract rather than passing unnoticed; the
  over-the-wire test asserts the absence directly.
- Three doors are held shut rather than merely unused: the group-defaults allowlist
  still rejects `relayConsent` as an unknown key, `options.relayConsent` is no longer
  an allowed patch path, and the connection form is asserted to raise no consent
  control for a relay selection.
- The frontend two-owners baseline shrinks to zero.
- **Denied is currently unreachable from any surface.** The store models it and the
  resolver honours it — a denied machine is never asked again and never upgraded —
  but the Git panel's consent card offers only Accept, and `internal/helper/consent`
  exposes no Deny at all. That is a gap in the ask, not in the ownership this ADR
  settles; it predates the deleted field, which could spell "denied" but governed
  nothing. Filed as `nocx-j49sp`.

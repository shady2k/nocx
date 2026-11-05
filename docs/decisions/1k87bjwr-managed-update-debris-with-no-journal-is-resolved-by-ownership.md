# ADR-1k87bjwr — Managed update debris with no journal is resolved by ownership

- **Status:** Accepted (implementation of `nocx-vh2hr.1.14`, 2026-11-04; replaces the
  "No record, but managed debris exists" rule in §7.3 of the distribution-and-updates
  design, which had refused)
- **Beads:** `nocx-vh2hr.1.14`, owner evidence on `nocx-vh2hr.1.6`

## Context

The update transaction owns three names beside the installed bundle:
`.nocx-swap.app`, `.nocx-backup.app` and `.nocx-update-<txID>/`. Their intervals are
not the journal's interval, and one of them outlives it on purpose: finalisation
renames the swap peer to the backup and only then deletes the record, because the
backup is the rollback copy the person may need after the update has been accepted.
So **every successful update leaves a managed file that no journal names.** A
second path produces the same shape: a failed swap→backup rename logs a warning and
deletes the journal anyway, leaving the swap peer behind with no record at all.

The design's round-3 rule refused whenever it found managed debris with no record,
on the sound reason that acting on a path name is guessing. The consequence was not
a guess avoided but a machine that could never update again: reconcile refused at
startup and again before every apply, and the only words the person got were that
the update could not finish. The evidence for the first failure was gone with the
journal, so there was nothing to diagnose from either.

## Decision

Reconcile resolves the two managed names itself when no journal explains them,
after asking the platform what the object actually is:

- **Ownership is proved, not assumed.** A `Platform.InspectBundle` call reports
  whether the path holds nocx's own artefact — `CFBundleIdentifier` equal to the
  application identity on macOS (ADR-0103), the ELF + AppImage type-2 shape on
  Linux — and the version the artefact declares where it declares one. An
  unreadable bundle answers "not nocx's", because unreadable is not proof.
- **Owned debris is removed** unless it is another name for the running install
  (same device and inode) or holds a release newer than the one running. Those two
  are kept: one would delete the running application, the other the only copy of a
  newer release.
- **A bundle that is not nocx's is refused**, with the path in the message and the
  step that clears it. Deleting another application is not recovery, and the
  renderer now names that path in the toast instead of the generic sentence.

## Alternatives rejected

**Delete at a managed name without inspecting it.** Cheapest, and wrong: the name is
a claim by whoever created it, and the first non-nocx `.app` a person drops there
would be destroyed by an update they did not ask to be destructive.

**Keep refusing and improve the message.** The message was already the second
complaint. It leaves the person to delete a dotfile in `/Applications` by hand, and
that is exactly the step the evidence shows them failing to discover.

**Keep the backup only when its version is older than the install.** That is the
normal direction and would delete nothing that matters, but it cannot be decided on
Linux, where the artefact carries no readable version — the rule would then depend
on the platform, and the two rules would disagree the first time a copy went the
other way.

## Consequences

A stale managed name no longer blocks any update. The backup an earlier update left
is deleted by the next reconcile instead of surviving as a rollback copy — it was
never usable as one, because nothing named it. A newer copy is preserved but no
longer prevents the update; the person keeps it until they move it themselves.
Refusal remains exactly one case: a foreign bundle at a managed name, now with the
path to act on.

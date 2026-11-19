# Interface rules

Behavioural rules for nocx surfaces that sit above the kit. The kit's own contract — which
component, which identity class, spacing, validation, toasts — is
[`frontend/src/ui/README.md`](../../frontend/src/ui/README.md) and wins where the two
overlap. Each rule names the feature that introduced it, so a later feature can tell an
inherited rule from a local choice.

## A setting that is not the edited record's says whose it is

When a form edits one record but a field in it belongs to another owner — the machine, the
group, the whole app — the field's `Field` description says so in one sentence, naming the
owner by what a person recognises (the host name, not a fingerprint), and what else the
change reaches. No badge or icon replaces the sentence: a badge says "different" without
saying how.

Introduced by: integration method on the host (`nocx-oalo1.11.2`,
[mock](ssh-foundation/host-integration-method.html), frame 1).

## A setting that cannot be set yet is shown, not hidden

When a field's owner does not exist yet (a host never connected, so no fingerprint), the
control is shown **disabled with its effective value**, and the description says what makes
it settable. Hiding it moves the control around between records and leaves a person looking
for it where it was. Never let the control accept a value that will be written under a
guessed key.

Introduced by: `nocx-oalo1.11.2`, frame 2.

## A field whose value failed to load says so; it never shows a default as if read

A failed read disables the control, puts the reason under it as the `Field` error and offers
a ghost **Retry** beside it that re-reads only. A default shown in place of an unread value
is a soft degrade the UI contradicts (AGENTS.md). Save of the rest of the form stays
available and does not write the unread field.

Introduced by: `nocx-oalo1.11.2`, frame 3.

## One Save that writes two stores reports a split outcome, and stays open

When Save writes the record and then a second store, and the second fails: the dialog does
not close, the section holding the failed field is shown, the field keeps the chosen value
with the reason as its error and takes focus, and a warning toast names both halves —
what was saved and what was not — ending "Save Connection to retry." The same Save retries.
Precedent: the password-binding split in `connections.tsx`.

Introduced by: `nocx-oalo1.11.2`, frame 4.

## A change that does not reach what is already open says so when it is made

If a setting applies to new sessions but not to ones already open, a `cm-hint` line under the
field says so **only while the choice differs from what is stored**. It is not a permanent
line: a hint that is always there is a hint nobody reads. It does not count open tabs — the
sentence is true whether there are none or ten.

Introduced by: `nocx-oalo1.11.2`, frame 1.

## One concept, one name, everywhere it is asked

A setting asked in a dialog and edited in a form carries the same label in both. The
connect-time ask says "Integration method", so the editor says it too.

Introduced by: `nocx-oalo1.11.2` (replaces "Delivery mode").

## Auto describes a policy, not an unanswered permission

When a method and permission are separate facts, an Auto option describes who chooses
and when; it never promises a new permission question or implies that permission was
cleared. The host's Auto option says "Auto — nocx chooses at connect". Its explanation
is visible whenever Auto is selected on a known, editable host, including an unchanged
stored Auto. It says that nocx asks only without a recorded machine answer, that a prior
helper grant is reused without asking again, and that clearing the method leaves the
machine's permission unchanged. This is policy copy, never a consent-status indicator:
the editor must not infer permission from the selected method.

Keep the owner description and the existing changed-value hint separate. Disabled fields
say why they cannot be edited; they do not claim to know the machine's permission and do
not repeat the Auto explanation. At narrow widths the explanation wraps in full, without
a tooltip, truncation, or a second consent control.

Clarified by: ALX-204, following the architect's ALX-18 decision of 2026-11-17T13:49:11Z
(B2 of ALX-195), for `nocx-oalo1.11.2`. Copy and mock delta:
[Auto copy](ssh-foundation/host-integration-method-auto-copy.md).

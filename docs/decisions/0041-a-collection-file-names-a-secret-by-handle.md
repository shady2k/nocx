# ADR-0041 — A collection file names a secret by handle

- **Status:** Accepted
- **Date:** 2026-08-26
- **Related:** AD-8 (one owner per behaviour), ADR-0011 (secrets as opaque references),
  ADR-0016 (a secret owns its name), `.internal/specs/2026-08-21-api-testing-design.md`
  §8, `.internal/specs/2026-08-25-a-secret-in-any-field-design.md` §§1, 3 and 11
- **Reverses:** the prohibition in the API-testing design's §8 that a collection file
  cannot name a secret at all.

## Context

The API-testing design's §8 made a deliberate format decision: a collection file could not
name a secret at all. A file could contain `{{token}}`, but the binding from that variable
to a stored value belonged to the app and was keyed by `(collection, environment, variable)`.
There was no file syntax for naming a vault record.

That decision had a concrete cost. A secret could not exist without an environment, because
there was nowhere to spell its binding key when no environment was selected. The same key
also meant a folder or a request could never own a secret directly. In the workbench,
"No environment" was therefore a dead end for a person who wanted to put a vault value in a
header, URL, parameter, body, or request variable.

The secret-in-any-field design makes a secret ordinary text with a vault reference inside
it. Its end-to-end proof starts from a header with **No environment** selected, creates a
secret through the picker, sends the request, and checks both the received value and the
saved file bytes.

## Decision

A collection file may name a secret by an opaque `secrow:` handle minted by this machine's
vault. A reference is stored in the field's text, for example
`{{secret:secrow:ab12cd34...}}`.

The display name belongs to the vault record. Surfaces show that name, but resolution never
uses the name. The handle is the address; the value remains in the vault and never enters
the collection file or the renderer.

## Rationale

### "There is no single resolution funnel to put the check in"

This objection targeted the earlier draft's mechanism: a secret would carry a scope, a
resolver would refuse cross-scope reads, and five call sites that fetch secrets by id would
need to make the same check. A check in one call site would be a check the others did not
make.

This decision proposes no such check. A handle minted on another machine is not present in
this vault, so an ordinary lookup fails on every path. No caller has to remember a separate
scope guard, and no second resolver rule is introduced beside the vault's existing lookup.

### "Removing the spelling removes the attack"

This objection applies, and it is conceded. §8 bought its guarantee from the format: there
was no syntax in which a file could name a secret. This decision reintroduces that syntax
and buys the guarantee from unguessability plus locality instead. That is a weaker **kind**
of guarantee, not the same guarantee described with new words.

The handle is opaque and machine-local, so a collection imported, cloned, or hand-edited on
another machine cannot resolve to a record in this vault. That is the boundary this decision
chooses; it is not a claim that a person cannot deliberately place one of their own secrets
in a request and send it somewhere.

## Consequences

- Renaming a secret no longer breaks a request. The handle is the address, and a rename keeps
  that address while changing only the display name.
- A collection cloned onto another machine carries handles that resolve to nothing there.
  The person must pick their own local secret. Moving secrets between machines is deliberately
  out of scope.
- A sealed vault cannot show a secret name. The chip says that the vault is locked rather
  than printing a handle to the person.
- The guarantee is now unguessability plus locality, not the absence of a spelling in the
  file. If handles ever become portable between machines, this decision must be reopened
  before portability ships: portability is exactly what turns "absent" into "resolvable".
- A collection file now contains a reference syntax, so reviewers and tooling must treat
  `secrow:` as an opaque address and must continue to reject secret values and display names
  from persisted request documents.

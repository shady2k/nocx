# A secret in any field, at any scope — design

- **Date:** 2026-08-25
- **Owner decisions in this document** are marked **[owner]**. They were taken in the
  brainstorming session recorded as `nocx-gnpvw`.
- **Reverses:** §8 of `.internal/specs/2026-08-21-api-testing-design.md` ("A collection
  file cannot name a secret at all"). §8 explicitly asked that the idea not be
  re-proposed, so the reversal carries an ADR of its own — see §3.
- **Supersedes:** the acceptance criteria of `nocx-5dfa5`, which assumed the binding
  document stays.

## 1. What a person can do that they cannot today

**Put a value the vault holds into any field of a request, without leaving the request,
and without an environment being involved at all.**

Today there is exactly one door and it is on the Auth tab (`AuthSecret`,
`frontend/src/api/request-form.tsx:385`). A person pasting a token into a **header** has
no door: they must leave for the environments page, add a row, tick Secret, paste, come
back. And if the environment picker says "No environment" there is no door anywhere,
because a secret is addressed by `(collection, environment, variable)` and a missing
environment leaves the address unspellable — `internal/apibind/store_impl.go:434` refuses
the write by name.

Afterwards: `@` in any text field offers what the vault holds, picking inserts a
reference, "Add a secret…" makes one on the spot, and the same value can be reached from
a request's own variables, a folder's, an environment's, or straight from the field.

**The end-to-end check that watches it happen** (rule 2, written now rather than at the
end): a person opens a request with no environment chosen, types `@` in a **header
value**, creates a secret from inside the panel, sends, and the header arrives at a test
server carrying the value — while the request file on disk contains neither the value nor
any name of it, only a `secrow:` handle.

## 2. What is settled

**[owner]** decisions, each recorded here because the rest of the document depends on it:

1. **The reference is the vault's opaque row handle, not the secret's display name.** A
   file stores `{{secret:secrow:ab12cd34…}}`; every surface _shows_ the display name.
2. **Rename must not break a request.** This is why (1) is a handle:
   `vault.renameSecret` exists and every row of the Secrets page offers it
   (`frontend/src/secrets.tsx:194`), and the inventory schema names the handle as "the
   address rename takes".
3. **Offer, never oblige.** Nothing rewrites, sanitises or refuses a credential somebody
   typed as text. This continues the owner's decision of 2026-08-23 (`nocx-tg9l8`).
4. **A Postman document never yields a secret reference.** Anything in an imported
   document that looks like one is not carried across; it is reported.
5. **The kind vocabulary gains `api-token`.** Certificates are a separate epic — see §13.
6. **Moving secrets between machines is out of scope.** A collection cloned on another
   machine carries handles that resolve to nothing there, and that is the accepted
   behaviour for now.

## 3. What this crosses, and what those documents already decided

**AD-8 (one owner per behaviour)** and the working rule beside it. This design exists
because the API workbench built a **second** answer to "what is a secret reference" beside
the one the product already had. It removes the second rather than extending it.

**§8 of the API-testing design** decided that a collection file cannot name a secret _at
all_, and recorded two objections to the "files carry opaque vault references" draft:

- _"There is no single resolution funnel to put the check in."_ — This objection targets a
  mechanism this design does not propose. That draft wanted a **scope on the secret plus a
  resolver refusing cross-scope reads**; five call sites fetch secrets by id and a check in
  one is a check the others do not make. Here there is **no check anywhere**: a handle
  minted on another machine does not exist in this vault, so it resolves to nothing by
  ordinary lookup, on every path, without anybody having remembered to guard it.
- _"Removing the spelling removes the attack."_ — This objection **does** apply, and the
  reversal must own it. §8 bought its guarantee from the format: there was no syntax in
  which a file could name a secret. This design reintroduces the syntax and buys the
  guarantee from unguessability plus locality instead — a `secrow:` handle is minted from
  a `sec:v1:<provider>:<32 hex>` id and never leaves the machine that minted it. That is a
  weaker _kind_ of guarantee, and §11 states exactly what it does and does not cover.

  What §8 paid for its stronger guarantee is the whole of the problem this design solves:
  the binding key `(collection, environment, variable)`, and with it "No environment has
  nowhere to put one" and the impossibility of a folder-scoped or request-scoped secret.

**ADR-0011** — a persisted domain record carries an opaque secret _reference_, never
secret material. Untouched: `secrow:` is a reference, the renderer may hold it, it routes
nothing, and the value never reaches the renderer.

**ADR-0016** — the secret owns its name. Untouched, and now load-bearing in the other
direction: the name is what every surface _displays_, and nothing resolves through it.

**ADR-0021 / §11.2 of the API-testing design** — the raw diagnostic shows a chip where a
secret went, never the bytes. Untouched; `PlacedSecret` keeps carrying the value to the
sender for eliding and nowhere else.

**ADR-0032** — the vault raises its own unlock. So the send asks for a value and gets a
value or a refusal; no surface in this design has a "sealed" branch to write.

**ADR-0017** — a connection references a secret. The same shape, one surface over.

## 4. The model

### 4.1 One grammar, one owner, a typed payload

`{{secret:PAYLOAD}}` is the product's vault reference. Its two scanners already exist and
neither changes:

- renderer — `frontend/src/secret-reference.ts`, `REFERENCE_RE = /\{\{secret:([^}]*)\}\}/g`
- backend — `internal/capability/secret.go:314`, `resolveLineRefRE = \{\{secret:(.+?)\}\}`

The payload is typed by prefix: a payload beginning `secrow:` is a **row handle** and
resolves through `ResolveRow`; anything else is a **display name** and resolves as it does
today, which is what the terminal prompt writes (`nocx-fk32`).

**A collection file may spell only the handle form.** This is the load-bearing half of
§11 and it is a rule of the resolver, not of the editor: the workbench's resolution accepts
`secrow:` payloads and refuses a display-name payload by name, wherever the text came from.
Without it the guarantee collapses — a hostile file would write
`{{secret:github token}}` and be resolved against your inventory, which is exactly the
name-guessing attack this design claims to have removed. The prompt keeps the name form
because a person types it at their own keyboard about their own vault; a file is not a
person.

**The ambiguity between the two payload kinds is closed at the source, not at the
reader:** the vault refuses a display name beginning with `secrow:`, on create and on
rename. One validation, and neither form can ever be read as the other.

`{{name}}` — the collection's own variable grammar — is unchanged and **can never reach the
vault**, because `:` is not in its alphabet (`frontend/src/api/variable-reference.ts`,
`internal/apicoll/substitute.go`). A name collision between a collection variable and a
vault secret stops being possible; it is not merely unlikely.

### 4.2 Scope falls out; it is not built

A secret reference is **text**. Therefore it is legal anywhere text is legal, and the scope
chain that already exists carries it with no new machinery:

```
request variable   token = {{secret:secrow:ab12…}}     ← request-scoped secret
folder variable    token = {{secret:secrow:ab12…}}     ← folder-scoped secret
environment value  token = {{secret:secrow:cd34…}}     ← environment-scoped secret
a field            Authorization: Bearer {{secret:secrow:ab12…}}   ← no variable at all
```

`request → folder → environment` is `internal/capability/api_scope.go`, whose one owner of
the send order is `requestLookup`. Prod and staging are two vault records that two
environment rows point at. "No environment" stops meaning anything: a request variable or a
literal field reference needs no environment to exist.

**Resolution is two passes, in this order:** the variable chain resolves `{{name}}` as it
does today, then the resulting text is resolved for `{{secret:…}}`. One extra pass, not
recursion — a secret's _value_ is never re-scanned, so a value that happens to contain
`{{` is sent as the bytes the vault holds.

### 4.3 What is deleted

Greenfield; there is no migration and no compatibility shim.

- `internal/apibind` — the whole package: `Key`, `Store`, `Bind`, `Unbind`,
  `UnbindCollection`, the binding document and its file.
- `apicoll.Environment.SecretVars` and the `Secret` column of the environments table.
- `capability.APIBindingOperation` / `APIBindingService` / `SecretBinder`, and
  `api.binding.bindSecret` on the wire.
- `SecretTarget` and both of its absence messages (`request-form.tsx:127`) — there is no
  longer an absence to report.
- `apicoll.ErrSecretShadowed` and `SecretShadowedName`. Shadowing existed because secrets
  were a fourth layer under the chain; a secret now _is_ a value in the chain, so a request
  variable overriding an environment one is ordinary override and needs no refusal.
- `apiimport`'s `secretOffer` and `BindWriter` — see §10.

`internal/capability/api.go`'s `SecretValues` seam **stays**, narrowed differently: it
resolves a reference rather than answering an environment's variables. It remains a
consumer contract with no parameter through which an identifier could travel outward.

### 4.4 The wire

`ScopeVariable.Scope` (`api_scope.go:21`) is documented as a closed vocabulary —
`request | folder | environment | vault`. `vault` leaves it: a secret is no longer a scope
of its own. The row gains a boolean saying the value is a reference the renderer cannot
read, so the Variables tab keeps saying which scope answered _and_ whether the answer is a
secret.

Every result shape touched gets its `contracts/` schema in the same commit, with
`additionalProperties: false` and an explicit `required`, plus the pair of tests rule 5
names — the DTO conformance test and the over-the-wire one.

## 5. Three doors

Every one of them already exists in this product. None is built here; all three are
mounted.

| Door                 | Where                                                                                     | What it does                                                                                                                                 |
| -------------------- | ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `@`                  | any text field: URL, header value, param value, a variable's value at any scope, the body | `SecretPicker` (`frontend/src/ui/secret-picker.ts`) — the same gesture as the terminal prompt                                                |
| **Insert a secret…** | the request panel's `⋮` menu, and the field's context menu                                | opens the same panel, for a person who does not know about `@`                                                                               |
| **SecretSource**     | the Auth tab                                                                              | `frontend/src/secret-source.tsx` — the segmented "Type a new one \| Use existing secret" plus the picker, exactly as `Edit Endpoint` asks it |

### 5.1 Why Auth gets a different door

Because it asks a different question. A header value is **mixed text** — `Bearer ` and then
a secret — so its door must insert a reference at a caret. The Auth tab's token field is
**wholly** a credential, which is the question `SecretSource` was written to own; its
comment says so and forbids a third vocabulary:

> One owner of the two-way choice. The connections editor's password field, the endpoint's
> key field and every custom-header value row all ask the same question … a new surface
> that needs the choice imports it rather than building a third one.

In `secret` mode the Auth field's stored text **is** `{{secret:secrow:…}}` — the same text
any other field would hold. The two doors write one thing.

### 5.2 What the picker already guarantees

From `secret-picker.ts`, unchanged and depended upon:

- `@` inserts an ordinary `@` and enters **no mode**; keystrokes keep going to the field.
  A space closes it, Esc closes it and leaves the `@` as text. "It offers, it never traps."
- Nothing matches → the panel closes silently.
- Only Enter or Tab on a selected row inserts.
- The vault's lifecycle states are **offers, not errors**: sealed offers to unseal,
  uninitialised offers to set up, both from inside the panel.
- `requestCreate(name)` — "Add a secret…" hands the host what was already typed, so the
  create dialog opens prefilled.

**The adapter is the new code.** The picker is mounted today over a CodeMirror document
(`prompt-vault.ts`). A plain `TextField` needs a controller that watches the value, finds
the trigger word, drives `setFilter`, and inserts over the trigger span. That controller is
written once, in `ui/`, and every field in the workbench uses it — including the body
editor, which is CM6 and can reuse the prompt's own path. This closes `nocx-hex1q` in the
same work rather than after it.

## 6. What a person sees in the field

`marks` / `onMarkClick` (`ui/text-field.tsx`) extend from the URL — their only caller today
(`request-form.tsx:328`) — to every text field named in §5. The tones already exist:
`reference`, `secret`, `unknown`.

- `{{secret:secrow:…}}` renders as a chip bearing the **display name**, read from
  `vault.inventory` (`InventoryEntry.id` is the handle, `.name` the name). Never a value —
  the renderer does not have one.
- **A sealed vault has an empty inventory**, so there is no name to show. The chip then says
  the vault is locked and offers to unlock; it does not print `secrow:ab12cd34` at a person,
  and it does not claim the reference is broken.
- **A handle no inventory answers** — the cloned-collection case of §2.6 — renders in the
  `unknown` tone and says the secret is not on this machine. It is distinguished from the
  sealed case, because the answers are different: one is "unlock", the other is "pick one".
- Clicking a chip opens the existing variable menu, which gains "Replace with another
  secret" and "Open in Secrets".

## 7. Sending

`Snapshot` (`internal/capability/api.go`) resolves in the order of §4.2 and hands
`SendInputs` out from under the gate, as it does today. Secrets that were substituted keep
arriving as `PlacedSecret`, carrying the value, so `apisend.MarkRequest` can elide those
bytes from the raw diagnostic — §11.2 of the API-testing design is untouched.

- A reference nothing answers **blocks the send and names it**, the way an unbound variable
  does (`nocx-pgp9c.6`). It is never sent as literal text and never as an empty string;
  `ResolveLine`'s contract already says so, and this path holds to it.
- A vault sealed at send time raises its own unlock and the call waits (ADR-0032). The
  workbench has no branch for it.
- A vault that seals mid-flight is an **error**, not an unresolved reference — answering
  would be a lie, and a retry after unsealing answers differently.

## 8. Kinds

`InventoryEntry.kind` (`frontend/src/generated/vault.inventory.ts:33`) is a closed
vocabulary and the schema says a new kind is an addition rather than a degradation into
`unknown`. It gains **`api-token`**.

The picker filters by kind where the surface knows what it wants — the Auth tab's Bearer
field offers `api-token` first — and never _only_ by kind: a person who keeps a token under
`password` must still be able to choose it.

## 9. Creating one

Two entrances, one call, and the value never passes through a collection file.

- From the picker: "Add a secret…" opens the vault's own create dialog with the typed text
  as the proposed name (`requestCreate`). The dialog is the vault's, not the workbench's.
- From the Auth tab: `SecretSource` in `new` mode, whose input is the existing
  `SecretValueField`.

The reference lands in the field **only after the write is accepted**. A refusal that had
already rewritten the field would leave a file pointing at a record that was never made —
the one state this work exists to get a person out of, and the rule `AuthSecret` already
holds.

`internal/secrets.Detect` and `SuggestName` are **not** wired into this surface. They are
the terminal's "we noticed a credential" path; using them here would put a nudge on a field
a person is typing into, and §2.3 says the product does not push. A person asks by typing
`@` or by opening the menu.

## 10. Import

A Postman document never yields a secret reference **[owner]**.

- The importer does not mint vault records and does not write `{{secret:…}}` — `secretOffer`
  and `BindWriter` go away with `apibind`.
- A Postman variable of `type: secret` becomes an ordinary collection variable with **no
  value**. The person gives it one, and may make that value a secret reference through the
  same door as everything else.
- Any `{{secret:…}}` the source document happens to contain is **dropped and reported** in
  the existing `apiimport.Unsupported` list. Dropping in silence is a known defect
  (`nocx-6hg2w.16`) and is not repeated here.

This is also why no import-time scan or consent step is needed: a handle from elsewhere
resolves to nothing anyway, so the report is for the person's information, not a guard.

## 11. Security: what this covers, and what it does not

**Covered.**

- A collection arriving by import, `git pull`, or a hand edit cannot reach a secret of
  yours. Its handles were minted by another vault and are absent here — no check, no funnel,
  nothing to forget.
- A name cannot be guessed into a value. `{{token}}` cannot reach the vault at all
  (grammar), and the display-name form of a vault reference is refused by the workbench's
  resolver (§4.1), so a file has no spelling that names a record by anything a writer could
  know. What is left is a 128-bit handle minted on this machine.
- No value ever enters a file under the collection root, and no value ever reaches the
  renderer. Both are asserted on bytes, not reviewed by eye.

**Not covered, and stated rather than implied.**

- A person can deliberately put any secret they own into any request and send it anywhere.
  That is their own act through their own picker, and this product does not police what a
  person does with their own credential.
- The guarantee is now unguessability plus locality, not the absence of a syntax. If
  handles ever become portable between machines — which §2.6 puts out of scope — this
  paragraph has to be re-opened before that happens, because portability is exactly what
  turns "absent" into "resolvable".

## 12. Testing

Rules 1–5 of `AGENTS.md`, made concrete.

1. **The happy path, end to end** (§1). One automated check drives the seam from a **header
   row** with **no environment chosen**: the door exists from the state a person starts in,
   `@` offers the vault's records, creating one reaches the vault's create call, the field
   then holds a reference and never the value, the request sends, and a test server receives
   the value. The saved file is read back **as bytes** and contains neither the value nor a
   display name.
2. **Every external call fails in a test.** The vault refuses; the vault is sealed and the
   unlock is cancelled; the create is rejected; `ResolveRow` answers false. Each has an
   assertion about what the _field_ and the _file_ hold afterwards, not only about the error.
3. **Invariants as intervals.** "A field holds a reference from the moment the write is
   accepted until the person edits it or the record is deleted." The closing events are
   named, and the delete case has a test: deleting a record leaves references that render
   `unknown` and block the send by name.
4. **The tests are not written by the implementer alone.** The acceptance criteria of each
   bead are written as assertions, in the bead.
5. **The wire.** Every changed result shape gets its `contracts/` schema plus the DTO and
   over-the-wire conformance pair.

Plus the ratchet's blind spot: `deadcode -tags gtk3 -whylive` is asked for the **new
resolver seam by name**, contrasted against a symbol known to be unwired, because
`-filter` cannot report a dead method behind a live interface and this codebase is
interfaces (`nocx-re6gk`).

## 13. Deliberately out

- **Certificates.** A self-signed certificate, its private key, and a trust root are
  **TLS material of the connection**, not text substituted into a field: they are chosen per
  host and attached to the transport. They need their own kinds (`tls-client-cert`,
  `tls-client-key`, `tls-trust-root`), their own seam in the sender, and their own surface.
  Adding only the kinds here would ship a record a person can create and cannot use.
  Separate epic, filed and linked.
  Note the naming trap for that epic: `private-key` in the current vocabulary is an **SSH**
  key. A TLS key is different material with the same word, and one kind for both would have
  the SSH picker offering TLS keys.
- **Moving secrets between machines** (§2.6).
- **A shared team vault.** Out for the same reason and further away.
- **Detecting a pasted credential and offering to store it.** §9.

## 14. Open questions

1. **Does the Rename dialog warn?** A rename is now safe for references, so the warning
   `nocx-5dfa5`'s discussion imagined is not needed. Left as it is; noted so the absence is
   deliberate.
2. **Deleting a record that references answer.** `Usage(ctx, row)` answers the _profiles_
   that use a secret (ADR-0017). It does not know about collections, and teaching it would
   mean scanning collection files. Proposal: it does not learn — deletion stays allowed and
   the references report themselves `unknown` at the field, which §12.3 already tests. Flag
   for the stress-test.

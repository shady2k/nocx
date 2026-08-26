# A secret in any field, at any scope — implementation plan

> **For agentic workers:** this plan is executed by an orchestrator dispatching one `omp`
> worker per task. A worker implements ONE task on its own branch, runs only the unit tests
> for the files it changed, and stops. It does NOT run `make ci-full`, the containerized
> jobs or the e2e suite — the orchestrator runs those once, on the merged tree, at the end
> of the epic (AGENTS.md, "Git authority").

**Goal:** A person can put a value the vault holds into any field of a request — at request,
folder or environment scope, or straight into the field — without leaving the request and
without an environment being involved.

**Architecture:** Delete the workbench's second answer (`internal/apibind` + `secretVars`)
and mount the product's existing one: the `{{secret:…}}` grammar, `ResolveLine`/`ResolveRow`,
`ui/secret-picker`, `secret-source.tsx`. A collection file stores an opaque `secrow:` handle;
every surface displays the vault's name. Scope is not built — a reference is text, and the
`request → folder → environment` chain already carries text.

**Tech Stack:** Go (`internal/capability`, `internal/apicoll`, `internal/apiimport`,
`internal/vault`, `internal/transport`), SolidJS + TypeScript (`frontend/src/api`,
`frontend/src/ui`), JSON Schema (`contracts/`), Playwright (`e2e/`).

**Spec:** `.internal/specs/2026-08-25-a-secret-in-any-field-design.md` — binding. Every task
below cites the section it implements. Read that section before writing code.

## Global Constraints

- **AD-8 / one owner per behaviour.** No task may add a second scanner, a second resolver
  or a second chooser. Where a task needs behaviour that exists, it imports it.
- **Reference grammar is unchanged.** `frontend/src/secret-reference.ts` `REFERENCE_RE` and
  `internal/capability/secret.go:314` `resolveLineRefRE` are NOT edited. Both already match
  a `secrow:` payload.
- **A collection file may spell only the handle form** (spec §4.1). The workbench resolver
  refuses a display-name payload, by name, wherever the text came from.
- **No value in a file, ever.** Assertions are on the bytes on disk, not on a struct.
- **No value to the renderer, ever** (ADR-0011). `PlacedSecret` carries the value to the
  sender for eliding and nowhere else.
- **Greenfield.** No migration, no compatibility shim, no reading of an old binding
  document. Delete and move on.
- **Kit only** (`frontend/src/ui/README.md`). A surface may place a kit component and may
  never repaint it.
- **Contracts.** Every JSON-RPC result shape a task changes gets its `contracts/` schema in
  the same commit, with `additionalProperties: false` and an explicit `required`, plus the
  `_DTOConformsToContract` and `_OverTheWireConformsToContract` pair.
- **Commit messages** follow AGENTS.md: `<type>(<scope>): <subject> (<bead-id>)`, prose body.
- **Acceptance criteria are assertions, in the bead** (AGENTS.md testing rule 4). Each task
  bead carries its criteria verbatim from this plan.

## Note on this plan's form

Steps are given as exact seams, exact signatures, exact test names and exact assertions
rather than as finished code bodies. That is deliberate: the acceptance criteria below are
written as assertions, which AGENTS.md rule 4 names as the cheapest defence against a test
that merely agrees with its implementation. A worker writes the failing test first from the
criteria, then the implementation.

## File structure

| File                                        | Responsibility after this work                                                |
| ------------------------------------------- | ----------------------------------------------------------------------------- |
| `internal/capability/api.go`                | `SecretRefs` — the narrow resolver seam; `Snapshot` resolves the second pass  |
| `internal/capability/api_scope.go`          | the scope chain; `ScopeVariable` loses the `vault` scope, gains `Secret bool` |
| `internal/capability/secret.go`             | unchanged owner of `ResolveLine`/`ResolveRow`; gains nothing                  |
| `internal/apicoll/collection.go`            | `Environment` loses `SecretVars`                                              |
| `internal/apicoll/substitute.go`            | variable pass only; knows nothing about secrets                               |
| `internal/apiimport/postman.go`, `write.go` | never mints a vault record; reports dropped references                        |
| `internal/vault/…`                          | `api-token` kind; refuses a display name beginning `secrow:`                  |
| `internal/app/app.go`                       | wires `SecretRefs` from the secret capability; no `apibind`                   |
| `internal/transport/ws_api_handlers.go`     | no `api.binding.bindSecret`; no `apibind` types                               |
| `frontend/src/ui/secret-picker-field.ts`    | **new** — mounts `SecretPicker` over a plain `TextField`                      |
| `frontend/src/api/request-form.tsx`         | fields carry marks and the picker; Auth uses `SecretSource`                   |
| `frontend/src/api/api-pane.tsx`             | host wiring; `secretTarget` and its absences deleted                          |
| `frontend/src/api/environment-view.tsx`     | no Secret column; a value may be a reference                                  |
| `frontend/src/api/folder-view.tsx`          | same treatment as the environment table                                       |
| `internal/apibind/**`                       | **deleted**                                                                   |

---

### Task 1 — The resolver seam: a request resolves `{{secret:secrow:…}}`

**Implements:** spec §4.1, §4.2, §4.4, §7. **Wave A.**

**Files:**

- Modify: `internal/capability/api.go` (`SecretValues` → `SecretRefs`; `Snapshot`)
- Modify: `internal/capability/api_scope.go` (`ScopeVariable`)
- Modify: `internal/apicoll/collection.go`, `internal/apicoll/substitute.go`
- Modify: `internal/app/app.go`, `internal/transport/ws_api_handlers.go`, `internal/transport/ws.go`
- Modify: `contracts/api.request.scope.schema.json`, `contracts/files/environment.schema.json`
- Test: `internal/capability/api_test.go`, `internal/capability/api_scope_test.go`,
  `internal/apicoll/substitute_test.go`, `internal/transport/ws_api_secret_test.go`

**Interfaces — produces:**

```go
// internal/capability/api.go — replaces SecretValues.
// Narrowed to the one call the snapshot makes; no parameter through which an
// identifier travels outward, and no method that takes one from outside.
type SecretRefs interface {
    // ResolveText substitutes every {{secret:secrow:…}} in text.
    // A display-name payload is REFUSED by name (spec §4.1) — a file is not a
    // person. Placed reports what was substituted, for eliding in the raw view.
    ResolveText(ctx context.Context, text string) (out string, placed []PlacedSecret, err error)
}
```

`ScopeVariable` (api_scope.go): `Scope` becomes `request | folder | environment`; add
`Secret bool` — "the value is a reference the renderer cannot read".

**Acceptance criteria:**

- Two-pass order: `{{name}}` resolves through the chain first, then the resulting text is
  resolved for `{{secret:…}}`. A test binds a request variable to `{{secret:secrow:X}}`,
  puts `{{token}}` in a header, and asserts the header goes out with X's value.
- A secret's value is **not** re-scanned: a vault value containing `{{` is sent verbatim.
- `{{secret:some display name}}` in a request file **blocks the send and names the
  reference**; it is never sent literally and never as an empty string.
- A handle no vault answers blocks the send and names it (`ResolveRow` false).
- A reference works from all four places with **no environment chosen**: request variable,
  folder variable, environment value, and a bare field.
- Every external call fails in a test: `ResolveRow` false, `GetSecret` errors, the vault
  seals mid-flight (an error, not an unresolved ref).
- `PlacedSecret` still reaches the sender so `apisend.MarkRequest` elides the bytes; a test
  asserts the raw diagnostic shows a chip and not the value.
- `deadcode -tags gtk3 -whylive '…/internal/capability.apiCollectionService.…'` shows the
  new seam wired, contrasted against a symbol known unwired (AGENTS.md; `-filter` cannot
  report a dead method behind a live interface).
- `api.request.scope` result: schema updated, both conformance tests present.

**Steps:** write the failing tests above → run, confirm they fail for the stated reason →
implement → run the package's tests only (`go test ./internal/capability/... ./internal/apicoll/...`)
→ commit.

---

### Task 2 — The kit adapter: `@` over a plain text field

**Implements:** spec §5.2. **Wave A.** No app files — kit only.

**Files:**

- Create: `frontend/src/ui/secret-picker-field.ts`
- Create: `frontend/src/ui/secret-picker-field.test.ts`
- Modify: `frontend/src/ui/README.md` (one row in the inventory table)

**Interfaces — produces:**

```ts
// Drives the existing ui/secret-picker over a plain <input>/TextField value.
// The picker itself is NOT modified.
export interface SecretPickerFieldController {
  onInput(value: string, caret: number): void // finds the trigger word, drives setFilter
  onKeyDown(e: KeyboardEvent): boolean // true when the panel consumed the key
  close(): void
}
export function createSecretPickerField(opts: {
  source: SecretPickerSource
  value: () => string
  onChange: (next: string, caret: number) => void
}): SecretPickerFieldController
```

**Acceptance criteria — the picker's own contract, held over an input:**

- `@` at a word start opens the panel AND inserts a literal `@`; **no mode is entered** —
  the next keystroke still reaches the field.
- A space closes it. Esc closes it and leaves the `@` as text. Nothing matches → it closes
  silently.
- Only Enter or Tab on a selected row inserts, and what is inserted is
  `{{secret:secrow:<id>}}` **over the trigger word**, with the caret after it.
- `@` **not** at a word start (`foo@bar`) does not open it.
- Sealed vault → the offer row to unseal; uninitialised → the offer row to set up; neither
  is drawn as an error.
- "Add a secret…" calls `requestCreate` with the text typed after `@`.
- The kit does not import from the app (`no-restricted-imports` stays clean).

---

### Task 3 — The vault: an `api-token` kind, and a name that cannot look like a handle

**Implements:** spec §4.1 (the ambiguity closed at the source), §8. **Wave A.**

**Files:**

- Modify: the vault's kind vocabulary and its create/rename validation (`internal/vault/…`,
  `internal/capability/secret.go` where the wire params are validated)
- Modify: `contracts/vault.inventory.schema.json`, regenerate
  `frontend/src/generated/vault.inventory.ts` (`npm run contracts:*` — never hand-edited)
- Test: the vault's own package tests, `internal/transport/ws_vault_*_test.go`

**Acceptance criteria:**

- `kind` gains `api-token`. It is an **addition** to the closed vocabulary, never a
  degradation into `unknown` — the schema comment's rule.
- `vault.createSecret` and `vault.renameSecret` **refuse** a display name whose first
  characters are `secrow:`, by a named error. Both paths have a test; a name merely
  containing `secrow:` later in the string is accepted.
- A secret of kind `api-token` appears in `vault.inventory` and on the Secrets page beside
  the others, with rename, replace and delete working unchanged.
- Contract regenerated and committed; `npm run contracts:check` passes.

---

### Task 4 — The importer never mints a secret

**Implements:** spec §10. **Wave A.**

**Files:**

- Modify: `internal/apiimport/postman.go`, `internal/apiimport/write.go`,
  `internal/apiimport/secretvar.go` (delete `secretOffer`, the `BindWriter` seam)
- Modify: `internal/transport/ws_api_handlers.go` (the import op loses its bindings arg),
  `internal/app/app.go`
- Test: `internal/apiimport/postman_test.go`, `internal/apiimport/telegram_shape_test.go`

**Acceptance criteria:**

- A Postman variable of `type: secret` becomes an ordinary collection variable with **no
  value**. Asserted on the written file's bytes.
- Any `{{secret:…}}` the source document contains is **dropped and reported** in
  `[]apiimport.Unsupported` — never written through, never dropped in silence
  (`nocx-6hg2w.16` is the defect not to repeat). A test feeds a document containing one and
  asserts both: the file does not contain it, and the report names it.
- `apiimport` imports nothing from `apibind` and has no path that reaches a vault write.
- The import operation's gates drop `vault` — it no longer writes credential material.
- A curl line's own `Authorization` header is still left alone on the request (`FromCurl`'s
  existing rule; a test that already asserts it must still pass).

---

### Task 5 — The workbench's fields: the picker, the chips, and Auth

**Implements:** spec §5, §5.1, §6. **Wave B — needs Tasks 1, 2, 3.**

**Files:**

- Modify: `frontend/src/api/request-form.tsx` (delete `AuthSecret` and `SecretTarget`;
  `SecretSource` on the Auth tab; `marks`/`onMarkClick` on every text field)
- Modify: `frontend/src/api/api-pane.tsx` (delete `secretTarget`; host the picker source;
  the variable menu gains "Replace with another secret" and "Open in Secrets")
- Modify: `frontend/src/api/api-store.ts`, `frontend/src/api/api-client.ts` (drop
  `bindSecret`; the inventory read for name display)
- Test: `frontend/src/api/api-workbench.test.tsx`, `frontend/src/api/request-form` tests

**Acceptance criteria — driven from the state a person starts in:**

- From a **header value row**, with **"No environment" selected**: `@` opens the panel, the
  panel lists what the vault holds, choosing one puts `{{secret:secrow:…}}` in the field,
  and the saved file contains neither a value nor a display name. This is the criterion the
  whole epic exists for; it is asserted on the bytes.
- The same from a param value, from the URL, and from a variable's value row at request,
  folder and environment scope.
- The `⋮` menu offers **Insert a secret…** and opens the same panel.
- The Auth tab renders `SecretSource` with segments "Type a new one" / "Use existing
  secret". In `secret` mode the stored text **is** `{{secret:secrow:…}}` — the same text a
  header would hold. `AuthSecret` and both "nowhere to put one" messages are gone.
- A chip over a reference shows the **display name** from `vault.inventory`, never a value.
- **Sealed vault** → the chip says the vault is locked and offers to unlock; it does not
  print the handle and does not claim the reference is broken.
- **A handle nothing answers** → the `unknown` tone, saying the secret is not on this
  machine — visibly distinct from the sealed case, because the answers differ.
- The reference lands in the field **only after** a create is accepted; a rejected create
  leaves the field exactly as it was, and a test asserts that.
- Nothing on this surface rewrites, sanitises or refuses a credential typed as text
  (spec §2.3). A test types a raw token into a header and asserts it is sent unchanged and
  nothing was offered unbidden.
- `internal/secrets.Detect` is **not** wired here (spec §9).
- Kit only: no `background`, `border`, `color`, `font-*`, `padding` or `box-shadow` on a kit
  component from this surface.

---

### Task 6 — The environment and folder tables lose the Secret column

**Implements:** spec §4.3, §4.2. **Wave B — needs Task 5** (shared store).

**Files:**

- Modify: `frontend/src/api/environment-view.tsx` (columns `Name | Value`; the Secret
  checkbox and the per-row `SecretValueField` go)
- Modify: `frontend/src/api/folder-view.tsx` (a value may be a reference; same chip)
- Test: `frontend/src/api/environment-view.test.tsx`, folder tests

**Acceptance criteria:**

- The environments table has two columns. A row whose value is a reference renders the chip
  and no readable value.
- `@` works in a value cell at both levels, inserting the same text as anywhere else.
- The environment file written to disk carries `{{secret:secrow:…}}` as an ordinary value
  and has **no** `secretVars` key. Asserted on bytes.
- The folder table gains the same capability, so a folder-scoped secret exists — the thing
  that was impossible before.
- Nothing in either surface calls `bindSecret`; the method is gone.

---

### Task 7 — Delete `internal/apibind` and the last of the second answer

**Implements:** spec §4.3. **Wave C — needs Tasks 1, 4, 5, 6.**

**Files:**

- Delete: `internal/apibind/**`
- Modify: `internal/app/app.go`, `internal/transport/ws.go`,
  `internal/transport/ws_api_handlers.go` (`WithAPIBindings`, `WithAPIVariables`,
  `NewAPIBindingOperation`, `api.binding.bindSecret`)
- Delete: `frontend/src/generated/api.environment.bindSecret.ts` and its contract
- Modify: `internal/apicoll` — remove `ErrSecretShadowed` / `SecretShadowedName`
- Modify: `e2e/api-secret-in-path.spec.ts` to the new door

**Acceptance criteria:**

- `grep -rn "apibind" --include=*.go .` returns nothing outside git history.
- `grep -rn "secretVars\|SecretVars\|bindSecret" frontend/src internal contracts e2e` returns
  nothing.
- The binding document file is no longer created by any path.
- `ErrSecretShadowed` is gone and a request variable overriding an environment one is
  ordinary override, with a test asserting the override wins rather than refusing.
- `go build ./...` and `npm run -w frontend build` are clean; `deadcode` reports nothing new.

---

### Task 8 — One automated check watches a person do it, plus the ADR

**Implements:** spec §1, §12.1, §3. **Wave C — needs Task 7.**

**Files:**

- Create: `e2e/api-secret-in-any-field.spec.ts`
- Create: `docs/decisions/0041-a-collection-file-names-a-secret-by-handle.md`
- Modify: `docs/decisions/INDEX.md`

**Acceptance criteria — the epic's DONE WHEN:**

- One Playwright spec, against the real backend, drives: open a request, **choose no
  environment**, type `@` in a **header value**, create a secret from inside the panel, send
  to the suite's test server, and assert the server received the value.
- The same spec reads the request file **from disk** and asserts it contains neither the
  value nor the display name — only a `secrow:` handle.
- The ADR records the reversal of §8 of the API-testing design: Context (what §8 decided and
  what it cost), Decision (the file names a handle), Rationale (both of §8's recorded
  objections answered — one does not apply because no check exists to forget, the other is
  conceded and paid for in §11), Consequences (the guarantee is unguessability plus
  locality; portability between machines would re-open it).
- `INDEX.md` updated.

---

## Waves

```
Wave A  (parallel, no shared files)   T1 backend spine · T2 kit adapter · T3 vault kinds · T4 importer
Wave B  (after A)                     T5 workbench fields+Auth  →  T6 environment/folder tables
Wave C  (after B)                     T7 delete apibind  →  T8 e2e + ADR
```

Gates run **once, on the merged tree, at the end of Wave C**: `make ci-full`. Workers run
only their own package's unit tests.

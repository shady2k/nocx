---
title: Stanced vault resolution across requests and runs
status: accepted
created: 2026-08-23
binding-design: docs/decisions/0032-the-vault-raises-its-own-unlock.md
related: nocx-k41yv, nocx-o9jdu, nocx-pgp9c.7
review: BMAD Mary/John/Winston/Amelia/Paige roles applied — requirements, JTBD, architecture, testability, documentation
---

# Stanced vault resolution across requests and runs

## 1. Problem

ADR-0032 raises unlock only when a sealed error leaves a JSON-RPC request. That makes intent an accident of the error carrier: a control response can raise, while an already-created run cannot. The tree also exposes `credential.SecretStore.Get`, so consumers in capability, connection, and SSH packages can bypass the stanced resolver entirely.

The user contract is independent of transport:

- an explicit operation that needs secret material raises one coalesced unlock, waits, and continues;
- a read that reports state never raises and reports sealed as a fact;
- cancellation is a user outcome, not a failed operation;
- a new material read cannot omit its stance.

## 2. Binding decisions

### 2.1 The vault implements the stanced read seam

`credential.Resolver.Resolve(ctx, id, stance)` is the only consumer-facing API that returns secret material. `vault.Vault` implements it because it owns both the raw read and `EnsureUnsealed`.

`ForOperation` calls `EnsureUnsealed` before the raw read. `ToReport` performs no unlock and maps `vault.ErrVaultSealed` to `credential.ErrSealedQuiet`. The zero stance is rejected.

The existing renderer replay remains a compatibility net for sealed failures outside this seam; it is no longer the policy owner for credential reads.

### 2.2 Raw reads are not a consumer capability

`credential.SecretStore` retains create/delete/exists. Its `Get` method is removed. The vault's raw `Get` survives only behind `credential.MaterialStore`, the composition-only backend a `Resolver` is built from; the transport holds it in one field that only `credentialResolver` reads, and hands handlers the `SecretStore` half.

This makes a call with no stance fail to compile, which the `testdata/stanceless` fixture proves. The boundary that matters more is asserted separately, in `internal/capability`: that package holds no `credential.Resolver` at all (§2.3).

`credential.Unsealer` is a named interface, not a method discovered by type assertion. Whether a resolver can raise a prompt is a composition decision written at the composition root, because the failure mode of getting it wrong is silence — no unlock, no log, nothing in the product to say so.

### 2.3 Runs wait and continue

`agent.ask` creates its durable run, then its stream task resolves the endpoint credential and secret-valued headers before transitioning to streaming or calling the model. The task holds no capability admission while it waits for unlock.

**No resolution may happen inside one, either.** An operation read blocks until a person answers the dialog, and the way they answer is `vault.unseal`, which runs under the vault gate — capacity one, a one-second wait. A read that waits while holding that gate refuses its own answer with `-32004 Control plane busy`: the dialog cannot be satisfied at all and the only way out is to cancel. This is not a rule to remember at each call site — it is enforced by `internal/capability` holding no `credential.Resolver`, asserted by a test in that package. `secrets.saveKeyPassphrase` and `vault.resolveLine` therefore take their operation for the store reads, release it, resolve, and take it again to write (`nocx-o3606`).

Successful unlock continues the same run. Cancellation terminalizes it as cancelled-by-the-person. Resolution happens before the external request, so no bytes leave the machine before authorization succeeds.

This shortens plaintext lifetime relative to the current pre-run resolution: material exists from the start of the stream task through completion, not while the control request creates the durable run.

A vault seal after resolution does not revoke copied material. Streams never resolve additional secret material after the external call begins; this structural rule is the mid-run answer.

### 2.4 Reports remain quiet

`agent.status`, endpoint editor reads, inventory projections, badges, and other descriptive reads use `ToReport`. Opening an editor cannot raise an unlock.

### 2.5 Cancellation and coalescing

One pending vault prompt may serve many operation waiters. Cancelling the dialog cancels those operations with a recognizable user-cancel outcome. Cancelling one operation releases only that waiter; the prompt remains for other waiters. The last waiter leaving cancels the pending prompt.

No surface may route Stop to an exchange token that does not exist yet. A pending ask owns its run before it can wait, so `agent.cancel` always addresses a real run.

## 3. Security invariants

- ADR-0011 stands: persisted records and wire payloads carry opaque `SecretID`, never plaintext.
- Secret material is resolved only after host/endpoint authorization data is fixed.
- Secret-valued headers and endpoint credentials are cleared when the stream task ends.
- No logs, errors, run records, model context metadata, or JSON-RPC payloads contain secret material.
- AD-1 and AD-6 are untouched: no PTY byte moves to JSON and the backend remains byte-blind.

## 4. Acceptance proof

1. Real UI journey: sealed vault → ask → unlock → one model request → completed answer.
2. Cancel journey: sealed vault → ask → cancel → run cancelled by person; model receives nothing.
3. Quiet read: opening the endpoint editor while sealed emits no unlock request.
4. Compile/architecture fixtures: a material read with no stance fails to compile, and `internal/capability` holds no resolver.
5. Answerable prompt: while an operation waits for the unlock it raised, `vault.unseal` over the wire succeeds and the operation continues; a dismissed one answers as the cancellation, not as a sealed vault.
6. Product-source check: the sentence `the vault is locked — unlock it and ask again` is absent.
7. Existing endpoint probe, API send, SSH connect, resolver, vault coalescing, contracts, race, and security gates remain green.

## 5. Ordered implementation

1. Add failing resolver, run cancellation, quiet-editor, and architecture tests.
2. Move material reads behind the stanced resolver and wire the vault implementation at the composition root.
3. Move assistant material resolution into the stream task and terminalize cancellation distinctly.
4. Migrate connection/SSH and capability call sites; delete raw consumer reads and obsolete helpers.
5. Amend ADR-0032 and remove stale comments/product sentences.
6. Run focused behavioral checks, full CI, gosec, and npm audit before PR.

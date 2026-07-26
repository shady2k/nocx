# Worker brief — STORE-7 / bead `nocx-cym`: export, import and backup as distinct modes (core)

Repo `/home/dev/repos/nocx`, branch `feat/persistence-storage-capabilities`. **One other worker
is live**, in `internal/transport/ws.go`, `internal/app/app.go` and `internal/config`. Read
before writing code:

1. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — **§7 is
   your task**, and §2 constrains it absolutely.
2. `.internal/plans/2026-07-26-persistence-storage-capabilities.md` — §Task 7.
3. `AGENTS.md` — engineering rules.

## The premise, in one line

With a keychain in the middle there is **no honest "back up everything"** unless the app reads
secrets back out — and it must not. So this is four separate products, each stating plainly what
it does and does not carry. A single "Export" button would be a lie.

## Your deliverable

A new package `internal/export` implementing the four modes as distinct operations:

1. **Configuration export** — profiles, groups, credential metadata, settings. Secret
   _references_ are present but **unresolved**: a `SecretID` travels, the material never does.
2. **Portable encrypted export** — explicit, user-authenticated, encrypted under a **new
   passphrase** supplied for this export. Not part of normal persistence. Choose a standard,
   well-reviewed construction from the Go standard library or `golang.org/x/crypto` (already an
   indirect dependency via `x/crypto/ssh`) — do not invent a scheme, and do not add a new
   third-party dependency without escalating first.
3. **Same-machine backup** — configuration documents plus `content.db`, accompanied by a plain
   statement that secrets stay in the OS keychain and are therefore **not** in the backup.
4. **Import** — metadata first; then the user maps existing credentials or supplies missing
   secrets. Import must never silently invent or resolve a secret.

Hard constraints, all from ADR-0011:

- **Private content — conversations and command history — is never silently included in a
  portable export.** It is frequently more sensitive than the host metadata beside it. If a mode
  can carry it at all, it must be an explicit, separately-stated choice.
- **No mode resolves a secret.** No code path in this package may call anything that returns
  plaintext. The `credential.SecretStore` read method exists for the SSH connect path, not for
  you; if you find yourself reaching for it, stop and escalate.
- Every mode states what it carries and what it omits, as data the UI can display — not as a
  comment in the source.
- `content.db` does not exist yet (`internal/content` is a stub). Handle its absence honestly
  rather than pretending: the backup mode should report it as absent, not fail.

## Files you own

- `internal/export/**` (create — the whole package is yours)

## Files owned by the OTHER worker — do not touch, escalate instead

- `internal/transport/ws.go`, `internal/app/app.go`, `internal/config/**`

**Do not add export RPCs and do not wire this into the composition root** — both files belong to
the other worker right now. A later task does the wiring; your package must simply be ready for
it. Do not touch `frontend/**` either.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims.
- **YAGNI, but not at the cost of honesty**: implement the four modes and their stated
  manifests; do not build a plugin system, a format-version negotiator, or scheduling.
- **TDD**: failing test first. At minimum, test that a configuration export contains the
  `SecretID` and **not** any secret material, and that private content is absent from a portable
  export unless explicitly requested.
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Scope runs to `go test ./internal/export/...`
- **No formatting runs.** No `gofumpt -w`, no `prettier`. The coordinator sweeps at the end.
- **No `bd` commands.**

## Report in `worker_done`

- The four modes as an API listing, and for each: exactly what it carries and what it omits.
- The encryption construction you chose for the portable export, and why — plus confirmation
  that no new dependency entered `go.mod` (quote `git diff --stat go.mod go.sum`).
- How you proved no mode can resolve a secret.
- Test count and the exact command.
- **Anything you could not verify, stated explicitly**, and anything deliberately left alone.

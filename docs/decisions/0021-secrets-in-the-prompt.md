# ADR-0021 — Secrets in the prompt: mask what we keep, resolve what we can't

- **Status:** Accepted
- **Schema lifecycle:** superseded by [ADR-0054](0054-contentdb-schema-changes-migrate-or-refuse.md).
- **Date:** 2026-08-02 (amended 2026-08-03)
- **Related:** [ADR-0008](0008-*.md) (output is never retained), ADR-0018
  (the encrypted store the masked rows land in), ADR-0011 §2 (a secret never
  comes back out of the backend), [ADR-0016](0016-a-secret-owns-its-name.md)
  (the vault owns the name `{{secret:NAME}}` resolves by), AD-6 (the backend
  never sniffs the byte stream).
- **Design:** this round's brief (secrets, rounds 1 and 2) and
  `.internal/plans/2026-07-30-vault-v1.md` (the reference grammar is private
  to `internal/vault`).

## Context

History records commands verbatim and completion offers them back, so a key
typed once lands in the encrypted store and is offered as a completion
candidate tomorrow. We shipped the recording and never shipped the guard.
This ADR settles what the guard is — and, just as important, what it is not.

The threat model is the owner's, stated verbatim: _"у нас нет задачи защищать
файл от процессов на этой же машине, лежит же история bash — и это никого не
парит… Наша цель — защитить файлы от чтения напрямую."_ We are protecting the
durable files from direct reading, not the running process from the machine.

## Decision

**One durable text, always masked, shaped for a later save.** The user sees
the real value on screen; the durable history gets the masked one, and the
row also keeps a STRUCTURED redaction segment per finding — the kind, the
span of the replacement in the masked command, and the head/tail the mask
shows (`prefix`/`suffix`, exactly the text already visible in the mask). A
segment carries no secret material, and it is what makes the next round's
receipt possible: the renderer draws an unresolved chip at the segment and
refuses to execute the command, and a save rewrites exactly that span to
`{{secret:NAME}}`. Rows carry stable ids so the save can address one entry.
The live viewport is untouched: xterm renders what the program printed, and
rewriting that stream would violate AD-6. Masking happens at the wire, in
exactly one place (`internal/transport/ws_history_record.go`), the single
writer of durable rows. The row and both contracts carry the count, the
kinds and the segments of what was masked, so a block can say "3 secrets
masked: openai, jwt" — an honest redaction that says nothing is
indistinguishable from there having been nothing to redact.

This amends the round-1 text "no redaction map, no two artifacts": the
segments are not a second copy of the text (the masked text is still the
only durable text) but the shape of the masks already in it — kind, span,
mask head/tail — so a later remediation is not guesswork. The store rebuilds
on a schema change and logs how many rows it discarded; there is no
migration, because spans flattened before this change cannot be told from
text the user typed.

**A line may reference a vault secret by name; the backend resolves it at
submit.** `{{secret:NAME}}` where NAME is the vault inventory name
(ADR-0016) — never a `sec:v1:...` reference, whose grammar is private to
`internal/vault`. The resolved value goes to the caller for the PTY write
and nowhere else: `history.record` receives the line with the reference
intact. A command carrying a reference moves to another machine and resolves
that machine's secret; a command carrying a pasted key is both dead and
dangerous. A sealed vault is a specific, actionable error (`-32001`,
`vault-sealed`), because the caller has to be able to tell "unseal and
retry" from "no such secret"; unresolved names are reported, never silently
left as literal text.

**A submitted credential awaiting a save decision is held by the backend,
never the renderer.** The offer moves to AFTER submit: the backend receives
the command at the history-write seam, holds the plaintext as a single-use
pending capture in process memory, and hands the renderer an opaque capture
id plus non-secret display metadata. The full contract (destruction
triggers, idempotent single-use save, fingerprint suppression) is pasted
into `internal/credential/capture.go`.

**A pending capture has no lifetime of its own** (amended 2026-08-03, after
the first round of real use). Round 1 gave it a 30-second expiry and killed
it at the next submission from that tab. Both were wrong, and for the same
reason: what they bounded was how long one credential sits in this
process's memory, while the same command sits in cleartext in the shell's
own history file on disk — which this ADR has already measured and decided
not to treat as a threat. What they cost was the decision itself. The
offer arrives when the command finishes, the person is still reading the
output it produced, and thirty seconds later it retired itself; and
deciding about a key is rarely the next thing anyone does, so running one
more command to check something lost the offer for good.

So a capture now lives until it is saved, dismissed, or one of the real
events takes it: the tab or session closing, the vault sealing, the
transport dropping, the application quitting, or the history record
failing. Several can be pending at once — one per block with an unanswered
offer — and the cost is one credential in memory per unanswered offer for
the life of the tab. That is the trade this product has already made
everywhere else, and unlike a timer it is a boundary the user can see: the
offer sits on its block until they answer it. Saving is two stores in one order —
create the vault secret (name collisions resolved atomically in the vault,
the real name comes back), then rewrite the linked history rows by stable
id — and a partial failure keeps the secret, leaves history safely masked,
and lets the rewrite be retried without minting `openrouter.ai-2`.
Detection is one implementation (`internal/secrets`) exposed over the wire
as `secrets.detect`; the TS port is deleted, and the renderer's prompt hint
calls the wire after its existing 500 ms debounce — one call per pause.

### What we can promise

- The secret does not reach our ledger: the durable command text is the
  masked one, and the mask facts are kinds, spans and mask heads/tails,
  never values. The store's own pass decides the row; a finding shown to
  the renderer is advice, never trusted. This round ships that, end to end,
  and the save path rewrites the row to a reference only after the vault
  holds the value.
- The secret never enters a model context: nothing in this seam feeds a
  model, and ADR-0011 §2 already refuses to hand stored values back out of
  the backend except through the value's single crossing, which is the PTY
  write.

### What we cannot promise — measured, and corrected

**The shell's own history file DOES record the resolved line — unless the
write is suppressed.** Measured 2026-08-03 on bash 5.3, zsh 5.9, fish 4.8
(interactive shells, disposable `$HOME`, a real reference-carrying command
written to the PTY the way the app writes it): all three record the line
with the resolved value in their history file after a clean exit, all three
render the echoed line in the scrollback, and all three expose the value in
`argv` to `ps` during the run. The round-1 text claimed "the secret does
not reach the shell's own history file"; that is false for a plain write.
The suppression seam works where the round-1 text guessed it would — the
same measurement shows a leading space suppresses the line under
`HISTCONTROL=ignorespace` (bash), `HIST_IGNORE_SPACE` (zsh) and fish's
default leading-space rule — so the write seam MUST emit the suppressed
form, and even then the shell must honor it. The product's language is
therefore "save for reuse", never "protect": the user's shell history is
the user's own history policy, and the app's promise is that it never
writes the plaintext anywhere it controls.

**Substitution puts the value in the process's argv, and argv is readable
by `ps`** for every process of that user, and is recorded by audit and by
sudo. No architecture of ours removes that — it is how exec works,
confirmed by the same measurement. What the design does is bound the
value's lifetime to that one submission: it is not in the ledger, not in
any log, and the app never writes it to the shell's history. The exposure
window is the command's own execution, which is the window any pasted key
already has.

## Consequences

**A masked command re-run from history looks real and cannot work.** The
durable text is the masked one, so re-running `curl -H "Authorization:
Bearer sk-p...7890" https://api.example.com` sends the mask, not the key.
That is correct: the mask must never be silently executed as if it were the
command. Enter on such a row must not run silently — the next round's
problem, named here so the block UI is built with it in mind.

**Searching for a fragment of a key finds nothing, correctly.** The masked
text contains no fragment of the key — a search for `sk-proj-abcdef` misses
the row. That is the feature, not a gap, and the search panel's coverage
line will have to say it: history is searchable, and key material is not in
it.

**A schema change rebuilds the store and says what it discarded.** The
schema gains the `redactions` column on a greenfield table; there are no
migrations and we wrote none. A database written by the previous schema is
rebuilt at open, and the log states how many rows were discarded — "your
history was discarded" is a fact the user is entitled to rather than
something to infer from an empty panel. This supersedes the round-1 text
that a mismatched database merely "fails to open cleanly".

**Suppression by value equality is session-scoped, deliberately.** The
fingerprint is HMAC(app-owned key, canonical secret bytes) with a key that
dies with the process: pending, saved and dismissed lookups work for the
application session, and a restart re-offers a value the user never decided
on — the same boundary the round's brief draws for dismissal ("for the rest
of the application session, not forever"). Making saved-status durable
would require a durable fingerprint key, and a key stored beside the
fingerprints would be an offline password oracle; this round does not build
a second key lifecycle for it.

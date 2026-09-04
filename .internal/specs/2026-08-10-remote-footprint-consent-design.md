# Consent before a remote footprint

- **Date:** 2026-08-10
- **Status:** design, approved by the owner; rewritten after stress-test
- **Supersedes:** **N3** of [`2026-08-05-nocxify-delivery-modes-design.md`](2026-08-05-nocxify-delivery-modes-design.md)
  ("script mode wraps and installs automatically, without asking")
- **Brainstorming bead:** `nocx-i5yl` · **Stress-test bead:** `nocx-ciwc`

## What is already decided, and by whom

Per AGENTS.md, the boundaries this design crosses and what they already say, before it
says what to build.

| Binding text                                          | What it already decided                                                                                                                                          | This design                                                                                              |
| ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| **N3** (2026-08-05 §3.5)                              | Script mode installs automatically, without asking; consent required only for the helper binary. Explicitly overrode ADR-0004 §2 for script delivery.            | **Reversed.** Requires an ADR.                                                                           |
| **N3's compensating control** (2026-08-05 §4.1)       | The product _shows_ the footprint with an uninstall action "even though consent was not asked". Implemented as P10 / `nocx-bu6q`.                                | **Kept and repaired.** Stops being a compensation, becomes ordinary inventory.                           |
| **ADR-0004 §2**                                       | Consent once per destination; automatic integration only as an informed opt-in, "never as the default".                                                          | **Restored** for script delivery.                                                                        |
| **ADR-0004 §7**                                       | Fail-open is absolute. Untouched by ADR-0024.                                                                                                                    | **Binding on the ask** (§4.2): a failure to decide never swallows a command.                             |
| **ADR-0024 decision 4**                               | _There is no in-band fallback tier._ No authoritative channel ⇒ conventional terminal: no blocks, no ledger.                                                     | **Obeyed.** It is why declining is expensive, and why `script` and "files on the host" are one choice.   |
| **ADR-0022**                                          | The ssh command line is the carrier; the backend composes the rewritten line for a child domain.                                                                 | Why the child path is authorisable at all (§4.3).                                                        |
| **ADR-0023**                                          | A jump route is its own host-key identity.                                                                                                                       | **Superseded as the consent key** by the host key itself (§3.2) — the route was a proxy for the machine. |
| **ADR-0015**                                          | The oracle is `ssh -G <host>`, cached per resolved identity.                                                                                                     | Used for mode resolution, **not** as the consent key.                                                    |
| **AD-6**                                              | The backend never sniffs the byte stream.                                                                                                                        | Why an unintegrated session gets no offer (§4.4).                                                        |
| **AD-5**                                              | Tier A is integration with no remote install.                                                                                                                    | Already contradicted by N3; the ADR must amend it either way.                                            |
| `DesiredMode` (`internal/profile/profile.go:38`)      | `raw` adds nothing; `script` runs the shell tiers; `helper` deploys the Tier-B binary. Inherited through profile → group → global. **N3 made `script` default.** | **Gains `auto`, which becomes the default.** No new axis (§3.1).                                         |
| `HelperConsent` (`internal/profile/profile.go:66`)    | Three-state, per destination, never inherited; script mode never consults it.                                                                                    | **Unchanged and not stacked** with this ask (§5.2).                                                      |
| `InstalledFact` (`internal/ssh/installed_fact.go:13`) | Keyed by the resolved `ssh -G` identity — observation of what is on a host.                                                                                      | **Observation only, never authorization** (§6).                                                          |

## 1. Context

### 1.1 What is true on this branch

`shady2k/fix-omp-hide` made the persistent remote bundle **load-bearing**, and the in-tree
comments that say otherwise are stale.

The argv launcher no longer embeds the integration scripts; it _sources_ the published
generation (`internal/shellintegration/launch.go:93`):

```sh
. "${HOME}/.nocx/integration/${NOCX_GENERATION}/nocx.bash" 2>/dev/null
```

`launcher_bash.go:211` gives the reason and `:215` the figures: embedding was **measured at
171,678 bytes before that change**, against nocx's own conservative single-argv cap of
**122,880** (`maxFullLauncherLen = 120 * 1024`, `launcher.go:210` — _not_ the kernel's
`MAX_ARG_STRLEN` of 131,072). The consequence is stated there too: "a failed publish leaves
`NOCX_GENERATION` unset, the source line names no file, and the session is a conventional
terminal with a visible native prompt (ADR-0024 decision 4 — the transient-integrated
middle tier is **deleted**, not degraded to)".

> **Correct these in the first package.** `launcher_publish.go:10` and
> `install_remote.go:152` still promise "transient-integrated". They are what led one
> reading of this branch to conclude that declining an install was free.

**This is why there is no separate consent axis.** With no middle tier, "run our scripts"
and "leave files on this host" are not two choices — they are one. A second field naming
the same decision would be the duplicate-owner defect AGENTS.md warns about.

### 1.2 Why N3 is reversed

The owner's principle: **if we leave a trace on someone's machine, we ask first.**

A no-disk delivery tier was considered and rejected by the owner: the argv cap is hard and
the scripts only grow, so a no-disk path is a reprieve, not a solution. Recorded so it is
not re-litigated.

### 1.3 What declining costs

A conventional terminal on that machine: native input, a visible native prompt, one
continuous grid, **no command blocks and no command ledger**. Not softened, and no middle
tier is built to hide it.

## 2. Greenfield

No installed base, no migration, no reconciliation of pre-existing state. Absent values
resolve to the new default; nothing converts.

## 3. The model

### 3.1 `desiredMode` gains `auto`, and `auto` becomes the default

```go
const (
    DesiredAuto   DesiredMode = "auto"   // pick the best tier actually available — the default
    DesiredRaw    DesiredMode = "raw"    // nothing added: no rewrite, no remote write
    DesiredScript DesiredMode = "script" // the shell tiers we ship
    DesiredHelper DesiredMode = "helper" // Tier B, a deployed binary — consent-gated
)
```

`auto` resolves, per machine, to the best tier that is genuinely available:

1. **helper** — a suitable binary exists for that platform. _(Tier B is not built; this arm
   is deliberate forward structure so the default need not change when it lands.)_
2. **script** — no binary, but a shell tier fits (known shell, bootstrap under the cap).
3. **raw** — neither. Today's `unsupported-shell` refusal and the over-cap refusal become
   an honest named outcome instead of a failure with an unhelpful reason.

**An explicit choice is the consent.** A user who set `script`, `helper` or `raw` — on the
profile, or on a group, or as a global default — has answered, and is not asked. Consent is
never inherited from the _hardcoded_ default, and does not need to be: the hardcoded
default is now `auto`.

**`auto` with no stored answer for the machine asks** (§4). Anything else would be N3 under
a new name.

Everything else about the axis is unchanged: the existing cascade, the patch surface, the
contracts, and the rule that an unrecognised stored value falls back at _resolution_ rather
than at decode, so an explicit choice never becomes a silent no-op.

### 3.2 The answer is keyed by the host key

Backend-owned, persisted, keyed by the **remote host's public key** — not the hostname, not
the profile, not the route.

- **The same machine reached any way is one answer.** Direct or through a bastion, one key,
  one question, one row on the footprint screen.
- **Two machines spelling themselves the same are two answers.** Different host keys.
  ADR-0023 made the jump route its own host-key identity precisely because a spelling does
  not name a machine; the host key is the thing the route was a proxy for, so it is used
  directly.
- **A rotated host key asks again** — correct, because a changed host key already means
  "check this is the machine you think", and ssh says so itself.
- **Consent is per machine, not per account.** One machine may carry a footprint in more
  than one home directory; the footprint screen lists each, under one answer.

The key is known before any write: it is verified against `known_hosts` in the dial path
(`ssh_dial.go:175`, and `:233` for the jump hop).

### 3.3 Two stores, two questions

The installed fact answers _what is on this machine_; the answer answers _what the user
permitted_. Neither is derived from the other, and **file presence never implies consent** —
a directory a stranger could have written must not decide for the user.

## 4. When the ask is raised

Resolution order, per connection:

1. Resolve `desiredMode` through the cascade.
2. `raw` → nothing is written and **nothing is asked**. The user answered more broadly
   already.
3. `script` / `helper` (explicit) → that is the consent. Proceed. `helper` additionally
   consults its own `HelperConsent`, unchanged.
4. `auto` → resolve the best available tier (§3.1). If it is `raw`, nothing is written and
   nothing is asked. Otherwise look up the answer for this host key:
   - present → honour it, silently;
   - absent → **ask, before anything is written.**

### 4.1 The ask rides with host-key trust

An unknown host already stops for host-key verification (`ErrUnknownHostKey`, the
`connections.trustHostKey` flow). The person is already there, already answering "is this
the machine I think it is". **The integration question is asked in that same moment** — one
interruption, not two, and both are about the same thing: what this machine is and what it
may be trusted with.

A host already in `known_hosts` has a known key, so the answer is found before connecting
and there is no interruption at all.

### 4.2 Fail-open, in the direction of less privilege

Deciding has a **hard budget of 3 seconds**. Saturation of the control plane (the ADR-0024
admission contract refuses under load), a slow oracle, or an unreachable answer store all
resolve the same way:

> The command goes out **as typed**, conventionally. Nothing is rewritten, nothing is
> installed, and **nothing is stored** — an undecided question is not an answer.

Three seconds is the ceiling of the pathological case, not the expected latency; the normal
answer is immediate. This preserves ADR-0004 §7: pressing Enter never swallows a command.
Degrade toward the plain terminal, never toward the larger privilege.

### 4.3 The first install on a machine always originates from a human act

`DomainRequest` (`internal/lifecycle/protocol.go:209`) carries `RequestID`, `Env`, `Host`,
`User`, `Port` — chosen by the **far-side shell**, and the channel authenticates who speaks,
not that what they say is true. The capability is well protected (substituted into the
rcfile text, never exported, never in a file or the renderer), so this needs the shell
process itself to be compromised — narrow, but it must not silently widen.

> A far-side request may **use** an existing installation on a machine already answered
> for. It may never **cause a first installation.** That always comes from the trust/ask
> moment, the connection form, or an explicit reconnect.

### 4.4 No offer where there is nothing to ask with

- **An unintegrated session gets no offer.** No channel, no `domain_request`, and AD-6
  forbids inferring from the stream. The offer for such machines lives in the connection
  manager.
- **Nested `ssh` inside an already-remote parent is out of scope** —
  `buildSSHChildBootstrap` (`internal/app/childdomain.go:215`) refuses it today and runs the
  command conventionally. Unchanged: nothing asked, nothing written.
- **No surface to ask on** (headless `devharness`, a disconnected frontend, automation) ⇒
  **no install, and nothing stored.** Being unable to ask is not the user declining.

## 5. What the user sees

### 5.1 Wording

Offered tiers, and what each leaves behind, with both outcomes named:

> **How should nocx use `db01`?**
> **[Blocks — keeps files in `~/.nocx`]** **[Plain terminal — leaves nothing]**

When a footprint already exists on that machine, declining offers to take it with it:
**[Plain terminal, and remove what's there]**. Never the default, but not hidden in
settings either.

### 5.2 `helper` is not stacked onto this

When Tier B lands, a `helper` user answers about the binary through `HelperConsent`, as
today. The two are never merged into one question: declining a deployed binary must not
also decline shell scripts — different risks.

### 5.3 Changing your mind

The connection form carries `desiredMode`, defaulting to `auto`; the footprint screen lists
what is installed where and revokes the answer. The removal action reads **"remove managed
integration files"** — `Publisher.Uninstall` deliberately leaves `~/.nocx`, `launch` and
`tmp`, and promising more would be a new untruth of the kind this design exists to remove.

## 6. Intervals, races and failures

Stated with both ends, per AGENTS.md rule 3.

- **The authorization interval.** An applicable answer (or an explicit mode) exists
  continuously from **before any write-capable delivery path is selected** until
  **publication completes or rolls back**. Selection, not the first byte: the full bootstrap
  launcher _contains_ the publishing prelude, so choosing it is already the authorised act.
- **A declined machine is not deactivated by halves.** No `EnsureInstalledRemote`, **and**
  no launcher that publishes or sources a managed generation — including the compact
  installed path an existing fact would otherwise select. Files are not deleted unless the
  user chose that at §5.1; reconnect is conventional until they re-answer.
- **Unwritable answer store ⇒ no remote write.** Otherwise nocx leaves a trace it cannot
  show the authorization for.
- **A dismissed dialog is neither granted nor denied.** Nothing stored, nothing installed,
  and it is asked again next time.
- **Concurrency.** One pending decision per host key; concurrent callers join it. One
  waiter cancelling does not cancel the shared decision.
- **Revocation.** Prevents any not-yet-started publish immediately; an in-progress publish
  completes under its captured authorization or is cancelled with defined partial-write
  recovery. Live sessions stay live until disconnect.
- **Granted, publish failed.** The answer stays; the **session says so** — "installation
  failed, conventional terminal". Today it is log-only on both halves: the SFTP publish is
  best-effort and logged (`ssh_real.go`, ignored branches at :768–775) and `launchSourceLine`
  suppresses stderr on purpose (`launch.go:88`).
- **Over the argv cap.** `fullBootstrapLauncher` returning false against
  `maxFullLauncherLen` is mapped to `ReasonUnsupportedShell` (`launcher_bash.go:230,237`;
  `launcher_auto.go:82,86,90,97`) — untrue and unactionable. Under `auto` this becomes a
  resolution to `raw` with a distinct, honest reason; the reason must exist before any UI
  explains the outcome.
- **The fact writer records observation, never authorization.** An accepted passport proves
  integration _ran_, not that it was permitted. A newly accepted passport for a machine
  answered "plain terminal" means a race or a bypass: it is surfaced, never silently
  recorded as healthy.

## 7. Scope

**In.** The `auto` value and its resolution; the answer store keyed by host key; the ask at
the trust moment; the 3-second budget; the human-origin rule; the no-surface rule; the
decline-and-remove option; the two visible-failure fixes; correcting the stale
transient-integrated comments; the ADR superseding N3.

**Out.**

- **Local install.** `internal/app/app.go:935` calls `EnsureInstalled`, which writes
  `~/.nocx` **and appends gate lines to the user's rc files**. Installing nocx on your own
  machine is the consent. Unchanged, by owner decision. _(Separate bead: local installation
  is invisible on the footprint screen.)_
- **A no-disk delivery tier.** §1.2.
- **Nested ssh inside a remote parent.** §4.4.
- **Tier B / the helper itself.** Not built; only the `auto` arm anticipating it.
- **PR #69's blocker** (`nocx-292k`). Required for this design too, but not gated on it, and
  its repair is pure observation (§6).

## 8. Acceptance criteria, as assertions

1. **Authorization interval.** From before any write-capable delivery path is selected until
   publication completes or rolls back, an applicable answer or explicit mode exists
   continuously. Every failure before that opening event leaves the remote filesystem
   untouched — asserted at each partial publisher failure, naming what exists on disk, in
   memory and in the store after each.
2. `auto` + unknown host ⇒ the integration question appears **in the same interaction** as
   host-key trust, and nothing is written before it is answered.
3. `auto` + known host with a stored answer ⇒ **no interruption**, and the outcome matches
   the answer.
4. The same machine reached directly and through a bastion is asked **once**; two machines
   sharing a spelling but not a host key are asked **twice**; a rotated host key asks again.
5. An explicit `script`/`helper`/`raw` — own, group or global — is never asked about. The
   hardcoded default never counts as an explicit choice.
6. `raw`, and `auto` resolving to `raw`, write nothing and ask nothing.
7. Declining leaves no new bytes on the machine, is remembered, and a later connection
   neither asks nor installs; choosing decline-and-remove removes exactly what
   `Publisher.Uninstall` removes and no more.
8. A decision that does not arrive within 3 seconds sends the command **as typed**, installs
   nothing, and stores nothing; the same holds when no surface exists to ask on.
9. A far-side `domain_request` naming a machine with no stored answer **cannot** cause an
   installation.
10. Granted + publish failed ⇒ the session **displays** the reason, asserted for each
    enumerated partial failure of the publisher.
11. Uninstall, revocation and inventory update are asserted **in both orders and at each
    partial failure**: which is durable, what the next connection does, and that clearing an
    answer while files remain cannot produce an ask that silently reuses them.
12. **No active artifact asserts N3 as current behaviour** — not code, comment, contract,
    generated type, UI label or test; historical design documents keep their text and gain an
    explicit supersession note. (Known sites: `profile.go:38` and the hardcoded fallback near
    `:786`; `ssh.go:196`; `ssh_real.go:728`; `ws.go:1197`, `:1436`; `ws_shell_footprint.go:5`;
    `contracts/open.schema.json` and `contracts/shell.footprint.status.schema.json` with their
    generated TS; `frontend/src/capability.ts:38,66`; `profiles.ts:59`; `connections.tsx:207`;
    `desiredmode_test.go`, `resolver_test.go`, `ssh_launcher_test.go`, `ssh_lifecycle_test.go`,
    `ws_contract_test.go`, `connections.behavior.test.tsx`, `e2e/shell-mode.spec.ts`.)
13. **End to end, executable.** Against the disposable sshd on the e2e harness (whose host
    key is fresh per run, so every run is honestly a new machine): connect; assert the trust
    interaction carries the integration question; decline; assert a working terminal **and**
    no `~/.nocx` on the far side; reconnect and assert no question and still no `~/.nocx`;
    change the answer through the named control; reconnect; assert blocks **and** `~/.nocx`
    exists. Each observable named as a selector or filesystem assertion, not as prose.

## 9. Testing notes

- Per AGENTS.md rule 4, these assertions belong in the beads; the implementer does not author
  them.
- Per rule 3, every external call gets a failure test — oracle resolution fails, publish fails
  mid-way, answer store unwritable, dialog dismissed, no surface available — and each "returns
  an error when…" is paired with "and on an ordinary machine it succeeds".

## Stress Test Results: consent before a remote footprint

Nine branches interrogated; six agreed, three changed the design materially.

### Resolved decisions

- **Policy scope** — saved connections inherit the cascade; everything else is treated per
  machine. The safety property is the ask itself, not policy reach. Accepted consequence:
  `off`-style policy on a group is not a guarantee for a hand-typed `ssh` to a member.
- **File presence is not consent** — and, after the owner's "orienting on files is strange",
  file state stopped driving the decision at all. Removed an entire class of edge cases
  (shared accounts, third-party installs, stale generations).
- **Fail-open** — a 3-second ceiling, then the command goes out as typed. Degrade toward the
  plain terminal, never toward the larger privilege.
- **Ask at connection creation** — the form carries the mode; the dialog serves only what is
  never saved.
- **Untrusted request fields** — a far-side request may use an installation but never cause a
  first one.
- **Host key as the consent key** — replaced "identity + route". The route was a proxy for
  "which machine"; the host key is the answer. This also let the ask ride with host-key trust,
  removing the second interruption.
- **No fourth axis** — the decisive correction. With ADR-0024 decision 4 having deleted the
  middle tier, "run our scripts" and "leave files here" are one choice, so a separate consent
  field would have been a second owner of one concept. `desiredMode` gains `auto` instead.
- **`auto` asks** — otherwise it is N3 renamed.
- **Reversibility** — an explicit mode restores prior behaviour; greenfield, nothing converts.

### Changes made

Removed the `FootprintPolicy` axis and its cascade projection; removed the route-keyed consent
and the two-store join it forced; removed the invented reconciliation of "pre-N3 state"
(greenfield); added `auto`, the host-key answer store, the trust-moment ask, the 3-second
budget, the human-origin rule, the no-surface rule, the `raw`-first ordering, the `helper`
non-stacking rule, and decline-and-remove.

### Deferred / parking lot

- Local installation is invisible on the footprint screen — separate bead.
- Tier B / the helper itself; only the `auto` arm anticipating it is in scope.
- A no-disk delivery tier — rejected by the owner, recorded in §1.2.

### Confidence assessment

- **Overall: medium-high.** The model is now a value on an existing axis plus one store,
  which is far less surface than the design started with, and every binding document it
  crosses is named.
- **Areas of concern:** (1) the `auto` → helper arm is structure for something unbuilt, and
  YAGNI is a real objection — kept only because it fixes the default once; (2) the ask riding
  with host-key trust couples two flows that are currently independent, and that coupling is
  the least-tested part of the design; (3) criterion 1's partial-failure enumeration depends
  on the publisher's steps being enumerable, which has not been verified against
  `publisher.go` in detail.

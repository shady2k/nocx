# Connection manager: groups, shared credentials, and computed authorization

- **Date:** 2026-07-29 (rev. 3 — rev. 2 after adversarial review; rev. 3 withdraws the
  pool-rotation finding of §2.9, which was wrong)
- **Brainstorming bead:** `nocx-52cd`
- **Status:** design approved, pending implementation plan
- **Supersedes in part:** [ADR-0006 — Reusable Credentials](../../docs/decisions/0006-reusable-credentials.md)
- **Adopts the diagnosis, rejects the mechanism of:** PR #59 / draft ADR-0013
  "Credential-Scoped Trusted Endpoints" (see §3.2; its §4, one owner of endpoint
  identity, is adopted and then refined in §3.4)
- **Note:** that draft claims ADR number 0013, which
  `0013-plain-css-with-semantic-custom-properties.md` already holds on `main` — the
  collision is `nocx-yclf` and must be resolved before any ADR from this work is numbered.

## 1. The problem that started this

A fleet of servers shares one username and password. The password rotates. The owner
wants to change it **once** for the whole fleet.

Today that is impossible by construction. `Credential.Host` is **required**
(`internal/profile/profile.go:145-153`, enforced again in `store.go` `SaveCredential`),
so one credential serves exactly one host, and a fleet of forty needs forty credentials.
Rotation is forty edits.

**What this design does and does not promise.** The requirement is to derive credential
use from the saved connection instead of maintaining a second, hand-typed host binding
that drifts, and to prevent _accidental_ and _connect-time_ redirection. It is **not** a
defence against a compromised renderer: a renderer authorized to call `profiles.update`
remains authorized to change where a credential is spent. `nocx-mon` required the host
binding to close the "any host" redirection hole, and the code's own comment
(`profile.go:132-144`) already concedes the limit of that defence — a binding the
constrained actor can rewrite is not an authorization boundary. §3.6 states the boundary
we actually ship on.

## 2. What we found in the code

Verified by reading, on `main` at `5c5eb2d`, and re-verified under adversarial review.
Bold entries had no bead.

1. **Editing a credential wipes its stored secret references.** `credentials.list` blanks
   `SecretID` and `PassphraseSecretID` before answering the renderer — correct, per
   ADR-0011 §2. But `credentials.create/update` (`ws.go:1107`) unmarshals the renderer DTO
   straight into the persisted `Credential`, and `SaveCredential` (`store.go:210`)
   **replaces** the whole stored record, writing the blanks back. The stored password and
   passphrase become unreachable, and password/passphrase authentication may silently stop
   working — not always visibly, since agent auth or `AuthAuto` may fall through to another
   method. The old secret is orphaned beyond recovery: the delete cascade (`ws.go:1381`)
   finds secrets _through_ the metadata that just lost their IDs.
2. **There is no group UI at all.** `groups.create/update/delete` exist in the RPC surface
   (`ws.go:1049`) and on `ProfileClient` (`profiles.ts:210-221`) with **zero** production
   callers. A group can only enter the system through the Tabby importer.
3. **`ProfileGroup.Defaults` never reaches a connection.** The Go merge (`applyDefaults`,
   `mergeSSHOptions`, `mergeInto`) is unreachable from `main()` (`nocx-hhj3`), and the
   resolver reads the raw stored profile directly (`resolver.go:41`). The live TypeScript
   side builds the render tree and implements **no inheritance at all**. Note the
   asymmetry: `BuildGroupTree`/`ResolveGroupPath` are dead _in Go_ while their TS
   equivalents are live and legitimately needed for rendering — see §3.3 for what is
   actually deleted.
4. **Credential IDs are minted client-side and frozen too early.** `updateField`
   (`connections.tsx:710-713`) sets `` `cred:${value}:${Date.now()}` `` only while
   `!credential.id`, so the ID is derived from the **first keystroke of the name** and then
   never revised: typing `p` first permanently yields `cred:p:…` for a credential finally
   named `prod-ops`. It is unslugified, and the server's `NewCredentialID` is bypassed
   whenever the renderer supplies an ID.
5. **`frontend/src/connections.tsx` is 916 lines** carrying the profile list, the profile
   form, the credential list, the credential form and the Tabby import — two entities
   sharing one list and one panel.
6. **`TextField` does not compose `Field`.** `ui/field.tsx:79` renders the required
   asterisk; `ui/text-field.tsx:51-59` re-implements its own label, description and error
   and passes `required` only to the DOM attribute. Required `TextField` instances in the
   connection and credential forms therefore carry no visual required marker, and the kit
   holds two label implementations.
7. **Multi-hop jump routes are broken.** `resolver.go:102` builds `jumpCfg` recursively but
   `resolver.go:119-131` copies only the immediate jump fields into the target config. The
   nested route is discarded; exactly one hop survives. Do not claim multi-hop support
   until the representation changes.
8. **Imports are alternate writers and are not atomic.** `importer/tabby.go:85` and
   `export/import.go:37` call `SaveProfile` directly, bypassing any domain service, and
   credentials are imported in a second pass after profiles. A failure halfway leaves a
   partially imported store on disk, not merely a transient in-memory inconsistency.
9. **A public key is pooled by path, not by key identity** — and a larger claim here was
   **withdrawn**, see below. `poolKeyFor` (`ssh_dial.go:38`) sets the pool identity to
   `string(cfg.SecretID)`, falling back to `cfg.KeyFile` and then to `~/.ssh/config`'s
   `IdentityFile`. For a key, that is a **path**: replacing the file contents at the same
   path changes what authenticates without changing the pool key. Agent auth leaves the
   identity empty by design, which the comment defends as "no second principal to
   isolate" — but the agent's key set can change underneath that assumption, so the
   behaviour should be chosen explicitly rather than inherited.

   **Withdrawn:** rev. 1 and rev. 2 of this document claimed the pool is not invalidated on
   password rotation. That is false. `savePasswordForCredential` (`ws.go:1244-1252`) mints a
   **fresh** `SecretID` for every password change and the resolver copies it onto the config
   (`resolver.go:91-93`), so the pool key changes and the old transport is not reused. The
   bastion is handled the same way through `JumpSecretID` (`ssh_dial.go:86`). The error came
   from reading `pool.go:67-73`'s doc comment — which says the identity is the
   "stored-credential ID" — instead of the construction site that actually populates it. Two
   independent reviewers made the same mistake from the same comment, which is itself the
   defect worth fixing: **the comment describes a weaker key than the code implements** and
   must be corrected to say `SecretID`.

10. **The replay ring holds raw output on the backend.** `ring.go:10` retains 256 KiB per
    session and `ring.go:33` keeps it across subscriber disconnects by design. Any
    frontend-only masking is therefore a display feature, not a confidentiality guarantee.
11. From the tracker, in scope: `nocx-u5ai`, `nocx-3isn`, `nocx-49x`, `nocx-hhj3`.
12. `nocx-3wjh` was stale — closed 2026-07-29 with evidence rather than worked.

## 3. Model

### 3.1 Authorization is computed, not stored

`Credential.Host` and `Credential.Port` are **deleted**. A credential carries identity
only: name, username, auth mode, key metadata, opaque secret references, secret versions
(§3.9).

**That deletion belongs to wave 2, not wave 1**, and the boundary matters. `checkBinding`
(`ssh_config.go:105`) refuses a connection whose `BoundHost` is empty — that is `nocx-mon`'s
defence, and the resolver fills `BoundHost` from `Credential.Host` (`resolver.go:85-86`).
Delete the field while computed authorization is still a wave away and every stored password
starts failing at connect time. So the field, `ErrCredentialHostRequired` and the
`BoundHost`/`BoundPort` wiring all survive wave 1 untouched, and go **in the same commit
range** that makes computed authorization live. Writing the wave-1 plan is what surfaced
this; the earlier revisions of this document had the deletion in wave 1 and would have
shipped a wave that breaks password auth.

At connect time the backend proves authorization **locally**, from the selected profile:

```
selected saved profile
  -> resolve its group inheritance chain
  -> obtain its effective credentialId and secret version
  -> resolve its canonical endpoint once
  -> authorize that (credential version, endpoint) pair
```

Repeated per credential-bearing hop, where each hop is justified by the effective profile
that supplies **that hop's** credential — not by the fact that some unrelated saved profile
happens to use the same credential at the same endpoint.

This is O(inheritance depth + jump depth), **not** a scan of every profile. Scanning would
widen the TOCTOU window, make authorization depend on unrelated records, and fail noisily
when one unrelated alias is malformed.

"Which connections use this credential?" is a **separate query for the UI**, never the
connect-time algorithm. Because inheritance exists, that query must be answered by
effective resolution, not by grepping for direct `credentialId` references.

Resolve **once**. Resolving for authorization and again for the dial reintroduces TOCTOU
and recreates the dual ownership this design exists to remove.

### 3.2 Why not PR #59's persisted grants

PR #59 replaces the host binding with backend-owned
`TrustedEndpoints []{ProfileID, Host, Port}`, minted when a profile referencing the
credential is saved. Its diagnosis is right and we keep it: the credential↔host
relationship must be _derived from the saved connection_, not typed twice into a
`Bind to Host` field that drifts.

We reject its mechanism. The grant table is derived data — it contains no fact absent from
the profile list — and the expensive machinery in that PR (a v0→1 storage migration, an
atomic `SaveProfileWithGrant`, an `AuthorizationRevision` for pool invalidation, a
`requiresReview` marker) exists only to keep that cache coherent.

Its stated threat model does not hold either. The ADR says "`open` must never add a grant,
else redirecting a profile to an attacker host would authorize itself" — but the grant
**is** minted by `profiles.update` (`ws.go:1022` accepts the renderer-supplied profile
whole), which the renderer can call. The same redirection works, one step longer.

Adversarial review surfaced the one thing the grant table _does_ buy: it turns **direct
profile-file tampering** and **`~/.ssh/config` drift** into fail-closed events rather than
silent re-authorization. That is real, and we take the consequence deliberately (§3.6)
rather than paying for a cache that a hostile renderer re-mints anyway.

### 3.3 Groups carry defaults; inheritance is typed, sparse and provenance-tracked

`ProfileGroup.Defaults` becomes genuine backend-side inheritance. `credentialId` is one
inherited field among port, jump host and keepalive — "one password for a fleet" is a
special case of "a group hands settings down", which is also what a fleet needs for a
non-standard port or a shared bastion.

Resolution order: **profile → nearest ancestor group → … → root group → global**.

**The current value types cannot express this, and that is a foundational blocker.**
`mergeInto` (`profile.go:188-221`) treats the zero value as "unset" and copies booleans
only when `true`; `ProfileGroup.Defaults` is an untyped `map[string]any`. Consequently a
profile cannot override an inherited `true` with `false`, and "inherit the port", "use 22
explicitly" and "unset" are the same state. Provenance is not merely unrendered — it is
**unrepresentable**. Inheritance therefore requires sparse, presence-aware typed values
(pointers, option wrappers or patch fields) and a typed defaults schema. This is a
prerequisite, not a refinement.

Three binding rules:

- **An inherited value is never materialized into the stored profile.** If it were, the UI
  could no longer distinguish "inherited 2222" from "overridden here to 2222". A user may
  still _deliberately_ pin a value before moving a profile — that is simply setting the
  field explicitly, which the model already supports, not a new mechanism.
- **Every effective field carries its provenance**, and the UI shows it.
- **Provenance explains state; it does not make mutations quiet.** This is the correction
  the review was right about and the earlier draft got wrong. A label reading "inherited
  from Prod" is passive explanation _after_ the fact. The dangerous operations — dragging a
  profile between groups, changing a parent group's credential, reparenting a subtree,
  deleting a group, editing a root default — change authentication for records that are not
  on screen. Therefore:
  - moving or reparenting shows the **effective-field diff before** it is applied;
  - any change to credential, username, endpoint or jump route requires explicit
    confirmation, visually distinguished from cosmetic folder changes;
  - editing group defaults shows the blast radius ("changes the credential for 37
    connections");
  - group deletion states what happens to children — reparent, promote to root, or refuse;
  - bulk group changes are atomic;
  - the backend validates group cycles, missing parents and maximum depth, and a group's
    `ParentGroupID` or defaults cannot be changed through generic CRUD that bypasses the
    impact workflow.

With those invariants, one tree is sufficient and a second hierarchy (folders for
navigation, sets for policy, tags for discovery) is machinery the user would have to be
taught for no gain we can currently name.

**What is deleted (`nocx-hhj3`):** Go exclusively owns inheritance, validation, effective
values and canonical hierarchy semantics. The frontend may project backend-provided groups
into a render tree — rendering a tree is not the same rule as resolving effective
configuration — but must not duplicate inheritance or path-resolution policy. `deadcode`
must report no unreachable functions in `internal/profile`.

### 3.4 One resolution, several explicitly-purposed identities

A single backend service composes saved profile + group chain + credential metadata +
`~/.ssh/config` into an `EffectiveProfile` with per-field provenance. PR #59's "one owner
of endpoint identity" is right, but collapsing everything into one string is wrong: SSH
host-key lookup is not always the canonical `host:port` (`HostKeyAlias`, bracketed
host-port forms, aliases). One **resolution result** carries several identities, each with
a stated purpose:

```
DialEndpoint       resolved network host + effective port
HostKeyIdentity    the name used for known_hosts lookup
AuthorizationPair  credential version + the effective profile endpoint it may be spent on
DisplayTarget      the original alias or input, for the UI
```

Rules: an alias is display metadata and never an authorization identity; DNS results and
CNAME targets are never persisted as identity — normal DNS movement must not rewrite
authorization; ports outside `1..65535` are rejected; DNS names compare case-insensitively
in canonical form and IP literals, including IPv6, are parsed and stored canonically.

Every writer — ordinary save, Tabby import, configuration import, `~/.ssh/config` adoption
— goes through this service. Today none of them do (§2.8).

Host is **required and never inherited**: inheriting it would let moving a profile between
groups change which machine it is.

That collides with one existing record category, and the collision is resolved by deletion:
`Base.IsTemplate` (`profile.go:47`, mirrored at `profiles.ts:27`) is declared in exactly
those two places and read by nothing — no writer, no reader, no UI. A template that may
omit a host would need "host is required" to grow an exception; a speculative field with no
consumer does not earn one. It is deleted in wave 2 under the repository's no-dead-code
rule. If templates are wanted later they arrive as a designed feature with their own
validity rules, not as a dormant boolean. Also specified: an adopted `~/.ssh/config` entry stores
the _alias_, not the resolved `HostName`; direct `host:port` input is normalized into host
plus explicit port; empty host is invalid at the service boundary including imports.

### 3.5 Precedence

`ssh_config.go:60-84` currently ranks "explicit profile option > config file > default" and
cannot tell an explicit value from an inherited one, because the merge never runs. Once
inheritance is real that distinction is load-bearing.

The table below is **closed, not illustrative**: a field not listed is not inheritable and
is rejected in group and global defaults. The implementation plan must fill one row per
field of `SSHProfileOptions` and `Base`, each stating inheritability, the OpenSSH analogue
if any, precedence, validation, unset-versus-explicit-zero semantics, and whether a change
affects endpoint identity, authenticator identity, or only channel behaviour.

```
credentialId    profile > nearest group > … > root > global      (no ssh_config analogue)
host alias      profile only — never inherited, always required
port            profile > group chain > ~/.ssh/config > global > 22
user            effective credential > profile > group chain > ~/.ssh/config > system
key identity    effective credential > profile > group chain > ~/.ssh/config
jump route      profile > group chain > ~/.ssh/config ProxyJump > none
```

Known non-uniformities the plan must resolve rather than assume: `readyTimeout` is not
exactly `ConnectTimeout`; `agentForward` needs a tri-state against `ForwardAgent`;
`jumpHost` currently names a saved profile ID while `ProxyJump` names a route;
`canBeJumpServer` has no OpenSSH analogue; auth mode is not one field but
`PreferredAuthentications` plus `IdentitiesOnly`; `IdentityFile` may appear several times
while our model holds one string.

A group default outranking hand-written `~/.ssh/config` is deliberate — a group default is
an explicit nocx policy ("all production is on 2222") — and is only defensible while
provenance and impact preview exist (§3.3).

**`~/.ssh/config` is evaluated by its own rules.** OpenSSH is first-obtained-wins per
keyword, positional, including wildcard blocks; nocx does not merge parsed `Host` blocks
structurally. It asks the evaluator for the alias's obtained values and overlays only what
the table above outranks. The plan must declare the **supported subset** — `Include`,
wildcards, negated patterns, `Match`, token expansion (`%h`, `%p`, `%r`, `%n`), multiple
`IdentityFile`, `ProxyJump`, `CanonicalizeHostname`, `IdentityAgent`, malformed includes —
because today's resolver reads only `HostName`, `User`, `Port` and one `IdentityFile`
(`ssh_config.go:45`), which is far less than "OpenSSH's own rules" implies. Conformance is
testable against `ssh -G <alias>` as an oracle over a fixture set; `ssh -G` is a test
dependency only, never a production one.

### 3.6 The security boundary we ship on

> Editing a saved profile, its inherited groups, or the applicable `~/.ssh/config` is an
> authorization change, and it takes effect on the next open.

Stated in full, so nothing is implied that is not true:

- profile, group and `~/.ssh/config` mutation **is** authorization;
- renderer mutation through the authenticated RPC surface is **inside** the accepted
  capability, not a defended boundary;
- direct same-user tampering with the profile document is **not** prevented;
- the backend still prevents secret material from crossing to the renderer (ADR-0011 §2),
  which is a different and still-held boundary;
- **this posture must be revisited before any remotely reachable backend.** Computed
  authorization under a local single-user trust model does not automatically carry into
  web or relay operation; that requires its own authority and threat-model decision.

The root problem is a confused deputy, and neither computed authorization nor a grant
stored in the same writable document solves it — a broker with an approval channel outside
the renderer would, which is why it is named below rather than dismissed: `profiles.json`
is plaintext and same-user writable, so whoever can write it can name a credential and a
target and make nocx spend a keychain capability the attacker lacks. Every design that
stores its proof in that same file — PR #59's grant table, or an endpoint fingerprint of
ours — falls to the same write.

Two candidate answers exist and both are **deliberately out of scope**, each needing its
own ADR:

- **Keyed integrity (MAC).** Must authenticate a canonical serialization plus schema
  version, generation and key ID — not raw bytes, which formatting and benign migrations
  would invalidate; needs a counter outside the document to detect rollback to an older
  correctly-signed file; must cover the whole authorization closure (profiles, groups,
  global defaults, credential metadata); collides with ADR-0011:69-71, which makes
  hand-editable documents a **feature**, so it needs an explicit detect → refuse → show the
  diff → review → re-sign path; and its strength must be measured per platform rather than
  inferred from the interface name — on Linux, if a same-user process can read the Secret
  Service entry, it degrades to a corruption detector.
- **A secret-use broker.** Agent-shaped: signs the challenge rather than returning key
  bytes, with "first use of a new endpoint requires confirmation". This is the materially
  stronger primitive and the only one that provides the approval path outside the renderer
  that `profile.go:144` says does not exist.

Shipping a poorly specified MAC would buy false confidence. Stating the boundary is the
honest position for a single-user local-first desktop app.

### 3.7 Pool identity must include the authenticator

**Start from what already works**, because the earlier revisions of this document got it
wrong (§2.9). `poolKeyFor` (`ssh_dial.go:30-58`) keys on resolved host, port, user, an
identity string and the jump route, and that identity string is `cfg.SecretID` — the
opaque reference to the actual secret, reminted on every password change. Password
rotation therefore already invalidates the pool, for the target and for the bastion alike.
No persisted revision counter is needed, and no wholesale fingerprint rewrite either. What
is needed is narrower:

**A requirement, not a bug.** Once a credential has versions (§3.9), the resolver must copy
the **selected version's** `SecretID` onto the config. Do that and the existing mechanism
keeps working unchanged — pooling by the selected version rather than by the mutable
credential aggregate falls out for free, so flipping which version is `candidate` does not
disturb a connection whose selected version has not moved.

Three things the plan must then decide rather than inherit:

- **Public keys**: key identity means the public-key fingerprint or loaded signer identity,
  **not the path**. Today `poolKeyFor` falls back to `cfg.KeyFile` and then to the resolved
  `IdentityFile`, so replacing the file contents at the same path changes what
  authenticates without changing the key. This is the one real gap in §2.9.
- **Agent auth**: the identity is empty by design — `pool.go` defends this as "no second
  principal to isolate" — but the agent's key set can change underneath that assumption.
  Choose explicitly between "agent transports stay reusable until closed", "agent
  connections are not pooled", "pool under an agent-session epoch", or "the broker reports
  the signing key actually used".
- **`AuthAuto` fallback**: the key describes the effective _policy_; the method the server
  selects may differ. The pool entry should record the method and key identity **actually**
  authenticated, and expose it — the rotation UI needs exactly that to say which secret
  version a live transport is running on.
- **Jump route**: introduce a structured recursive route identity and hash its canonical
  serialization. Today's one-hop formatted `jumpRouteKey()` cannot represent an arbitrary
  route, and §2.7 must be fixed first.

And one documentation fix that is not cosmetic: `pool.go:67-73` describes the identity as
the "stored-credential ID" when the code sets it from `SecretID`. That comment is what led
two independent reviewers to report a rotation bug that does not exist. A comment claiming
a **weaker** key than the code implements is worse than none, and it gets corrected in the
same wave.

A changed key prevents new _reuse_; it does not drain existing idle entries. If retirement
must stop existing transports, the pool needs an explicit drain API (§3.8).

### 3.8 Live sessions, revocation, and what "reconnect" means

Ordinary organization never kills a live shell — a profile is a saved launch recipe, and
deleting the recipe need not kill the launched process:

- **edit / move / reparent / delete a profile** — existing channels continue; the pooled
  transport survives until its last channel closes; a later `open(profileId)` re-reads
  current state and fails closed if the profile is gone;
- **delete credential metadata** — existing authenticated channels may continue, but the UI
  warns;
- **retire or revoke a credential version** — the user is asked which they mean: stop future
  connections only, or also disconnect sessions currently authenticated with that version.
  Default to preserving live shells;
- **emergency revoke** — a separate, explicitly destructive action that closes transports
  and channels;
- **host-key trust changed or revoked** — the plan must decide explicitly whether existing
  sessions survive.

"Reconnect" is two different events and must not be conflated. A **WebSocket/browser**
reconnect is not a new SSH authorization event: the backend session survives and the replay
ring reattaches (AD-9). An **SSH transport** reconnect is a new authorization event and uses
current state, never the old session's snapshot.

### 3.9 Credential versions are foundational

Because rotation is a rollout (§6) and not an edit, a credential holds secret **versions**
from the start — `current` and an optional `candidate` — even though the rollout UI and
orchestration arrive last.

This is a scope decision taken deliberately: building the credential service and the pool
fingerprint around a single `SecretID` and adding versions later would rewrite the
credential service, pool identity, orphan collection, `credentials.hasPassword`, and the
export shape a second time.

**A version is not "a credential with a different `SecretID`."** The shape must be typed
per auth method, or the schema comes out password-shaped and breaks on the first key
rotation. A version may carry a password `SecretID`; a keyboard-interactive secret
reference; a private-key public fingerprint **plus** a passphrase `SecretID`; an external
signer identity; or, for agent auth, no secret reference at all.

**A straggler pin is keyed by profile provenance, not by endpoint:**

```
CredentialVersionSelection { CredentialID, ProfileID, HopProfileID?, VersionID }
```

Keying pins by canonical endpoint was the obvious shape and it is wrong for the same
reason endpoint-keyed authorization was: two profiles reaching one endpoint would share a
pin, an `~/.ssh/config` edit would orphan or silently transfer it, and a jump hop would be
ambiguous. The profile still authorizes the endpoint through computed resolution (§3.1);
the pin only selects **which version that profile uses**. Probe evidence is keyed by the
full effective-resolution fingerprint, which is a different key and must not be conflated
with the pin.

Promotion changes the credential's default `current`; individual stragglers keep
profile-scoped pins. Where a group-wide selection lives, if it exists at all, is a plan
input (§9).

## 4. `~/.ssh/config` as a live source

`~/.ssh/config` is already read at connect time (`ssh_config.go:44-59`) and is invisible
everywhere else: a user with fifty aliases opens Connections and sees "No connections yet".

**Decision: a live, read-only source — never a copy.**

- Surfaced in the **quick-connect palette** and in a **terminal hint** when the user types
  `ssh <host>`. Not in the connection manager, which lists only nocx-owned records — so an
  empty manager honestly means "you have not saved any connections".
- **Priority is ours**: when a saved profile targets the same alias, the system duplicate is
  not offered. The plan must define alias identity precisely — a profile named differently
  but pointing at the same alias is the same target and must also suppress the duplicate.
- **"Save as connection"** adopts an external host after connecting, storing the alias.
- A **one-off importer** sits alongside the Tabby one, labelled honestly: it produces a
  detached copy, not a synchronised view.

The terminal hint is a **frontend** feature, and its reach is narrower than it first looks.
AD-6 forbids the backend interpreting the byte stream, so the hint cannot be built there.
On the frontend, nocx knows the command text **only while its own command editor owns the
input** — submission runs through `ShellInputTarget` (`terminal-content.ts:219-245`), so
the text is in hand. In raw terminal mode it does not: OSC 133 marks prompt and command
boundaries but does not expose the shell's current editable line, and reconstructing
`ssh <host>` from keystrokes is unreliable because history, completion, cursor movement and
pasted control sequences are all handled remotely.

So: the hint ships for the nocx command editor. Supporting arbitrary native shell input
needs cooperating shell integration (AD-5 Tier B), not merely OSC 133, and is not promised
here. (Input ownership is **ADR-0004**, not AD-4 — AD-4 is the `x/crypto/ssh` foundation
decision at `architecture.md:125`. An earlier draft of this document confused the two.)

Rejected — a one-off import as the _only_ path: an imported record freezes one evaluation of
a positional config, and later edits to an earlier wildcard block, an `Include`, a `Match`
condition or alias ordering silently do not reach it. Rejected — writing profiles back into
`~/.ssh/config`: first-obtained-wins means an appended `Host foo` may fail to override an
earlier `Host *`, so nocx would have to reorder the user's own blocks, and even a separate
`Include`d file needs its include placed at a position that changes semantics. A bad
serialization breaks `ssh`, `git` and `ansible`, not just nocx.

## 5. Surface

Organizing principle: **nothing hidden, nothing asked twice.** Both reference apps show a
blank Port field while the connection actually goes to 2222 because a group or
`~/.ssh/config` said so. We are building the engine that knows the effective value _and its
origin_; the surface must show it.

- **Settings rail gains three entries: Connections, Credentials, Vault.** The "Saved
  credentials" toolbar button — an entity switch disguised as an action — is deleted. Tabby
  import moves into the existing Export / Backup / Import section. One primary action
  remains in the toolbar.
- **Full-width list, dialog editor.** The screen's job is finding and connecting, which
  happens daily; editing happens rarely and does not deserve half the width permanently.
- **A row reports state, not just address.** Live connected/disconnected, the credential in
  force _and where it came from_, last used. This needs backend data that does not exist
  today — `session.Config` (`session.go:27`) retains host and config, not a profile ID — so
  profile↔session association and last-used persistence must be designed, not assumed.
- **The credential is a link** to its record, which answers "used in 23 connections" via
  effective resolution (§3.1) — the reverse view, computed, never stored.
- **`Test` is a first-class row action, separate from `Connect`**, and it is the same
  primitive fleet rotation needs (§6). It therefore depends on the backend probe, which must
  exist before the row action does.
- **Creation starts from one field**, accepting `deploy@10.0.0.1:2222`, a bare alias, or
  `ssh://…`. `parseQuickConnect` (`profiles.ts:137`) already does this parsing and is
  currently used only by the palette.
- **Every field carries its provenance** — `2222 · from group Prod`, `deploy · from
credential prod-ops`, `22 · from ~/.ssh/config` — and editing flips it to "overridden
  here" with a revert control.
- **Failures speak**, from a typed domain error taxonomy carried resolver → JSON-RPC → UI:
  "credential prod-ops was rejected", "host does not resolve", "the host key changed" — not
  `Internal error`, today's entire answer (`nocx-3isn`).
- **Groups get an interface**, including what a group hands down and the impact preview of
  §3.3.
- **`TextField` composes `Field`**, so the required marker appears everywhere at once and the
  kit stops carrying two label implementations.
- **Accessibility is acceptance criteria, not polish**: dialog-within-form focus handling,
  provenance links reachable by keyboard, destructive impact confirmations announced, focus
  restored on close.

## 6. Rotation is a rollout, not an edit

Forty hosts do not switch atomically, and a design that assumes they do half-works silently.
A credential carries versions (§3.9):

```
Credential "prod-ops"    current: v7    candidate: v8
```

Create a candidate without overwriting the current one; choose a canary selection; run
bounded probes; record per-endpoint status; roll forward in bounded batches; promote on a
declared threshold; keep the old version for explicitly pinned stragglers; retire
deliberately, with the list of remaining dependents.

The canary is an **ephemeral selection**, not a persisted set — if it survived across
rollouts it would be a fleet under another name, and §3.3 chose one tree.

**Probe constraints, none optional:**

- a probe forces a **fresh transport and bypasses the pool**, authenticates, and closes
  without launching a shell or running a command;
- it sends **only the intended candidate method**, never `AuthAuto`'s full chain —
  `MaxAuthTries` is finite and burning it is how a rollout locks the fleet out;
- **never** try several passwords against one host: that is what triggers lockouts and is
  indistinguishable from password spraying;
- verify the host key **before** probing;
- distinguish a network failure from a rejected password; report "locked" only when the
  server actually says so, since generic SSH errors do not carry it;
- keyboard-interactive and MFA cannot be bulk-probed and need their own interaction model;
- bound concurrency **globally and per bastion**, not per target — one dead bastion must be
  reported once, not as forty independent password failures;
- a probe result is keyed by endpoint, host-key identity, credential version, effective
  username and auth policy, plus a timestamp, and is invalidated by any change to the
  endpoint, group chain, `~/.ssh/config`, host key or credential.

**Fallback is policy, never a silent retry.** Legitimate: an endpoint explicitly pinned to
the current version as a known straggler; the user choosing "connect with the previous
version" after a candidate failure; a declared rollback of the whole rollout, shown as a
state transition. Forbidden: a candidate rejection followed by an invisible retry with the
current version during an ordinary connection — it hides rollout state and spends extra
authentication attempts.

Rollout status is operational evidence (private metadata), not profile configuration; its
retention, manual clearing, and import/export behaviour must be specified.

**Promotion, retirement and revocation are three transitions, not one.** Conflating them
would drain transports that are legitimately still running a pinned previous version:

- **promote candidate** — the candidate becomes `current`; the previous version stays
  usable for profiles explicitly pinned to it, and nothing is drained;
- **retire previous** — the version may no longer be selected for new connections, and the
  user chooses whether existing transports on it drain (§3.7, §3.8);
- **emergency revoke** — a separate destructive action that closes everything using the
  version now.

Later, and not now: for fleets already managed by an external secret system, a credential
becomes "username + policy + external secret locator" and nocx reports adoption without
being the rotation authority.

## 7. Work breakdown

Eight deliverables, each named by an outcome that stops being false exactly once. Order
revised after review — the earlier draft had `Test` in the UI wave while its primitive was in
the last wave, and transactional imports after the surface although §3.4 requires every
writer to route through the engine.

**Each wave is self-sufficient**: it lands on `main` leaving the product coherent, with
nothing user-visible half-built and no feature flag hiding an unfinished surface. That is
the constraint that replaces "one long-lived branch", and it is stronger — a flag hides
incomplete work, self-sufficiency means there is nothing to hide. Two consequences worth
stating rather than discovering:

- **Wave 2 is self-sufficient only because there is nothing to inherit yet.** No group UI
  exists and `Defaults` has never been written by anything but the Tabby importer, so
  turning on real inheritance changes no observable behaviour on its own. This is a fact
  about today's data, not a licence: once wave 6 lets users author group defaults, changing
  resolution semantics without shipping their display in the same wave would be a
  regression.
- **The "+ create a credential" affordance belongs to wave 6, not wave 5.** It lives inside
  the connection form, which wave 6 rewrites; putting it in wave 5 would build it twice.
  Wave 5 therefore delivers the Credentials settings section and the single shared form
  component, and wave 6 wires the affordance into the new form.

1. **"Editing a credential no longer loses the password."** Patch-style DTOs preserving
   `SecretID`/`PassphraseSecretID`, create distinguishable from update (`nocx-u5ai`),
   backend-minted IDs, crash-safe secret replacement and orphan recovery, and the
   version-capable credential model of §3.9. `Credential.Host` is **untouched** here (§3.1).
   _Active data loss; must not wait for the redesign._ Plan:
   `.internal/plans/2026-07-29-wave-1-credential-correctness.md`.
2. **"A connection knows what it inherited, and says so."** The effective-profile engine:
   typed sparse presence-aware values, provenance, the resolution identities of §3.4, the
   closed precedence table of §3.5, group cycle/depth validation, the structured jump-route
   model. The TypeScript inheritance copy deleted, `deadcode` clean (`nocx-hhj3`). **This is
   also where `Credential.Host` dies**, in the same commit range that makes computed
   authorization live, because the binding check it feeds cannot be removed before its
   replacement exists (§3.1).
3. **"Every writer goes through one door."** Ordinary CRUD, the group impact workflow of
   §3.3, Tabby import, configuration import, adoption — all routed through the engine, with
   transactional semantics and a declared collision policy.
4. **"SSH does what the profile says."** Multi-hop routes (§2.7), the four ignored options
   (`nocx-49x`), the narrowed pool work of §3.7 — public-key identity by fingerprint rather
   than path, an explicit agent-auth decision, the structured jump route, and the corrected
   `pool.go` comment — plus the forced-fresh probe primitive, transport drain and revocation.
5. **"A credential stops being a side door."** The existing epic `nocx-0w2f` — its own
   settings section and one shared credential form component, replacing the "Saved
   credentials" toolbar button. Reused, not re-created. The "+" affordance is wave 6's.
6. **"The connection list answers questions."** The surface of §5, including groups and
   their impact preview, `Test` as a row action, provenance display, the "+ create a
   credential" affordance in the new connection form, and the error taxonomy.
7. **"Hosts from `~/.ssh/config` appear where they are looked for."** §4, plus the adoption
   flow.
8. **"Changing a fleet password is a rollout, not a hope."** §6 — orchestration and UI over
   the primitives already built in waves 1 and 4.

**Integration discipline.** A wave merges to `main` as soon as it is done and green. That
is possible only because of the self-sufficiency rule above, and it is what pays for it: a
long-lived branch rewriting `profile`, `connection`, `ssh`, `transport`, imports and a
916-line UI component would meet a `main` that had moved underneath it, and a single
5,000-line review event cannot distinguish merge damage from feature defects.

Feature flags were considered and are **not** used. A flag hides unfinished work behind a
switch someone must later remember to remove; the self-sufficiency rule removes the need
by making each wave complete on its own terms. Where a wave genuinely cannot avoid landing
an incomplete surface, that is a signal the wave is cut wrong and should be re-cut, not
flagged.

Each wave therefore: ends green under the full gate; is reviewed on its own; keeps its
internal history (no squash) so `git bisect` still works; and takes the merge slot
(`bd merge-slot`) for the whole merge-and-resolve. An integration test suite exists before
wave 3, when the first wave lands that changes behaviour a user can see.

Each wave carries a `discovered-from` edge to `nocx-52cd` and references this document; the
document, not a label, is what says these are one feature.

## 8. Deliberately out

- **The vault-versus-keychain decision** (`nocx-25k9`, `nocx-25k9.1`). The shipped binary
  wires the OS keychain and ~371 lines of AES-GCM vault are reachable only from their own
  tests. Its own ADR and its own epic, and it must be answered before any vault UI.
- **Trust: MAC or broker** (§3.6). Its own ADR.
- **Stream semantics and redaction.** `nocx-23v`'s strong form — a vault-injected secret
  never leaves the machine unmasked — is impossible in general once plaintext reaches an
  arbitrary process, and its partial forms collide with AD-6. Redaction is really three
  products: display masking (frontend); protected injection, where the secret is delivered
  by descriptor or helper and never enters the stream at all; and producer-declared
  sensitive regions from a cooperating helper. Only the first is frontend-shaped, and
  §2.10 shows why frontend-only masking cannot be called a guarantee. Separately,
  `architecture.md:74` leaves a session with no attached client unable to answer "what is
  the cwd", which a backend shadow VT would not fix — a VT model knows what was displayed,
  and cwd is known only because the shell emits OSC 7. The answer is a semantic sideband
  from a trusted producer, with a narrowly enumerated OSC extractor as compatibility
  fallback. AD-6 and AD-1 both need amending in the document rather than routing around.
- **Replay confidentiality in a web/relay deployment.** AD-9 defines retention mechanics and
  says nothing about confidentiality or lifecycle; "auth token + bind-to-localhost"
  (`architecture.md:111`) is not sufficient for a remote deployment.
- **A second hierarchy** (folders for navigation, sets for policy). One tree with visible
  provenance and impact preview is the answer for now.

## 9. Inputs the implementation plan must resolve

Raised by adversarial review and not answered here, in rough order of how early they bite:

1. The typed sparse defaults schema, replacing `map[string]any` (§3.3) — blocks wave 2.
2. One precedence row per field, with the non-uniformities of §3.5 resolved.
3. The declared `~/.ssh/config` supported subset, and the `ssh -G` conformance fixture set.
4. Credential deletion under inheritance: direct references are greppable, inherited ones
   need effective resolution.
   4b. Whether a group-wide credential-version selection exists at all, or whether stragglers
   are only ever pinned per profile (§3.9).
5. Optimistic concurrency: what happens when two dialogs edit the same profile, group or
   credential.
6. The provenance wire contract — stable source kinds and identifiers that never leak a
   secret reference.
7. The effective-profile failure model: missing group, invalid default type, missing
   credential, unreadable SSH config, unsupported directive, circular jump.
8. Import collision policy for IDs, names, aliases, groups and credential mappings, with
   rollback; and the review step for an imported profile naming an existing local
   credential.
9. Export schema migration: removing `Host`/`Port`, adding typed defaults and versions.
10. Legacy credential decode: existing records carry required `Host`/`Port` that must be
    discarded or migrated deliberately.
11. Global defaults: storage, schema, UI and provenance source are undefined today.
12. The pool drain API, and `~/.ssh/config` cache/watch behaviour (latency, parse errors,
    atomic reads, palette refresh).
13. Profile↔session association and last-used persistence (§5).
14. Secret-update recovery: ADR-0011 calls for an identifier journal that the current code
    does not implement.
15. Scale targets: forty hosts is the motivating case — declare the intended sizes for
    effective resolution, group-impact calculation, palette enumeration and rotation probes.

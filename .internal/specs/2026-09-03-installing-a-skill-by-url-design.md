# Installing a skill by its URL

Bead: `nocx-qja4m`. Extends
[`2026-08-31-assistant-skills-design.md`](2026-08-31-assistant-skills-design.md),
whose §14 deferred this in one clause — "Installing a skill from a hub or a URL,
and therefore hermes's trust tiers for external sources — **we have no external
source**". The tiers were deferred _with_ the source, not instead of it. This
spec supplies the source and asks what the tiers were meant to buy.

## 1. What a person gets

Somebody publishes a skill. The person pastes its URL into the Skills page, reads
the name, the description, the body and whatever the scan matched in it, approves,
and the skill is in their list. The next ask in any pane indexes it, reads it, and
answers from it. The row says where it came from and can say so again later,
because the source is recorded rather than remembered.

## 2. What this deliberately is not

**The assistant may not install.** hermes has no agent-callable install tool at
all — its three skill tools are `skills_list`, `skill_view` and `skill_manage`,
and reaching an install means shelling out through `terminal`. That is not an
oversight to improve on; it is the same conclusion §6 of the skills spec reaches
from the other end. A model that read a URL in a tool result and proposed
installing it would be laundering untrusted bytes into an instruction through a
door the person holds open — which is precisely the taint path layers 2 and 3
exist to cut. **A person installs; the assistant may not.** No `skills.install`
tool declaration, no entry in `internal/agenttools/registry.go`.

That refusal is also what keeps [ADR-0053](../../docs/decisions/0053-a-tool-declares-the-classes-it-can-reach.md)
intact. `session.run` is already the one declaration that is "a door, not an
action", and the ADR's whole argument is what that costs. An install tool would
be the second — its effect class depends entirely on what is behind the URL — and
the answer to a second door is not a better classifier, it is not building it.

**No hub, no index, no search, no marketplace.** hermes has nine source adapters;
we have one source kind, and it is a URL a person typed. Everything an index buys
— discovery, ranking, a trusted-repo list — is a decision about whose bytes to
prefer, and we have nobody to prefer yet.

**No archive, no clone, no support files.** `internal/skill` writes exactly one
`SKILL.md` (`write.go` `Create`/`Update`) and removes exactly one (`Delete`); the
only code in the product that writes a skill's whole directory is backup restore,
and it is deliberately not exposed as an operation. Installing one file keeps the
whole existing write path and every containment check on it. This is the same
line the skills spec already drew in §14 for `references/`.

hermes fetches `SKILL.md` **plus** the support files it links, and the reasoning
for the bound is worth keeping even though we are not taking the feature —
`tools/skills_hub.py:1605`: _"Bare URLs cannot safely enumerate a repository, so
only exact references below references/templates/scripts/assets are fetched.
Other repository files are never copied."_ If we ever fetch a second file, that
sentence is the shape it takes: named by the first file, allowlisted directory,
same origin.

**No signature verification.** hermes does not verify NVIDIA's `skill.oms.sig`
either; the signature is the _reason_ a repo is on a hardcoded trust list, not a
check that runs. A tier earned by a constant in a source file is not a boundary
and must not be built as if it were one.

## 3. Where the bytes land, and why it is a fourth root

Provenance is **structural** — `internal/skill/skill.go:1-12`, verbatim:

> PROVENANCE IS THE ROOT, never a field in the file (spec §6 layer 1). Content
> cannot forge which directory it sits in; a `provenance:` key in frontmatter
> could be written by anything able to write the file, so it is deliberately not
> read.

A downloaded skill therefore cannot be `authored` — that means "what the person
wrote or placed by hand" — and cannot be `managed` — "what the assistant drafted
and the person approved", which is also the only root any tool writes to. Either
reuse would hand it trust it did not earn, and would break the branches that read
provenance for something else: `refuseForeignCollision` (`write.go:294-305`),
`Approve` refusing non-managed (`store_doc.go:333`), `Remove` refusing builtin
(`store_doc.go:368`).

So: **`ProvenanceInstalled = "installed"`**, root `<ConfigDir>/installed-skills`,
appended **last** in the slice at `internal/app/app.go:559-563`.

Last is the whole of the precedence decision, because precedence _is_ slice order
— `discoverDetailed`'s `seen` map is the entire collision rule
(`discover.go:133-136`). Nothing a person wrote and nothing we shipped may be
shadowed by something downloaded.

**But losing silently is not acceptable here, and today it is silent.** The
collision arm is a bare `continue`. For discovery that is defensible; for an
install it is not — a person who installs a skill and then cannot find it in the
list has been told nothing. **The install refuses the name instead**, naming the
skill that already holds it and its provenance, which is the same shape
`refuseForeignCollision` already uses for the assistant's writes. hermes calls
this a shadow refusal and reaches it from the same argument.

The four provenances and what each may do, complete, so no reader has to derive it:

| provenance  | root                           | who writes it              | scanned on read        | deletable | approvable            |
| ----------- | ------------------------------ | -------------------------- | ---------------------- | --------- | --------------------- |
| `authored`  | `<ConfigDir>/skills`           | the person, by hand        | yes                    | yes       | n/a — never `changed` |
| `builtin`   | the binary                     | us                         | **no** — our own bytes | no        | n/a                   |
| `managed`   | `<ConfigDir>/managed-skills`   | the assistant, on approval | yes                    | yes       | yes                   |
| `installed` | `<ConfigDir>/installed-skills` | **this spec**, on approval | yes                    | yes       | yes                   |

`installed` behaves like `managed` everywhere the existing branches ask a
question, with one exception in §6. That is deliberate: the two share the property
that decides every one of those branches — the bytes were not written by the
person, and the person approved a specific version of them.

## 4. What is fetched

One `GET`, one document, through the seam the product already has.

`internal/apifetch` is the person-initiated fetch, reached today by
`api.import.postman` with a `url` param from the import dialog. It runs over
`internal/httppolicy`, which owns "the http:// address rule and the credential
boundary, for every HTTP client in nocx" — extracted precisely so a second caller
would not re-derive it. We are that second caller and we re-derive nothing.

What we inherit, stated so it is not mistaken for something we chose: `https` is
**unrestricted**; `http` is permitted only where every resolved address is
loopback or private, checked at connection time with the validated IPs threaded
into the dial so a rebind cannot slip between check and use
(`httppolicy/policy.go:114-123`, `:165-199`); redirects are bounded at ten, and
credentials are stripped on an origin change, with the hop refused outright if a
secret was in the URL. There is **no SSRF refusal for https and no allowlist**,
and that is the existing rule for every outbound request in the product. For a
URL a person typed it is also the right rule: the person chose the address, and a
denylist here would refuse their own machine while permitting the whole internet.

Bounds this adds, none of them inherited:

- **64 KiB**, the ceiling `internal/skill/write.go:396-410` already enforces. A
  larger response is refused before it is parsed, not truncated — a truncated
  skill is a skill whose instructions end in the middle of a sentence.
- **One redirect chain, no second request.** The document is whatever the URL
  returns. Nothing in it causes another fetch.
- **A `Content-Type` is not consulted.** The bytes are parsed as frontmatter plus
  body by the same parser discovery uses, and a document that does not parse is
  refused with what was wrong. Trusting a header to say what a file is, when the
  file itself answers definitively, is a second derivation.

The name comes from the frontmatter, not from the URL's last path segment, and is
validated by the same `^[a-z0-9][a-z0-9-]{0,63}$` discovery applies
(`discover.go:23`). A URL cannot name a skill; only a skill can.

## 5. The pipeline

hermes's order is quarantine → scan → policy → confirm → install, and the order
is the argument: nothing is decided about bytes that are not yet somewhere they
can be examined, and nothing is installed that a person has not seen. Ours is the
same order with one fewer step, because we have no tier matrix to consult:

1. **Fetch** into memory. There is no quarantine directory: hermes needs one
   because it ingests a tree and shells out to an external scanner; we hold one
   document under 64 KiB and our scanner is a function.
2. **Parse and validate** — frontmatter present and closed, `name` canonical,
   `description` non-empty, body non-empty, total under the ceiling. Each refusal
   names what was wrong and does not proceed.
3. **Refuse a shadowed name** (§3).
4. **Scan** — `skill.Scan`, the same eleven patterns, the same advisory contract:
   findings are evidence, never a refusal. `scan.go:55-57` is explicit that
   callers "surface findings but do not turn them into an unreadable result".
5. **Ask the person**, showing name, description, the whole body, the source URL,
   and every finding with its pattern, line and line number. Not the first
   finding — **every** finding. The existing write path attaches only the first
   because a tool result is bounded at 8 KiB; a dialog is not, and a person
   deciding whether to adopt instructions should see all of the evidence.
6. **Write** through the existing atomic path — tmp, fsync, rename, fsync-dir —
   into the installed root, with the containment checks `write.go` already
   performs, and **record the digest and the source** in the same write.

The refusal at every step is the person's to read, in their words, and says which
step refused. A silent degrade that leaves them looking at an unchanged list is
the failure mode AGENTS.md names.

## 6. What is recorded, and what an update may do

`skills.json` today holds exactly `schemaVersion`, `disabled` and `digests`
(`store_doc.go:24-28`). There is nowhere to say where a skill came from, so
`storage.Module{Name: "skills"}` goes to `Current: 2` and gains:

```json
"sources": { "<name>": { "url": "https://…", "installedAt": "<RFC3339>" } }
```

Keyed by name, like `digests`, and read with the same strictness the document
already applies — a non-canonical name or an unparseable URL fails the whole list
rather than degrading, exactly as a bad digest does today (`store_doc.go:137-158`).
An entry exists only for an `installed` skill; the reader never infers provenance
from its presence, because provenance is the root and stays the root.

**Integrity is already solved and must not be solved again.** `hashSkillDirectory`
(`discover.go:270-313`) hashes the whole skill directory; the digest is recorded
at approval, compared at discovery, and fail-closed — no recorded digest means
`changed`. A `changed` skill is dropped from the prompt index entirely
(`write.go:104-121`) and, if read anyway, is prefixed and framed untrusted
(`execute.go:547-551`). This is the load-time check **hermes does not have**: its
tiers are an install-time decision and nothing verifies an installed skill against
its lock afterwards. We keep ours and extend `statusFor` (`skill.go:40-45`) so
`installed` is hashed and compared like `managed`.

**An update re-runs the whole pipeline against the recorded URL, and may not
change where a skill came from.** hermes states both halves and we take both:
_"An update must never change a skill's provenance"_, and the cross-registry
fallback was deleted as _"unsafe by construction"_ because skill names are not
namespaced across sources, so a same-named skill elsewhere could silently
reassign provenance. Concretely: an update is `install` pinned to
`sources[name].url`; it cannot be pointed somewhere else; and if the on-disk
digest no longer matches the recorded one — the person edited the file — the
update **refuses and says so**, rather than overwriting work it did not author.
Re-installing over that is a separate, explicit answer, never the rerun default.

## 7. How the four trust layers apply

§6 of the skills spec earns trust in four layers. An installed skill changes what
two of them mean, and the spec should say so rather than let a reader assume the
old sentences still hold.

**Layer 1, provenance** — unchanged in mechanism, extended by one value. §3.

**Layer 2, an untainted drafting input** — **does not apply, and there is no
substitute.** The layer exists because a managed body is drafted by a model from
the person's own questions and the assistant's own prose, never from tool results.
An installed body was written by a stranger; there is no drafting step to keep
clean. This is the layer an install genuinely loses, and naming it is the point:
layers 3 and 4 were written as a backstop for layer 2's residue, and for an
install they are the whole defence rather than a backstop.

**Layer 3, the static scan** — applies at both boundaries exactly as before, and
is now doing more work than it was designed for. Eleven regexes are not a
sanitiser and this spec does not claim they are.

**Layer 4, the classifier and the person's approval** — the classifier judges a
_proposed tool call_, and there is no tool call here. What remains, and what
carries the weight, is the person approving exact bytes with the findings beside
them. Which is why §5 step 5 shows every finding rather than the first, and why
`nocx-swn1m` — the finding that reaches the renderer and is never drawn — blocks
this work rather than sitting beside it.

**What is deliberately not claimed.** A person who approves a plausible-looking
body has adopted it, exactly as if they had typed it into their own instructions.
That is the same boundary the skills spec already draws, and an install does not
move it — it makes it load-bearing. hermes says the same thing in `SECURITY.md`
and it is worth having in our words too: the scan is a review aid; the boundary
is the person reading what they install.

## 8. Wire contracts

Two new methods, following `contracts/README.md` and the four `skills.*` schemas
already there. Both are declared once as JSON Schema with
`additionalProperties: false` and an explicit `required`, the renderer's types are
generated, and the Go side is validated.

- **`skills.preview`** — params `{url}`; result `{name, description, body, url,
findings[]}`, or a refusal. It fetches, parses, scans and **writes nothing**.
  Separating preview from install is what lets the person read before deciding
  rather than approve a dialog describing bytes it has not shown them.
- **`skills.install`** — params `{url}`; result `{name, provenance: "installed"}`.
  It re-runs the fetch rather than trusting a body the renderer hands back: a
  round trip through the client is a place for the bytes to change, and the
  digest recorded must be over what was written.

That re-fetch is a deliberate cost. The alternative — install what preview
returned — is one request cheaper and makes the person's approval refer to bytes
the server never verified. If the second fetch returns something different, the
install refuses and says the document changed.

**Each contract lands in the same commit as its handler and its client method**, and
this is a constraint rather than a preference. A generated type with no consumer is
a new entry in `check-dead-exports`' baseline, and that baseline may only SHRINK —
its updater refuses to write one that grows — so "declare the contracts, wire them
next" has no committable state at all. The rule is general and was bought twice in
one wave (`nocx-0c7qz`): a contract lands with its consumer, a package with its
caller, an API change with its call sites. A task whose output has no consumer
inside the task has the wrong boundary, and the gate is the hook rather than the
brief, so a worker cannot be briefed out of it.

Registration follows `ws_skill_handlers.go`: a method on `skillSettingsSource`, a
`case`, a `validate…Raw`, a `regResponder` in `configSpecs`, and a regenerated
`openrpc.json`. The transport registration test is the authority for the method
set.

## 9. The surface

On the Skills page, above the list, beside the enable switch. The precedent to
copy is `PostmanImportDialog` (`frontend/src/api/import-dialogs.tsx`), the one
place in the product where a person pastes a URL and the backend fetches it, and
three of its decisions transfer directly:

- **The dialog does not classify its own input.** `classifyPastedSource` owns "is
  this a URL", and its comment says why: a dialog deriving that a second time
  "would be the second derivation of 'is this a URL', which is the `ssh`-without-
  a-space defect in another costume".
- **One source is held, visibly, and can be taken back.**
- **Refusals stay in the dialog**, in the backend's own sentence, in the kit's
  validation slot.

The confirmation is not `showConfirm`. A skill body does not fit in a confirm's
sentence, and the person is being asked to adopt instructions — the closest
existing shape is the backup restore's preview-then-confirm
(`frontend/src/backup-restore-section.tsx:181-188`), where what will happen is
spelled out before the button. Here the preview _is_ the body, in a `CodeBlock`,
with the findings above it.

Every element comes from `frontend/src/ui/`. The row for an installed skill is the
`RecordRow` the list already uses, with `installed` as its kind badge and the
source URL available on the row.

## 10. Tests

Per AGENTS.md rule 2, the epic's happy path is watched end to end: a Playwright
spec against a fake local endpoint serving a `SKILL.md`, where a person opens
Settings, pastes the URL, reads the preview, approves, sees the row, and a later
ask in another pane answers from its content. No `waitForTimeout`.

Per rule 3, every external call has a test where it fails, and the partial
failures are enumerated because this procedure touches three stores: the fetch
fails; the fetch returns a document that does not parse; the scan matches; the
name is shadowed; the write succeeds and the digest record fails — what is on
disk, what is in `skills.json`, and what the next discovery pass reports. The
invariant is stated as an interval: **from the moment bytes are written, the
skill is either recorded with its digest and source or absent from disk** —
there is no state in which an installed skill exists unrecorded, because an
unrecorded skill is `changed` and silently unusable.

Per rule 5, the wire is a party: `…_OverTheWireConformsToContract` for both
methods, off the real socket.

Per rule 4, the acceptance criteria in each child bead are written as assertions,
not prose.

## 11. Deliberately out

- An agent-callable install (§2), a hub or index (§2), archives, clones and
  support files (§2), signature verification (§2).
- Sharing installed skills between machines beyond what backup already carries.
  Backup snapshots `authored` and `managed` today (`backup.go:15-33`); whether
  `installed` joins them is a real question — a restore that reinstates a
  stranger's skill on a new machine is a different act from restoring the
  person's own — and it is decided in its own bead, not smuggled in here.
- Notifying the person that an installed skill has an update upstream. The
  recorded URL makes it possible; nothing in this spec does it.
- Project-level and ecosystem roots, still, from §14.

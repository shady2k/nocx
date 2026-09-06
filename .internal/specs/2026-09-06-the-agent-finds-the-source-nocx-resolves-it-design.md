# The agent finds the source, nocx resolves it, the person approves the plan

**Status: proposal. The shape was decided with the owner on 2026-09-06; the
mechanics below are this document's.**

Follows the correction made to
[`2026-09-04-skills-under-the-same-policy-design.md`](2026-09-04-skills-under-the-same-policy-design.md)
§5 on 2026-09-06 (`nocx-y30kd`), which restored the machine that section had
deleted by citing capabilities the assistant does not have. This document
specifies that machine. It changes nothing §5 decided about WHO acquires —
acquisition stays conversational, Settings keeps management and has no field an
address can be typed into.

## 0. What crosses, and what it already decided

- **[ADR-0053](../../docs/decisions/0053-a-tool-declares-the-classes-it-can-reach.md)**
  — a declaration states the classes a tool can reach, and a tool whose act
  depends on its arguments is a door. `skills.resolve` is **not** a door: it
  reads a public forge and writes nothing, so its class is fixed and it
  declares `EffectCrossBoundary`, exactly as `fetch.url` does. Nothing here
  needs the door mechanism.
- **AD-8, one owner per behaviour.** Three behaviours already have owners and
  this document adds none of them a second time: what may be fetched belongs to
  `internal/httppolicy`; what belongs to a skill belongs to `internal/skill/bundle.go`;
  and **"nothing is installed that has not been read" belongs to the Store's
  one preview slot** (`internal/skill/preview.go`, `approvedPreview`). §4 is
  built on that third one rather than beside it.
- **AD-1** — this is JSON-RPC control plane only; no byte stream is involved.
- **The 09-04 spec §8** — a skill lands **disabled**, and the person turns it
  on after looking. Unchanged, and it is half of why §7 below can say what it
  says about authenticity.
- **`internal/skill/bundle.go`** decided one hop, same effective origin on every
  redirect, text only, a 404 fails the install, `references/` and `scripts/`
  only. All of it survives: this document changes where the FIRST address comes
  from, and nothing about what a skill is made of once that address is known.

## 1. The failure, measured

A person has `https://www.agentmail.to/docs/integrations/skills` — a
documentation page, which is what a person actually has. Two runs of the same
local model, 2026-09-06:

- **Run 1.** Called `skills.install` → refused (HTML, over 64 KiB) → fetched the
  page with `fetch.url` → read `npx skills add agentmail-to/agentmail-skills`
  off it → offered to run that. The approval dialog stopped it.
- **Run 2.** Did not call `skills.install` at all.

Neither installed anything. Both obeyed us.

Behind the page is a repository with **nine** skills. The one the person wanted
is at `agentmail/SKILL.md` and links seven `references/*.md` files. Today the
best reachable outcome is that the person is asked to go and find
`raw.githubusercontent.com/agentmail-to/agentmail-skills/main/agentmail/SKILL.md`
by hand — and they are never told the other eight exist.

**What a person can do when this is done, and cannot do now:** give the
assistant any link that leads to skills — a docs page, a repository, a file in
one — and end up choosing from what is actually in that repository, with the
one they chose installed from a pinned commit.

## 2. The shape

> the agent finds the SOURCE → **nocx mechanically** turns it into an immutable
> install plan → the person approves the plan

The two halves each do what they are good at, and neither does the other's job.
A model is good at reading a marketing page and noticing that it names
`agentmail-to/agentmail-skills`; it is bad at being reproducible, and it must
never be the thing that carries bytes. Enumerating a repository and pinning a
ref to a commit is mechanical, boring, and exactly what an address alone cannot
do.

**The unit of acquisition is a repository. The unit of installation is a skill,
or an explicitly chosen set of them.**

**And the rule that makes walking a page safe: from a page you may take a FACT
and never a COMMAND.** "The skill lives in repository X" is evidence, and nocx
goes and checks it. "Run `npx skills add …`" is an instruction, and it is never
executed. That is precisely what run 1 walked into, and it is the same
distinction `bundle.go` already draws when it lets `SKILL.md` name its own
support files and refuses to let a support file name more.

## 3. What the person sees

```
Person:     поставь скилл https://www.agentmail.to/docs/integrations/skills

Assistant:  (fetch.url on the page)
            The page says the skills live in agentmail-to/agentmail-skills.
            (skills.resolve — from here nocx works, not the model:
             canonicalizes the repository, resolves main to a commit,
             enumerates what is in it)

            That repository holds nine skills:
              agentmail              — TypeScript/Python SDK: inboxes, threads, webhooks
              agentmail-cli          — the same from a shell, through the official CLI
              agentmail-send-email   — sending and replying over MCP
              agentmail-check-email  — reading and parsing inboxes over MCP
              …five more
            Which one, or all of them?

Person:     the SDK one

            (approval window)
```

If the person was unambiguous to begin with — "install agentmail-cli from
agentmail-to/agentmail-skills" — nothing is asked and the approval window comes
straight up. The question in the middle is the assistant having something real
to ask about, not a step in a wizard.

**The approval window names the whole route**, because that is the one moment
the person can judge it:

- where this started — `www.agentmail.to/docs/integrations/skills`;
- where it ended up — `github.com/agentmail-to/agentmail-skills`;
- **that those are different origins**, stated, not left to be noticed;
- the ref asked for, and the commit it resolved to;
- the path inside the repository;
- the name and the description, description prominent (09-04 §4);
- every file that will land, with its bytes and its scan findings.

The last three lines are what the window already shows (`ApprovalInstall`,
`internal/assistant/skillinstall.go`). The first five are what this document
adds to it.

## 4. `skills.resolve`, and why it needs no new trust mechanism

A new **read-only** agent tool. Given any address that leads to a forge, it
answers with:

- the **canonical repository** — `github.com/agentmail-to/agentmail-skills`,
  however the caller spelled it;
- the **ref** that was asked for (explicit, or the repository's default), kept
  because it is the update channel;
- the **resolved commit**, kept because it is the immutable fact;
- the **candidates**: for each, the path, the name and the description from its
  own frontmatter;
- a **short-lived resolution handle**.

`skills.install` then accepts **either** an address, as today, **or** a handle
plus the explicit paths chosen from that resolution. The backend fetches those
paths at that commit. **The model never carries the bytes and never carries the
address they came from.**

### The handle is the existing mechanism, widened

The Store already holds one slot recording what it last showed a person, and
`Install` refuses anything that does not match it: _"nothing has been read from
that address in this session, so there is nothing to install"_
(`internal/skill/install.go`). That slot is the ancestor of this handle, and the
handle must be **the same slot generalized from one document to one
resolution** — not a second thing that answers the same question. Its
properties come with it, and they are the right ones:

- one slot, so there is never a question about which resolution an install
  refers to;
- installing **spends** it (`forgetPreview`), because an approval is for one
  occasion and not a standing permit;
- resolving again forgets the previous one, and the refusal that produces is
  recoverable by resolving again.

The digest comparison at install is unchanged and still does its job: the bytes
written are the bytes shown.

### What resolve may reach

Public **GitHub** and **GitLab** in the first version. Two mechanical steps per
forge — ref → commit, and commit → the paths in the tree — plus a bounded
prefix read of each candidate's frontmatter for its name and description.

- Everything goes through `internal/apifetch` and therefore
  `internal/httppolicy`, like every other HTTP client in nocx. Nothing here
  constructs an `http.Client`.
- The candidate count and the per-candidate read are **bounded**, and the bound
  is a refusal naming itself, not a silent truncation — a repository with four
  hundred skills is a real thing and "we showed you some of them" is the failure
  mode this whole epic exists to avoid.
- An address on no adapter's forge is **not** an error: it is "I cannot
  enumerate that", and the caller falls back to `skills.install` with the plain
  address, which is today's path and stays exactly as it is.
- Unauthenticated forge APIs are rate-limited. When the limit is what refused,
  the refusal says so in those words, because "try again in an hour" and "that
  repository does not exist" are opposite instructions to a person.

### What resolve deliberately does not do

**Private repositories are deferred, explicitly.** Somebody else's git
credentials is a design about secrets — where they are held, what a resolution
may spend, what an approval window must say about spending one — and it is not
another shape of address. Naming it here rather than leaving it to be inferred
is the point.

**Search is not part of this.** "Install the agentmail skill", with no link at
all, is a different problem and a later one. With a link of any kind, the
resolver covers the measured failure.

**Arbitrary ZIP addresses stay refused.** An archive makes sense as a known
adapter's own transport — a commit archive — where package identity, layout and
update semantics come from the adapter. It is not a second way to name a source.
09-04 §5 is right about this and nothing here disturbs it.

## 5. What is written down afterwards

Both the **ref** and the **commit**. The ref is what an update would follow; the
commit is what was actually installed and the only one of the two that means
anything six months later. Storing only the ref would record a moving target as
though it were a fact.

The stored source keeps the address the person started from as well. It is the
only record that a transition happened at all, and a person auditing an
installed skill a month later is entitled to see that it came from a page they
recognise by way of a repository they may not.

## 6. The happy path, and how it is watched

Per AGENTS.md rule 2, this epic proves its one sentence end to end, and the
check is written now rather than at the end:

> Given the AgentMail documentation page, the person ends with the `agentmail`
> skill installed, disabled, from a pinned commit, having been shown that nine
> skills existed and having chosen one.

Driven headless through `cmd/nocx-server` against a **fake forge** served from
the test — a page, a repository with nine skills, an API answering the ref and
the tree. It asserts, in one run: nine candidates offered; one chosen; the
approval carrying both origins and the resolved commit; the bytes on disk
matching the bundle that was shown; `enabled` false.

The fake forge is not a compromise for the sake of hermeticity. A test that
reaches the real GitHub would be measuring GitHub's availability and rate limit,
and per AGENTS.md a test may not depend on timing or on somebody else's uptime.

## 7. What this does not buy, stated plainly

**Nobody proves the repository is official.** A substituted page leads
mechanically to a hostile repository exactly as well as a real page leads to a
real one, and a resolver is not evidence of anything except that the resolution
was done consistently. Authenticity would need same-origin `/.well-known/`, a
registry assertion, or a publisher signature, and this document proposes none of
them.

What is actually load-bearing is elsewhere, and all of it already exists or is
decided:

- the source and the origin transition are shown, prominently, at the one moment
  a decision is made;
- the person chose a specific skill explicitly;
- the skill lands **disabled** and the person turns it on after looking (09-04 §8);
- changing the bytes on disk turns it off again;
- being enabled grants nothing — every action a skill leads to is judged by
  policy like any other (09-04 §3).

Our digest remains TOFU: good change detection after the first install, no
evidence at it. Saying so is part of the deliverable; a window that implies more
than that is worse than one that implies nothing.

## 8. A second place the wrong belief lives

`frontend/src/skills-section.tsx` carries the same false sentence 09-04 §5 did —
"the assistant searches, follows a page to a repository and calls
`skills.install`". The conclusion it supports is correct and stays: there is
deliberately no field on that page an address can be typed into. The premise is
not, and it is corrected in the same commit as this document, because a claim
about a capability we do not have is how the machine got deleted the first time.

## 9. Provenance

The shape in §2 was proposed by codex, agreed with the owner on 2026-09-06, and
written up in `.internal/HANDOFF-2026-09-06-skills.md` §4 step 3. The measured
failure in §1 is from that session. The reading of `preview.go` and `install.go`
that makes §4 a widening rather than an addition is this document's own.

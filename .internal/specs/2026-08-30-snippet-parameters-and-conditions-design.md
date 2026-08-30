# Snippet parameters and conditions — a body that asks, and a body that varies

Revises `.internal/specs/2026-08-13-snippets-design.md` (the snippets spec). Read §7, §8
and §11.1 of that document first: this one supersedes part of §7 deliberately and is bound
by §8 and §11.1 unchanged.

## 1. What a user can do that they cannot today

Save one prompt whose **choices** and whose **paragraphs** both vary, and fire it filled in.

The case that drove it, from the owner, verbatim in shape: an orchestrator prompt sent to a
coding agent several times a day. The worker it names changes between `claude`, `omp` and
`codex`. Whole sentences are kept or dropped — "if tasks can run in parallel, do that" is
in some days and out others. Today both edits are made by hand in the body before every
fire, which is the same failure the snippets feature was built to remove, one level up.

Today a body can ask a free-text question (`{{ask:name=default}}`) and nothing else. It
cannot offer a choice, and it cannot vary its own text.

## 2. Deliberately out

- **`else`.** `{% if not x %}` as a second block covers it, at the cost of repeating the
  name. A third tag to save one repetition is not worth a third tag.
- **Nesting.** An `{% if %}` inside an open block is a structural error, reported and
  refused. The reason is not the parser — a depth counter is trivial — it is that a flat
  body keeps the settings preview a flat legend and the form a flat list. Supporting it
  later is additive and breaks nothing written before.
- **Loops and filters.** No stated use.
- **Branching on a value** (`worker == codex`). The owner's case is boolean. When it
  arrives it is a new comparison form inside `{% if %}`, and no condition written under
  this spec changes.
- **Escaping `|` inside a default.** See §4: the limitation is named, not solved.
- **Migration of stored `{{ask:…}}` bodies.** Greenfield (AGENTS.md, "Clean-only"). They
  become literal text; the seeds are rewritten in the same commit.

## 3. What this supersedes, and what it preserves

**Superseded — §7.1 and §7.2 of the snippets spec.**

§7.1 argued that a snippet placeholder written as a bare `{{cwd}}` would put a second owner
on one token shape, colliding with the vault's `{{secret:NAME}}`. That argument is retired,
and the replacement rule is stronger because it is decidable by looking at one character:

> **A `{{…}}` containing a colon is a namespaced directive; the namespace must be
> registered or the span stays literal text. A `{{…}}` with no colon is a parameter.**

There is no ambiguity to resolve at runtime and no second derivation of "whose token is
this" — the two owners are separated by the colon, not by a lookup that could disagree.
`reference-namespaces.ts` loses `ask` and keeps `secret` (vault) and `env` (snippets); the
disjoint-union test that asserts the registry's contents changes with it.

The model is the one SQL editors use: an unknown variable is a question, not an error.

**Preserved verbatim — §8 and §11.1.**

§8: one resolution, in the renderer, at fire time, reading the ACTIVE pane's facts at the
call. Conditions and parameters resolve in that same pass; nothing moves to the backend.

§11.1: a `{{secret:…}}` leaves snippet resolution **intact** and is handled by the
destination policy. No parser written here may consume, rewrite or empty one.

**Amended — §8's cheap path.** "A snippet with no `ask:` spans skips the form entirely"
becomes "a body with no parameters and no flags skips the form entirely". It is still the
common path and still costs nothing.

## 4. The grammar

Two families of braces. They cannot collide, because they are different braces.

### 4.1 Values — `{{…}}`

| Written                       | Means                                                                              |
| ----------------------------- | ---------------------------------------------------------------------------------- |
| `{{name}}`                    | parameter, asked as a text field                                                   |
| `{{name=claude}}`             | parameter with a default                                                           |
| `{{name=claude\|omp\|codex}}` | parameter with an option list; the FIRST option is the default                     |
| `{{env:cwd}}`                 | a session fact from the closed table (`resolve.ts` `ENV_KEYS`) — unchanged         |
| `{{secret:NAME}}`             | a vault reference — not ours, left intact — unchanged                              |
| `{{evn:cwd}}`                 | colon, unregistered namespace → literal text, reported as unrecognised — unchanged |

A `|` anywhere in the value part makes the declaration an option list. **A default
containing a literal `|` is therefore not expressible.** No escape is introduced for it: a
second escaping mechanism costs more than the case it buys, and no such case has been
named. Stated here so it is a decision rather than a discovery.

A parameter name is the text before the first `=`. Only the first `=` separates, so a
default may contain one — the rule `splitAsk` already applies (`resolve.ts:25`), and it
survives under a new name.

### 4.2 Logic — `{%…%}`

```
{% if parallel %}     … {% endif %}
{% if not parallel %} … {% endif %}
{%%                   → a literal "{%"
```

Nothing else. The notation is borrowed from the Jinja family (Jinja2, Django, Liquid, Twig,
Nunjucks, Ansible) because a reader recognises it, and because separate delimiters for
logic are what make §11.1 structurally safe rather than carefully maintained.

**Why not a real engine.** Handlebars and Mustache treat `{{secret:NAME}}` as a variable
and render it to the empty string — exactly the §11.1 failure, silently, on the vault's
path. Nunjucks and LiquidJS fail to parse it. All four would need `{{secret:…}}` shielded
before compilation and restored after, which is a hand-written escape hatch inside a
foreign engine — the riskiest part of the feature, owned by nobody here. And none of the
four can express §4.1's inline defaults and option lists at the point of use, so a separate
declaration header would have to be invented anyway. What an engine actually saves is the
block matcher, which is a stack.

## 5. One name, one role

- A name that is **substituted** into the text is a parameter: a text field, or a select
  when it declares options.
- A name that appears **only** inside `{% if %}` is a **flag**: a checkbox. It has no
  string value and is never substituted. No syntax declares it — its role is derived from
  how the body uses it.
- **A flag starts un-ticked**, and no syntax makes one start ticked. A sentence that
  should be there by default is written unconditionally, and its exception goes under
  `{% if not x %}` — which is what the negated form is for. Adding a default-on flag would
  need a second declaration form for a role that is otherwise derived (§5), and the
  negation already expresses it.
- **Being both is a structural error.** `{% if worker %}` where `worker` is also
  substituted is refused, not read as "always true". A truthiness rule would require the
  notion of an empty answer to a select, which has no meaning; the error says so instead,
  and the feature that people will actually want here — comparing the value — is named in
  §2 as a later addition.
- **One name declared twice with different defaults or different options is an error.**
  Today first-occurrence wins, silently (`resolve.ts:33`, `askFields`). For a free-text
  default that was a shrug; for two disagreeing option lists it is a body that offers a
  choice the author did not write.

## 6. One parser, three consumers

`reference.ts` stops being a regular expression and becomes the parser. It returns one
result, and preview, form and fire all read it:

```
parse(body) → {
  fields:      [{ name, kind: 'text'|'select'|'flag', defaultValue, options[],
                  inside: { name, negated } | null }]        // first-occurrence order
  blocks:      [{ openFrom, openTo, closeFrom, closeTo, name, negated }]
  spans:       [{ from, to, kind: 'env'|'secret'|'param', arg }]
  diagnostics: [{ from, to, kind, detail }]
}
```

`preview.ts` may not re-derive any of it. Its own header already forbids exactly that
(`preview.ts:11`: "This module DECIDES nothing"), and AD-8 is the general form. The
consequence that matters: **the legend an author reads in Settings and the refusal a fire
produces come from one computation**, so they cannot disagree.

`diagnostics` is the whole vocabulary of "this body is malformed":

| kind                      | Example                                              |
| ------------------------- | ---------------------------------------------------- |
| `unclosed-block`          | `{% if x %}` with no `{% endif %}`                   |
| `stray-endif`             | `{% endif %}` with no open block                     |
| `nested-block`            | `{% if a %}{% if b %}`                               |
| `unterminated-tag`        | `{% if x` with no `%}`                               |
| `unknown-tag`             | `{% for x %}`                                        |
| `condition-on-parameter`  | `{% if worker %}` where `worker` is substituted (§5) |
| `conflicting-declaration` | `{{w=claude}}` … `{{w=a\|b}}` (§5)                   |
| `unrecognised-span`       | `{{ask:port}` — one brace; today's behaviour, kept   |

## 7. Resolution order

In `resolve.ts`, one pass, in this order — the order is load-bearing and each step says why:

1. **Parse.** Any diagnostic → refuse, naming them. Fire refuses too, not only the
   preview: a body arrives through backup/restore and may never have been opened in
   Settings.
2. **Compute the visible fields** — those whose enclosing block is satisfied by the answers
   held so far. A field inside a block the person switched off is not asked. Otherwise an
   optional paragraph levies its questions on everybody.
3. **Missing answer for a visible field** → `needs-fields`, as today.
4. **Drop the excluded blocks.** When an `{% if %}` or `{% endif %}` is the only
   non-whitespace content on its line, the whole line goes, newline included; otherwise
   only the tag goes. Without this rule a body with two conditions always arrives with
   holes in it. This is Handlebars' standalone-line behaviour; Go templates make you write
   `{{- -}}` by hand, and everyone forgets.
5. **Only now, check `env` availability.** Consequence, and a good one: an `{{env:branch}}`
   inside a dropped block no longer refuses the fire (§11.2 of the snippets spec still
   governs the ones that survive).
6. **Substitute, right to left.** An answer is inserted **literally** and is never
   re-parsed: a person who types `{{worker}}` into a field gets those characters.

## 8. The form

`snippet-ask-dialog.tsx` grows two controls, both already in the kit — `Select`
(`ui/select.tsx`) for an option list, `Checkbox` (`ui/checkbox.tsx`) for a flag. Nothing new
enters `ui/`, and no surface repaints a kit component (AGENTS.md, "read the kit").

One behavioural change: **the form is reactive.** Un-ticking a flag removes the fields that
live inside its block, per §7 step 2. The dialog remains one form for one fire — the
palette still hands over and closes (§10.1 of the snippets spec).

## 9. Security

An answer equal to `{{secret:prod}}` stays a live vault reference and falls under the
destination policy that already governs one written into the body: delivered to a pane,
refused to the clipboard (`fire.ts`, `secret-to-clipboard`). This is **not** a new mechanism
around secrets — §2 of the snippets spec puts those out of scope — and not a new exposure:
§7.5 already states that a resolved answer is ordinary text at the destination, identical to
the person typing it by hand, which is what they would otherwise do. It is written down here
because it was asked, not because it is advertised.

## 10. What does not change

The backend, the wire, the contracts, the schemas: **not one line**. A snippet body is an
opaque string to `internal/snippet` and stays one. `snippets.*` keeps its five methods and
its ten conformance tests untouched.

One Go file changes: the seeds in `internal/snippet/service.go`, whose bodies are written in
the retired syntax. The rule that governs them survives unchanged — a seed must fire in an
ordinary local pane, so it may use `{{env:cwd}}` and parameters, and may not use an env key
that depends on where the pane is pointed.

## 11. Acceptance — as assertions

- [ ] One e2e watches a person do the new thing end to end: create a snippet carrying an
      option list and a condition, fire it with the palette chord while a program owns the
      pane and no command editor exists, pick a worker from the select, un-tick the flag,
      and see the resolved text reach the program with the excluded sentence gone, **no
      blank line where it was**, and no trailing newline.
- [ ] `parse()` is the only function in `frontend/src/snippets/` that reads the grammar:
      `preview.ts`, `resolve.ts` and the dialog contain no brace literal and no regular
      expression over a body. Checked by reading the diff.
- [ ] For a body carrying each diagnostic in §6's table, the settings preview and a fire
      report the **same** diagnostic — one test, both surfaces, one table of bodies.
- [ ] A field inside a switched-off block is never asked, and an `{{env:…}}` inside one
      never refuses the fire.
- [ ] `{{secret:NAME}}` survives resolution byte-for-byte, including inside a kept block —
      and `frontend/src/secret-reference.ts`, `secret-chip.ts` and `submit.ts` appear in no
      commit of this work (§7.3 of the snippets spec, still binding).
- [ ] `reference-namespaces.ts` no longer lists `ask`, and the registry's disjointness test
      is updated rather than deleted.
- [ ] `preview.test.ts:36` — which today asserts a bare `{{cwd}}` is unrecognised — reverses
      to assert it is a parameter, in the commit that makes it one.
- [ ] The seeds fire in an ordinary local pane, proved by a test, not by reading them.

## 12. The blind spot this buys, stated plainly

Dropping `ask:` makes every well-formed bare `{{name}}` a valid parameter. So `{{wroker}}`
is no longer diagnosable as a typo of `{{worker}}` — it is simply another field, and the
author learns it from the form asking a question they did not mean to ask. `{{env}}`, a
missing colon, becomes a parameter rather than an error.

What stays diagnosable is everything with a colon: `{{ask:port}}` and every misspelled
namespace still report as unrecognised, because a colon commits the span to the registry.

This is the price of the SQL-editor model and it was accepted deliberately. The mitigation
is the preview line, which names every field it found — an author reading it sees `wroker`
listed and knows.

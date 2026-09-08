---
name: skill-authoring
description: How to write a skill for this machine when the person asks you to remember a procedure.
---

# Writing a skill

A skill is a procedure this machine follows, written once so nobody has to say
it again. It is not a summary of the conversation you just had.

## When to read this

- The person asks you to remember, save or write down how something is done.
- You are about to explain again something you already explained in an earlier
  session.
- You are changing a skill that already exists.

Not for a fact about one machine, one host or one incident — that is a note,
and `notes.create` is the tool for it. Not for something the person asked you
to do once.

## What you actually write

`skills.create` takes three things — `name`, `description`, `body` — and writes
the file itself. **You do not write the `---` frontmatter.** One at the top of
`body` does not become the skill's frontmatter; it becomes the first lines of
its text, under the real one. `skills.update` takes the same three and replaces
the whole skill, so pass the body you want the skill to have, not the part you
changed.

Each limit below is a refusal, not a warning: a write that breaks one writes
nothing and says so.

- `name` — lowercase letters, digits and hyphens, starting with a letter or a
  digit, 64 characters at most. It is lowercased and trimmed for you.
- `description` — 2048 characters at most; control and invisible formatting
  characters are dropped.
- `body` — must not be empty.
- the whole file, frontmatter included — 64 KiB at most.
- the name must be free. A skill the person wrote, and one this machine ships,
  each hold their name against you, for `skills.update` as much as for
  `skills.create`.

## The description is what gets you found

It is the only line of the skill that reaches the system prompt. You choose
which skill to read from the description alone, and so will the next session,
so write what task it is for, in the words a person would use for that task —
"how we deploy this service", not "deployment notes". A description that says
"helpful information" matches nothing. Two thousand characters is the ceiling,
not the target; one sentence is almost always right.

## The body is a procedure

Write the steps, the exact commands, the paths, and the one thing that goes
wrong. Do not retell what happened in the conversation; the next reader was not
there. Do not restate what the system prompt already says.

End a step with something the reader can check — "the service answers on 8080"
tells them where they are, "start it and make sure it works" does not. A line
that would not change what the next reader does is a line to cut.

## What belongs in the body and what does not

Keep the body under a page. If there is a long reference — a host table, an
error catalogue — the person can put it in `references/` beside the SKILL.md,
and you read it with `skills.read`, naming the skill and the path.

Write paths as the next reader will see them. A path inside one person's home
directory is wrong for everybody else who reads the skill.

## What never goes in

No secrets, no API keys, no passwords, no personal data. A skill is a plain
file on disk and a person may share it.

## Checking it landed

A skill you wrote appears on the Skills page and is switched on from the
moment it is written; you are offered it on later runs.

A skill the PERSON placed by hand is where this goes quietly wrong. Such a file
has to open at its very first byte with `---`, carry `name` and `description`
between there and a closing `---`, and stay inside the same limits. A file that
fails any of that is dropped from the list with nothing but a line in the log —
no error, and no skill. If the person says a skill they added is not there,
that is the first thing to look at.

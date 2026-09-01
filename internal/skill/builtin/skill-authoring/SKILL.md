---
name: skill-authoring
description: How to write a skill for this machine when the person asks you to remember a procedure.
---

# Writing a skill

A skill is a procedure this machine follows, written once so nobody has to say
it again. It is not a summary of the conversation you just had.

## The description is what gets you found

The description is the only line in the system prompt. Write what task it is
for, in the words a person would use for that task — "how we deploy this
service", not "deployment notes". A description that says "helpful information"
matches nothing.

## The body is a procedure

Write the steps, the exact commands, the paths, and the one thing that goes
wrong. Do not retell what happened in the conversation; the next reader was not
there. Do not restate what the system prompt already says.

## What belongs in the body and what does not

Keep the body under a page. If there is a long reference — a host table, an
error catalogue — the person can put it in `references/` beside the SKILL.md
and you read it with `skills.read` when you need it.

## What never goes in

No secrets, no API keys, no passwords, no personal data. A skill is a plain
file on disk and a person may share it.

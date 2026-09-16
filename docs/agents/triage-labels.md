# Triage Labels

The skills speak in terms of five canonical triage roles. In this repo they are `br`
labels, each label string equal to its role name.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

Apply with `br label add <id> <label>`, remove with `br label remove <id> <label>`. `br`
creates a label the first time it is applied, so there is nothing to provision first.

**They are a second axis, and they do not replace the area label.** Every bead still
carries exactly one area label from the list in [`AGENTS.md`](../../AGENTS.md). A triage
label is orthogonal to it, the way `mvp` and `phase-1/2/3` are.

**`wontfix` is a label and a close.** `br` has no terminal "wontfix" state: label the bead
**and** `br close <id> --reason "wontfix: <why>"`, so the reason survives in the close
metadata and not only in a label. A `wontfix` bead left open is a bead still in somebody's
listing.

**`ready-for-agent` and `ready-for-human` are for work arriving from outside the queue.**
For a bead already inside an epic, `br ready` is the authority on whether it is takeable —
it computes blockers and holds, which a hand-applied label cannot. Two answers to "is this
takeable" is the failure AGENTS.md's "look for the existing answer" rule is against, so
apply these two at triage and let `br ready` govern in-flight work.

Every label write is a backlog write: `br sync --flush-only` and commit
`.beads/issues.jsonl` immediately after.

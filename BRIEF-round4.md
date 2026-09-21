# Round 4: the owner has decided things that move your plan

Same rules. Read and think; no code, no build, no tests, no tracker, no branch,
no commit. `pwd` first.

Round 3 converged and I took it to the owner. He then decided five things, three
of which change your staging. Two questions are still open and I want your
argument on them, not your agreement.

## What the owner decided

**1. Retention settings already exist, and you invented a second model.**

Read `internal/settings/settings.go`, the History section — six registered
knobs: `history.enabled`, `history.retentionDays` (0 = "kept until the size
limit"), `history.retentionMiB` (default 4096, the logical content budget
eviction acts on), `history.diskCeilingMiB` (default 8192, physical ceiling over
DB + WAL, compacts rather than deleting), `history.outputEnabled`,
`history.outputCapKB` (default 256, per command, head and tail kept, middle
dropped).

Your "10,000 logical rows or 8 MiB per session" is a second retention model
beside that one. The durable journal is content and lives under the existing
budget. My fault, not yours — the round-1 brief never mentioned settings.

**2. The journal never deletes. `clear` is a VIEW boundary.**

Nothing is removed from the journal except by the retention threshold. A
`clear`/RIS does not discard rows; it writes a boundary and the client simply
does not show what came before it.

Consequence I drew and want you to check: **the reset/discard callback becomes
mandatory rather than optional.** Under a discard policy we could have declined
to capture those rows; under "store everything" we must capture them before the
mutation destroys them.

**3. `history.outputEnabled` is a property of the SINK.**

Off means "not written to disk". The current session still scrolls. It was
always wired to the store's policy, so this is what it already meant.

**4. The in-session tier IS libghostty's own scrollback**, read through the
`HISTORY` point tag. Not a journal buffer of ours in memory.

The owner's reason, and it is the strong one: **without shell integration there
are no block boundaries, so there are no intervals, so a journal organised
around intervals has nothing to hang rows on.** The emulator's scrollback does
not care — it is just rows.

**5. A new `Terminal` settings section, with a scrollback depth knob.**

`terminal.scrollbackLines`, default 10,000, minimum 0 with a zero label saying
scrollback is off, maximum conservative (100,000) pending measurement. The byte
limit stays internal as a memory ceiling. The description must state all three
of: pruning is page-granular so usually MORE lines are kept than asked; heavily
styled output keeps FEWER; lowering the value prunes immediately and zero erases
what is retained.

## What I claim follows, and want you to attack

**A. The fork patch leaves the critical path of the xterm.js cutover.**

Live scroll becomes: the point tag in `bridge.c`, a read surface on the port, and
a page contract. No library patch. The drain feeds only the durable tier.

**B. The "output then reset inside one `vt_write`" hole stops mattering for the
live tier, by definition.** The emulator IS the authority on what is scrollable
now, and an ordinary terminal loses those rows too. The hole is now purely a
durability question.

**C. The seam between the two tiers is a pattern this codebase already uses.**
`contracts/session.output.schema.json` joins two sources on one timeline with a
declared meeting point — the client compares `produced` against `replayFrom`,
attaches at the later, and knows the difference is a gap. So this is two tiers of
one timeline with a declared join, not two owners of one behaviour. I withdraw my
round-2 worry about two readers; tell me if I withdrew it too early.

## Two questions still open. Argue, do not agree.

**Q1. Where is the `clear` boundary enforced?**

Either the reader refuses to page before the last boundary — then `clear` means
something, and a person who typed it before a colleague attached is right about
what they did — or it is only where the default view starts, and scrolling up
crosses it, which makes `clear` cosmetic. `clear` on this machine emits
`ESC[H ESC[2J ESC[3J`, and `ED3` means "erase saved lines", so the program did
ask for a discard we are deliberately not performing.

I lean to enforcing at the reader with an explicit, visible way to reveal what
is behind the boundary. Say whether that is right, and what it costs in the page
contract.

**Q2. What happens to scroll-up while the alternate screen is active?**

The alternate screen has no scrollback of its own, and you established that a
reference belongs to the screen it was created on. So during `vim`, does
scroll-up do nothing, or show the primary screen's history? Say what the headers
permit, what comparable terminals do, and which you would take.

## What to produce

Short. Write to, and only to:

`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region/ANSWER-round4.md`

1. **A, B and C** — confirm or refute each, with evidence.
2. **Q1 and Q2** — your answer and its cost.
3. **Retention, rebuilt on the existing knobs.** Say what is genuinely missing
   and needs a new setting, and what you withdraw.
4. **The staging order, rebuilt.** Given the patch has left the critical path,
   is there now a SMALLER first stage than the one you named? Name the first
   task and what it must prove.
5. **Anything still open**, explicitly, or say nothing is.

Final line, exactly:

`WORKER_DONE::sbk4-7f2822281217`

Or exactly:

`WORKER_BLOCKED::sbk4-7f2822281217 <one line why>`

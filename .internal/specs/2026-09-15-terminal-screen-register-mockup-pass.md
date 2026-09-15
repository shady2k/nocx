# The terminal screen's visual register — the mockup pass

- **Bead:** nocx-9bpeq (nocx-9bpeq.12–.16)
- **Status:** decided by the owner in session, 2026-09-15 ("take all of it, follow the mockups")
- **Amends:** `.internal/specs/2026-09-14-terminal-screen-visual-register-design.md` §3.1, §3.2, §5.1, §5.2, §8, §9.
  Everything not named here stands.
- **Crosses:** ADR-0008 (keyboard-first ledger — Stop and the target menu stay reachable without a
  pointer), ADR-0004 §3 (the Run/Ask switch), ADR-0012 (imperative code gets the kit through vanilla
  emitters and render islands, §6 of the base spec), ADR-0013 (tokens only), AD-1 (no new data on the data
  plane; `git.open`/`files.open` are existing control-plane calls), AD-6 (the backend never sniffs; home and
  branch are not read out of the byte stream), AD-8 (one owner per fact: one home per session, one branch
  source per pane, one path formatter)
- **Evidence:** `output/imagegen/nocx-concepts-v2/01-command-states.png`, `03-split-and-files.png`,
  `nocx-concepts-v4/background-input-request.png` (in the main checkout, gitignored); the owner's
  screenshot of the dev-web stand at 7a625ce1.

## 1. Why a second pass

The first pass took only subtractions from the mockups — no `ok`, no clock, no status bar, no send arrow —
and replaced the chips with muted `--font-size-2xs` UI text. On a running page that reads poorer, not
calmer: `home/dev` in grey at 12px, `77ms exit 127` in a corner, a composer whose input is not visible as
an input. What makes the mockups pleasant is additive: a prompt line with an accent path and the branch,
metadata at a readable size in the mono face, a status that names itself, a Stop control where the command
runs, a composer that looks like a place to type, and air between blocks.

## 2. The prompt line (replaces §3.1 "Left" and §5.2 "Left")

Every command block and the composer open with the same line, drawn by one new kit primitive:

```
~/repos/nocx on ⎇ main                                        Exit 1 · 1.2s  ⋮
› go test ./internal/session
```

`PromptContext` — `ui/prompt-context.ts`, identity `ui-prompt-context`, stylesheet
`styles/components/prompt-context.css`, a row in `ui/README.md`, vanilla emitter (no Solid twin until a
Solid surface needs one):

- `createPromptContext(facts, opts)` / `updatePromptContext(el, facts, opts)`.
  `facts = { host?: string; hostStrong?: boolean; path: string; branch?: string }`,
  `opts = { tone?: 'normal' | 'dim' }`.
- Mono face (`--font-family-mono`), `--font-size-sm`, one line, ellipsis at its end.
- Parts, each a span with a `data-part`: `host` (`--color-text` when `hostStrong`, else muted) followed by a
  muted `:`; `path` in `--color-accent`; when a branch is known, a muted word `on`, the kit
  `GitBranchIcon` at `--icon-size-sm` in muted, and `branch` in `--color-accent`.
- `data-tone="dim"` dims the whole line one step (the unfocused composer, §5.3 of the base spec).
- It is the one place the where-facts are drawn: `Meta` stays the primitive for the right-hand status group
  and anything else that is metadata, but the where-line is no longer a `Meta`.

**Path format** — `cwdLabel(cwd, home?)` in `frontend/src/cwd-label.ts`, still the only formatter:
`home` itself is `~`; a path under `home` is `~/rest`; otherwise the absolute path. More than four
segments after the `~`/root keeps the last three behind `…/` (`~/…/b/c/d`, `/…/b/c/d`). A missing cwd is `~`
exactly as today. Without a known home the absolute path is shown — never a guessed `~`.

**The non-human author** badge (base spec §3.1) stays before the prompt line.

## 3. Where home and the branch come from

Neither is in the byte stream, and neither is invented by the renderer.

**Home: one per session, from the binding the link opener already opens.** `terminal-links/open.ts`
derives home with `homeFromRoot` from a `files.open` binding and caches one binding per session. That is the
existing answer (AGENTS.md "Look for the existing answer"): it moves into a session-home source both the
link opener and the prompt line consume — one binding and one home per session, opened when the session
first reports a verified cwd rather than on the first click. No new contract, no new round trip beyond the
one links already pay.

**Branch: `git.open` + `git.close`, per pane, single-flight.** `git.open(sessionId, cwd)` already returns
the first status inline, including `branch`, `detached` and the short head. A per-pane branch source asks
when the verified cwd changes and when a command block settles (a `git checkout` changes it), debounced, at
most one request in flight, and closes the binding immediately. State `ok` gives `branch`, or the short
head when `detached`, or nothing when unborn with no branch name. Every other state (`notARepository`,
`consentRequired`, `noCwd`, `gitUnavailable`, …) gives **no branch and no UI** — this is ambient decoration
and must never raise a consent prompt, a toast or a deploy. If reading `internal/transport/ws_git.go` shows
`git.open` can deploy a helper or prompt on a remote session without prior consent, the source asks only for
local sessions, and a bead is filed for the remote case.

A block records the branch the pane knew **when the command was submitted** and never updates it
afterwards: the line is history. A block restored from a record carries no branch (the record does not
have one; out of scope here).

**Seams**, fixed so the renderer work can be split:

- `setBlockWhere(block: HTMLElement, facts: { home?: string; branch?: string }): void`, exported from
  `scrollback/blocks.ts` — updates that block's prompt line in place; a block keeps the cwd and location it
  was built with.
- `CommandEditor.setWhereFacts(facts: { home?: string; branch?: string }): void` in `editor.ts` — the
  composer's prompt line; cwd and location keep their existing setters.

## 4. The command row and the status (amends §3.1 "Right", §3.2)

- **Sigil.** A command block's command row begins with the kit `ChevronRightIcon` at `--icon-size-sm`,
  muted, `aria-hidden` — the mockup's `›`, never as a text glyph. Ask and tool kinds keep their rows as
  they are.
- **Status group** is mono `--font-size-sm` (a `Meta` variance `data-size="sm"` with the mono face, not
  surface CSS). Order: status word, a muted `·`, duration. The failure word is capitalised by the kind's
  rules (`Exit 1`); success stays silent (base spec table unchanged).
- **Running:** kit `Spinner` `sm`, the word `Running` in the accent tone, `·`, elapsed whole seconds, then a
  kit `createButton` `variant="default"` `size="sm"` with `SquareIcon` and the text `Stop`, calling the injected
  `RunningBlockActions.stop()` — present only while `isActive(block)` holds, removed when the block
  settles, always visible (not hover-revealed) and in the tab order. The ⋮ menu keeps its Stop item: the
  button is the visible door, the menu the second door to the same handler.
- The failed row keeps the base spec §3.3 treatment.

## 5. Rhythm (amends §8)

A block row: `padding-block: var(--space-3) var(--space-4)`; prompt line to command row `var(--space-1)`;
command row to output `var(--space-2)`. The composer's outer block padding is `var(--space-3)`. All through
tokens; nothing literal.

## 6. The composer (amends §5.1, §5.2, §9 "a boxed composer")

```
────────────────────────────────────────────────────────────  hairline
  ~/repos/nocx on ⎇ main                     gpt-5 ▾  2 blocks  prompt line left, controls right
 ┌──────────────────────────────────────────────────────────┐
 │ Run ▾ │ git diff█                                         │  the input field
 └──────────────────────────────────────────────────────────┘
```

- The prompt line is `PromptContext`, `tone="dim"` while the composer is unfocused, host strong when remote.
- **The input is a visible field.** The CM6 editor and the mode switch sit inside one box:
  `border: 1px solid var(--color-border)`, `border-radius: var(--control-radius-md)`, background
  `--terminal-background` (base spec §7 stands — no raised surface in a row). When the editor is focused the
  border is `--color-accent`. The box is the `.nocx-editor` surface's own placement CSS in
  `styles/surfaces/composer.css`, since it paints no kit component. No send arrow.
- **`Run ▾`.** `ModeIndicator` gains a trailing `ChevronDownIcon` and a divider after it, and its click opens
  the kit `ContextMenu` (render island, §6.3 of the base spec) listing the registered targets by word, the
  active one checked, each item switching to that target. The ⌘/Ctrl+Enter chord is unchanged. The
  indicator is reachable and the menu operable from the keyboard.
- The meta row keeps its fixed height (nocx-i4h04); the box must not change height when a control appears.

## 7. What stays rejected

A pane header row (every pane pays a row of chrome for facts the prompt line states), `Input: …`
recipient labels, a status bar, a send arrow, a success mark, the attention model of v3–v5.

## 8. Tasks

| Bead                                       | Takes                    | Files (disjoint within a wave)                                                                                                                                                                                        |
| ------------------------------------------ | ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| nocx-9bpeq.12 prompt line and block header | §2, §4, §5               | `ui/prompt-context.ts`+css+test, `ui/icons/{GitBranch,ChevronRight}Icon.tsx`, `ui/meta.ts`+`meta.css`, `ui/README.md`, `cwd-label.ts`, `scrollback/blocks.ts`, `styles/surfaces/command-block.css`, `pets/overlay.ts` |
| nocx-9bpeq.13 home and branch sources      | §3                       | `terminal-links/open.ts`, new `where/` module, their tests                                                                                                                                                            |
| nocx-9bpeq.14 e2e register, second pass    | §2, §4, §6 as assertions | `e2e/` only                                                                                                                                                                                                           |
| nocx-9bpeq.15 composer (after .12)         | §6                       | `editor.ts`, `styles/surfaces/composer.css`, `ui/mode-indicator.ts`, `ask-entry.ts`                                                                                                                                   |
| nocx-9bpeq.16 wiring (after .12, .13, .15) | §3 seams                 | `terminal-content.ts`, `panes.ts`                                                                                                                                                                                     |

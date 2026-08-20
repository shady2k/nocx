# The landing page: a scrollback you can read, and nothing in it that is not true

- **Date:** 2026-08-20
- **Beads:** `nocx-knks8` (this brainstorming session)
- **Status:** design, settled with the owner on 2026-08-20, one question at a time.
  Adversarially reviewed by codex in the same session; four of its five factual claims
  about this repository were verified against the code and two of them found our own
  documentation to be wrong (§2).
- **Consulted:** codex (2026-08-20) — supplied the leading visual idea, the correction to
  the hero hypothesis, and most of the accidental-lie inventory in §7.

## 1. In one sentence

**A public page for nocx that reads like one edited terminal session, sells the one thing
v0.2.0 actually does best — your working context survives — and contains no sentence that
the code cannot back.**

**What a person can do that they cannot today:** land on a URL, understand in thirty
seconds what nocx is, see it working in a real screenshot, and download the build for their
platform already knowing the two things that will otherwise surprise them (the macOS
quarantine step and the glibc floor).

**What checks it.** Two different things, and conflating them is how a page like this goes
wrong. The _structure_ is gated mechanically by `scripts/check-site.sh` (§11) — relative
paths, images that exist, and a literal grep for the phrasings §7 forbids. The _claims_ are
not machine-checkable and are not pretended to be: they are read once, by eye, against the
honesty ledger in §7, which is the acceptance criterion for the copy.

## 2. What is true on 2026-08-20, and where our own documents are not

The tag `v0.2.0` sits on `916f2ac`, which is the tip of `main`. So
`docs/release-notes/v0.2.0.md` is not a summary of the release — it **is** the release, and
it is the only source this page draws feature claims from. Anything not in that file does
not go on the page.

Three drifts were found while establishing that. Two were documentation errors and are
**fixed on this branch**; the third is a fact about the release that the page must respect.

**README said the macOS build is unsigned. It is not.**
`.github/workflows/release.yml:211` runs `codesign --force --deep --sign -`. The build is
**ad-hoc signed and not notarized** — no Developer ID, no notarization, but a signature.
`unsigned` was both wrong and, for a reader deciding whether to trust the download, wrong
in the direction that matters. README now says what the release notes say.

**README, AGENTS.md and `docs/architecture.md` said Wails v2; `go.mod` says
`wailsapp/wails/v3 v3.0.0-beta.9`.** The drift reached AD-3, a binding invariant, and the
open question beside it. Neither was violated: AD-3 permitted the move on one condition —
"migrate to v3 only if multi-window is required" — and `8004fd72` (`nocx-mgbjx`) records
that condition firing, because v2's runtime has no window-creation call at all. AD-3 now
records v3 and why; the open question is marked closed. The page says v3.

**The sidebar is local-only in v0.2.0, and this one is not a documentation error.**
`nocx-457v` ("The Git panel works on an SSH host, through the remote helper") has all 17
children closed, and the work is real: `cmd/nocx-helper/main.go` and `internal/helper/**`
exist on `feat/nocx-u5rr-helper-client` and `feat/nocx-v0pb-helper-proto`. **Neither branch
is merged into `main`.** So at `916f2ac` — which is `v0.2.0` — `cmd/` holds only
`devharness`, `e2e-sshd` and `manifest-sign`, `internal/app/app.go:807` wires
`gitlocal.NewFactory()` as the only implementation, and `RepoFactory.Open(ctx, cwd)` takes
a local path. The epic is correctly still open. **The page never says the panels work over
SSH**, because the build a visitor downloads cannot do it. When the helper merges and ships
in a release, this section of the page changes with it.

Two facts were established that make the page _stronger_, not weaker:

**The assistant payload is precisely known.** `internal/assistant/assistant.go:88`: what
goes to the endpoint is a system rule (frame content is data, not instructions), the
question, and the text of explicitly attached frames as labelled data. A question with no
attached frames carries **only the question**. No host metadata, no terminal contents. This
can be stated verbatim and is worth more than any adjective.

**The assistant's HTTP guard is real engineering and should be visible.**
`internal/assistant/httpguard.go`: `http://` is permitted only to loopback and private
addresses; the rule is enforced on every connection and every redirect hop against the
**resolved address**, not the string; `http://` always dials direct so proxy variables
cannot reroute it; the `Authorization` header does not survive a change of scheme, host or
port.

**Licensing is now unblocked.** The whole `go.sum` module set was scanned for
GPL/LGPL/MPL/AGPL: nothing. Direct dependencies are Apache-2.0 (eino, jsonschema), MIT
(wails v3, go-keyring, go-sqlite3, creack/pty) and BSD (dbus, gorilla/websocket, pkg/sftp,
`golang.org/x/*`). Frontend runtime dependencies are MIT throughout (CodeMirror 6,
xterm.js, solid-js, shiki, `@wailsio/runtime`). The one copyleft contact is the Linux
AppImage, which bundles GTK 3 and WebKitGTK (**LGPL-2.1+**) as shared libraries — permitted
under the LGPL, but it carries distribution obligations (ship the licence texts, do not
prevent relinking). That is a packaging matter, not a constraint on our own licence.

## 3. Prerequisite: the repository gets a LICENSE

**MIT**, chosen by the owner on 2026-08-20. Until `LICENSE` exists, the code is legally
all-rights-reserved and the page may not use the words "open source". This lands **before**
the page does, in its own commit: `LICENSE` at the repository root, and
`package.json`'s `"license"` field corrected from the scaffold default `ISC` to `MIT`.

## 4. Where it lives and how it deploys

```
site/
  index.html          # EN — canonical
  style.css           # one stylesheet
  assets/             # screenshots, icon, og image
  .nojekyll
.github/workflows/pages.yml
scripts/check-site.sh
```

`pages.yml` has **no build step**: it triggers on `push` to `main` under `site/**` plus
`workflow_dispatch`, takes `pages: write` and `id-token: write`, runs under
`concurrency: pages`, and calls `actions/upload-pages-artifact` with `path: site/` followed
by `actions/deploy-pages`. It is a separate workflow from `ci.yml` and `release.yml` and
touches neither. No node, no npm, no second `node_modules` — deliberately, because the
containerized gates already serialize on one Docker daemon and share named volumes
(`nocx-x6z3`), and a landing page is not worth entering that.

**The owner must set Settings → Pages → Source to "GitHub Actions" once.** Nothing in the
repository can do this.

**Every internal path is relative** — `./style.css`, `./assets/hero.webp`. The site is a
Project Page served from `https://shady2k.github.io/nocx/`, so a root-absolute `/style.css`
resolves to the user page and yields an unstyled document. `check-site.sh` fails on any
`href="/` or `src="/`.

## 5. The visual language

**Leading idea: the page is a scrollback you can read.** Not a terminal UI stretched over
marketing, and not a SaaS grid of cards — one long, carefully edited session, where each
section is a block in a single journal: input, result, explanation, next context. The
product's own model is the page's structure.

**Typography speaks in two voices.** Claims are set in a neutral grotesque. Monospace is
reserved for text that denotes _observable system state_ — commands, versions, exit status,
platform constraints, file paths. Section headings are scrollback labels (`01 / resume`,
`02 / workspace`, `03 / secrets`) rather than decorative words like FEATURES. The whole
page must not be monospace; that reads as a styled README within one screen.

**Rhythm comes from command blocks, not cards.** One hairline axis down the left: prompt
marker, then metadata, then the content at full width. Sections are separated by pause,
hairline rules and a change of density — never by drop shadows and rounded boxes.

**Colour is semantic, exactly as in a terminal.** From `tokyo-night`, the app's default
theme, so the page and the product look like one thing: canvas `#1a1b26`, surface
`#1f2335`, text `#c0caf5`, accent `#7aa2f7` for actions and links, `#9ece6a` for confirmed
state, `#e0af68` for a constraint the reader must not miss, `#f7768e` for refusal only. The
ANSI palette must not become confetti.

**The screenshot is evidence, not decoration.** One large, real, legible frame on the first
screen — no device mockup, no perspective. Every later image is a **crop of that same
scene**, tied by a leader line to the claim it proves. The page is then the examination of
one working session rather than a gallery of unrelated screens.

**Motion is nearly absent**: a caret settling, block metadata arriving. No typing
simulation. `prefers-reduced-motion` is honoured and no meaning depends on animation.

**Not doing:** glassmorphic cards, glowing gradients, a window floating at an angle, a
marquee of commands, a matrix background, a decorative `$` before every heading, and a
pixel-copy of nocx's own chrome. The page inherits the product's grammar; it does not
impersonate the product.

Dark only. Fonts from the system stack (`ui-sans-serif`, `ui-monospace`) — **no Google
Fonts**: a page whose thesis is "no cloud, no telemetry" must not fetch a file from a third
-party CDN on first paint. Single column below 720px.

## 6. Structure and the message

The owner's first hypothesis — lead with vault + assistant + restore — was **wrong, and
codex's objection is accepted**: those are three mechanisms, not one story, and side by
side in a hero they read as a feature bundle rather than a reason to try the thing.

Restore leads. It needs no explanation, it is the centre of the release itself, and it is
the first thing a person will feel after installing. The vault is the most distinctive
proof and comes third. The assistant is a strong third act and a weak hero: without a
configured endpoint it does nothing, and the word "AI" pulls attention off the product.

**Positioning line, EN canonical:**

> **A local terminal that keeps your working context** — tabs, their output, notes and
> secret references survive a restart.

Every clause is checkable against the release notes, and it claims nothing about a third
party's endpoint.

**Section order.**

1. **Hero** — the line, the two download buttons, `View on GitHub`, and directly beneath
   the buttons, not in a footnote: `v0.2.0 · early release · no formal support`,
   `macOS: ad-hoc signed, not notarized — first launch needs one Terminal command`,
   `Linux: x86_64 AppImage · glibc 2.35+`.
2. **`01 / resume` — Close it. Open it. Keep working.** Tabs, the output they printed, the
   front tab, window geometry, sidebar width. Worded as **"tabs and their captured output
   are restored"**, never "sessions resume where they stopped" — processes do not survive a
   restart and the second phrasing will be read as though they do.
3. **`02 / workspace` — your terminal becomes a workspace.** Named, coloured tab groups;
   Notes as tabs with a search panel; the Files/Git/Ports sidebar following the tab's
   directory; encrypted command history with retention, a disk ceiling and a per-command
   output cap. **Local machine only** (§2).
4. **`03 / secrets` — references, not secrets in history.** One concrete scene: the value
   goes to the vault, the command keeps a reference chip, running it asks for an unlock and
   the backend substitutes. Bounded exactly as §7 requires.
5. **`04 / ask` — ask from the scrollback.** Select finished output, the command chip
   appears in the input, the Run switch sends the line to the assistant instead of the
   shell, the answer arrives as a block in the same scrollback. The payload sentence from
   §2 appears **in this section**, not in a privacy page.
6. **`05 / edit` — the command line is an editor.** CodeMirror 6, shell highlighting,
   command and path completion, ghost text. A daily comfort, not the positioning.
7. **`06 / connect` — SSH.** Import from `~/.ssh/config` and a Tabby export; connection
   passwords and key passphrases in the vault. Not described as a full replacement for a
   dedicated SSH manager.
8. **What nocx deliberately doesn't do** — no mandatory account, no cloud service of its
   own, no product telemetry, no cloud sync. This **replaces** the competitor table (§8).
9. **Install and limits** — the `xattr` command and the `chmod +x` line in full, the
   distribution reasoning linked to ADR-0003, rollback linked to the README rather than
   reproduced.
10. **A narrow technical strip** — Go backend, xterm.js WebGL frontend, Wails v3, one
    WebSocket carrying a binary data plane and a JSON-RPC control plane. Credibility for a
    technical reader; it does not answer "why download", so it sits low.
11. **Closing CTA** — v0.2.0, both platforms, Releases, release notes.

## 7. The honesty ledger

This section is the acceptance criterion for the copy. A sentence that contradicts a row
here is a defect, not a matter of taste.

| Tempting phrase                                                | Why it misleads                                                                                       | What the page says instead                                                                                                                                   |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `open source`                                                  | No LICENSE until §3 lands                                                                             | After §3: `MIT licensed`. Never before.                                                                                                                      |
| `no cloud` beside the assistant                                | Reads as "data never leaves the machine"                                                              | Split in two: "no account, no cloud service of its own, no product telemetry" **and**, in `04 / ask`, the §2 payload sentence                                |
| `built-in AI`, `works with any model`                          | An integration ships, not a model                                                                     | `Bring your own endpoint`                                                                                                                                    |
| `your data never leaves your machine`                          | False with a remote endpoint                                                                          | Never used. Product telemetry, GitHub's own HTTP logs, and user-initiated SSH/assistant/update traffic are three different things and are not merged         |
| `secrets never leak`, `zero-knowledge`, `end-to-end encrypted` | True only of the rewritten command record                                                             | "Once a secret is saved, nocx replaces its value in its own record of the command with a reference; the value lives in the vault"                            |
| one vault story for both platforms                             | `internal/vault/{file,system}`; `purge.go` states a keychain not answering is an ordinary Linux state | Describe behaviour, not a protection model. Any per-platform security claim belongs in a separate note with a threat model, which this page does not attempt |
| `unsigned`                                                     | Factually wrong (§2)                                                                                  | `Ad-hoc signed, not notarized — no Developer ID`                                                                                                             |
| `secure updates`                                               | Reads as Apple attestation                                                                            | "The update manifest is verified with an embedded Ed25519 key; the app carries no Developer ID signature and is not notarized"                               |
| bare `Linux`                                                   | Implies broad reach                                                                                   | `Linux x86_64 AppImage · glibc 2.35+` on the button itself; the distribution list below. Never "no dependencies"                                             |
| panels "about the machine the tab is on"                       | The remote helper is not in v0.2.0 (§2)                                                               | Local machine, and SSH is not mentioned in that section                                                                                                      |
| `full Git client`                                              | It is diff/stage/commit/log                                                                           | Name the four operations                                                                                                                                     |
| `production-ready`, `for teams`, `daily driver`                | Not demonstrated                                                                                      | `v0.2.0 · early release · no formal support`                                                                                                                 |

**No analytics on the site.** A third-party tracker on a page arguing "no telemetry"
immediately requires a footnote, and the footnote is worse than the data is useful.

## 8. No competitor table in this version

Dropped, and not merely deferred for legal caution — comparative claims are lawful when
they are truthful, objective and verifiable (EU 2006/114/EC art. 4; US nominative fair use;
RF ФЗ «О рекламе» ст. 5 ч. 3 forbids only _incorrect_ comparison). Two better reasons:

1. **We could not fill the licence row about ourselves** until §3 lands, and a table whose
   first honest cell is missing is not a table.
2. **A table of individually true cells still lies through its choice of rows.** Doing it
   properly needs a date, the competitor's version or plan, a definition per row, a source
   per contested cell, and an explicit `Unknown` in place of every guess. That is its own
   piece of work, not a subsection.

"What nocx deliberately doesn't do" does the same positioning work, names no one else, and
cannot go stale when a competitor ships something.

If it is revived later it carries: date of verification, version/plan per column, a
one-line definition of every row (notably "vault" and "assistant"), a source link on every
contested cell, `Unknown` rather than inference, rows phrased as user tasks rather than
nocx's internal architecture, no logos, and no adjective about anyone else's quality.

## 9. No roadmap on the page

"Coming soon" is itself a promise of proximity, and `docs/vision.md` cannot supply the
content: it says of itself that it goes stale, and it has — Linux is listed as future while
the AppImage ships, and the assistant and semantic command line are listed as future while
working versions are in v0.2.0. It is a record of intent, not of status.

So the page carries no roadmap and no dated commitment. §6's "deliberately doesn't do"
section occupies that slot: it strengthens the positioning, promises nothing, and cannot
expire. Direction, if the owner wants it public, belongs in a GitHub roadmap that is
maintained.

## 10. Screenshots — the owner's part

Taken by the owner on macOS; nothing in this repository can produce them (the container
path is Linux WebKit at a container viewport, which is not the shipped chrome).

**Amended 2026-08-20, after the first attempt: the page ships with no figures at all.**
The original plan was framed placeholders reading `Product screenshot`, which is honest
while the frames are days from being filled and reads as an unfinished page once they are
not. The owner decided the screenshots wait indefinitely, so the frames came out along with
their CSS rather than sitting empty on a public URL — and their caption text was written as
a brief for the photographer, not for a reader. The page is text-only and complete; §11's
asset check has nothing to check until the figures return.

The first attempt is worth recording because it will recur. The frame offered was an
**empty window** — one unnamed tab, closed sidebar, nothing in the scrollback. A hero
screenshot is evidence for the claim printed above it, and this page's claim is that a
restart no longer costs you your work, which an empty scrollback is precisely unable to
show. So the brief below is about the **state of the session**, not the framing of the
window: what has to be on screen is a session somebody is already living in.

Two smaller things that attempt surfaced. The app was running a theme that is not
`tokyo-night`, and the page is painted in `tokyo-night` so that page and product read as
one thing — either the capture uses that theme, or the page is repainted to match whatever
the owner actually runs, which is arguably the more honest of the two. And the block
timestamps rendered in Russian on what is an English page.

- `hero` — the whole window, 1440×900 logical, captured on a retina display (2880×1800),
  theme `tokyo-night` to match the page. The frame must legibly show the tab strip with a
  workspace pill, several command blocks, the sidebar and the prompt, with a real agent TUI
  (Claude Code or aider) producing coloured output.
- `resume` — the same window after a restart, so section 2 can be a genuine before/after.
- `sidebar-git` — the Git panel with changed files and the commit form.
- `vault-chip` — a block whose command shows a reference chip where the secret was.
- `assistant-block` — an assistant answer as a block, its question as the header, with the
  command chip and the Run switch visible so no reader concludes nocx reads the terminal on
  its own initiative.
- Every frame is checked for real hostnames, usernames in paths, keys, and the contents of
  private repositories.
- `<picture>` with WebP and a PNG fallback; the hero is ≤ 400 KB after compression.

## 11. Checks

`scripts/check-site.sh` — plain shell, no node, wired into the pre-commit hook:

1. no root-absolute `href="/` or `src="/` in any HTML under `site/` (§4);
2. every `<img>` resolves to a file that exists;
3. no forbidden phrase from §7's left column appears in any page — a literal grep list.

The third is the one that earns its place: it is the only check that can catch the failure
this whole design exists to prevent, and it costs a `grep -F -f`. It cannot verify a claim
is true; it can only refuse the phrasings already known to be false, which is exactly what
a returning author six months from now will reach for.

When RU lands (§12) a fourth check compares the set of section `id`s across the two files
and fails on any difference.

## 11a. Amended 2026-08-20 — the page's graphics are terminal material

Text-only and complete was correct against "no screenshots"; it was not correct
against "make it obvious this is about a terminal". Nothing but the words said
terminal, and ten identically-shaped chapters read as a document rather than a
product page. Reviewed with codex a second time; the direction taken is its
framing, an **engineering terminal artefact** — the page's main graphic is real
terminal material and real state, never a drawing of the interface.

**The honest line, restated, because it is the whole design.** A real transcript
rendered as text is honest and announces what it is. A CSS reconstruction of
nocx's tabs, sidebar, workspace pills or assistant block is a screenshot we
never took, and is forbidden exactly as a drawn placeholder was. Between them
sit window ornaments — a title bar, three traffic-light dots — which are not
literally false and are not used: beside the product's name they read as
"this is nocx's window", and they buy nothing.

So the hero carries a shell transcript that is **genuinely captured from this
repository** (`git log --oneline -3`, `go test ./internal/session/
./internal/settings/`), captioned as text rather than a screenshot and
reproducible by any reader. Its container names the artefact — `SHELL
TRANSCRIPT · EXIT 0` — rather than imitating a window. It stays in English on
the Russian page: translating real output would forge it.

Behaviour is shown rather than described. Restore is a state table whose last
row is the warning-coloured one (a running process does not resume). Secrets is
a transformation with a synthetic `<value>`, labelled as nocx's own record of
the command. The assistant is its payload in three columns. That replaces the
prose that asked a visitor to build the product in their head.

## 12. Russian second, not simultaneously

EN is canonical and ships first. RU is translated from the finished page rather than
tracking a moving one — codex's objection to two languages is that the near-identical files
drift and the safety caveats drift first, and translating once from a settled text is the
cheapest defence available short of dropping RU entirely, which the owner does not want.

When it lands: `site/ru/index.html`, mutual `<link rel="alternate" hreflang>`, a switcher
that preserves the current anchor via a relative path. Version, verification dates,
platform requirements and every §7 phrasing must match **literally**; the RU page
translates meaning and never "improves" the pitch.

## 13. Deliberately out of scope

The README is not rewritten here — its three drifts (§2) are real and each needs its own
bead, but the page does not depend on them and coupling them would block it. No custom
domain. No blog or documentation site. No newsletter. No analytics. No animation heavier
than a CSS transition. No light theme. No generated comparison table. No change to
`docs/vision.md`, whose "not a public launch" framing the owner has now superseded in
practice and which should be updated deliberately rather than as a side effect of shipping
a page.

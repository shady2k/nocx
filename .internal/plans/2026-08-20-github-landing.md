# nocx landing page — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Publish `https://shady2k.github.io/nocx/` — one English page that reads like an edited terminal scrollback, leads with the fact that a restart no longer costs you your work, and contains no sentence the code at `v0.2.0` cannot back.

**Architecture:** A `site/` directory of hand-written static files with no build step, deployed by a dedicated GitHub Actions workflow that uploads the directory as a Pages artifact. A shell script gates the two structural defects that actually occur (root-absolute paths in a subdirectory-hosted site, missing assets) and greps for a literal list of phrasings the design forbids. Nothing enters `node_modules`, `ci.yml`, or the containerized runners.

**Tech Stack:** HTML5, CSS custom properties, zero JavaScript, zero external requests, `bash` for the gate, GitHub Actions (`upload-pages-artifact` + `deploy-pages`).

**Spec:** `.internal/specs/2026-08-20-github-landing-design.md`. Where this plan and the spec disagree, the spec is right and the plan is a defect.

## Global Constraints

- **Every internal path is relative** — `./style.css`, `./assets/hero.webp`. Never `/style.css`. The site is a Project Page under `/nocx/`.
- **Zero external requests.** No Google Fonts, no CDN, no analytics, no tracking pixel, no remote image. System font stacks only.
- **Zero JavaScript.** No `<script>` tag of any kind.
- **Dark only.** No light theme, no `prefers-color-scheme` branch.
- **Palette is `tokyo-night`, the app's default theme**, values copied verbatim from `frontend/src/styles/themes/tokyo-night.css`: canvas `#1a1b26`, chrome `#0e0f15`, surface `#1f2335`, surface-raised `#2b3049`, divider `#2a2b3d`, text `#c0caf5`, text-muted `#a9b1d6`, text-dim `#9098bd`, border `#5f6590`, accent `#7aa2f7`, success `#9ece6a`, warning `#e0af68`, danger `#f7768e`.
- **Feature claims come only from `docs/release-notes/v0.2.0.md`.** If it is not in that file, it does not go on the page.
- **The sidebar is described as local-only.** The remote helper is on unmerged branches and is not in `v0.2.0`. The page says nothing about panels over SSH.
- **`prefers-reduced-motion: reduce` disables every transition and animation**, and no meaning depends on motion.
- **Version strings are `v0.2.0`** and the release date is `2026-08-20`.
- **Forbidden phrasings** are enforced literally by `scripts/site-forbidden-phrases.txt` (Task 2). Adding a phrase there is cheap; removing one requires changing spec §7.
- **AMENDED 2026-08-20 — there are no placeholder figures.** Tasks 3 to 6 below still describe `.figure--placeholder` frames, because that is what they built and this document records what was done. They were removed afterwards, CSS included: the owner deferred the screenshots indefinitely, and a frame reading `Product screenshot` stops being a placeholder and becomes the page's impression once it is not about to be filled. **Do not rebuild them.** Task 8 restores the figure markup and the figure CSS together, with real images.

---

### Task 1: The repository gets a licence

Spec §3. This is a prerequisite, not a nicety: until `LICENSE` exists the code is legally all-rights-reserved, and the page may not describe the project as open source.

**Files:**

- Create: `LICENSE`
- Modify: `package.json` (the `"license"` field, line 22)

**Interfaces:**

- Consumes: nothing.
- Produces: the fact that `LICENSE` exists at the repository root and names MIT, which Task 6's footer copy depends on.

**Acceptance Criteria:**

- `LICENSE` exists at the repository root, is the unmodified MIT text, and carries `Copyright (c) 2026 shady2k`.
- `package.json` reports `"license": "MIT"`.
- GitHub's repository sidebar shows "MIT license" after the branch merges (verified by eye once, not automatable here).

- [ ] **Step 1: Write the licence file**

Create `LICENSE` with exactly this content, changing nothing but nothing:

```
MIT License

Copyright (c) 2026 shady2k

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 2: Correct the scaffold default in `package.json`**

`"license": "ISC"` on line 22 is npm's default, never a decision. Change it:

```json
  "license": "MIT",
```

- [ ] **Step 3: Verify both**

Run:

```bash
head -3 LICENSE && grep '"license"' package.json
```

Expected:

```
MIT License

Copyright (c) 2026 shady2k
  "license": "MIT",
```

- [ ] **Step 4: Commit**

```bash
git add LICENSE package.json
git commit -m "docs(license): nocx is MIT, and says so where it counts (nocx-knks8)"
```

The body should record that the whole `go.sum` module set was scanned for GPL/LGPL/MPL/AGPL and carries none, so the choice was unconstrained; and that the Linux AppImage's bundled GTK/WebKitGTK are LGPL-2.1+ shared libraries, which is a packaging obligation rather than a constraint on our own licence.

---

### Task 2: The gate, before the thing it gates

Spec §11. Written first so the page cannot be authored past it.

**Files:**

- Create: `scripts/check-site.sh`
- Create: `scripts/site-forbidden-phrases.txt`
- Create: `site/index.html` (skeleton only — Task 4 fills it)
- Create: `site/.nojekyll` (empty)
- Modify: `.githooks/pre-commit`

**Interfaces:**

- Consumes: nothing.
- Produces: `scripts/check-site.sh`, exit 0 when `site/` is clean and exit 1 with a `FAIL:` line naming the file otherwise. Every later task runs it.

**Acceptance Criteria:**

- The script exits 1 and names the file when an HTML file under `site/` contains `href="/…"` or `src="/…"` that is not protocol-relative.
- It exits 1 and names both file and asset when an `<img src="./assets/…">` points at a file that does not exist.
- It exits 1 and names the file and the phrase when any line matches a phrase in `site/site-forbidden-phrases.txt`, case-insensitively.
- It exits 0 on a clean `site/`.
- The pre-commit hook runs it only when something under `site/` or `scripts/check-site.sh` is staged.

- [ ] **Step 1: Write the forbidden-phrase list**

Create `scripts/site-forbidden-phrases.txt`. Every line is a literal, case-insensitive substring. Each is here because spec §7 names it, and the comment says which row.

```
# Phrasings the landing page may never contain. See spec §7 (the honesty
# ledger) in .internal/specs/2026-08-20-github-landing-design.md — each line
# below is a row of that table. Matching is literal and case-insensitive.
#
# This file cannot check that a claim is true. It refuses the specific
# wordings already known to be false, which is the failure that actually
# recurs: a later author reaches for a familiar marketing phrase without
# knowing what it costs here.
#
# Blank lines and lines starting with # are ignored.

# The build is ad-hoc signed (release.yml:211), not unsigned.
unsigned

# Reads as Apple attestation, which no part of this is.
secure updates

# An integration with a user-configured endpoint ships; a model does not.
built-in AI
works with any model
private AI

# False the moment the assistant reaches a remote endpoint.
your data never leaves
never leaves your machine
never leave your machine

# The vault rewrites our own record of a command. It makes no claim about
# PTY output, shell history, the remote host's logs, the clipboard or swap.
zero-knowledge
end-to-end encrypted
military-grade
bank-level
leak-proof
secrets never

# Not demonstrated, and the page says early release instead.
production-ready
enterprise-ready
battle-tested

# It is diff, stage, commit and log.
full git client
complete git

# The AppImage bundles GTK and WebKitGTK and still needs a kernel and glibc.
no dependencies
zero dependencies
```

- [ ] **Step 2: Write the script**

Create `scripts/check-site.sh`:

```bash
#!/usr/bin/env bash
# Structural gate for site/. Three checks, each bought by a defect with a
# known way of happening; see spec §11.
#
# What this cannot do is verify that a claim on the page is true. Check 3 is
# the closest available: it refuses wordings already established as false.
# The claims themselves are read by eye against spec §7.
#
# bash 3.2 compatible on purpose: this runs from the pre-commit hook, and
# macOS still ships bash 3.2, where `mapfile` does not exist. Filenames under
# site/ are ours and contain no newlines, so word-splitting on \n is safe here
# and buys portability that a -print0 pipeline would cost.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
site="$root/site"
phrases="$root/scripts/site-forbidden-phrases.txt"
fail=0

if [ ! -d "$site" ]; then
  echo "FAIL: $site does not exist"
  exit 1
fi

# Newline-separated, iterated with IFS set to newline only. Every loop below
# runs in THIS shell, never behind a pipe: a `while read` on the right of a |
# runs in a subshell, so `fail=1` set inside it is discarded and the script
# exits 0 while printing FAIL. That bug is silent and total.
pages="$(find "$site" -name '*.html' | sort)"

if [ -z "$pages" ]; then
  echo "FAIL: no HTML under $site"
  exit 1
fi

page_count="$(printf '%s\n' "$pages" | wc -l | tr -d ' ')"

IFS='
'

# 1. Root-absolute paths. The site is served from /nocx/, so href="/style.css"
#    resolves against the *user* page and yields an unstyled document. This is
#    the classic Project Pages failure and it is invisible in local preview,
#    where the page usually sits at the server root.
for f in $pages; do
  hits="$(grep -nE '(href|src)="/' "$f" | grep -v '="//' || true)"
  if [ -n "$hits" ]; then
    echo "FAIL: ${f#$root/} has a root-absolute path; the site is served from a subdirectory"
    printf '%s\n' "$hits" | sed 's/^/       /'
    fail=1
  fi
done

# 2. Every referenced local asset exists. A hero that 404s is worse than no
#    hero, and it survives review because the alt text still reads fine.
for f in $pages; do
  refs="$(grep -oE '(src|href)="\./[^"]+"' "$f" |
    sed -E 's/^(src|href)="\.\///; s/"$//' || true)"
  for rel in $refs; do
    [ -n "$rel" ] || continue
    if [ ! -f "$site/$rel" ]; then
      echo "FAIL: ${f#$root/} references a missing asset: $rel"
      fail=1
    fi
  done
done

# 3. Forbidden phrasings (spec §7).
if [ ! -f "$phrases" ]; then
  echo "FAIL: $phrases is missing"
  exit 1
fi

phrase_list="$(grep -vE '^\s*(#|$)' "$phrases" || true)"

for phrase in $phrase_list; do
  for f in $pages; do
    hits="$(grep -niF -- "$phrase" "$f" || true)"
    if [ -n "$hits" ]; then
      echo "FAIL: ${f#$root/} contains the forbidden phrase \"$phrase\" (spec §7)"
      printf '%s\n' "$hits" | sed 's/^/       /'
      fail=1
    fi
  done
done

unset IFS

if [ "$fail" -ne 0 ]; then
  echo
  echo "check-site failed. The honesty ledger is spec §7 in"
  echo ".internal/specs/2026-08-20-github-landing-design.md — read the row before"
  echo "editing either the page or the phrase list."
  exit 1
fi

echo "OK:   check-site ($page_count page(s))"
```

> The `IFS` assignment above is a real newline between the quotes, not `\n`.
> With `IFS` set this way, `for phrase in $phrase_list` splits on lines rather
> than on words, which is what makes multi-word phrases like
> `never leaves your machine` match as one phrase instead of four.

- [ ] **Step 3: Make it executable and create the skeleton it checks**

```bash
chmod +x scripts/check-site.sh
mkdir -p site/assets
touch site/.nojekyll
```

Create `site/index.html` as a skeleton — Task 4 replaces the body:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>nocx — a local terminal that keeps your working context</title>
    <link rel="stylesheet" href="./style.css" />
  </head>
  <body>
    <main><h1>nocx</h1></main>
  </body>
</html>
```

Create an empty `site/style.css` so the reference resolves:

```bash
touch site/style.css
```

- [ ] **Step 4: Prove each check fails before trusting that it passes**

An unexercised check is a check that does not work. Break the page three ways and watch it refuse each one.

```bash
# 3a — root-absolute path
sed -i.bak 's|href="./style.css"|href="/style.css"|' site/index.html
./scripts/check-site.sh; echo "exit=$?"
mv site/index.html.bak site/index.html
```

Expected: `FAIL: site/index.html has a root-absolute path…` and `exit=1`.

```bash
# 3b — missing asset
sed -i.bak 's|<h1>nocx</h1>|<img src="./assets/nope.png" alt="x" />|' site/index.html
./scripts/check-site.sh; echo "exit=$?"
mv site/index.html.bak site/index.html
```

Expected: `FAIL: site/index.html references a missing asset: assets/nope.png` and `exit=1`.

```bash
# 3c — forbidden phrase, single word
sed -i.bak 's|<h1>nocx</h1>|<h1>nocx is production-ready</h1>|' site/index.html
./scripts/check-site.sh; echo "exit=$?"
mv site/index.html.bak site/index.html
```

Expected: `FAIL: site/index.html contains the forbidden phrase "production-ready" (spec §7)` and `exit=1`.

```bash
# 3d — forbidden phrase, MULTI-WORD. This is the one that breaks silently:
# without IFS set to newline, `for phrase in $phrase_list` splits on spaces
# and hunts for the word "never", which matches innocent prose everywhere and
# misses the actual phrase.
sed -i.bak 's|<h1>nocx</h1>|<p>your data never leaves your machine</p>|' site/index.html
./scripts/check-site.sh; echo "exit=$?"
mv site/index.html.bak site/index.html
```

Expected: `FAIL: … contains the forbidden phrase "never leaves your machine" (spec §7)` — the whole phrase quoted, not a single word — and `exit=1`.

If any of 3a–3d prints `OK` instead, stop: the most likely cause is a loop that was moved behind a pipe, where `fail=1` is set in a subshell and discarded, so the script prints `FAIL:` lines and still exits 0.

- [ ] **Step 5: Run it clean**

```bash
./scripts/check-site.sh; echo "exit=$?"
```

Expected: `OK:   check-site (1 page(s))` and `exit=0`.

- [ ] **Step 6: Wire it into the pre-commit hook**

Read `.githooks/pre-commit` first and follow its existing shape — it already skips gates when nothing relevant is staged and prints `OK:` / `FAIL:` lines in a fixed format. Add a stanza matching that style, gated on staged paths:

```bash
if git diff --cached --name-only | grep -qE '^(site/|scripts/check-site\.sh|scripts/site-forbidden-phrases\.txt)'; then
  ./scripts/check-site.sh || exit 1
else
  echo "OK:   check-site — skipped (no site/ changes staged)"
fi
```

- [ ] **Step 7: Verify the hook runs it**

```bash
git add scripts/check-site.sh scripts/site-forbidden-phrases.txt site/ .githooks/pre-commit
git commit -m "build(site): the landing page cannot be authored past its own gate (nocx-knks8)"
```

Expected: the hook output contains `OK:   check-site (1 page(s))`.

If the commit is rejected because `.githooks` is not the active hooks path, run `make hooks` and retry — do not use `--no-verify`.

---

### Task 3: The visual system

Spec §5. All of it lives in one stylesheet; no later task adds CSS anywhere else.

**Files:**

- Modify: `site/style.css` (created empty in Task 2)

**Interfaces:**

- Consumes: nothing.
- Produces: the class vocabulary every later task writes markup against — `.block`, `.block__label`, `.lede`, `.state`, `.constraint`, `.mono`, `.cta-row`, `.cta`, `.cta--primary`, `.cta__label`, `.cta__detail`, `.figure`, `.figure--placeholder`, `.figure__frame`, `.plain`, `.plain--negative`, `.strip`, `.rule`. A later task inventing a class not in this list means this task was wrong; extend it here rather than adding a `<style>` block.

**Acceptance Criteria:**

- Every colour is one of the `tokyo-night` values in Global Constraints, declared once as a custom property on `:root` and never repeated as a literal below.
- No `@import`, no `url(http…)`, no font file reference — system stacks only.
- Sections are separated by a hairline rule and vertical space; there is no `box-shadow` and no `border-radius` above `4px` anywhere.
- At 375px wide, the page is one column and nothing scrolls horizontally.
- `@media (prefers-reduced-motion: reduce)` sets `transition: none` and `animation: none` on everything.

- [ ] **Step 1: Write the stylesheet**

Replace `site/style.css` entirely:

```css
/* nocx landing page.
 *
 * The page is a scrollback you can read: sections are blocks in one journal,
 * separated by pause and a hairline, never by cards and shadows. Two voices —
 * a grotesque for claims, monospace for anything that denotes observable
 * system state (a command, a version, a platform floor). Colour is semantic
 * exactly as in a terminal, and the palette is the app's own default theme so
 * page and product read as one thing.
 *
 * Spec: .internal/specs/2026-08-20-github-landing-design.md §5.
 */

:root {
  /* Verbatim from frontend/src/styles/themes/tokyo-night.css — the app's
   * default. Changing a value here without changing it there breaks the one
   * thing this palette is for.
   *
   * This is the subset the page uses, not the theme's whole ramp. The theme
   * also defines --chrome, --surface-raised and --danger; none has a job here
   * (the page has no window chrome, no raised control, and nothing on it
   * fails), and carrying them unused would be three values that drift out of
   * step with the app while looking authoritative. Add one when a rule needs
   * it. */
  --canvas: #1a1b26;
  --surface: #1f2335;
  --divider: #2a2b3d;
  --text: #c0caf5;
  --text-muted: #a9b1d6;
  --text-dim: #9098bd;
  --border: #5f6590;
  --accent: #7aa2f7;
  --success: #9ece6a;
  --warning: #e0af68;

  --sans: ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
  --mono: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace;

  --measure: 62ch;
  --gutter: clamp(1.25rem, 4vw, 3rem);
  --rhythm: clamp(3.5rem, 9vw, 7rem);
}

*,
*::before,
*::after {
  box-sizing: border-box;
}

html {
  -webkit-text-size-adjust: 100%;
}

body {
  margin: 0;
  background: var(--canvas);
  color: var(--text);
  font-family: var(--sans);
  font-size: clamp(1rem, 0.96rem + 0.2vw, 1.0625rem);
  line-height: 1.65;
  -webkit-font-smoothing: antialiased;
}

main {
  max-width: 72rem;
  margin: 0 auto;
  padding: 0 var(--gutter);
}

/* --- the block: one hairline axis, metadata, then content ---------------- */

.block {
  position: relative;
  padding: var(--rhythm) 0 0 clamp(1rem, 3vw, 2.25rem);
  border-left: 1px solid var(--divider);
}

.block::before {
  /* The prompt marker. Purely decorative — every block also carries a real
   * label in .block__label, so a reader who never sees this loses nothing. */
  content: '';
  position: absolute;
  left: -3px;
  top: calc(var(--rhythm) + 0.62em);
  width: 5px;
  height: 5px;
  background: var(--accent);
}

.block__label {
  font-family: var(--mono);
  font-size: 0.8125rem;
  letter-spacing: 0.06em;
  color: var(--text-dim);
  margin: 0 0 0.75rem;
  text-transform: none;
}

.block h2 {
  font-size: clamp(1.65rem, 1.2rem + 2vw, 2.5rem);
  line-height: 1.15;
  letter-spacing: -0.02em;
  margin: 0 0 1rem;
  max-width: 20ch;
  font-weight: 620;
}

.block h3 {
  font-size: 1.0625rem;
  font-weight: 620;
  margin: 2rem 0 0.35rem;
}

.lede {
  max-width: var(--measure);
  color: var(--text-muted);
  margin: 0 0 1.25rem;
}

.block p,
.block li {
  max-width: var(--measure);
}

/* Monospace means "observable system state", never emphasis. */
code,
kbd,
.mono {
  font-family: var(--mono);
  font-size: 0.9em;
}

code {
  color: var(--text);
  background: var(--surface);
  padding: 0.1em 0.35em;
  border-radius: 3px;
}

pre {
  font-family: var(--mono);
  font-size: 0.875rem;
  background: var(--surface);
  border: 1px solid var(--divider);
  border-radius: 4px;
  padding: 1rem 1.15rem;
  overflow-x: auto;
  max-width: var(--measure);
  line-height: 1.6;
}

pre code {
  background: none;
  padding: 0;
}

a {
  color: var(--accent);
  text-underline-offset: 0.2em;
}

a:hover {
  text-decoration-thickness: 2px;
}

.rule {
  border: 0;
  border-top: 1px solid var(--divider);
  margin: var(--rhythm) 0 0;
}

/* --- constraints: the reader must not be able to miss these -------------- */

.constraint {
  font-family: var(--mono);
  font-size: 0.8125rem;
  line-height: 1.55;
  color: var(--warning);
  border-left: 2px solid var(--warning);
  padding: 0.15rem 0 0.15rem 0.75rem;
  margin: 0.5rem 0 0;
  max-width: var(--measure);
}

.state {
  font-family: var(--mono);
  font-size: 0.8125rem;
  color: var(--text-dim);
  margin: 0.5rem 0 0;
}

.state strong {
  color: var(--success);
  font-weight: inherit;
}

/* --- calls to action ----------------------------------------------------- */

.cta-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  margin: 1.75rem 0 0;
}

.cta {
  display: inline-flex;
  flex-direction: column;
  gap: 0.15rem;
  padding: 0.7rem 1.1rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  text-decoration: none;
  transition:
    border-color 120ms ease,
    background-color 120ms ease;
}

.cta:hover {
  border-color: var(--accent);
  background: var(--surface);
}

.cta--primary {
  border-color: var(--accent);
}

.cta__label {
  font-weight: 620;
}

.cta__detail {
  font-family: var(--mono);
  font-size: 0.75rem;
  color: var(--text-dim);
}

/* --- figures: evidence, not decoration ----------------------------------- */

.figure {
  margin: 2rem 0 0;
  max-width: 100%;
}

.figure img {
  display: block;
  width: 100%;
  height: auto;
  border: 1px solid var(--divider);
  border-radius: 4px;
}

.figure figcaption {
  font-family: var(--mono);
  font-size: 0.8125rem;
  color: var(--text-dim);
  margin: 0.6rem 0 0;
  max-width: var(--measure);
}

/* A placeholder is honestly a placeholder. It never imitates the product —
 * a drawn fake UI is the one thing worse than an empty frame. */
.figure--placeholder .figure__frame {
  display: grid;
  place-items: center;
  border: 1px dashed var(--border);
  border-radius: 4px;
  background: var(--surface);
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 0.8125rem;
  text-align: center;
  padding: 1rem;
}

/* --- the technical strip -------------------------------------------------- */

.strip {
  font-family: var(--mono);
  font-size: 0.8125rem;
  color: var(--text-dim);
  line-height: 1.9;
  max-width: 72ch;
}

.strip dt {
  color: var(--text-muted);
}

.strip dd {
  margin: 0 0 0.6rem;
}

/* --- lists ---------------------------------------------------------------- */

.plain {
  list-style: none;
  padding: 0;
  margin: 1rem 0 0;
}

.plain li {
  padding: 0 0 0 1.25rem;
  position: relative;
  margin: 0 0 0.5rem;
}

.plain li::before {
  content: '—';
  position: absolute;
  left: 0;
  color: var(--text-dim);
}

.plain--negative li::before {
  content: '\00d7';
  color: var(--text-dim);
}

/* --- footer --------------------------------------------------------------- */

footer {
  margin: var(--rhythm) 0 0;
  padding: 2rem 0 4rem;
  border-top: 1px solid var(--divider);
  font-family: var(--mono);
  font-size: 0.8125rem;
  color: var(--text-dim);
}

footer a {
  color: var(--text-muted);
}

/* --- narrow --------------------------------------------------------------- */

@media (max-width: 40rem) {
  .block {
    padding-left: 1rem;
  }

  .cta {
    width: 100%;
  }
}

@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    transition: none !important;
    animation: none !important;
    scroll-behavior: auto !important;
  }
}
```

- [ ] **Step 2: Verify no colour literal escaped the token block**

Every `#rrggbb` in the file must be on a line that declares a custom property. Anything else is a colour written twice, which is how a palette stops matching the app.

```bash
grep -nE '#[0-9a-fA-F]{6}' site/style.css | grep -vE '^\s*[0-9]+:\s*--'
```

Expected: **no output**. If a line prints, replace its literal with the matching `var(--…)`.

Then confirm the reverse — that no token is declared and never used:

```bash
for t in canvas surface divider text text-muted text-dim border accent success warning; do
  n=$(grep -c "var(--$t)" site/style.css || true)
  [ "$n" -eq 0 ] && echo "UNUSED: --$t"
done; echo "done"
```

Expected: `done` with no `UNUSED:` lines.

- [ ] **Step 3: Verify there is no external request**

```bash
grep -nE '@import|url\(|https?://' site/style.css
```

Expected: no output.

- [ ] **Step 4: Run the gate and commit**

```bash
./scripts/check-site.sh
npx prettier --write site/style.css
git add site/style.css
git commit -m "feat(site): the page speaks in the product's own palette and two voices (nocx-knks8)"
```

---

### Task 4: The head and the hero

Spec §6.1. Everything a visitor needs in order to decide, above the fold, including the two things that will otherwise surprise them.

**Files:**

- Modify: `site/index.html`
- Create: `site/assets/icon.png` (copied from `build/appicon.png`)

**Interfaces:**

- Consumes: the class vocabulary from Task 3.
- Produces: `<main>` containing `<header class="block">` and the download row. Task 5 appends its sections after it, inside the same `<main>`.

**Acceptance Criteria:**

- The positioning line appears once, verbatim: "A local terminal that keeps your working context — tabs, their output, notes and secret references survive a restart."
- Both download links point at `https://github.com/shady2k/nocx/releases/latest`.
- The macOS button carries `ad-hoc signed, not notarized` and the Linux button carries `x86_64 AppImage · glibc 2.35+` as visible text, not a tooltip.
- `v0.2.0 · released 2026-08-20 · early release, no formal support` is visible within the hero, not in the footer.
- `./scripts/check-site.sh` exits 0.

- [ ] **Step 1: Copy the icon**

```bash
cp build/appicon.png site/assets/icon.png
```

- [ ] **Step 2: Write the head and hero**

Replace `site/index.html` entirely:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>nocx — a local terminal that keeps your working context</title>
    <meta
      name="description"
      content="A local terminal that keeps your working context: tabs, their output, notes and secret references survive a restart. macOS and Linux, no account, no cloud service of its own, no product telemetry."
    />
    <link rel="icon" href="./assets/icon.png" />
    <link rel="stylesheet" href="./style.css" />
  </head>

  <body>
    <main>
      <header class="block">
        <p class="block__label">nocx</p>
        <h2>A local terminal that keeps your working context</h2>
        <p class="lede">
          Tabs, their output, notes and secret references survive a restart. Go backend, xterm.js on
          WebGL, one WebSocket. macOS and Linux.
        </p>

        <div class="cta-row">
          <a class="cta cta--primary" href="https://github.com/shady2k/nocx/releases/latest">
            <span class="cta__label">Download for macOS</span>
            <span class="cta__detail"> universal .dmg · ad-hoc signed, not notarized </span>
          </a>
          <a class="cta" href="https://github.com/shady2k/nocx/releases/latest">
            <span class="cta__label">Download for Linux</span>
            <span class="cta__detail">x86_64 AppImage · glibc 2.35+</span>
          </a>
          <a class="cta" href="https://github.com/shady2k/nocx">
            <span class="cta__label">Source on GitHub</span>
            <span class="cta__detail">MIT</span>
          </a>
        </div>

        <p class="state">
          <strong>v0.2.0</strong> · released 2026-08-20 · early release, no formal support
        </p>

        <p class="constraint">
          macOS quarantines the download because there is no Apple Developer ID and no notarization.
          Clearing it is one command, once — it is in
          <a href="#install">Install</a>, in full, before you download.
        </p>

        <figure class="figure figure--placeholder">
          <div class="figure__frame" style="aspect-ratio: 16 / 10">
            Product screenshot — the whole window: tab strip with a workspace pill, several command
            blocks, the sidebar, the prompt
          </div>
          <figcaption>nocx running an agent TUI, theme Tokyo Night.</figcaption>
        </figure>
      </header>

      <hr class="rule" />
    </main>
  </body>
</html>
```

- [ ] **Step 3: Run the gate**

```bash
./scripts/check-site.sh; echo "exit=$?"
```

Expected: `OK:   check-site (1 page(s))` and `exit=0`.

- [ ] **Step 4: Look at it**

```bash
python3 -m http.server 8000 --directory site
```

Open `http://localhost:8000/`, then narrow the window to 375px and confirm one column and no horizontal scrollbar. Stop the server with Ctrl-C.

- [ ] **Step 5: Commit**

```bash
npx prettier --write site/index.html
git add site/index.html site/assets/icon.png
git commit -m "feat(site): the hero says what nocx does and what will surprise you (nocx-knks8)"
```

---

### Task 5: The six content blocks

Spec §6, sections 2–7. Every claim traces to `docs/release-notes/v0.2.0.md`; nothing else may be added.

**Files:**

- Modify: `site/index.html` (append inside `<main>`, after the `<hr class="rule" />` from Task 4)

**Interfaces:**

- Consumes: Task 3's classes; Task 4's `<main>`.
- Produces: sections with `id` attributes `resume`, `workspace`, `secrets`, `ask`, `edit`, `connect`, which Task 6's footer and Task 4's constraint link may reference.

**Acceptance Criteria:**

- Section `resume` says "tabs and their captured output" and nowhere claims a process or a TUI resumes.
- Section `workspace` describes Files, Git, Ports and Notes as the local machine, and the word SSH does not appear in it.
- Section `secrets` says the value goes to the vault and the command keeps a reference, and makes no claim about PTY output, shell history, the remote host, the clipboard or disk beyond that.
- Section `ask` states, in its own body, that the assistant sends the question and the text of explicitly attached frames to an endpoint the reader configures, and that a question with no attached frames carries only the question.
- Section `connect` names `~/.ssh/config` and a Tabby export as import sources and does not describe nocx as a replacement for a dedicated SSH manager.
- `./scripts/check-site.sh` exits 0.

- [ ] **Step 1: Append the six blocks**

Insert immediately after the `<hr class="rule" />` that Task 4 left at the end of `<main>`:

```html
<section class="block" id="resume">
  <p class="block__label">01 / resume</p>
  <h2>Close it. Open it. Keep working.</h2>
  <p class="lede">
    A restart used to cost you everything on screen. Now the tabs come back, each still showing the
    blocks it had, and the tab that was in front is the tab you land on. The window's size and
    position return with them, along with the sidebar's width and the panel you had open.
  </p>
  <p class="state">
    What returns is <strong>tabs and the output they captured</strong> — not running processes. A
    program that was mid-run is not resumed; what it printed is still there to read.
  </p>
  <p>
    Prefer a clean start? Turn it off in Settings under Interface and a restart gives you one fresh
    tab, as before.
  </p>

  <figure class="figure figure--placeholder">
    <div class="figure__frame" style="aspect-ratio: 16 / 10">
      Product screenshot — the same window after a restart, for a genuine before and after
    </div>
    <figcaption>The same session, reopened.</figcaption>
  </figure>
</section>

<hr class="rule" />

<section class="block" id="workspace">
  <p class="block__label">02 / workspace</p>
  <h2>Your terminal becomes a place you keep things</h2>
  <p class="lede">
    A workspace is a set of tabs with a name and a colour, made from the tab strip's menu and
    arriving with its first tab open. Each is a pill in the strip: one click switches, right-click
    renames, recolours or closes. Closing asks first, tells you how many tabs go with it, and names
    anything still running.
  </p>

  <h3>Four panels, about the machine the tab is on</h3>
  <ul class="plain">
    <li>
      <strong>Files</strong> shows the directory the tab is in and follows it —
      <code>cd</code> somewhere and the tree moves, with no click and no tab switch.
    </li>
    <li>
      <strong>Git</strong> is the repository the tab is in: changed files with their line counts,
      staging and unstaging, a commit form, and the log. It refuses what git refuses, visibly.
    </li>
    <li>
      <strong>Ports</strong> lists what something in the tab is listening on, and the tunnels you
      have forwarded.
    </li>
    <li>
      <strong>Notes</strong> is a library. Press the shortcut and you are typing; the note opens as
      a tab, saves as you pause, and is found later by any word inside it, not only its title.
    </li>
  </ul>

  <h3>History that outlives the window</h3>
  <p>
    Commands you run are stored on disk, encrypted. Press Up at the prompt and your history is there
    after a restart, not only within the session. The recall panel searches it and tells you how far
    back the store goes, rather than quietly returning less than you expected. Retention in days, a
    disk ceiling and a per-command output cap all live in Settings; recording output is a separate
    switch from recording commands.
  </p>

  <figure class="figure figure--placeholder">
    <div class="figure__frame" style="aspect-ratio: 16 / 10">
      Product screenshot — the sidebar with the Git panel open, changed files and the commit form
      visible
    </div>
    <figcaption>The Git panel, on the repository the tab is in.</figcaption>
  </figure>
</section>

<hr class="rule" />

<section class="block" id="secrets">
  <p class="block__label">03 / secrets</p>
  <h2>A reference where the key used to be</h2>
  <p class="lede">
    Type a key into the prompt and run it, and the block offers to store it. The value goes into the
    vault and the command keeps a reference where the secret was. Recall that command later and the
    reference comes back, rendered as a chip; run it and the vault asks you to unlock before
    substituting the real value. You never have to find a settings page first.
  </p>
  <p class="state">
    Precisely: once a secret is saved,
    <strong>nocx replaces its value in its own record of that command</strong>
    with a reference, and the value lives in the vault. That is a claim about our record of the
    command and nothing else.
  </p>
  <p>Connection passwords and key passphrases live there too.</p>

  <figure class="figure figure--placeholder">
    <div class="figure__frame" style="aspect-ratio: 16 / 9">
      Product screenshot — a command block showing a reference chip where the secret was
    </div>
    <figcaption>The stored command, with the value gone from it.</figcaption>
  </figure>
</section>

<hr class="rule" />

<section class="block" id="ask">
  <p class="block__label">04 / ask</p>
  <h2>Ask about the output, without leaving the scrollback</h2>
  <p class="lede">
    Select the output of a finished command and a chip naming that command appears in your input
    line, so you can see what you are about to send while you type the question. Beside the prompt
    is a switch reading Run; flip it, or press the shortcut, and the line goes to the assistant
    instead of the shell. The answer streams back as a block in the same scrollback, your question
    as its header. There is no chat pane and no mode to leave.
  </p>

  <h3>What is sent, exactly</h3>
  <p>
    nocx ships an integration, not a model: you configure the endpoint under Settings → Assistant →
    Endpoints, and until you do, the surfaces say so rather than failing when you ask. What goes to
    that endpoint is a system rule stating that frame content is data rather than instructions, your
    question, and the text of the frames you attached, labelled as data.
    <strong class="mono">A question with no attached frames carries only the question.</strong>
    Retention is your provider's, not ours.
  </p>
  <p class="constraint">
    Over plain http, nocx will only reach loopback and private addresses, and it checks the address
    it actually resolved and connected to on every request and every redirect — not the text you
    typed. Credentials are dropped on any change of scheme, host or port.
  </p>

  <figure class="figure figure--placeholder">
    <div class="figure__frame" style="aspect-ratio: 16 / 9">
      Product screenshot — an assistant answer as a block, with the command chip and the Run switch
      visible
    </div>
    <figcaption>The question, the attached command, and the answer — one flow.</figcaption>
  </figure>
</section>

<hr class="rule" />

<section class="block" id="edit">
  <p class="block__label">05 / edit</p>
  <h2>The command line is an editor</h2>
  <p class="lede">
    The prompt is built on CodeMirror. Shell syntax is highlighted as you type, Tab completes
    commands and paths — directories complete with a trailing slash and Tab again walks into them —
    and the top candidate appears as ghost text that Right accepts. When nothing matches, the panel
    says so instead of going blank.
  </p>
  <p>
    Enter takes the highlighted completion and stops, leaving the command in the line where you can
    read it before running it. Tab, Right or End walk into it.
  </p>
  <p>
    Snippets are the other half: save a phrase once and fire it into whatever is currently taking
    input, including a program in the middle of reading stdin. A multi-line body is refused when the
    program has not enabled bracketed paste, rather than being pasted line by line into something
    that cannot handle it.
  </p>
</section>

<hr class="rule" />

<section class="block" id="connect">
  <p class="block__label">06 / connect</p>
  <h2>Your hosts, brought in rather than retyped</h2>
  <p class="lede">
    Import existing connections from <code>~/.ssh/config</code> or a Tabby export. Passwords and key
    passphrases go to the vault with everything else, so a connection references a secret rather
    than carrying one.
  </p>
</section>

<hr class="rule" />
```

- [ ] **Step 2: Run the gate**

```bash
./scripts/check-site.sh; echo "exit=$?"
```

Expected: `OK:` and `exit=0`. If it names a forbidden phrase, the copy is wrong — read the spec §7 row and rewrite the sentence. Do not edit the phrase list.

- [ ] **Step 3: Check the claims against the release notes, by eye**

Open `docs/release-notes/v0.2.0.md` beside the page and confirm every factual sentence traces to it. This is the check the script cannot do.

- [ ] **Step 4: Commit**

```bash
npx prettier --write site/index.html
git add site/index.html
git commit -m "feat(site): six blocks, each tracing to the release notes (nocx-knks8)"
```

---

### Task 6: What it does not do, how to install it, and the footer

Spec §6.8–§6.11, §8, §9. This is where the page earns its positioning without naming anyone else.

**Files:**

- Modify: `site/index.html`

**Interfaces:**

- Consumes: Task 3's classes, Task 5's section ids.
- Produces: `id="install"`, referenced by Task 4's hero constraint.

**Acceptance Criteria:**

- A section states, as a positive list, that there is no mandatory account, no cloud service of its own, no product telemetry and no cloud sync.
- The telemetry claim is scoped to the application, not to the website or to GitHub.
- The install section contains the `xattr` command and the `chmod +x` line in full, and links ADR-0003 for the distribution reasoning and the README for rollback.
- The update sentence says the manifest is verified with an embedded Ed25519 key and that the app carries no Developer ID signature and is not notarized.
- There is no comparison table and no roadmap anywhere on the page.
- `./scripts/check-site.sh` exits 0.

- [ ] **Step 1: Append the closing blocks**

After the final `<hr class="rule" />` from Task 5:

```html
<section class="block" id="boundaries">
  <p class="block__label">07 / boundaries</p>
  <h2>What nocx deliberately doesn't do</h2>
  <p class="lede">These are not gaps waiting to be filled. They are the shape of the product.</p>
  <ul class="plain plain--negative">
    <li>No account. There is nothing to sign up for and nothing to sign in to.</li>
    <li>
      No cloud service of its own. nocx runs on your machine; the network it touches is the network
      you point it at — the hosts you connect to, the assistant endpoint you configure, and a check
      for its own updates.
    </li>
    <li>
      No product telemetry. The application reports no usage, and there is no analytics on this page
      either. GitHub keeps its own ordinary server logs for downloads and for this site; that is
      theirs, and we do not pretend otherwise.
    </li>
    <li>No cloud sync. Your settings, history and vault stay where they are.</li>
  </ul>
</section>

<hr class="rule" />

<section class="block" id="install">
  <p class="block__label">08 / install</p>
  <h2>Installing, and what to expect</h2>

  <h3>macOS</h3>
  <p>
    Download the <code>.dmg</code>, open it, drag nocx into Applications. There is no Apple
    Developer ID, so the build is ad-hoc signed rather than signed by an identity Apple can attest,
    and it is not notarized — macOS quarantines it on download. Clear that once, on first install:
  </p>
  <pre><code>xattr -dr com.apple.quarantine /Applications/nocx.app</code></pre>
  <p>
    Then open nocx normally. Later in-app updates fetch the build directly and do not re-quarantine
    it. The reasoning behind shipping this way is written down in
    <a
      href="https://github.com/shady2k/nocx/blob/main/docs/decisions/0003-distribution-without-a-developer-id.md"
      >ADR-0003</a
    >.
  </p>

  <h3>Linux</h3>
  <p>Download the <code>.AppImage</code>, make it executable, run it:</p>
  <pre><code>chmod +x nocx-*-linux-amd64.AppImage
./nocx-*-linux-amd64.AppImage</code></pre>
  <p>
    It bundles its own GTK 3 and WebKitGTK, so it does not need the host's, and it links against
    glibc 2.35 — the floor set by building on Ubuntu 22.04. That covers Ubuntu 22.04+, Debian 12+,
    Fedora 39+, RHEL 9+ and Arch. It is not "runs everywhere": if it refuses to start and
    <code>ldd --version</code> reports a glibc before 2.35, your distribution is below the floor.
  </p>

  <h3>Updates</h3>
  <p>
    nocx verifies its update manifest with an embedded Ed25519 public key. That protects the
    integrity of its own update channel; it is not a signature Apple attests, and the app is neither
    Developer ID signed nor notarized. If an update goes wrong, the previous version is kept beside
    the active one and the recovery commands are in the
    <a href="https://github.com/shady2k/nocx#rollback-procedures">README</a>.
  </p>
</section>

<hr class="rule" />

<section class="block" id="built">
  <p class="block__label">09 / built</p>
  <h2>How it is put together</h2>
  <dl class="strip">
    <dt>Backend</dt>
    <dd>Go — PTY, SSH, session, transport, settings. One core, several build targets.</dd>
    <dt>Frontend</dt>
    <dd>
      xterm.js on WebGL for the terminal, CodeMirror 6 for the prompt, SolidJS for the surrounding
      UI.
    </dd>
    <dt>Desktop shell</dt>
    <dd>Wails v3, embedding the backend locally.</dd>
    <dt>Transport</dt>
    <dd>
      One WebSocket: a raw binary data plane for terminal bytes, JSON-RPC 2.0 for control. Terminal
      bytes are never wrapped in JSON.
    </dd>
    <dt>Platforms</dt>
    <dd>macOS (universal) and Linux x86_64.</dd>
  </dl>
</section>

<hr class="rule" />

<section class="block" id="get">
  <p class="block__label">10 / get</p>
  <h2>Try it</h2>
  <div class="cta-row">
    <a class="cta cta--primary" href="https://github.com/shady2k/nocx/releases/latest">
      <span class="cta__label">Download for macOS</span>
      <span class="cta__detail">universal .dmg · ad-hoc signed, not notarized</span>
    </a>
    <a class="cta" href="https://github.com/shady2k/nocx/releases/latest">
      <span class="cta__label">Download for Linux</span>
      <span class="cta__detail">x86_64 AppImage · glibc 2.35+</span>
    </a>
  </div>
  <p class="state">
    <strong>v0.2.0</strong> · released 2026-08-20 · early release, no formal support
  </p>
</section>

<footer>
  <p>
    nocx is MIT licensed ·
    <a href="https://github.com/shady2k/nocx">Source</a> ·
    <a href="https://github.com/shady2k/nocx/releases">Releases</a> ·
    <a href="https://github.com/shady2k/nocx/blob/main/docs/release-notes/v0.2.0.md"
      >What changed in v0.2.0</a
    >
  </p>
</footer>
```

- [ ] **Step 2: Confirm what is absent**

```bash
grep -niE 'warp|tabby|ghostty|wezterm|iterm|kitty|roadmap|coming soon|planned' site/index.html
```

Expected: no output. The page names no competitor and promises no future.

- [ ] **Step 3: Run the gate and look at it**

```bash
./scripts/check-site.sh && python3 -m http.server 8000 --directory site
```

Read the whole page top to bottom once, as a stranger. Stop the server.

- [ ] **Step 4: Commit**

```bash
npx prettier --write site/index.html
git add site/index.html
git commit -m "feat(site): the boundaries are the product, and the install page says what will happen (nocx-knks8)"
```

---

### Task 7: Deploy

Spec §4.

**Files:**

- Create: `.github/workflows/pages.yml`

**Interfaces:**

- Consumes: `site/` as produced by Tasks 2–6.
- Produces: a published page at `https://shady2k.github.io/nocx/`.

**Acceptance Criteria:**

- The workflow triggers on `push` to `main` limited to `site/**` and on `workflow_dispatch`.
- It has no build step and installs nothing.
- It declares `pages: write` and `id-token: write`, and a `concurrency` group so two pushes cannot race.
- It runs `scripts/check-site.sh` before uploading, so a broken page cannot deploy.
- `ci.yml` and `release.yml` are unmodified.

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/pages.yml`:

```yaml
# The landing page. No build step by design: site/ is hand-written static
# files, and keeping npm out of this job keeps it clear of the containerized
# runners that already serialize on one Docker daemon (nocx-x6z3).
name: pages

on:
  push:
    branches: [main]
    paths:
      - 'site/**'
      - 'scripts/check-site.sh'
      - 'scripts/site-forbidden-phrases.txt'
      - '.github/workflows/pages.yml'
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

# One deployment at a time. Queue rather than cancel: a superseded run has
# already been merged to main, so its content is not stale, it is early.
concurrency:
  group: pages
  cancel-in-progress: false

jobs:
  deploy:
    runs-on: ubuntu-24.04
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - uses: actions/checkout@v4

      # The same gate the pre-commit hook runs. A page that fails it must not
      # reach the public URL just because someone bypassed a local hook.
      - name: check-site
        run: ./scripts/check-site.sh

      - uses: actions/configure-pages@v5

      - uses: actions/upload-pages-artifact@v3
        with:
          path: site/

      - id: deployment
        uses: actions/deploy-pages@v4
```

- [ ] **Step 2: Verify nothing else was touched**

```bash
git status --short .github/workflows/
```

Expected: only `?? .github/workflows/pages.yml`.

- [ ] **Step 3: Commit**

```bash
npx prettier --write .github/workflows/pages.yml
git add .github/workflows/pages.yml
git commit -m "build(site): the page deploys itself, and refuses to if the gate is red (nocx-knks8)"
```

- [ ] **Step 4: Hand the owner the one thing they must do**

Nothing in the repository can enable Pages. Report to the owner, verbatim:

> Settings → Pages → Source: **GitHub Actions**. Once, before the first push to `main`.

---

### Task 8: Real screenshots replace the placeholders

Spec §10. Blocked on the owner; everything before it ships without it.

**Files:**

- Create: `site/assets/hero.png`, `site/assets/hero.webp`
- Create: `site/assets/resume.png`, `site/assets/resume.webp`
- Create: `site/assets/sidebar-git.png`, `site/assets/sidebar-git.webp`
- Create: `site/assets/vault-chip.png`, `site/assets/vault-chip.webp`
- Create: `site/assets/assistant-block.png`, `site/assets/assistant-block.webp`
- Create: `site/assets/og.png`
- Modify: `site/index.html`

**Interfaces:**

- Consumes: nothing — the placeholder figures and every `.figure*` rule were **removed**
  in `site: the page has no frames waiting to be filled` once the screenshots became
  indefinite. This task restores **both the markup and the CSS**; the stylesheet block is
  in that commit's parent and the markup shape is below.
- Produces: the finished page.

**Acceptance Criteria:**

- The `.figure`, `.figure img`, `.figure figcaption` rules are restored to `site/style.css`, and every screenshot is a `<picture>` with a WebP source and a PNG fallback.
- The hero image is at most 400 KB after compression.
- No frame shows a real hostname, a username in a path, a key, or the contents of a private repository.
- `og.png` is 1200×630 and referenced from `<head>` with `og:image`, `og:title`, `og:description` and `twitter:card`.
- `./scripts/check-site.sh` exits 0 — which now also proves every asset exists.

- [ ] **Step 1: Collect the frames from the owner**

The brief is spec §10. Each frame is captured on macOS with theme Tokyo Night, at 1440×900 logical on a retina display, giving 2880×1800 PNG.

- [ ] **Step 2: Scrub before anything else**

Open every frame and look for a real hostname, a username in a path, an API key, a token in scrollback, and any private repository name. A leak on a public page is not recoverable by deleting the file — it is cached.

- [ ] **Step 3: Produce WebP and check the weight**

This environment has no image tooling at all — no ImageMagick, no `cwebp`, no Pillow. The
icon was resized with a one-off `npx sharp-cli`, and that is the route here too.

```bash
cd site/assets
for f in hero resume sidebar-git vault-chip assistant-block; do
  npx --yes sharp-cli --input "$f.png" --output "$f.webp" --format webp --quality 82
done
ls -lh *.webp *.png | awk '{print $5, $9}'
```

Expected: `hero.webp` at or under 400K. If it is larger, drop `-q` to 75 and re-measure; do not ship a heavier hero.

- [ ] **Step 4: Replace one placeholder, exactly this shape**

For the hero, replace the whole `<figure class="figure figure--placeholder">…</figure>` with:

```html
<figure class="figure">
  <picture>
    <source srcset="./assets/hero.webp" type="image/webp" />
    <img
      src="./assets/hero.png"
      width="2880"
      height="1800"
      alt="The nocx window: a tab strip with a workspace pill, several command blocks with their output, the sidebar, and the prompt"
    />
  </picture>
  <figcaption>nocx running an agent TUI, theme Tokyo Night.</figcaption>
</figure>
```

Repeat for `resume`, `sidebar-git`, `vault-chip` and `assistant-block`, keeping each existing `<figcaption>` and writing an `alt` that describes what is in the frame rather than repeating the caption.

- [ ] **Step 5: Add the social card**

In `<head>`, after the description:

```html
<meta property="og:type" content="website" />
<meta property="og:title" content="nocx — a local terminal that keeps your working context" />
<meta
  property="og:description"
  content="Tabs, their output, notes and secret references survive a restart. macOS and Linux."
/>
<meta property="og:image" content="https://shady2k.github.io/nocx/assets/og.png" />
<meta property="og:url" content="https://shady2k.github.io/nocx/" />
<meta name="twitter:card" content="summary_large_image" />
```

`og:image` is absolute because a crawler resolves it outside the page's context; `check-site.sh` allows it because it only rejects root-absolute paths, not full URLs. Confirm that after editing.

- [ ] **Step 6: Verify no placeholder survived**

```bash
grep -c 'figure--placeholder' site/index.html
```

Expected: `0`.

- [ ] **Step 7: Run the gate and commit**

```bash
./scripts/check-site.sh; echo "exit=$?"
npx prettier --write site/index.html
git add site/index.html site/assets/
git commit -m "feat(site): the screenshots are the argument, so the page now makes it (nocx-knks8)"
```

---

## Ordering

Tasks 1–7 are strictly sequential: each edits the file the next one reads. Task 8 depends on Task 6 and on the owner, and is the only task that may sit unclaimed while the rest merges — the page is publishable with placeholders, and a placeholder that says `Product screenshot` is honest in a way a drawn imitation would not be.

```
1 → 2 → 3 → 4 → 5 → 6 → 7
                     ↘ 8 (also blocked on the owner's frames)
```

## What this plan does not build

Named so it is a decision rather than an omission:

- **The Russian page.** Spec §12 — it is translated from the settled English page, in its own plan.
- **A competitor table.** Spec §8 — dropped with reasons, not deferred by accident.
- **A roadmap.** Spec §9.
- **README changes.** Its drifts were fixed separately in `afb53074`; nothing further is needed for the page.
- **A custom domain, a documentation site, a blog, a newsletter, analytics, a light theme, or any JavaScript.** Spec §13 and Global Constraints.
- **`docs/vision.md`.** Spec §13: its "personal / small-team tool, not a public launch" framing is superseded in practice by publishing this page, and it should be updated deliberately rather than as a side effect. That is its own bead, and this plan does not touch it.

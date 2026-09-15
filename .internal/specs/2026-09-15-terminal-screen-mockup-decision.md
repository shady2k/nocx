# The terminal screen follows the mockups — decision and plan

- **Status:** owner decision 2026-09-15 ("follow the mockups"); analysis by a read-only codex consult (gpt-6-astra), adopted by the coordinator
- **Supersedes:** the passages listed in §2 of both `2026-09-14-terminal-screen-visual-register-design.md` and `2026-09-15-terminal-screen-register-mockup-pass.md`
- **Policies adopted where the consult asked the owner:** the running process bar is shown for ordinary running commands and omitted in alternate screen, accepting its row cost; the bar's Stop keeps the stop owner and Ctrl+C is labelled on a separate Interrupt action; renderer font/cell pitch and the ANSI palette stay out of this pass (font choice pending with the owner).

## Decision

Follow the composition of `01-command-states.png` and `03-split-and-files.png`: history starts below a context strip; completed blocks are unboxed, inset transcript entries; failure marks the output; an idle pane has an inset composer card containing a field; a running pane exposes input and process controls. `02-fullscreen-tui.png` governs alternate-screen presentation. The background-request image governs attention and keyboard ownership, not a second competing block design.

The present implementation does **not** have that composition. It has bottom-aligned history, no context strip, edge-to-edge separators and failure panels, a full-width composer row with one outlined field, and no running footer. Those are explicit outcomes of the two written specs, not five missing CSS adjustments. Changing fonts alone cannot solve them.

This is a read-only consultation. No code, tests, builds, containers, tracker records or commits were changed. Recommendations below describe subsequent implementation work.

### Evidence and measurement convention

I inspected all ten supplied current/mockup image files, the two specifications, the named implementation files, the kit inventory/rules, ADR-0008, and the scrollback controller and renderer font configuration. The owner capture is **2822 × 1910**, including browser/window chrome; the scripted captures are **1400 × 900**; the three principal mockups are **1586 × 992**. Comparing their raw pixel sizes directly would produce the wrong type scale.

In the principal mockups the body resembles roughly 17–18 image-pixel monospace type, with approximately 23–24 px between output baselines. Normalize that to the requested **14 px body**: multiply image distances by approximately **0.8**. Raster font identification and inferred CSS font size are estimates, not recoverable design metadata. Edge coordinates below are image measurements, approximately ±2 px; normalized implementation values are deliberate rounded choices, not claims that the image specifies CSS.

Useful anchors:

| Feature                  | Image measurement                          | Normalized target at 14 px body                                    |
| ------------------------ | ------------------------------------------ | ------------------------------------------------------------------ |
| `01` tab strip           | y=0–54                                     | 42–44 px                                                           |
| `01` context strip       | y=54–106                                   | 40–42 px                                                           |
| `01` first prompt        | near x=32, y=130                           | 24 px inline gutter; prompt begins about 12 px below context strip |
| `01` transcript rules    | x≈32–1486, y≈280 and 479; pane ends x≈1516 | inset about 24 px at both ends                                     |
| `01` failure bar         | x≈29–34, y≈353–464                         | 4 px bar, covering output only                                     |
| `01` running footer      | y≈923–992                                  | 54–56 px                                                           |
| `01` Stop button         | about 106 × 41 px                          | about 84 × 32 px                                                   |
| `03` pane headers        | y≈55–112                                   | 44 px                                                              |
| `03` first local divider | x≈24–556, y≈252                            | about 20 px side inset in this narrower pane                       |
| `03` composer card       | left x≈13–568, y≈865–975                   | 10–12 px external gutter; 88–92 px card height                     |
| `03` field               | left x≈27–554, y≈912–958                   | 38 px height; 12 px card padding                                   |
| `03` Run segment         | x≈28–103                                   | about 60 px                                                        |
| `03` submit button       | x≈516–546, y≈920–950                       | 24 px square, with 6 px clearance                                  |
| Output baseline pitch    | approximately 23–24 px                     | approximately 19–20 px                                             |

The owner capture has roughly **45% of the terminal pane's height empty above its first visible block**. This is not an empty session: `ls` and a failed `p` are already present. In the scripted running capture, the first rule is at y≈327 despite the tab strip ending at y=38. The equivalent composer capture starts history near y≈249. That change of position is evidence of the bottom alignment, not evidence that a different top padding is needed.

## 1. Ranked gap, with the concrete changes

Ranks describe contribution to the owner's screenshot. The running footer is marked separately because that screenshot is idle; the supplied running captures prove its absence.

### 1 — History is attached to the wrong edge

**Mockup:** the first command starts directly below context chrome. Unused space is between the last short transcript entry and the bottom composer, clearly visible in both panes of `03`. Longer history scrolls normally; `01` shows the return-to-output affordance.

**Current:** `style.css:462`, `.scrollback-inner { margin-top: auto }`, pushes the whole short transcript downward inside the flex `.scrollback-area`. `.pane` in `styles/base.css:455` correctly supplies a column; it is not the source of the empty upper half. `ScrollbackController` inserts `.scrollback-layout` before the composer and maintains the follow sentinel after the transcript.

**Change:** keep the pane as a column, the scroller as `flex: 1 1 auto; min-height: 0`, and the composer as a non-growing sibling. Change the transcript to `margin-top: 0; flex: 0 0 auto`. Leave spare height below its contents. Do not insert a fake leading spacer, give every block a fixed height, or give the transcript a bottom-justified wrapper under a new name.

Keep `overflow-anchor: none`, the existing controller as the only scroll-position owner, and the untransformed `.scrollback-follow-sentinel` immediately after the transcript. A short transcript's sentinel remains visible, so follow intent remains true. Once output overflows, the existing explicit follow operation scrolls the tail into view. An explicit history jump or user scroll must still suppress automatic following.

**Invariant:** bottom alignment is an implementation choice; no-jump transitions and preservation of follow intent are the requirements bought by nocx-i4h04. Audit `ScrollbackController._glide`, `_paintedTop`, `_captureGlideOrigin`, `scrollToBottomIfFollowing`, `revealLastBlock` and viewport resizing against top alignment. Preserve the FLIP measurement/mutation boundary and reduced-motion path. Short history should naturally have zero positional delta on submit/settle; overflowing history can still move and must keep the controller's compensation. Do not delete the controller because a short-history screenshot now looks stable.

**Owner:** layout task, `style.css`, `scrollback/controller.ts`, `terminal-content.ts`. No new kit component is needed for a scroll container.

### 2 — The composer is a field on a transcript row, not the mockup's card containing a field

**Mockup:** `03` has two rectangles: a subtle outer card, then an inner input field. The prompt belongs inside the card, above the field. The field contains a padded `Run ▾` segment, a full-height separating rule, the draft, and a submit arrow at the trailing edge. Active and inactive panes have the same structure. The inactive arrow is subdued; the active nonempty draft has a blue square submit button. The card is inset from the pane and its bottom edge.

**Current:** `styles/surfaces/composer.css` gives `.nocx-editor` a full-width top border and `12px 16px` padding. Its chrome is 22 px high, followed by an 8 px gap and a 38 px field. `.nocx-editor-field` is CM6's own `.cm-editor`, with a border and 6 px radius. There is no outer card or submit control. The near-empty `~` prompt and tiny Run control make the wide outlined field read as a thin strip even though its floor is now 38 px. Raising that floor again is not the missing fix.

**Change:** introduce a passive, vanilla kit **ComposerFrame** (`ui/composer-frame.ts`, `styles/components/composer-frame.css`) with identity `ui-composer-frame`, and slots `__context`, `__controls`, `__field`, `__editor`, `__submit`. It owns the outer card and inner field, but never owns document state or keyboard dispatch. `CommandEditor` places its existing CM6 view into the empty editor slot. Do not remount CM6 when focus, mode or controls change.

Concrete dimensions: outer margin 12 px inline and bottom; card padding 12 px; 1 px `--color-divider` card border; 8 px card radius; card ground `--terminal-background`; context row fixed at 22 px; context-to-field gap 8 px; field min-height 38 px; 6 px radius and 1 px `--color-border` outline. The resulting one-line card is approximately 94 px border-box, before its external bottom margin. This closely matches `03` after normalization. Its subtle outer boundary matters more than an extra 2 px of height. Remove the old pane-wide composer seam.

The **field wrapper**, not CM6 and the wrapper simultaneously, owns the input border. Use a typed focus attribute on ComposerFrame, updated from the editor's existing focus notifications, for the accent border. Ordinary CM6 editing metrics, cursors, wrapping and widget-buffer correction stay in the adapter stylesheet. Strip the duplicate `.nocx-editor .nocx-editor-field` border/background once ownership moves.

Place existing PromptContext and ghost model/grant/recovery controls in the context slots. On narrow splits, the path can ellipsize and secondary controls can go into the existing menu vocabulary. Do not keep the grant's current 13rem minimum if it displaces the draft in a 400 px pane. The fixed context row must not grow when a model or grant appears.

Use an existing kit IconButton for submit, with a typed primary appearance if the component does not yet expose one. The frame places it; `icon-button.css` owns its blue fill, disabled treatment and focus ring. Target 24 px square plus 6 px field clearance. Accessible name follows the active target (`Run command` / `Send question`). Click calls **`CommandEditor.submit()`**, retaining secret resolution, submission guards, multiline handling and target dispatch. It must never write directly to the session. Empty/disabled states must follow the existing submission rules; a disabled empty pointer button need not remove the existing keyboard behavior for an empty Enter.

**Run segment:** `.ui-mode-indicator` currently inherits the badge's small type, clears its padding, and puts a divider after the chevron. That order is now correct. Give ModeIndicator a typed `field` variant: 14 px UI type, 12 px inline padding, 60–64 px minimum inline size, an approximately 36 px one-line hit area, 14 px chevron, and a trailing 1 px divider. Own these in `mode-indicator.css`, not `.nocx-editor .ui-mode-indicator`. Remove the stale CSS comment claiming the divider is between the word and chevron.

For a full-height segment, move the existing ModeIndicator into a leading slot beside CM6 in ComposerFrame's field grid: `minmax(64px, max-content) minmax(0, 1fr) auto` for mode/editor/submit, with the 64px value supplied by its token. Remove the CM6 target gutter when doing so; never show both. Adapt `TargetIndicator` in `ask-entry.ts` to project the same target changes into that mounted kit control. The entire CM6 document now occupies the second column, so every wrapped/document continuation line still begins after the same segment by construction. Mode remains outside `.cm-content` and its accessible textbox text. This preserves the reasons the gutter was introduced while avoiding a 36px gutter button inflating the first text line or the 30-line height cap. The segment can stretch with the field; its button stays aligned to the first input line in multiline mode. `ask-entry.ts` currently derives menu entries from `TARGET_PRESENTATION` and toggles between two targets; this consult does not license a second registry or a third-target implementation.

**Owner:** composer task for editor/frame/mode/ask-entry; shared-kit task for a needed IconButton variant. ADR-0008's rejection of a card around every _finished command_ does not prohibit a bounded input card. Retain keyboard Run/Ask selection and a visible focus ring for every new button.

### 3 — The pane context strip is completely missing

**Mockup:** `01` has `folder ~/repos/nocx │ branch main` directly beneath tabs, on chrome rather than transcript ground. `03` uses the same location with a local/remote pane identity: `Local ~/repos/nocx …` and `dev@staging SSH /srv/nocx …`, with an accent line identifying the active split. `02` reuses this region for the foreground program and `Keyboard → nvim`, plus Session actions.

**Current:** neither the owner nor scripted images have it. `TerminalContent` renders location facts into blocks and the composer through `_syncWhereSources`, `_onHomeKnown`, `_onBranchChanged`, `_recordBlockWhere`. Those sources already exist; a new Git lookup is unnecessary. Both specs explicitly rejected pane chrome as redundant.

**Change:** add kit **PaneContext** (`ui/pane-context.ts`, `styles/components/pane-context.css`, identity `ui-pane-context`) with supplied facts and action slots, mounted once per terminal pane immediately above `.scrollback-layout`. Height 40 px for a single pane, 44 px for split identity presentation; horizontal padding 24 px; one bottom divider; 14 px UI type, 14 px mono paths; 16 px folder/terminal/server/branch icons. Local path and branch in this _chrome_ variant are muted, matching the image; prompt lines inside blocks remain accent. A one-pixel vertical divider separates path and branch. Use SVG kit icons, not emoji or a literal box-drawing divider.

Extend PromptContext with a typed `chrome` presentation for the where-parts, rather than inventing another path/branch emitter. PaneContext supplies the surrounding local/remote/program identity and slots; PromptContext continues to own the formatted where-line. This allows `on` in transcript prompts and a vertical rule in chrome without surface overrides.

TerminalContent feeds the pane strip the same current home, verified cwd and branch it feeds the composer. Historical block branches stay snapshots at submission. Do not fill `main` when unknown, infer home from `/home/<name>`, or scrape a shell prompt to obtain host/branch. At the owner's home directory the correct line can simply be a folder plus `~`; a missing branch there is not a bug.

For splits, place the context strip inside each pane's own viewport, not once above the entire split layout. Keep active-pane indication distinct from active-tab indication. On alternate-screen mode use the program/input-owner variant, retain actual session actions, and let the live terminal occupy the remaining pane. Never put this strip inside xterm's descendants.

**Owner:** layout task owns PaneContext, its wiring and `panes.ts` if needed; shared-kit task owns PromptContext's chrome variance. See fixed interfaces below.

### 4 — The failed block is a full-width error panel

**Mockup:** `01` has normal ground behind both prompt and command. Only output carries a narrow coral rail. The rail starts at the first output line and ends at its last line. Text is inset from it; the header is not an alert panel.

**Current:** `.cmd-block[data-outcome='failure']` in `styles/surfaces/command-block.css` paints `--color-danger-surface` over the whole block. Its `::before` spans `inset-block: 0` at the pane's left edge, width 3 px. The owner capture's broad plum rectangle and edge rail are among its largest visual differences. The corresponding light screenshot is a large pink region. This treatment follows September 14 §3.3 and is explicitly retained by September 15 §4.

**Change:** preserve `data-outcome` as the model projection; remove the failure background from the block and remove its full-height pseudo-element, including the nested-turn offset exception. Add an output-decoration host, or use the existing `.cmd-output` as that host: `position: relative` with a 4 px danger pseudo-element spanning only its output rows. Its normal background stays transparent. Put the rail in the existing side gutter, approximately 8 px before the output text, so failure does not shift any characters relative to success. With a 24 px text gutter, a rail at x≈16–20 is a reasonable normalized target.

Do not create a rail for a command with no output: the red `Exit N` label is sufficient. Unknown/cancelled states retain their own meanings. Do not make every nonzero exit red merely because the sample shell returned 130; use the existing kind/outcome rules. Hover, selection and grants remain separate intentional states; the output rail survives them.

**Kit ownership:** move the visual command-shell renderer into a vanilla kit **CommandBlockFrame**, retaining existing `.cmd-*` identity names where callers depend on them. Move frame/header/output-decoration appearance into `styles/components/command-block-frame.css`. BlockManager and `settleBlockOutcome` keep lifecycle, records, selection and outcome decisions; the kit receives slots and state. Do not migrate `.term-line` rendering or its ownership into Solid. `styles/surfaces/command-block.css` becomes placement-only for that component.

Keeping the old surface as a manual colored control because a lint exemption happens to allow it would repeat the problem the brief explicitly asks to avoid.

**Owner:** blocks task.

### 5 — Block rhythm and the rules make the transcript read as full-width rows

**Mockup:** dividers start and end at the content inset. They separate entries without boxing them. The first entry has no leading divider. Prompt and command are a closely related pair; output follows promptly, with 12–16 px of breathing room before the next divider.

**Current:** each direct `.scrollback-inner > *` gets 16 px padding, but `.cmd-block` paints `border-top` across the whole row. In the owner image the rule begins at the pane edge, not the text gutter. The first visible rule can belong to a later block after earlier session content; `.cmd-block:first-child` is not a general test for “first visible command.” Header padding is 12 px top/8 px bottom, prompt-to-command gap 4 px, block bottom padding 16 px. Those numbers alone are not unreasonable. Their relationship to the separate running body is wrong for a mockup comparison.

**Change:** use a 24 px ledger gutter, retaining a single measurement source for both frozen and live content. Draw a 1 px inset separator in the frame's own decoration rather than a full-width `border-top`. Its offset uses that same gutter token. Assign first-entry separator state structurally when the frame is placed; do not use viewport visibility to continuously add/remove rules during scroll. A restored-history boundary remains a real, separately labelled boundary.

Target prompt/command line-height 20 px, 4 px between them, 8 px between the command and output region, and 12–16 px after output. Keep existing body pad tokens of 2 px top/6 px bottom unless measured geometry requires a coordinated change. Count those pads when judging the visible gap; do not add an extra 8 px to a gap already supplied by the header.

**Running/frozen seam to fix deliberately:** a running `.cmd-block` contains only its header; `.xterm-live-container` is a sibling. The current 16 px `.cmd-block` bottom padding is therefore **before** live output, while the same padding is **after** output in a frozen block. Remove trailing block padding from the running-header-only variant, and move that spacing to the live body's trailing layout space. Apply the same total trailing space after frozen output. Update controller measurements, not just CSS: `_bodyPaddingPx()` and live-region sizing must agree with the final DOM. This is a concrete source of visible rhythm change at freeze even if both sides use identical `.term-line` metrics.

Do not add CSS wrapping to frozen output. It intentionally preserves serialized rows and horizontally scrolls long logical lines. Keep live/frozen first-column alignment and `usableViewport()` subtraction of the live row's computed padding. A wider gutter necessarily changes available columns through the existing fit path; do not compensate by scaling the canvas or moving cells independently.

**Owner:** blocks task owns frame rhythm; layout task owns `style.css`, the live sibling and controller measurement. The shared tokens are the contract between them.

### 6 — The command row highlights the wrong visual emphasis

**Mockup:** `› git diff --stat`, `› go test …`, `› npm run build`: a small pale chevron; the executable is blue; ordinary arguments and flags are body text. Output colors represent actual output, such as green `ok` or Git diff additions. They do not repeat the input highlighter's rainbow.

**Current:** the chevron already exists (`createHeader`, `.cmd-header-command`, `.cmd-header-sigil`) and sits outside copied command text, which is correct. `shell-highlight.ts` maps grammar roles to `tok-*`; `scrollback/shell-paint.ts::paintShellInto` paints those roles into the header. `style.css:276` makes `.tok-command` green (`--color-success`), flags/atoms amber, arguments mapped as `tok-path` violet, and strings green. The owner's green `ls` and the scripted command rows directly show this difference. `p` is normal-colored and underlined because command existence is unresolved; that is a useful separate diagnostic.

**Change:** keep the shared tokenizer and its command-existence verdict. In a **terminal-command presentation**, map command → `--color-accent`; path/ordinary argument/flag/atom → `--color-text`; operator → `--color-text-muted`; comments → `--color-text-dim`; string → `--color-text`; variable/keyword → `--color-accent-secondary` where grammar supplies that distinction. Keep `.tok-command.tok-unresolved` normal-colored plus its warning dotted underline. Do not implement “color the first word” with a regex: pipelines, assignments and substitutions already have a grammar.

Give this presentation a kit identity `ui-shell-command`, placed on the header's text host and installed on the editor's owning host by its adapter. Define token appearance under that identity in `styles/components/shell-command.css`. A small vanilla kit helper can apply the identity; it must not insert document characters. Keep the generic `.tok-*` vocabulary for syntax in other surfaces, especially assistant code fences. Do not make the quieter terminal command style an accidental global recolor of every snippet.

Keep the chevron at 14 px, in a 20 px command line, with 4 px trailing gap; center its glyph vertically without increasing row height. It remains `aria-hidden` and outside `.cmd-header-text`. Masked/reference-bearing commands retain their existing safe rendering path.

**Owner:** shared-kit task owns the shell presentation; blocks/composer tasks consume the helper. No new tokenizer and no ANSI-output recoloring.

### 7 — Status is too small, too high, and too far from the command's baseline

**Mockup:** success shows only duration; failure shows `Exit 1 · 1.2s` in the command-line band; running has spinner, `Running · 12.4s`, and an always-visible Stop button. Text is approximately the body size. The running controls occupy the two-line header's right-hand region without squeezing the location to a one-character fragment.

**Current:** `createHeader` puts `.cmd-header-right` inside `.cmd-header-meta`, beside the prompt, above the command. `Meta[data-size='sm']` is mono but **12.25 px** (`0.875rem × 14`), not 14 px. The comment calling it a readable size is not evidence of equivalence. `.cmd-header-right` is non-shrinking; duration reserves `3.25rem` (45.5 px), and each element has an 8 px gap. The hidden overflow button still reserves its slot. Stop is size `sm`, 24 px high. `formatRunningDuration` deliberately uses whole seconds; its ticker runs once per second. The supplied running screenshots **already have Stop**. Do not re-file its earlier construction-time absence as today's defect.

**Change:** add `Meta` size `terminal`, using `--font-size-terminal`, explicit 20 px line-height and tabular figures. Apply the same variance to standalone separators. Keep the generic small Meta unchanged. Header structure should have a prompt region, command region and status region, with the status region aligned to the command band for settled blocks and vertically centered across the two text lines for running blocks. Use grid placement while retaining `.cmd-header-right` for existing selectors and cleanup. At narrow widths, dedicate a full row to status if necessary rather than truncating away the remote host or Stop.

Use 4 px between status/separator/duration, then 12 px before Stop. A duration text span must not both reserve a wide column and add leading whitespace after the dot; reserve the whole group's width or use a roughly `4ch` duration floor. Stop uses Button `default`, size `md` (32 px), SquareIcon 14 px, label 14 px, about 84 px total width. Align overflow controls at the trailing edge; retain keyboard/focus/selection reveal so hover is not required.

For exact `12.4s` presentation, change the existing running ticker to 100 ms while visible, based on the same start time and clock, and format one decimal below a minute. Pause visual updates when hidden, recompute on visibility, and clean up at settle/disposal. No live-region announcement every tenth of a second. Alternatively whole seconds are an acceptable _explicit_ divergence, but not a claim of exact mockup fidelity. For finished durations normalize subsecond display to tenths of a second (`49ms` → `0.0s`, `113ms` → `0.1s`); retain precise milliseconds in a title/details value. A `<0.1s` convention is more informative but is a deliberate product choice, not what `01` literally draws.

`settleBlockOutcome` remains the one updater that removes spinner, running controls and old status parts. Continue preserving author badges and the kind-specific ask/tool statuses. A geometry refactor must not turn every block kind into a shell command.

**Owner:** blocks task; Meta variance owned by shared-kit task.

### 8 — Font size, pitch and family need separate treatment

**Mockup:** ordinary-width, regular-weight, open monospace; visually closer to the SF Mono/Menlo/JetBrains Mono category than to a narrow condensed terminal face. This is resemblance, not a reliable font identification from generated pixels. Prompt, command and output read at one size; metadata is only slightly quieter. The body baseline pitch is about 1.35–1.45 times the inferred font size.

**Current:** `renderers/font.ts` has `FONT_SIZE = 14`, `LINE_HEIGHT = 1.2`, and the stack `ui-monospace, SF Mono, Menlo, Monaco, …, monospace`. The theme's mono stack matches. PromptContext and command text already use 14 px. Meta is 12.25 px and the mode switch inherits the badge register. `style.css` gives `.term-line` the renderer-measured `--term-cell-height` and `--term-cell-delta`, overriding a merely nominal `1.2` body line-height. The scripted screenshots look markedly narrower than the mockup; the owner's screenshot has a different apparent width/weight. The raster does not establish which installed fallback font actually rendered either screenshot.

**Change within this pass:** retain 14 px terminal font size and the existing font-family settings; put prompt/command/status at a consistent 20 px UI line box and give Run 14 px UI type. Preserve terminal output's actual measured cell height and character advance. Do not change font weight on terminal output, strip ANSI bold, add letter spacing by eye, or set frozen rows to `line-height: 1.4` independently of xterm.

**Limit:** sizing/spacing fixes will remove most of the _header/composer_ type mismatch. They cannot remove a condensed fallback face, different glyph drawings, or output whose pitch remains approximately 17 px rather than the mockup's 19–20 px. They certainly cannot remove the structural gap ranked 1–5. The owner can choose a deterministic regular mono face later without blocking the structural pass. No font purchase, new font dependency or invented font decision is required by this consult.

Exact output pitch/family matching requires an explicit renderer/font change: change the renderer's font configuration and its matching CSS font token/family together, let the renderer measure the new cells, then let `cell-metric.ts` publish them to frozen rows. That preserves AD-6 ownership; it nevertheless exceeds this pass's explicit “terminal cells and ANSI palette are not touched” scope. Choose either retained cell metrics with this documented residual difference, or authorize that coordinated font pass. A DOM-only pitch change is not a third option.

### 9 — Ground and chrome differ from the mockup's neutral palette

**Mockup:** near-black neutral terminal ground; slightly lighter charcoal chrome and footer; pale neutral body text; blue accent; coral failure. There is subtle image texture/lighting, not a flat authoritative palette.

**Current:** Tokyo Night is blue-violet: terminal ground `#1a1b26`, body `#c0caf5`, accent `#7aa2f7`, danger `#f7768e`, chrome `#0e0f15`, divider `#2a2b3d`. Chrome is darker than content, whereas the mockup's context/footer/chrome generally read lighter. Light's white terminal ground is correct for its theme and must not become the gray application canvas.

**Change:** match role and shape through tokens, not sampled literals pasted into surface CSS. Use the following concrete mapping. Existing literal values are listed so a worker cannot silently replace a semantic role with a convenient nearby token.

| Use                                             | Token                         | Tokyo Night | Light     |
| ----------------------------------------------- | ----------------------------- | ----------- | --------- |
| Transcript, live region, field and subdued card | `--terminal-background`       | `#1a1b26`   | `#ffffff` |
| Body and command arguments                      | `--color-text`                | `#c0caf5`   | `#1a1a1a` |
| Path/executable/active indicator                | `--color-accent`              | `#7aa2f7`   | `#2563eb` |
| Context labels/duration                         | `--color-text-muted`          | `#a9b1d6`   | `#4b5160` |
| Secondary hints                                 | `--color-text-dim`            | `#9098bd`   | `#585f6e` |
| Failure rail/Exit                               | `--color-danger`              | `#f7768e`   | `#c81e1e` |
| Dividers/card boundary                          | `--color-divider`             | `#2a2b3d`   | `#ced3db` |
| Operable field/button boundary                  | `--color-border`              | `#5f6590`   | `#7c8593` |
| Context/footer chrome                           | new `--color-terminal-chrome` | `#1f2335`   | `#e6e8ed` |

Define the new semantic chrome role with an explicit value in every theme (12 theme files), not a literal fallback in each component. Tokyo Night's value is its existing surface hue; Light's is its canvas hue. For other themes, choose their existing surface/canvas color that distinguishes the band from terminal ground. The tab strip and activity rail can use this terminal-shell role in terminal presentation through their own kit variants; do not repaint their components from `.pane` CSS.

This closes the contrast _direction_ gap while keeping the chosen theme recognizable. It does not make Tokyo Night's violet ground identical to a generated neutral-black image. Exact palette reproduction is a theme decision and would include the terminal palette, excluded here. Do not create one neutral patch behind frozen output while leaving live xterm violet. `--color-danger-surface` may remain for other consumers; it simply stops being the ground of a failed command.

### 10 — Tab strip is close in anatomy, small in scale

**Mockup:** approximately 44 px strip after normalization; roughly 185–200 px tabs; index, title, close; blue 2–3 px top active line; restrained rounded top corners; add after the tabs; search and split at the far end. Native traffic lights precede it in a desktop window.

**Current:** `.tabbar` in `styles/components/tab-strip.css` hardcodes 38 px even though `--tab-height: 38px` exists. `.nocx-tab` in `tab.css` uses min 150/max 220 px; 14 px title; 10.5 px index; active 2 px top line. Close is opacity zero except on hover. The owner screenshot's leading workspace control, single tab and add/menu controls reflect real application functionality, not a missing second tab.

**Change:** set `--tab-height: 44px`; use that token in `.tabbar` rather than leaving its hardcoded 38 px twin. For horizontal tabs use 184 px minimum and the existing 220 px cap; title remains 14 px; index becomes approximately 12 px; 6 px top radii; retain 2 px active accent line. Keep the close button's slot visible for the active tab, hover and `:focus-within`, and give its own keyboard focus a visible state. Preserve vertical-tab dimensions and grouping behavior through orientation-specific selectors.

Keep real workspace creation and tab-management actions. Do not delete them merely because the image omitted them. Reuse existing search/split actions if placing them at the strip's far end; do not create nonfunctional glyphs for symmetry. Respect `--titlebar-inset-start` and actual platform window controls. The browser frame in the owner's screenshot is not nocx chrome and is excluded from comparison.

The right activity rail is secondary but contributes to scale: currently 48 px wide with 20 px large glyphs; mockup normalizes to roughly 56 px with 22–24 px glyphs. Set existing `--activity-bar-width` to 56 px and add a scoped kit icon-size variant, rather than enlarging `--icon-size-lg` globally. The owner is `sidebar.tsx`, with `.activity-bar` layout in `styles/components/sidebar.css` and the outer `#activitybar` box in `style.css`; change its current 48px button pitch to the same rail-width token. Reusing the existing width token also keeps `styles/components/toast.css`'s trailing offset aligned; do not introduce a competing rail-width variable. Preserve existing sidebar actions, labels and notification semantics. Files-panel tree typography/content is not terminal output and should not be fixed by changing the terminal font.

**Owner:** layout task owns tab/rail presentation and their kit CSS; shared-kit task supplies the metric and any IconButton variant.

### Running-state gap — mandatory despite not appearing in the owner's idle screenshot

**Mockup `01`:** a roughly 56 px bottom chrome bar: info icon and `Process running` on the left; `Send input` and `Stop Ctrl+C` on the right. It replaces the idle composer. It is not an OS clock or a permanent application-wide status bar. `02` has no such footer over the full-screen program. The background-request sequence also shows a terminal taking input directly, without a second composer stealing ownership.

**Current:** `CommandEditor.hide()` sets `display: none`; `_syncLifecycleOwnership` hands input back to the renderer. This is intentional. It removes the composer completely, leaving no replacement footer, as both running captures show. Stop in the block header already exists.

**Change:** new passive kit **ProcessBar** (`ui/process-bar.ts`, `styles/components/process-bar.css`, identity `ui-process-bar`), a 56 px non-growing sibling below `.scrollback-layout`. Use the terminal chrome token, 1 px top divider, 24 px horizontal padding, 14 px UI text, 16 px info icon and 32 px default Buttons. PromptReady shows ComposerFrame; ordinary running shows ProcessBar; alternate-screen shows neither. Summoned/frozen assistant presentation follows its existing explicit ownership path and must not leave a second active input surface underneath it.

TerminalContent projects the existing lifecycle and input-owner facts into the bar. `Process running` means an active execution, not “output contained a word we recognized.” `Send input` focuses the existing live terminal; it does **not** submit an empty command, insert a newline, summon Ask, or create a new textbox. Thus there remains one place keystrokes go. Hide/disable the action when there is no writable target, using the current capability result. Controls must not be immediately focus-bounced back into the renderer by the pane's general mousedown listener.

**Important semantic mismatch:** the image's `Stop Ctrl+C` label cannot be copied literally with current behavior. `runningActions.stop()` calls `signalActiveCommand('stop')`; the keyboard Ctrl+C path calls `signalActiveCommand('interrupt')`. The code explicitly distinguishes those intents and delegates stop escalation to the backend. Keep block and footer **Stop** bound to the same stop owner. Put **Ctrl+C** on an `Interrupt` action in the footer/menu, or show a separate interrupt shortcut hint. Do not label Ctrl+C as the equivalent shortcut for a stronger Stop. Making them equivalent would be a deliberate behavioral decision beyond visual styling.

**Unavoidable space tradeoff:** nocx-g6hnk removed reserved composer space because normal-buffer TUIs need those rows. A permanent 56 px footer necessarily removes roughly three current terminal rows (and the context strip removes more). You cannot have an always-visible, non-overlapping footer and simultaneously give a normal-buffer program the entire previous viewport. Top alignment does not solve that arithmetic.

Recommended policy: make the mockup footer the normal structured-running presentation, retain the existing explicit native-input route as the full-grid escape, and omit the footer in alternate screen. The new context strip's Session actions must expose that existing escape clearly. The owner must accept the ordinary-running row cost as part of “follow the mockup.” If preserving all normal-buffer rows is absolute, use controls in top context chrome instead and record that the `01` bottom bar is intentionally not reproduced. Do not guess whether the normal-buffer program is a TUI, overlay controls on its bottom cells, or reserve an invisible 94 px composer while calling it a footer.

**Owner:** layout task for ProcessBar, state projection, focus, measurement and existing signal reuse. The composer task keeps `hide()` as an actual ownership handoff; a hidden editor never remains input-active to support the new footer.

## 2. What the two specifications must stop telling workers

The implementation pass needs one authoritative decision record that explicitly supersedes these passages:

| Existing instruction                                                          | Required replacement                                                                                                                                  |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| September 14 §2: composer differs from history only by caret                  | Composer is a bounded input card; completed transcript stays unboxed.                                                                                 |
| September 14 §3.3, retained September 15 §4: full-row failure tint and rail   | Output-only failure rail; no failure ground across header/command.                                                                                    |
| September 14 §4: full-width appearance is necessary                           | Layout rows may remain full-width, but transcript separators and error decoration are inset.                                                          |
| September 14 §5.1 / September 15 §6: no submit arrow                          | Submit arrow is an accessible control invoking the existing submit path.                                                                              |
| September 15 §6: field paint is surface CSS; no outer card                    | A kit frame owns field/card appearance; surface/adapter owns placement and editing.                                                                   |
| September 15 §7: pane header, recipient labels and status bar remain rejected | Add pane context and truthful input-owner indication; add a running-only process bar with the row-cost policy above. No permanent clock/status strip. |
| September 14 §10: tab strip outside scope                                     | Horizontal terminal-shell tab scale and focus states are in this pass.                                                                                |
| Both specs: bottom alignment assumed by comments                              | Top-aligned short transcript; explicit follow and transition stability remain binding.                                                                |
| September 15 §4: small mono Meta and whole-second running clock               | Terminal-sized Meta; choose the decimal ticker explicitly.                                                                                            |

“Everything else stands” is not enough here: old assertions such as the composer ground rule can remain, but assertions requiring full-row failure paint or forbidding a card/arrow/footer must change. Otherwise workers will reproduce the conflict and the previous screenshot again.

## 3. Shared geometry and API contract before parallel work

These are proposed new tokens unless identified as existing. Put metric literals in `styles/tokens.css`; put every color literal in theme files. New components may have private layout variables only when derived from these shared values.

| Token                                                           | Value / action                                                                                        |
| --------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Existing `--font-size-terminal`                                 | Keep 14px; no renderer font change in this scope.                                                     |
| `--terminal-ui-line-height`                                     | 20px, for app-owned prompt/command/status text only.                                                  |
| Existing `--pane-inline-padding`                                | `var(--space-6)` = 24px.                                                                              |
| Existing `--tab-height`                                         | 44px; replace hardcoded `.tabbar` height.                                                             |
| `--terminal-context-height` / `--terminal-split-context-height` | 40px / 44px.                                                                                          |
| `--terminal-process-bar-height`                                 | 56px.                                                                                                 |
| `--terminal-card-margin` / `--terminal-card-padding`            | `var(--space-3)` = 12px each.                                                                         |
| `--terminal-command-gap`                                        | `var(--space-1)` = 4px.                                                                               |
| `--terminal-output-gap`                                         | `var(--space-2)` = 8px; include rather than duplicate existing header gap.                            |
| `--terminal-block-trailing-space`                               | `var(--space-4)` = 16px; after output in both live and frozen presentation.                           |
| Existing output pad tokens                                      | Keep 2px / 6px initially, counted in live/frozen measurements.                                        |
| `--terminal-failure-rail-width` / `--terminal-failure-rail-gap` | 4px / 8px.                                                                                            |
| `--terminal-mode-segment-width`                                 | 64px minimum.                                                                                         |
| Existing height/radius tokens                                   | Field 38px (`lg`); Stop/footer buttons 32px (`md`); card 8px (`lg`) and field 6px (`md`) radii.       |
| `--terminal-tab-min-width` / existing `--activity-bar-width`    | 184px for horizontal tabs / 56px for the activity rail; use the existing rail token for overlays too. |
| `--color-terminal-chrome`                                       | Explicit per-theme value as above.                                                                    |

Freeze these seams in the worker briefs so integration does not become another design round:

- **PromptContext:** add an optional typed presentation `prompt | chrome`; existing calls default to `prompt`. Formatting remains `cwdLabel`; no fetching inside the component.
- **Meta:** add `size: 'terminal'` to emitter/update/separator options; keep `sm` unchanged for other consumers.
- **Shell presentation:** export a kit helper that marks an existing owning host as `ui-shell-command`; it never modifies input document content or tokenization.
- **ComposerFrame:** emitter returns stable root and named DOM slots plus focus/disabled projection methods and disposal. `CommandEditor` still exposes `root`, `submit`, `show`, `hide`, `isVisible`, `setWhereFacts`, `resized`, and containment behavior to existing callers. No new renderer-facing composer API is necessary.
- **CommandBlockFrame:** emitter returns the existing header/right/command/output-host slots, retaining public `.cmd-*` hooks. `blocks.ts` keeps record/lifecycle/selection ownership and public `setBlockWhere`. Never move a live xterm descendant into the frame; live output remains the controller-owned sibling.
- **ProcessBar:** presentation facts and callbacks only (`running`, input availability, focus-input, stop, interrupt). It calls no client API itself. TerminalContent owns those callbacks and existing failure reporting.
- **Import/documentation ownership:** one worker owns `ui/index.ts` and kit README additions; one worker owns all `style.css` imports and removals. Other workers import new kit modules by their direct paths while working. No worker edits a shared file “just for one import.”

## 4. Four tasks with disjoint file ownership

These are implementation task briefs, not spawned workers. They can run concurrently against the contracts above; integration depends on all four, and nobody reports the feature complete from an isolated component screenshot. Task D is the integration owner, not a fifth task.

### A — Shared terminal visual vocabulary

**Exclusive files:** `frontend/src/styles/tokens.css`; `frontend/src/styles/themes/*.css`; `frontend/src/ui/prompt-context.ts`; `frontend/src/styles/components/prompt-context.css`; `frontend/src/ui/meta.ts`; `frontend/src/styles/components/meta.css`; new `frontend/src/ui/shell-command.ts` and `frontend/src/styles/components/shell-command.css`; `frontend/src/ui/icon-button.tsx`, `icon-button-element.ts`, `styles/components/icon-button.css` if a submit/rail variant is required; `frontend/src/ui/index.ts`; `frontend/src/ui/README.md`. Own adjacent component-specific verification files if implementation verification needs changes.

**Deliver:** shared tokens; PromptContext chrome presentation; terminal-sized Meta/separator; scoped command emphasis; kit-owned submit/rail icon variants; final inventory/export entries for all workers' components. Do not change renderer font files, ANSI tokens, generic highlighter behavior, or `style.css`.

**Screenshot acceptance:** at 14px root, path, command and status read at one size; `git` is blue while `diff --stat` is ordinary text; unresolved `p` retains its distinct underline; no `~/repoonmain`; light has readable path/status/control boundaries; card/chrome token roles remain distinguishable in both themes. Inspect actual integrated terminal screenshots, not only a kit gallery.

### B — Command frame, output failure and status

**Exclusive files:** `frontend/src/scrollback/blocks.ts`; `frontend/src/styles/surfaces/command-block.css`; new `frontend/src/ui/command-block-frame.ts` and `frontend/src/styles/components/command-block-frame.css`; corresponding block/frame verification files. Do not edit `style.css`, controller, shared Meta/PromptContext or editor files.

**Deliver:** passive kit frame around the existing block model; inset rules; two-line header/status placement; output-only failure rail; running-header spacing contract; 32px Stop; chosen decimal timing formatter/ticker; preserved public selectors, kind-specific behavior, masked commands and branch snapshots. Send D the exact live sibling padding/geometry requirement rather than modifying D's file.

**Screenshot acceptance:** a success, a multi-line failure and an output-producing running command use one prompt/command anatomy; failure header and command have normal ground; rail ends at output, not at the frame edge; first transcript entry has no extra top rule; rules are inset at both ends; Stop is visible without hovering and no longer remains after settlement; long paths/commands in a 400–500px split do not hide Stop or overlap status. Compare the last live frame to the first frozen frame: first output baseline and trailing spacing remain consistent.

### C — Composer card and submission control

**Exclusive files:** `frontend/src/editor.ts`; `frontend/src/ask-entry.ts`; `frontend/src/ui/mode-indicator.ts`; `frontend/src/styles/components/mode-indicator.css`; `frontend/src/styles/surfaces/composer.css`; new `frontend/src/ui/composer-frame.ts` and `frontend/src/styles/components/composer-frame.css`; corresponding editor/mode/frame verification files. Do not edit TerminalContent or shared IconButton/PromptContext files.

**Deliver:** inset outer card plus inner field; prompt/control row; full Run segment with trailing divider; submit button invoking `submit()`; stable CM6 host; narrow-pane handling for existing controls; genuine hide/ownership handoff. Keep wrapping, IME, selection, secret chips, completion ghost and cursor ownership unchanged. Frame rendering must not take over terminal state.

**Screenshot acceptance:** both panes of a split contain a card at their own bottom edge; two outlines are visible without becoming heavy nested panels; Run, draft and arrow share a field baseline; draft begins directly after the segment rather than being centered in the remaining width; the empty field and first typed character have the same height; inactive card does not show a live caret; a wrapped command aligns all continuation lines; appearing model/grant controls do not change the chrome height. Inspect a visible keyboard focus ring on mode and submit controls.

### D — Pane composition, process bar, tab scale and final integration

**Exclusive files:** `frontend/src/terminal-content.ts`; `frontend/src/scrollback/controller.ts`; `frontend/src/panes.ts`; `frontend/src/styles/base.css`; `frontend/src/style.css`; new `frontend/src/ui/pane-context.ts`, `frontend/src/styles/components/pane-context.css`, `frontend/src/ui/process-bar.ts`, `frontend/src/styles/components/process-bar.css`; `frontend/src/tab.tsx`, `tab-strip.tsx`, `styles/components/tab.css`, `styles/components/tab-strip.css`; `frontend/src/sidebar.tsx`, `frontend/src/styles/components/sidebar.css`; the two `.internal/specs/2026-09-14-terminal-screen-visual-register-design.md` and `.internal/specs/2026-09-15-terminal-screen-register-mockup-pass.md`; integration/screenshot scenario files under `e2e/`. Own controller/TerminalContent/sidebar-specific verification files if needed. The existing toast width assertion in `frontend/src/ui/toast.test.tsx` also belongs to D for reconciliation with A's new rail metric; the toast placement itself already uses the shared token.

**Deliver:** top-aligned transcript and preserved follow intent; context strip from existing facts; process footer and explicit row-cost policy; truthful Stop/Interrupt actions; alternate-screen and native-input presentation; atomic sizing transitions; tab/rail scale; stylesheet imports for A/B/C/D and removal of relocated rules; explicit spec amendments. D alone touches global CSS, so imports and live/frozen padding cannot race between workers.

**Screenshot acceptance:** with only the owner's `ls` and failed command, history begins below the context strip, not halfway down the pane; idle space is below history. On a 1400×900 run, verify ordinary running footer, idle composer, overflow with Follow output, user-scrolled history, two splits with Files open, and alternate-screen TUI. The context strip must name the right pane, not whichever tab fetched branch last. No bottom footer/card over a full-screen grid. At 27–50ms command completion, watch the transition rather than inspecting only the final still: no jump, scrollbar flash, lost follow state or padding swap. In a long run, the returning composer must not cover the last output rows. Keyboard-only inspection must show reachable Stop, mode menu, submit and session actions without focus being stolen by a background request.

**Dependency/order:** settle the documented footer/renderer limits and shared contracts first; start A–D concurrently with the above file partition. A establishes shared values; B/C/D consume them without inventing substitutes. D integrates all four and performs the whole-screen comparison. Resolve deviations against the images and this decision, not against superseded assertions from the first spec. If actual font/palette matching is also required, treat it as a separately authorized coordinated renderer/theme change, not an unowned fifth parallel worker.

## 5. What not to copy

- **Illustrative content:** filenames, test timings/results, `nocx.service`, sample SSH host, branch `main`, pseudo-agent conversation and fake requests are not product state. The owner at `~` should not see `~/repos/nocx` until actually there. Script setup commands currently fill the supplied comparison captures; future visual comparisons should present equivalent meaningful command states, not score similarity against setup noise.
- **TUI internals:** Neovim tabs, source syntax colors, quickfix list, NORMAL status line and cursor are the program's grid. Do not rebuild them as nocx controls or style their xterm cells. The same applies to remote `$` prompt content in `03`.
- **Conflicting prompt variants:** use `01`'s structured two-line `›` anatomy for app-owned command blocks. Do not alternate between `$`, `›`, inline remote prompts and two-line prompts just because different generated panels do. Raw remote shell output remains raw.
- **False shortcut equivalence:** `Stop Ctrl+C` is not an accurate statement of this branch's Stop/Interrupt semantics. Preserve the geometry, correct the wording as described above.
- **Generated presentation texture:** no gradients/noise/vignettes over terminal text, synthetic glow, blurred edges, inconsistent icon strokes or guessed fractional pixel colors. Use solid theme roles and SVG kit icons.
- **OS/browser controls:** no fake traffic lights inside the web stand and no imitation browser tab/address bar. Respect native desktop titlebar insets.
- **Explanatory poster framing:** the background-request image's headline, four-panel numbering, instructional captions and footer disclaimers are not application chrome.
- **Invented activity:** `Needs input` requires a real unresolved request state; unread and unresolved are separate. Do not infer one from `[y/N]` text, make opening a tab resolve a request, force a background tab switch or discard its shell draft. The background image does not authorize implementation of a missing orchestration protocol during a visual pass. Present only facts already available through the actual status/request owner.
- **Removal of real capabilities for visual cleanliness:** keep keyboard navigation, overflow/session actions, provenance, grants and workspace controls. Fit them into the defined slots; do not hide essential behavior until the screenshot is quiet. The pet is not present in the mockups, but it is existing product state, not a generated defect to “fix” by deleting it. Use a pet-free comparison capture for geometry and separately ensure the enabled pet cannot cover the prompt, failure rail or controls.

## Completion criterion

One integrated view must show the owner's two-command scenario at the top under context chrome, with inset separators, an output-only failure rail and an inset composer card with a real Run segment and submit control. The same build must show a running command with reachable controls and the chosen footer policy; two independent bottom composers in split view; and unobstructed alternate-screen content. Compare those at the same app viewport, root scale, theme and font environment.

A border assertion, a kit gallery, a full transcript that happens to hide bottom anchoring, or a static screenshot after the animation has stopped is insufficient evidence. The remaining font/output-pitch and neutral-palette differences must be stated explicitly if renderer cells and ANSI colors remain outside scope. Those are the concrete limits; the missing composition is entirely addressable in this pass.

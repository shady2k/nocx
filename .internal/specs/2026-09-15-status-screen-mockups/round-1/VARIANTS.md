# nocx status screen — four concepts

## 1. Project overview

**Image:** [variant-1.webp](variant-1.webp)

**Idea:** Give every registered project a visible home, with feature progress and recent changes together.

**First impression:** nocx has closed 200 of 225 tasks but finished none of its six stages; iaam has no features and an unsynced backlog; Blog migration is nearly done. The 108 nocx epics outside any feature remain a conspicuous, clickable group.

**Drill-down:** Open a project, then a feature and stage, then its epics and tasks. A stage's blocker badge opens its dependencies; the outside-feature row opens those epics. Recent events jump to the affected record. New events briefly highlight while counts update in place.

**Trade-offs:** The large nocx card dominates. More repositories or features require vertical scrolling and filtering. Blocker names and the actual stale item take another click.

## 2. Roadmap explorer

**Image:** [variant-2.webp](variant-2.webp)

**Idea:** Keep projects available in a left rail while one feature unfolds as a readable hierarchy.

**First impression:** The selected feature's six stages, with the two-laptop stage expanded to show its ongoing epics, a stale item, an unknown-size epic, and the two named dependencies outside the stage.

**Drill-down:** Select a repository, select a feature, expand stage → epic → task. The image shows this nesting already open. Dependency rows navigate to their source records; the outside-feature group expands separately. The bottom activity strip opens the full cross-project feed.

**Trade-offs:** Best for understanding one project's structure, weaker for comparing projects. Expanded branches push other content down; the two sidebars consume width. Other stages' blocker details and undecomposed children remain behind their chevrons.

## 3. Six-stage roadmap

**Image:** [variant-3.webp](variant-3.webp)

**Idea:** Put the six ordered stages side by side so unfinished scope and dependencies are easy to compare.

**First impression:** The undecomposed first stage has unknown size; stages two through four have substantial completed work but unresolved scope; restart recovery has not started. Project summaries remain visible above.

**Drill-down:** Select a project and feature, then an epic row to inspect its tasks. Select a blocker badge to show a regular details section below the columns, as pictured for stage two. Those blockers belong to nocx but sit outside the selected stage. The outside-feature row opens a separate collection.

**Trade-offs:** Long names wrap heavily and six stages already use most of the available width. Larger roadmaps need horizontal scrolling or a stage subset. Ordered columns can imply strict sequencing even though several stages are active concurrently.

## 4. Live desk

**Image:** [variant-4.webp](variant-4.webp)

**Idea:** Start with what changed and what needs attention, keeping project outcomes in a secondary roadmap summary.

**First impression:** Active work, two stale items, unknown scope, and one unsynced repository; the latest closure is highlighted beside actionable attention rows.

**Drill-down:** Click a signal counter to filter affected records across projects. Select an event or attention row to inspect the record and its feature/stage context. Review on the sync warning shows incoming backlog changes. Open roadmap reveals feature → stage → epic → task navigation.

**Trade-offs:** Activity can distract from outcomes, and a quiet project can look less important. Individual stage progress requires another click. The feed needs stable scrolling and restrained highlights during bursts of updates.

## Data and review notes

The nocx feature and stage counts, seven in-progress titles, five undecomposed items, and shown blocker relationships come from `real-data-feature-status.txt`; titles are shortened for reading. The 108 outside-feature epics come from the brief. Unknown size has words and badges, never a zero-of-zero completion bar. Task closure and stage completion remain separate measures.

iaam and oh-my-portal data are illustrative, as requested. Event times, stale ages, incoming-change counts, cross-project signal totals, and the explicitly tagged example task in variant 2 demonstrate possible states; they are not measurements from the supplied snapshot. Illustrative totals use 7 + 2 + 1 active items across the three projects, 1 + 1 stale items, and 5 + 2 undecomposed items.

All four full-window PNGs were generated with the built-in image generation tool and visually inspected. Variants 2–4 received correction passes for generated data errors. Exact numeric labels carry the counts; generated bar lengths are approximate. [PROMPTS.md](PROMPTS.md) records the generation and correction prompts. These are static concepts; drill-down and live behavior above describe the intended interaction.

## Pick

**I would pick variant 1.** It answers “where are we across all my projects?” immediately, keeps work without a feature visible, and gives recent changes enough space without overwhelming the roadmap. Variant 2 is a natural drill-down destination when the owner needs to investigate a stage.

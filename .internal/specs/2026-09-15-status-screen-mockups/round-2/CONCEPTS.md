# Round 2 — stage graphs and live agent activity

Six raster concepts for the Status tab. Open the images at full size to compare them.

| Image                              | Name                | Main question                                                   |
| ---------------------------------- | ------------------- | --------------------------------------------------------------- |
| [graph-1.webp](graph-1.webp)       | Follow the blocker  | Which cross-stage prerequisite holds up this work?              |
| [graph-2.webp](graph-2.webp)       | Dependency tiers    | Which prerequisites converge on each blocked epic?              |
| [timeline-1.webp](timeline-1.webp) | Wave trace          | When did agents work, wait, exchange messages, finish and exit? |
| [timeline-2.webp](timeline-2.webp) | Wave conversation   | What did they say, and which question needs an answer?          |
| [timeline-3.webp](timeline-3.webp) | Agents and activity | Who is doing what now, and what changed?                        |
| [combined.webp](combined.webp)     | Stage and live wave | How does the structural blocker relate to the live work?        |

## Evidence and example data

The current nocx reference and all four round-1 images were inspected before generation. These concepts retain the Status tab, far-right icon rail, dark navy surfaces, thin borders, small chips and compact rows of variants 1–2. The file browser is collapsed to its icon to make room for the investigation.

**Backlog facts:** names, epic statuses and dependency edges come from `../real-data-stage-graph.json`; stage counts and decomposition information come from `../real-data-feature-status.txt`. Titles are shortened. “Shared session surface” and “One session surface” name the same stage-6 epic.

The supplied sources have differences that should remain visible in this review:

- The brief describes three stage-4 dependents of “Coordinator learns worker finished.” The JSON contains **two in stage 4**, “Discover and message agents” and “Worker parent and checkpoints,” plus **one in stage 3**, “Claude hooks and screen state.” Graph 1 shows the third as an external dependent.
- The JSON calls stage 4 open; the feature summary calls it in progress. The stage heading uses the feature summary, while epic status chips use the JSON. Neither source was rewritten.
- The feature summary mentions “The attention queue” as an external stage-4 blocker. The supplied JSON does not identify its dependent epic. No connecting edge was invented.
- There are no per-epic closed/total task counts in these files. Every expanded graph node reserves the field as **Tasks — / —**, with an explicit unavailable-data explanation. “Not decomposed” comes from the feature summary and is distinct from a missing count. Unknown scope never becomes 0% complete.
- “Backend output survives” and “Warpify” are external to stage 2, but their owning stages are absent. Graph 2 says “Outside stage” and explains that the host stage is unavailable.

**Illustrative activity:** every agent identity, assignment, message, timestamp, checkpoint, wait duration and live change is an example grounded in the requested backlog names, not a recording of actual agent activity. The small claude badges in the standalone graphs are examples too. No agent state is inferred from an epic's backlog status.

The three timelines and combined screen depict the same example wave at 14:30:

| Agent                    | Work and observed state history                                                                                                                                                                                              |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Coordinator · claude     | Starts three workers; working 14:00–14:12, idle 14:12–14:15, working from 14:15.                                                                                                                                             |
| Session surface · claude | Takes “One session surface for pane control” at 14:02; works continuously; checkpoints at 14:16.                                                                                                                             |
| Worker lifecycle · codex | Takes “Coordinator learns worker finished” at 14:03; asks claude a peer question at 14:10 and receives an answer at 14:11; asks the coordinator a separate question at 14:18; blocked for 12 minutes waiting for that reply. |
| Window budget · omp      | Takes “Helper window budget hides prompts” at 14:04; reports and closes the task at 14:22; finished until exiting at 14:26. The task remains closed after process exit.                                                      |

The example closure advances stage 6 from **17/24 to 18/24**, and the feature from **200/225 to 201/225**. Stage 4 stays **49/53** and completed stages stay **0/6**. The standalone graphs retain the original snapshot; timelines and the combined screen show the illustrative later state. A report saying checks passed is example message content, not a claim that tests were run for this design task.

## 1. Follow the blocker

**Image:** [graph-1.webp](graph-1.webp)

**One idea:** Give the dependency chain that explains the selected stage a clear left-to-right reading order, while keeping unrelated epics quiet.

**What the owner sees first:** Stage 4 waits on the session surface in stage 6. That prerequisite unlocks worker lifecycle reporting, which unlocks two undecomposed stage-4 epics. A dashed stage-3 dependent shows the wider consequence. Three completed stage-4 epics recede below the graph.

**Interaction:** Selecting an epic title drills into its epics/tasks at the next level; breadcrumbs return to the stage or feature. Focusing a node or selecting its blocker chip highlights the upstream chain and opens the lower detail panel shown here. “Open epic” provides an explicit drill action from that panel. The agent badge and “View agent activity” open the relevant participant and time range. External nodes navigate to their own stage with a breadcrumb back to the original investigation. At feature level, the same layout shows stages rather than mixing stages and tasks in one graph. A collapsed undecomposed stage opens a named empty state with unknown size.

**Trade-offs:** Excellent for following one explanation; less efficient for several disconnected dependency clusters. Long titles use width, and the detail panel takes height. The amber chain is a structural bottleneck, **not a schedule-derived critical path**: there are no durations, due dates or completion forecasts. A selected chain must not imply that unrelated remaining work is done.

**Data needed beyond the supplied snapshot:** Per-epic task counts with an explicit completeness flag; stage membership for external nodes; participant-to-epic/task links for agent badges; navigable parent ancestry. Edges and statuses are supplied, so bottleneck highlighting can be derived without recording estimates.

## 2. Dependency tiers

**Image:** [graph-2.webp](graph-2.webp)

**One idea:** Arrange prerequisites above their dependents in fixed tiers, with a side inspector that names every incoming blocker.

**What the owner sees first:** External backends has three blockers: Two machines, one session; Backend output survives; Warpify. On the left, Updates leave programs running blocks host deployment, and connecting from another machine waits for both host deployment and the shared session.

**Interaction:** Click a title to drill into that epic. Focus a node or select its blocker chip to highlight its incoming edges and populate the inspector. Select a blocker in the inspector to focus or open its source. Expand “4 closed epics” to show the actual closed records; they are grouped, not omitted from the stage. The two active epics without dependency edges remain visible below. Breadcrumbs change the level; the system lays it out automatically. Larger levels use ordinary document scrolling and a named dependency filter, never a draggable canvas.

**Trade-offs:** Converging prerequisites read more clearly than in a long chain, and the inspector provides a textual check on the arrows. Sparse tiers consume vertical space; high-degree nodes can still produce crossings. Highlighting External backends makes that selected bottleneck prominent but does not rank it above the separate host-deployment chain.

**Data needed beyond the supplied snapshot:** The same task-count and participant association data as graph 1; complete external ancestry to replace “host stage unavailable”; complete edge sets and stable node ordering. A real implementation should expose missing dependency data rather than treating an incomplete graph as proof that an epic is unblocked.

## 3. Wave trace

**Image:** [timeline-1.webp](timeline-1.webp)

**One idea:** Put every participant on the same real clock so concurrency, waiting intervals and message handoffs become visible together.

**What the owner sees first:** Codex has an amber interval from 14:18 to the 14:30 now edge, waiting for its coordinator. Claude continues working. Omp's green finished interval ends in a separate grey exited interval. Parent indentation and the connecting bracket identify who started each worker.

**Interaction:** Click a message arrow to open its text and participants in the lower panel; the pending coordinator question is open in the image, with the peer exchange beside it. Click a task diamond to inspect the taken/checkpoint/closed event and its stage effect. Click a state segment for its exact start, end, declared reason and observation source. Select a time window from the toolbar. Collapsing a parent groups its workers without inventing an aggregate working state. Open pane goes to the participant's terminal; View task drills into the backlog.

**Live behavior:** The right edge follows observed time and contains no future plan. Scrolling back or changing the time window pauses following, with an explicit return-to-live control; incoming records continue to be collected. Messages at the same time can be grouped until selected. Exact timestamps are authoritative; generated segment lengths and marker placement are approximate design geometry.

**Trade-offs:** Best for overlap and elapsed waits, but densest and least approachable for reading a conversation. Many arrows overlap lanes; show selected exchanges in full and quiet the rest. A timestamp scale must be exact in implementation. Colour is paired with words, outlines and event shapes.

**Data needed beyond the supplied snapshot:** Stable wave/participant IDs; parent participant ID; agent kind and pane link; timestamped state transitions with reason, waiting recipient and source; task assignment intervals; timestamped task events and stage-count deltas; sender/recipient message records and reply linkage; distinct finished and process-exit events. A current-state snapshot alone cannot reconstruct the bars.

## 4. Wave conversation

**Image:** [timeline-2.webp](timeline-2.webp)

**One idea:** Make the recorded exchange the main reading surface and keep current participant state in a compact nested roster.

**What the owner sees first:** The expanded codex question has been waiting for the coordinator for 12 minutes. The thread shows the initial brief, a worker-to-worker question and reply, a checkpoint, the completion report with stage progress, and the later exit.

**Interaction:** Click a message summary to expand its full text; linked replies remain attached to their original question while retaining their own timestamps. Select an agent in the roster to filter its sent and received messages and task events. Select Questions to find outstanding conversations. “Open coordinator pane” navigates to the existing terminal where the owner can act; opening a message does not send anything. An exited participant's pane action, where a retained pane exists, opens that retained pane; otherwise it becomes a history action.

**Live behavior:** New events append at the bottom now boundary. Reading older messages keeps the scroll anchor and displays a new-events affordance. Switching filters does not make absent events look like an agent was idle.

**Trade-offs:** Most readable account of intent and decisions. Overlapping work and exact idle intervals are harder to compare than in the trace. Long conversations push task events away, so the roster and event filters remain available. The timeline spacing represents event order rather than elapsed duration.

**Data needed beyond the supplied snapshot:** Message body, sender, recipient, timestamp, reply-to identifier and unresolved/resolved question state; participant ancestry and current-state timestamps; task-event links and stage-count changes. Waiting for the coordinator must be a recorded reason, not guessed from the last message or silence. Current roster updates and historical thread entries need the same participant identities.

## 5. Agents and activity

**Image:** [timeline-3.webp](timeline-3.webp)

**One idea:** Put each worker's current task, state age and latest message in a card, then provide a shared chronological event table underneath.

**What the owner sees first:** Three workers belong to the coordinator. Claude is working, codex needs a coordinator answer, and omp has exited after closing its task. The stage-progress strip explains what the completed work moved.

**Interaction:** Select a card to filter the feed to that participant and its conversations. Select View conversation to open the pending exchange in a detail panel; View report opens the completed worker's report. Task opens the named backlog record. Open pane goes to an active participant's terminal. Feed filters separate messages, taken events, checkpoints and closures without hiding the current-state cards. Selecting a feed row opens the full record, including both endpoints of a message.

**Live behavior:** State ages update in place; cards keep their positions as new events arrive. A finished worker remains visible in the wave and subsequently changes to exited. The now row stays at the feed's bottom while following. Additional coordinators create separate parent groups.

**Trade-offs:** Fastest current-state scan and closest to round-1 project cards. More than a few workers require wrapping or a compact list, reducing space for history. Last-message previews lose conversational context, and the miniature state summaries cannot support duration comparisons. Exited cards should eventually collapse into completed participants rather than displacing running workers.

**Data needed beyond the supplied snapshot:** Stable parent-child relationships; current task and stage links; state-entered timestamps; last sent/received message references; recorded task progress and event history. No meaningful agent completion percentage is supplied, so none is invented. Historical transitions are still needed even though the main cards show the current state.

## 6. Stage and live wave

**Image:** [combined.webp](combined.webp)

**One idea:** Keep the selected stage's dependency explanation beside the live exchange that helps the owner act on it.

**What the owner sees first:** Stage 4 waits on stage 6, and the codex worker associated with the intermediate epic has an unanswered coordinator question. Both the roadmap context and the wave's stage-6 work remain visible.

**Interaction and navigation:** This is **one split screen**, reached from round-1 project overview cards by opening nocx, Replace herdr, and stage 4 in the roadmap. The Projects breadcrumb and All projects link return to the overview. The left roadmap changes the selected stage; the center graph stays at one epic level. Selecting an agent chip focuses its conversation in the right pane. The wave scope explicitly remains Stages 4 + 6 so its upstream worker does not vanish when stage 4 is selected. “Expand timeline” opens the full timeline with the selected wave, participant and message preserved; Conversation/Trace changes representation of that same activity. Returning restores the graph context.

**Why this arrangement:** It connects “what blocks this stage?” with “what is the worker waiting for?” in one place, while preserving the already favored overview and roadmap. The compact graph uses a vertical arrangement to fit the split; graph 1 remains the fuller standalone investigation.

**Trade-offs:** Three columns need a wide window. At narrower widths, the live pane should open as a separate view through the same navigation path. The compact graph collapses three closed epics and links to the stage-3 dependent rather than drawing every context node. An agent's unanswered question and an epic's dependency are separate facts: answering the question does not automatically remove the dependency or close the epic.

**Data needed beyond the supplied snapshot:** Everything needed by the selected graph and timeline, plus reliable cross-links among repository, feature, stage, epic/task, wave, participant, pane and message; a coherent snapshot/event cursor for count updates. If an association is unknown, the product should offer the broader wave rather than attach an agent to a guessed task.

## Shared recording requirements

These are requirements for the proposed views, not claims about which records nocx already persists. The supplied files describe backlog snapshots and contain no agent event stream. No backend persistence audit was performed for this image-only brief.

A live implementation needs ordered event IDs and timestamps, reconnect replay/deduplication, explicit history gaps and observation provenance. On multiple hosts, distinguish occurrence time from receipt time and disclose uncertain clock ordering. Lost observation is “unknown” or a visible gap, never fabricated working/idle time. Keep declared worker completion, observed screen activity, task closure and process exit separate. Record explicit messages and structured task/lifecycle facts; these concepts do not require saving raw terminal output.

## Generation and review

All six PNGs were made with the built-in image generation tool and visually inspected after generation. Graph 1 was regenerated to correct its node title, then edited for the task-count field and progress colour. The combined image received colour corrections so working participants and incomplete stages use blue. Graph 2's six dependency edges were checked against the JSON; graph 1's external source and dependent were checked against their stage membership. Timeline message order, the 12-minute wait and the illustrative stage-count change were checked across the set.

These are static concept images. Their controls describe intended behavior; they are not a working UI. Bar geometry is illustrative, and final typography, colour tokens and exact timestamp placement should come from nocx's UI system in implementation. The generation and correction prompt set is recorded in [PROMPTS.md](PROMPTS.md). Outputs are confined to this round-2 directory.

## Recommendation

Keep round-1 **project cards → roadmap** as the entry path. Use **Follow the blocker** as the default standalone graph, with **Dependency tiers** for stages with converging prerequisites. Make **Wave conversation** the default live pane in the **Stage and live wave** split: the owner's next action is usually understanding or answering a specific question. Offer **Wave trace** for elapsed waits and concurrency investigations. Keep **Agents and activity** as the alternative for scanning a larger day's active workers.

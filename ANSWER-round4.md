# Round 4

## 1. The three claims

1. **A — confirm for the live cutover; refute for final acceptance.** Live scrolling can be built from libghostty `HISTORY`: the point type already distinguishes `HISTORY` from `SCREEN`, `ghostty_terminal_grid_ref` can resolve history points across pages, and `ghostty_terminal_get_scrollbar` exposes the retained-row count. The current bridge is the missing part: it hard-codes `ACTIVE` and narrows `y` to `uint16_t`. Therefore the first live stage needs a bridge/port history reader, the page contract, the new limit, and UI paging—not the drain patch. But the drain remains mandatory for the accepted durable tier: reset/discard can destroy rows inside one `vt_write`, before any post-write harvest. If durable reveal is part of “xterm cutover complete,” the patch moved later on that critical path; it did not leave it.

2. **B — confirm, with the same boundary.** For live scrollback, libghostty is the authority. If RIS or ED3 removes retained rows during the write, there is no live-history hole: those rows no longer belong to that tier. The hole is exclusively durable. It becomes visible whenever a user asks to reveal output from before a clear/reset, because only the durable journal can answer then.

3. **C — refute as stated.** `session.output` is good precedent only because both sides share one monotonic byte coordinate and publish the join explicitly: `produced`, `replayFrom`, and a gap. Libghostty history positions are not that coordinate; reflow and pruning move them, while the journal will have its own cursor. Without a stable row identity plus `liveBase`/`durableThrough` (and explicit gaps), two tiers can overlap, duplicate, or omit rows. I withdraw the general “two readers are inherently wrong” objection, but not the concrete join objection. Reserve those fields now; implement their meaning with the durable drain stage.

## 2. The two open questions

### Q1 — clear boundaries

Enforce the newest unrevealed boundary in the server-side merged reader, and render a visible sentinel such as **Earlier output hidden by clear — Reveal**. Ordinary paging must stop there. Reveal should be explicit, per client/view, and should send the boundary cursor back on the next request; it must not mutate the session or weaken the default for a newly attached client.

A sufficient contract is a page request with `before`, `limit`, and optional `revealThroughBoundary`, and a response with the row interval, `more`, `visibilityFloor`, an optional `stoppedAtBoundary { cursor, kind }`, and an explicit gap/reason. The cost is one indexed boundary lookup per page (normally cached), one sentinel row, and one extra request after Reveal—no copying or deletion. If durable output was disabled or already retained out, the response must report `unrecorded`/`expired`; it must not offer a reveal that silently returns nothing. Clear remains a view boundary, not secure deletion.

### Q2 — alternate screen

Do **not** make ordinary scroll-up reveal primary-screen history while the alternate screen is active. Libghostty says an alternate screen has no scrollback and keeps the viewport active; tracked references can preserve primary state, but the ordinary public point lookup remains tied to the active screen. Comparable terminals make the same product choice: xterm's alternate screen has no saved lines and maps alternate scrolling to cursor input, kitty leaves scrolling to the application on the alternate screen, WezTerm generates arrow keys when mouse reporting is absent, and VTE applies scrollback only to the normal screen ([xterm control sequences](https://invisible-island.net/xterm/ctlseqs/ctlseqs.pdf), [xterm manual](https://invisible-island.net/xterm/manpage/xterm.pdf), [kitty overview](https://sw.kovidgoyal.net/kitty/overview/), [WezTerm setting](https://wezterm.org/config/lua/config/alternate_buffer_wheel_scroll_speed.html), [VTE API](https://gnome.pages.gitlab.gnome.org/vte/gtk3/method.Terminal.set_scrollback_lines.html)).

Thus wheel input follows the TUI's mouse mode; without mouse reporting, an xterm/WezTerm-style arrow fallback is reasonable. Page-up/scrollbar should otherwise do nothing. A future explicit “Show primary history” command may open a clearly separate view, but it is not ordinary alternate-screen scrolling. The implementation cost is one active-screen branch in input handling; it avoids a second pager and an ambiguous viewport switch.

## 3. Retention, rebuilt

I withdraw the proposed 10,000-row/8 MiB per-session durable cap and the later 20,000-row/16 MiB variants. Durable output uses the existing history policy: `history.retentionDays`, `history.retentionMiB`, and `history.diskCeilingMiB`; command/card projections remain bounded by `history.outputCapKB`. The durable sink writes only when history storage and `history.outputEnabled` permit it. Turning output storage off never changes current-session scrolling.

The only genuinely missing user setting is `terminal.scrollbackLines`: default 10,000, minimum 0, maximum 100,000; zero disables and erases live scrollback, lowering it prunes immediately. Its description must say allocation is page-granular, so the terminal will usually retain more lines than requested, while heavily styled content may retain fewer because of the internal byte ceiling. That byte ceiling is implementation policy, not another setting.

## 4. Revised staging

1. **Live HISTORY slice.** Add `terminal.scrollbackLines`, expose a bridge/port history pager, and prove with the real emulator that output exceeding a small viewport is pageable without shell integration; lowering/zero prunes immediately; RIS/ED3 removes live history; alternate-screen paging does not expose primary history. This is the smaller first task and the decisive proof that the drain is not needed for live scrollback.
2. Add the server page contract and frontend paging/cut over live scrollback from xterm.js, including the clear-boundary sentinel.
3. Add and pin the fork's synchronous drain/reset effect.
4. Build the durable journal under the existing history retention and output-enable policy.
5. Establish the stable live/durable join, then build command cards/projections on it.

## 5. Still open

The stable cross-tier row coordinate and join fields need a concrete contract before stage 4, and the internal byte ceiling needs measurement against the 100,000-line maximum. Nothing else in these two questions remains open in my recommendation.

WORKER_DONE::sbk4-7f2822281217

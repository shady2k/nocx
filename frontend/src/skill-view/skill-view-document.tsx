// ═══════════════════════════════════════════════════════════════════════════
// SkillViewDocument — a skill's markdown, read as a document (nocx-okee0).
//
// THE RENDERER IS NOT NEW, AND THAT IS THE POINT. `createAnswerBody`
// (scrollback/answer-body.ts) is nocx's one owner of rendered markdown —
// headings, hung list markers, quotes, inline code, tables, and fenced
// regions tokenised by the same shell lexer the command editor uses. A
// second renderer beside it would agree with it everywhere anybody looked
// and disagree somewhere nobody did, which is the shape AGENTS.md names.
//
// AND ITS SAFETY IS THE REASON, NOT A BONUS. A skill can arrive from a URL,
// so its bytes are somebody else's. That module escapes every byte through
// one owner and NEVER turns `[text](url)` into an anchor — the text and the
// URL are both shown, verbatim and inert. Nothing it emits carries an
// attribute the file's author can influence. Reaching for a markdown library
// here would have meant re-earning all of that, in the one surface where a
// person is deciding whether to trust the thing they are reading.
//
// WHAT IT BRINGS THAT THE SCROLLBACK DOES NOT: the row class. `.term-line`
// is pinned in style.css to the terminal's measured cell metrics so a frozen
// block occupies exactly the grid rows it did while live. A document pane
// has no grid to line up with, and a heading sized to a terminal cell is not
// a heading. So the rows are `.ui-md-line` here — the same `data-md`
// grammar, none of the pitch.
//
// IMPERATIVE INSIDE A SOLID COMPONENT, deliberately. `createAnswerBody`
// builds DOM directly, because the scrollback it was written for is
// imperative; wrapping it in a reactive tree would mean re-laying the
// document out on every unrelated signal. An effect that rebuilds the body
// when the TEXT changes is the whole of the reactivity this needs, and
// `replaceChildren` on the host is the whole of the teardown.
// ═══════════════════════════════════════════════════════════════════════════

import { createEffect, on, onCleanup, type JSX } from 'solid-js'
import { CommandSnapshotStore } from '../command-snapshot'
import { createAnswerBody } from '../scrollback/answer-body'

/** Markdown by EXTENSION, matching the one the file viewer's registry
 *  already recognises (file-viewer/language-registry.ts) so "which files are
 *  markdown" has one answer in this product. Everything else in a bundle —
 *  a `scripts/setup.sh`, a JSON manifest — is shown as bytes, because it is
 *  not a document and rendering it would say it was. */
export function isMarkdownPath(path: string): boolean {
  const lower = path.toLowerCase()
  return lower.endsWith('.md') || lower.endsWith('.markdown')
}

export function SkillViewDocument(props: { text: string; ariaLabel: string }): JSX.Element {
  let host!: HTMLDivElement

  createEffect(
    on(
      () => props.text,
      (text) => {
        host.replaceChildren()
        // A store nobody has fed: with no snapshot ingested its `status` is
        // `unavailable`, which is exactly right here. The shell lexer then
        // tokenises a fenced command WITHOUT judging whether it exists —
        // and it must not judge, because the commands in a skill are about
        // some other machine and a "this command does not exist" mark would
        // be a claim about a session this document has nothing to do with.
        const body = createAnswerBody(host, {
          store: new CommandSnapshotStore(),
          rowClass: 'ui-md-line',
        })
        body.append(text)
        body.finish()
      },
    ),
  )

  onCleanup(() => host.replaceChildren())

  return (
    <div
      class="skill-view__doc"
      aria-label={props.ariaLabel}
      tabIndex={0}
      ref={(el) => {
        host = el
      }}
    />
  )
}

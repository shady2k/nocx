// PromptContext — the terminal screen's prompt line (spec 2026-09-15 §2): the
// line every command block and the composer open with, `~/repos/nocx on
// ⎇ main`, drawn by ONE primitive so a block and the composer cannot start
// naming "where" two ways (AD-8). It replaces the where-Meta a block's meta
// row used to carry; `Meta` stays for the header's right-hand status group.
//
// Vanilla-emitted, like Meta and ModeIndicator: the terminal screen that uses
// it is imperative DOM (ADR-0012), and a block builds one once. No Solid
// version exists until a Solid surface needs one.
//
// Identity `ui-prompt-context`; variance on data-tone and a part's
// data-emphasis. A surface places it and never repaints it (ui/README).

import { FolderIcon, GitBranchIcon, iconElement } from './icons'

export interface PromptContextFacts {
  /** What a person CALLED the saved connection this pane was opened from,
   *  or nothing when there is no name — an unnamed connection, a hand-typed
   *  host, a local pane (nocx-ouru5.1.2). It reads first, before the host,
   *  because it is the thing they chose; the host is where it goes. The
   *  composer is the only surface that passes one: a block's prompt line is
   *  history and names the machine, never the bookmark. */
  name?: string
  /** Where the command ran or will run — undefined/empty for this machine.
   *  The block's existing `location` fact, unchanged in meaning. */
  host?: string
  /** Normal text colour instead of muted — the composer's safety question,
   *  "where does Enter go" (base spec §5.2). A finished block's host is
   *  history and stays muted; never pass true there. */
  hostStrong?: boolean
  /** The short path (`cwdLabel`), always shown — a block must read on its
   *  own even with no host and no branch. */
  path: string
  /** The branch the pane knew when the command was submitted, or nothing to
   *  report — no source, not a repository, consent required, and so on all
   *  render no branch at all (spec §3). */
  branch?: string
}

export interface PromptContextOptions {
  /** `dim` steps the whole line down one register — the composer while it is
   *  not focused (base spec §5.3). */
  tone?: 'normal' | 'dim'
  /** Composer adds folder/branch icons and a divider. History keeps the branch
   *  in the hover title; chrome and prompt render inline context facts. */
  presentation?: 'prompt' | 'chrome' | 'composer' | 'history'
}

function part(name: string, text: string, strong: boolean): HTMLSpanElement {
  const span = document.createElement('span')
  span.className = 'ui-prompt-context__part'
  span.dataset.part = name
  if (strong) span.dataset.emphasis = 'strong'
  span.textContent = text
  return span
}

function fill(el: HTMLSpanElement, facts: PromptContextFacts, opts: PromptContextOptions): void {
  el.dataset.tone = opts.tone ?? 'normal'
  el.dataset.presentation = opts.presentation ?? 'prompt'

  if (opts.presentation === 'history' && facts.branch) el.title = `${facts.path} · ${facts.branch}`
  else el.removeAttribute('title')

  const children: Element[] = []
  if (opts.presentation === 'composer') children.push(iconElement(FolderIcon))
  if (facts.name) {
    children.push(part('name', facts.name, false))
    // A real span, not CSS `content`: the separator is part of the line a
    // person reads and copies, and a pseudo-element is neither.
    const divider = document.createElement('span')
    divider.className = 'ui-prompt-context__divider'
    divider.setAttribute('aria-hidden', 'true')
    divider.textContent = '·'
    children.push(divider)
  }
  if (facts.host) {
    children.push(part('host', facts.host, facts.hostStrong === true))
    const colon = document.createElement('span')
    colon.className = 'ui-prompt-context__colon'
    colon.setAttribute('aria-hidden', 'true')
    colon.textContent = ':'
    children.push(colon)
  }
  children.push(part('path', facts.path, false))
  if (facts.branch && opts.presentation !== 'history') {
    children.push(part('on', opts.presentation === 'composer' ? '' : 'on', false))
    children.push(iconElement(GitBranchIcon))
    children.push(part('branch', facts.branch, false))
  }
  el.replaceChildren(...children)
}

export function createPromptContext(
  facts: PromptContextFacts,
  opts: PromptContextOptions = {},
): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-prompt-context'
  fill(el, facts, opts)
  return el
}

/** Restate an existing PromptContext in place — `setBlockWhere`'s seam
 *  (scrollback/blocks.ts) re-renders a block's prompt line through this once
 *  a home or a branch source reports one, without disturbing the header's
 *  other children. */
export function updatePromptContext(
  el: HTMLSpanElement,
  facts: PromptContextFacts,
  opts: PromptContextOptions = {},
): void {
  fill(el, facts, opts)
}

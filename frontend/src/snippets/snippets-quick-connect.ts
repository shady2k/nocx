/**
 * Snippets in the quick-connect palette (design §10.1) — the SAME surface
 * the server list, the command palette and the secret picker use, opened by
 * ⌥⌘P and by the strip's snippets action.
 *
 * It is a provider rather than a panel of its own because the palette
 * already answers every question this surface has: a filter field, rows,
 * the keyboard, and a drill for a command that needs values before it can
 * run. A second surface with its own list, keys and accept path is the
 * shape this repo has deleted once already (AGENTS.md, the suggestion
 * surface beside the completion dropdown), and it is what the owner's
 * review caught here.
 *
 * Two things this module owns and nothing else does:
 *
 *  - A body with `ask:` fields does NOT fire from here: the palette closes
 *    and the fields are answered in a form, all of them at once
 *    (snippet-ask-dialog.tsx). They were drill steps first, and a step that
 *    filters a list cannot also be where a value is typed — a person reads
 *    the field as a filter and waits for rows (owner review).
 *  - A refused fire re-opens the palette with the reason on it, which is
 *    how "the refusal renders in the panel and stays" (§11) is expressed in
 *    a surface somebody else owns.
 */
import type { QuickConnectItem, QuickConnectProvider } from '../quick-connect'
import { CopyIcon } from '../ui/icons'
import { needsForm } from './resolve'
import type {
  SnippetDestination,
  SnippetFireOutcome,
  SnippetFireRequest,
  SnippetRefusalReason,
} from './fire'
import type { Snippet, SnippetsStore } from './snippets-store'

export interface SnippetsQuickConnectDeps {
  /** The one library every surface reads (design §6). */
  store: SnippetsStore
  /** The fire path — the same adapter the toolbar menu and the completion
   *  dropdown use, so there is one owner of what a snippet becomes. */
  fire: (req: SnippetFireRequest) => Promise<SnippetFireOutcome>
  /** Say why a fire was refused. The composition root re-opens the palette
   *  with this sentence: the surface the refusal is about is still there,
   *  and a toast would take the explanation away from it (§11). */
  onRefused: (message: string) => void
  /** "Manage snippets…" — the row that outlives an empty list, the way the
   *  vault's "Add a secret…" does: the person whose library is empty is
   *  exactly the person who needs the page that fills it. */
  onManage: () => void
  /** This body asks for values: the palette closes and the form opens, for
   *  the destination the row's activation chose. */
  onAsk: (snippet: Snippet, destination: SnippetDestination) => void
  /** The resolved text reached the clipboard. Said as a toast rather than on
   *  the palette: the palette is gone by then, and there is nothing left on
   *  screen for the sentence to be about. */
  onCopied: (title: string) => void
  /** A fire landed. The keyboard goes back to the pane: the person fired
   *  INTO something, and their next keystroke belongs to it (design §9.5).
   *  Without this the palette's dialog leaves focus on the document and the
   *  Enter that submits what was just inserted reaches nobody — which is
   *  what the e2e gate caught. */
  onDelivered: () => void
}

/** The sentence a refusal renders as — this module owns the words, the fire
 *  adapter owns the reasons (AD-8: one owner per behaviour). Not exported:
 *  its two callers are both here, and the surfaces get the SENTENCE, never
 *  the vocabulary to build one of their own. */
function snippetRefusalMessage(outcome: SnippetFireOutcome): string | null {
  if (outcome.kind !== 'refused') return null
  // Typed on the union rather than inferred: the switch below must stop
  // compiling when a refusal reason is added, which is the only thing that
  // makes "the palette owns the words" checkable.
  const r: SnippetRefusalReason = outcome.reason
  switch (r.kind) {
    case 'no-owner':
      return 'There is no terminal or editor here to insert into.'
    case 'env-unavailable':
      return `${r.keys.map((k) => `{{env:${k}}}`).join(', ')} cannot be answered right now — nothing was inserted.`
    case 'multi-line-no-bracketed-paste':
      return 'This snippet has more than one line and the running program has not enabled bracketed paste — a newline would be read as Return, so nothing was inserted.'
    case 'unresolved-secret':
      return r.name !== undefined
        ? `{{secret:${r.name}}} could not be resolved — unlock the vault or check the name.`
        : 'A secret in this snippet could not be resolved — unlock the vault or check the name.'
    case 'malformed':
      return r.detail !== ''
        ? `This snippet cannot be read: ${r.detail} — nothing was inserted.`
        : 'This snippet cannot be read — nothing was inserted.'
    case 'write-failed':
      return 'The write was refused — nothing was inserted.'
    case 'secret-to-clipboard':
      return `{{secret:${r.name}}} cannot be copied — the clipboard outlives this fire and is read by everything on the machine.`
  }
}

/** The row's second line: the body's first non-empty line, bounded. A body
 *  is multi-line and a row is one line. */
function summary(body: string): string {
  const line = body.split('\n').find((l) => l.trim() !== '') ?? ''
  return line.length > 80 ? `${line.slice(0, 79)}…` : line
}

export class SnippetsQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'snippets'
  readonly label = 'Snippets'
  readonly kinds = ['snippet'] as const

  constructor(private readonly deps: SnippetsQuickConnectDeps) {}

  /** Fire one snippet with the answers it asked for — the one accept path
   *  the palette row, the ask form and the completion dropdown all take
   *  (AD-8). Returns the refusal sentence, or null when it was delivered,
   *  so a caller that OWNS a surface (the ask form) can keep the reason on
   *  its own; callers with no surface of their own leave it to onRefused,
   *  which puts it back on the palette. */
  async fire(snippet: Snippet, answers: ReadonlyMap<string, string>): Promise<string | null> {
    // Named rather than inlined: 'clipboard' is the OTHER destination the
    // fire adapter still honours, and nothing offers it since the palette
    // became a quick-connect variant — the refusal's "copy instead" went
    // with the old panel (nocx-8rtr.2). The seam stays typed so the day it
    // gets a surface again, it is a caller and not a rebuild.
    const destination: SnippetDestination = 'input'
    const outcome = await this.deps.fire({ snippet, answers, destination })
    const message = snippetRefusalMessage(outcome)
    if (message !== null) {
      this.deps.onRefused(message)
      return message
    }
    this.deps.onDelivered()
    return null
  }

  /** Put the snippet on the clipboard instead of into the pane — the row's
   *  second destination. A body with fields is answered first, exactly as
   *  the insert path answers it: the destination changes where the resolved
   *  text lands, never what it is (design §9.2). */
  private async copy(snippet: Snippet): Promise<void> {
    if (needsForm(snippet.body)) {
      this.deps.onAsk(snippet, 'clipboard')
      return
    }
    const outcome = await this.deps.fire({
      snippet,
      answers: new Map(),
      destination: 'clipboard',
    })
    const message = snippetRefusalMessage(outcome)
    if (message !== null) {
      this.deps.onRefused(message)
      return
    }
    this.deps.onCopied(snippet.title)
  }

  /** The same fire, without the palette notice — for a caller that shows
   *  the refusal itself (the ask form). */
  async fireReporting(
    snippet: Snippet,
    answers: ReadonlyMap<string, string>,
    destination: SnippetDestination,
  ): Promise<string | null> {
    const outcome = await this.deps.fire({ snippet, answers, destination })
    const message = snippetRefusalMessage(outcome)
    if (message === null && destination === 'clipboard') {
      // The form owns the refusal sentence, but a DELIVERY to the clipboard
      // leaves nothing on screen to say so — the form closes behind it.
      this.deps.onCopied(snippet.title)
    }
    return message
  }

  /** The offer that outlives an empty list — and an unavailable one, since
   *  the settings page is where both are dealt with. Synchronous, like
   *  every trailing row: it runs on each keystroke. */
  getTrailingItems(): QuickConnectItem[] {
    return [
      {
        id: MANAGE_SNIPPETS_ROW_ID,
        kind: 'snippet',
        label: 'Manage snippets…',
        run: () => this.deps.onManage(),
      },
    ]
  }

  async getItems(): Promise<QuickConnectItem[]> {
    // Re-read on every open: there is no change notification on the wire,
    // so a surface that shows the list re-reads before showing it (§6).
    await this.deps.store.refresh()
    const state = this.deps.store.state()
    if (state.kind === 'unavailable') {
      // The degrade is visible and honest: one row saying why, and no row
      // that pretends to fire something (§11.5). Activating it looks again,
      // which is the only useful answer to "we could not read the library".
      return [
        {
          id: 'snippets:unavailable',
          label: "Couldn't load your snippets",
          detail: state.message,
          kind: 'snippet',
          system: true,
          badge: 'error',
          run: () => void this.deps.store.refresh(),
        },
      ]
    }
    if (state.kind !== 'ready') return []
    return state.snippets.map((snippet) => this.itemFor(snippet))
  }

  private itemFor(snippet: Snippet): QuickConnectItem {
    const asksFirst = needsForm(snippet.body)
    const base = {
      id: `snippet:${snippet.id}`,
      label: snippet.title,
      detail: summary(snippet.body),
      kind: 'snippet' as const,
      // The clipboard is a DESTINATION, not a remedy. It was reachable only
      // as the alternative offered after a refusal, so it went with the
      // floating panel and left SnippetDestination with one caller
      // (nocx-8rtr.2). Offering it on every row is what it always was: a
      // person who wants the phrase somewhere else asks for it before
      // anything has gone wrong, and never learns it exists by being told no.
      action: {
        icon: () => CopyIcon({}),
        ariaLabel: `Copy "${snippet.title}" to the clipboard`,
        run: () => void this.copy(snippet),
      },
    }
    if (!asksFirst) {
      return { ...base, run: () => void this.fire(snippet, new Map()) }
    }
    // Fields to answer: the palette closes on activation (it closes after
    // any run) and the form takes over with all of them at once. The
    // answers come back through the same fire, resolved at fire time (§8).
    return { ...base, run: () => this.deps.onAsk(snippet, 'input') }
  }
}

/** A row that is not a library record, so it carries a reserved id rather
 *  than a snippet's. */
const MANAGE_SNIPPETS_ROW_ID = '__manage_snippets__'

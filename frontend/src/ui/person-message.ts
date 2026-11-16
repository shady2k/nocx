// PersonMessage — the kit's line for words the PERSON sent into a turn that is
// still being written (nocx-a62cl.6.1, ADR-anntnxdj decision 4).
//
// The case it exists for: a person types while the agent answers, and the
// message is delivered into that same turn — before the next model call. The
// turn is one block (the coordinator's decision of 2026-11-15), so the message
// cannot be a block of its own: it takes a seat among the turn's children, at
// the point in the answer where it was sent.
//
// WHY NOT BlockNotice. That component states a fact about the block itself
// ("this answer was saved"), in the block's own muted voice. This is somebody
// else SPEAKING, with their own words, and reading as the block's own remark
// is exactly what it must not do — the point of the line is that the text is
// not the agent's. So the words are drawn verbatim and named as the person's.
//
// The surface PLACES this and never repaints it. The label and the accessible
// name arrive as props because a kit module may not own copy it cannot
// localise (ui/README.md); this module's CSS file owns the
// ui-person-message family and nothing else.

/** The words the line draws, resolved by the caller through the catalogue. */
export interface PersonMessageLabels {
  /** Who spoke, as the transcript names them. */
  readonly label: string
  /** What a screen reader hears when it reaches the line — the label alone
   *  ("You") says nothing about WHERE this message went. */
  readonly aria: string
  /** Said beside a message the turn could NOT take: it is kept, and asked once
   *  this answer ends (ADR-anntnxdj decision 4). Absent when the words are
   *  already in the turn. */
  readonly queued: string
}

/** What became of the message: the agent took it into this turn, or it waits to
 *  be asked next. The line says which, because the two are different promises. */
export type PersonMessageState = 'steered' | 'next_question'

export class PersonMessage {
  readonly root: HTMLElement
  private readonly labels: PersonMessageLabels

  constructor(text: string, labels: PersonMessageLabels, state: PersonMessageState = 'steered') {
    this.root = document.createElement('div')
    this.root.className = 'ui-person-message'
    // A note in the flow, not a live region: it is part of what is being
    // read, and the answer that follows it is announced by its own deltas.
    this.root.setAttribute('role', 'note')
    this.root.setAttribute('aria-label', labels.aria)

    const who = document.createElement('span')
    who.className = 'ui-person-message__label'
    who.textContent = labels.label

    const said = document.createElement('span')
    said.className = 'ui-person-message__text'
    // textContent, never markup: a person's message is what they typed, and a
    // line that rendered it as the answer's own formatting would make the two
    // indistinguishable afterwards.
    said.textContent = text

    this.root.append(who, said)
    this.labels = labels
    this.root.dataset.state = state
    if (state === 'next_question') this.markQueued()
  }

  /**
   * Restate THIS line as one the turn did not use after all.
   *
   * The case: the agent took the message and a cancel then dropped it from its
   * own queue. The line was drawn as used; it says so no more — the same words,
   * marked as waiting, rather than a second line saying the same thing
   * (ADR-4khgzeyp decision 3).
   */
  markQueued(note?: string): void {
    this.root.dataset.state = 'next_question'
    if (this.root.querySelector('.ui-person-message__note') !== null) return
    // The note is the line's own, so the words stay the person's.
    const said = document.createElement('span')
    said.className = 'ui-person-message__note'
    said.textContent = note ?? this.labels.queued
    this.root.append(said)
  }
}

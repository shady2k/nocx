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
}

export class PersonMessage {
  readonly root: HTMLElement

  constructor(text: string, labels: PersonMessageLabels) {
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
  }
}

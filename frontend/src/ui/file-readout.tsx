/**
 * FileReadout — one file, read: what it is, and its bytes or the reason they
 * are not here (nocx-872jc.2).
 *
 * WHY THIS IS A COMPONENT AND NOT THREE LINES IN A SURFACE. Every part of it
 * already exists in the kit: `FactList` draws the named facts, `CodeBlock`
 * draws preformatted bytes, `StatusCard` draws "a state and the one thing to
 * do about it". What does NOT exist is the decision between them and the
 * SENTENCES that go with it — and those need one owner, because two surfaces
 * want them: the Skills page reads a skill's SKILL.md, and the approval prompt
 * (nocx-872jc.3) shows the same file before a person adopts it. A surface that
 * composed the three parts itself would be the second author of "this file is
 * not text", and the two copies would agree everywhere anybody looked and
 * disagree the day the budget changed. So the kit grows by one component that
 * owns the wording, and by no new appearance at all: nothing here paints, and
 * everything visible is a kit component this one places.
 *
 * IT KNOWS NOTHING ABOUT SKILLS, deliberately. `facts` is whatever the caller
 * says the file is — the Skills page names the skill, the file and its
 * provenance; the approval prompt will name what it knows. And the outcome
 * union is this component's own four-way answer to "what is on screen",
 * not a second spelling of `skills.file`'s `refusal` field: `maxBytes` exists
 * only on the branch where it means something, and the caller's `switch` over
 * the wire's closed union is checked by the compiler for totality.
 *
 * NOT `src/file-viewer/`, which is the other thing in this product that shows
 * a file and cannot be the one used here. That is a workspace TAB: it is
 * opened into the tab strip (so reading would mean leaving the Skills page),
 * it hosts a CodeMirror editor, and it reads through `files.read` addressed to
 * a live binding on some machine. A builtin skill's bytes are inside the
 * binary and have no path any file provider can open, which is why
 * `skills.file` exists at all; a tab bound to a filesystem could not read the
 * provenance this component's first caller must be able to read.
 *
 * A FINDING IS MARKED WHERE IT SITS, and that is the whole reason `marks`
 * lives on the `text` branch of the outcome and nowhere else (nocx-872jc.4).
 * A scan match used to reach a person only as a card in a list underneath the
 * file — a line number and a quoted copy of a line, beside the file that
 * already holds it — so the reader had to count lines to find the thing being
 * talked about, and a bundled `scripts/setup.sh` had no list at all. Here the
 * line is highlighted IN the bytes: the evidence and the file are one thing,
 * which is what "in the file where it sits" means. A refused file has no
 * marks BY CONSTRUCTION rather than by a caller remembering — there is no
 * field to put them in on the other three branches — because an empty
 * affordance beside a file nothing read would say "nothing was found", which
 * is a verdict, and nothing here gives verdicts.
 *
 * THE KIT DOES NOT KNOW WHAT A PATTERN IS. A mark is a line number and the
 * caller's own words for it, for the reason `facts` is the caller's: the
 * words for a skill scan's pattern ids belong to the surface that has the
 * vocabulary, and a kit that imported them would be the second place a
 * pattern id is turned into English.
 *
 * THE THREE REFUSALS ARE DRAWN, NEVER THROWN, and they are drawn in two
 * tones because they are two different kinds of true sentence. `not-text` and
 * `too-large` describe a file that IS there — nothing was refused about the
 * request and nothing happened to the file — so they are `warning`, the tone
 * the scan findings already use for "evidence to read, not a failure".
 * `unreadable` is the request itself coming back empty-handed, with no subject
 * to describe, so it is `danger` and carries the caller's own sentence
 * verbatim: a sentence of ours would put our guess in front of the person
 * instead of what happened.
 */
import { Show } from 'solid-js'
import type { JSX } from 'solid-js'
import { CodeBlock } from './code-block'
import { FactList, type Fact } from './fact-list'
import { StatusCard, type StatusCardTone } from './status-card'
import { formatBytes } from './format-bytes'

/**
 * One line of the file worth pointing at, and the caller's words for why.
 *
 * `lineNumber` counts from 1 over the same text this outcome carries, which
 * is the only way the mark can land on the line the producer meant. `label`
 * is what the mark says about itself when a reader hovers or asks — the
 * caller's sentence, never one composed here.
 */
export interface FileReadoutMark {
  lineNumber: number
  label: string
}

/** What is on screen for this file. One member per true sentence. */
export type FileReadoutOutcome =
  /**
   * The file, verbatim. `''` is an empty FILE, not an absent one. `marks`
   * are the lines worth pointing at, and they exist ONLY here: a file whose
   * bytes were not read has nothing to mark, and a slot for marks on those
   * branches would be an affordance that reads as "nothing found".
   */
  | { kind: 'text'; text: string; marks?: readonly FileReadoutMark[] }
  /** The bytes exist and are not something that can be shown as lines. */
  | { kind: 'not-text' }
  /** The file is bigger than the budget the read was measured against. */
  | { kind: 'too-large'; maxBytes: number }
  /** There was no file to describe; the message is the refusal's own words. */
  | { kind: 'unreadable'; message: string }

export interface FileReadoutProps {
  /**
   * What this file IS, in the caller's own words — the skill it belongs to,
   * its path, where its bytes came from. Drawn in every state, including the
   * ones with no bytes: a reason is only useful beside the thing it is about.
   */
  facts: readonly Fact[]
  /**
   * What the block of bytes is, for a screen reader. The visible answer is in
   * `facts`; this is the accessible name for the region that holds the file.
   */
  ariaLabel: string
  outcome: FileReadoutOutcome
}

interface Refusal {
  tone: StatusCardTone
  title: string
  description: string
}

/**
 * The sentence for a file whose bytes are not here, or null when they are.
 *
 * The switch has no default ON PURPOSE, the way `provenanceTone` does not: the
 * outcome is a closed union, so a fifth state fails this return-type check
 * rather than rendering as a viewer that quietly shows nothing.
 */
function refusalFor(outcome: FileReadoutOutcome): Refusal | null {
  switch (outcome.kind) {
    case 'text':
      return null
    case 'not-text':
      return {
        tone: 'warning',
        title: 'This file is not text',
        description:
          'Its bytes are not something that can be shown as lines, so there is nothing here to read. The file is on disk exactly as it was — this is a fact about the file, not a refusal of the request.',
      }
    case 'too-large':
      return {
        tone: 'warning',
        title: 'This file is larger than the read budget',
        // The budget is NAMED, in the units a person reads sizes in, because
        // it travelled on the wire for exactly that purpose. A second copy of
        // the number here would one day quote a limit nothing enforces.
        description: `The budget is ${formatBytes(outcome.maxBytes)}, and the read stops there. The file is on disk exactly as it was; what is bounded is the reading, not the file.`,
      }
    case 'unreadable':
      return {
        tone: 'danger',
        title: 'This file could not be read',
        description: outcome.message,
      }
  }
}

/**
 * The file's bytes with the marked lines wrapped, or the plain string when
 * nothing is marked.
 *
 * The newlines are emitted BETWEEN lines rather than appended to them, so the
 * children reassemble the text byte for byte: a `<pre>` that gained or lost a
 * trailing newline would be showing something other than the file. And the
 * label goes on `title` rather than into a visually-hidden span inside the
 * block, because anything inside the `<pre>` is part of what a reader selects
 * and copies — our sentence would end up pasted into the middle of somebody's
 * script.
 */
function markedText(text: string, marks: readonly FileReadoutMark[]): JSX.Element {
  const said = new Map<number, string[]>()
  for (const mark of marks) {
    const at = said.get(mark.lineNumber)
    if (at) at.push(mark.label)
    else said.set(mark.lineNumber, [mark.label])
  }
  const out: JSX.Element[] = []
  text.split('\n').forEach((line, index) => {
    if (index > 0) out.push('\n')
    const labels = said.get(index + 1)
    out.push(
      labels ? (
        <mark class="ui-file-readout__match" title={labels.join(' \u00b7 ')}>
          {line}
        </mark>
      ) : (
        line
      ),
    )
  })
  return out
}

/** One file, as a person reads it: read-only, and never blank. */
export function FileReadout(props: FileReadoutProps) {
  const refusal = (): Refusal | null => refusalFor(props.outcome)
  /** The bytes, or null when there are none. Null rather than `''`, because
   *  an empty file still gets a block: an empty reader and a refused one must
   *  not look the same. */
  const bytes = (): string | null => (props.outcome.kind === 'text' ? props.outcome.text : null)
  const marks = (): readonly FileReadoutMark[] =>
    props.outcome.kind === 'text' ? (props.outcome.marks ?? []) : []
  /**
   * The key to the highlighting, drawn only when something is highlighted.
   *
   * It is NOT a summary of the findings and deliberately carries no line
   * numbers: those are in the file, on the lines themselves, and a list of
   * them underneath is exactly the arrangement this component was changed to
   * stop. What a reader cannot get from a highlight alone is what the colour
   * MEANS, so that is all this says. `warning` and never `danger`, the tone
   * the scan's other three surfaces already use, because a mark is evidence
   * to read and never a refusal.
   */
  const legend = (): string => {
    const words = [...new Set(marks().map((mark) => mark.label))]
    return words.length === 0 ? '' : words.join(' \u00b7 ')
  }

  return (
    <div class="ui-file-readout" data-state={props.outcome.kind}>
      <FactList facts={props.facts} ariaLabel="What this file is" />
      <Show when={refusal()}>
        {(said) => (
          <StatusCard tone={said().tone} title={said().title} description={said().description} />
        )}
      </Show>
      <Show when={legend()}>
        {(words) => (
          <StatusCard
            tone="warning"
            title="Highlighted lines below matched a static scan"
            description={`${words()}. The match is highlighted where it sits, in the bytes themselves \u2014 it is evidence to read there, not a refusal, and nothing changed because a pattern matched. Lines nothing matched are not lines anything has vouched for.`}
          />
        )}
      </Show>
      <Show when={bytes() !== null}>
        <CodeBlock ariaLabel={props.ariaLabel}>
          {marks().length === 0 ? (bytes() ?? '') : markedText(bytes() ?? '', marks())}
        </CodeBlock>
      </Show>
    </div>
  )
}

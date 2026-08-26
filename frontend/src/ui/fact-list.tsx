/**
 * FactList — the kit's read-only named facts (nocx-n7xha).
 *
 * The kit had two row grammars and neither is this one. `Field` wraps a
 * CONTROL — a label, a description, an error and the thing a person edits;
 * `RecordRow` describes a RECORD inside a CollectionView — a title, a kind
 * badge, a status and the row's actions. A list of "this name has this
 * value, and you cannot change it here" is a third thing, and the surface
 * that needed it first was the approval prompt, which had been printing a
 * JSON blob instead: `{"sessionId":"d70a2dd1…"}` above a sentence in
 * English, with the one internal handle two earlier beads had taken off
 * every other surface still inside it.
 *
 * A generic named-row list is what makes a paraphrase honest. The rule the
 * prompt was built on is right — restating the model's proposal in words is
 * honest only while the restatement is EXHAUSTIVE — and a list that renders
 * every parsed argument as a row is exhaustive by construction, for tools
 * nobody has written a sentence for yet as much as for the ones they have.
 * So the property this component owes its callers is simply: every fact
 * given is a row, in the order given, and none is dropped.
 *
 * `value` is a string and never a slot, for RecordRow's `detail` reason: a
 * JSX slot here would be the free-form blob coming back through the side
 * door, and with it the freedom for each surface to invent its own grammar
 * for a value. A value that needs a code block is not a fact in a list; it
 * is a CodeBlock beside one.
 *
 * `note` is the honest half. A value the product cannot fully vouch for —
 * a working directory the shell has not confirmed (AD-5) — carries its
 * qualification ON its row, where it cannot drift away from the value it
 * qualifies. A paragraph two elements down would be a second owner of the
 * same caveat, which is how a caveat ends up describing the wrong value.
 *
 * Identity: `.ui-fact-list` on the `<dl>` that carries the appearance, plus
 * the `__row/__name/__value/__note` parts only it renders. Surfaces place
 * it and never repaint it.
 */
import { For, Show } from 'solid-js'

export interface Fact {
  /** What the fact is called. The caller's word, and for an argument of a
   *  proposed tool call that is the argument's own key — a name this
   *  surface invented would be a name the model did not use. */
  name: string
  /** The fact, in words. A string, never a slot — see the header. */
  value: string
  /** One line qualifying how far the value can be trusted, on the row it
   *  belongs to. Absent when the value needs no qualification. */
  note?: string
}

export interface FactListProps {
  /** The facts, in the order they should be read. Every one becomes a row. */
  facts: readonly Fact[]
  /** What this list is, for a screen reader — the visible rows name the
   *  facts, nothing names the group. */
  ariaLabel?: string
}

/** A read-only list of named facts. */
export function FactList(props: FactListProps) {
  return (
    <Show when={props.facts.length > 0}>
      <dl class="ui-fact-list" aria-label={props.ariaLabel}>
        <For each={props.facts}>
          {(fact) => (
            <div class="ui-fact-list__row">
              <dt class="ui-fact-list__name">{fact.name}</dt>
              <dd class="ui-fact-list__value">
                {fact.value}
                <Show when={fact.note}>
                  {(note) => <span class="ui-fact-list__note">{note()}</span>}
                </Show>
              </dd>
            </div>
          )}
        </For>
      </dl>
    </Show>
  )
}

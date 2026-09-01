/**
 * The form a snippet's parameter fields are answered in (design §10.1,
 * owner review) — every field at once, in a kit Dialog, with the palette
 * closed behind it.
 *
 * The first attempt walked the fields as palette steps, reusing the drill
 * the "Forward a port" command uses. It was wrong for a reason worth
 * keeping: a drill step is a PICKER over choices somebody can offer, and it
 * looks like one — the same field that filters a list cannot also be the
 * place a value is typed, because a person reads it as a filter and waits
 * for rows that never come. Fields nobody can offer choices for are a form.
 *
 * Answering is also not the same shape of question as picking: the fields
 * belong to ONE fire, they have defaults, and a person wants to see them
 * together before committing. So the palette hands over and closes, and
 * this is what they see.
 */
import { For, Match, Show, Switch, createMemo, createRoot, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Dialog } from '../ui/dialog'
import { Field as FieldRow } from '../ui/field'
import { Select } from '../ui/select'
import { Stack } from '../ui/stack'
import { StatusCard } from '../ui/status-card'
import { TextField } from '../ui/text-field'
import { FLAG_ON, needsForm, visibleFields } from './resolve'
import { parse, type Field } from './parse'
import type { SnippetDestination } from './fire'
import type { Snippet } from './snippets-store'

export interface SnippetAskDialogDeps {
  /** Fire the snippet with the answers. Resolves to the refusal sentence
   *  when the fire was refused, or null when it was delivered — the form
   *  stays open on a refusal, beside the answers that caused it. */
  fire: (
    snippet: Snippet,
    answers: ReadonlyMap<string, string>,
    destination: SnippetDestination,
  ) => Promise<string | null>
  /** Called AFTER the form closes on a delivered fire, so the keyboard goes
   *  back to the pane rather than to whatever the dialog left it on. The
   *  order matters: focusing before the close lets the closing dialog take
   *  it straight back. */
  onDelivered: () => void
}

export interface SnippetAskDialogHandle {
  /** Ask for this snippet's fields, for the destination the person chose
   *  BEFORE answering: which destination it is never depends on the answers,
   *  and asking twice for one fire would be a second question about a thing
   *  already decided. Does nothing for a body with none — the caller fires
   *  those directly. */
  ask(snippet: Snippet, destination: SnippetDestination): void
  dispose(): void
}

export function mountSnippetAskDialog(
  parent: HTMLElement,
  deps: SnippetAskDialogDeps,
): SnippetAskDialogHandle {
  const [snippet, setSnippet] = createSignal<Snippet | null>(null)
  const [destination, setDestination] = createSignal<SnippetDestination>('input')
  /** Every answer this form has been given, by name — including answers to
   *  questions that are no longer on screen. Nothing is ever deleted from
   *  it: un-ticking a flag hides the questions inside its block, and a
   *  person who ticks it again is asking for the same paragraph back, not
   *  for a blank one. */
  const [answers, setAnswers] = createSignal<ReadonlyMap<string, string>>(new Map())
  const [error, setError] = createSignal('')
  const [firing, setFiring] = createSignal(false)

  /** The questions that apply right now. A field inside a block the person
   *  switched off is not asked — an optional paragraph must not levy its
   *  questions on everybody (design §7 step 2). Recomputed from the answers,
   *  so a tick can reveal a question halfway through the form.
   *
   *  Two things about it are deliberate. It is a MEMO, because `For` keys
   *  its rows on the identity of the objects it is handed: a fresh `parse`
   *  per read would produce new Field objects on every keystroke, every row
   *  would be rebuilt, and the field being typed into would lose the caret.
   *  And it is created in an explicit ROOT, because a memo is a computation
   *  and one created at mount time otherwise belongs to no owner — nothing
   *  would ever dispose it, which is what Solid warns about on stderr. */
  const [visible, disposeState] = createRoot((disposeRoot) => {
    const parsed = createMemo(() => {
      const s = snippet()
      return s === null ? null : parse(s.body)
    })
    const fields = createMemo((): readonly Field[] => {
      const p = parsed()
      return p === null ? [] : visibleFields(p, answers())
    })
    return [fields, disposeRoot] as const
  })

  /** A field's value before anybody has touched it. Read as a fallback
   *  rather than written into the map when the field appears: a form that
   *  seeds on render has to decide what "appears" means for a field that
   *  comes and goes with a tick, and the answer differs from the one a
   *  person expects. A flag starts un-ticked; everything else starts on its
   *  default. */
  const answerFor = (f: Field): string =>
    answers().get(f.name) ?? (f.kind === 'flag' ? '' : f.defaultValue)

  const setAnswer = (name: string, value: string): void => {
    setAnswers((prev) => new Map(prev).set(name, value))
  }

  const close = (): void => {
    setSnippet(null)
    setError('')
    setFiring(false)
  }

  const submit = async (): Promise<void> => {
    const s = snippet()
    if (s === null || firing()) return
    setFiring(true)
    // What is SENT is the visible questions, each with the untouched default
    // filled in — an unanswered field is not the empty string, and the
    // resolver would refuse one. Answers to hidden questions ride along
    // unread: their block is cut before the substitution reaches them.
    const filled = new Map<string, string>(answers())
    for (const f of visible()) filled.set(f.name, answerFor(f))
    try {
      const refusal = await deps.fire(s, filled, destination())
      if (refusal !== null) {
        // Stays open with the reason and the answers: the person is one
        // edit away from a fire that works, and closing would take both
        // the sentence and the typing away (design §11).
        setError(refusal)
        return
      }
      close()
      deps.onDelivered()
    } finally {
      setFiring(false)
    }
  }

  const dispose = render(
    () => (
      <Show when={snippet()} keyed>
        {(s) => (
          <Dialog
            open={true}
            onClose={close}
            onSubmit={() => void submit()}
            title={s.title}
            size="md"
            footer={
              <>
                <Button variant="default" onClick={close}>
                  Cancel
                </Button>
                <Button variant="primary" onClick={() => void submit()}>
                  Insert
                </Button>
              </>
            }
          >
            <Stack>
              <For each={visible()}>
                {(f) => (
                  <Switch>
                    <Match when={f.kind === 'flag'}>
                      <Checkbox
                        label={f.name}
                        checked={answerFor(f) === FLAG_ON}
                        onChange={(on) => setAnswer(f.name, on ? FLAG_ON : '')}
                      />
                    </Match>
                    <Match when={f.kind === 'select'}>
                      <FieldRow for={`snippet-ask-${f.name}`} label={f.name}>
                        <Select
                          id={`snippet-ask-${f.name}`}
                          value={answerFor(f)}
                          options={f.options.map((o) => ({ value: o, label: o }))}
                          onChange={(v) => setAnswer(f.name, v)}
                        />
                      </FieldRow>
                    </Match>
                    <Match when={f.kind === 'text'}>
                      <TextField
                        id={`snippet-ask-${f.name}`}
                        label={f.name}
                        value={answerFor(f)}
                        onInput={(v) => setAnswer(f.name, v)}
                      />
                    </Match>
                  </Switch>
                )}
              </For>
              <Show when={error() !== ''}>
                <StatusCard tone="danger" title="Nothing was inserted" description={error()} />
              </Show>
            </Stack>
          </Dialog>
        )}
      </Show>
    ),
    parent,
  )

  return {
    ask(s: Snippet, dest: SnippetDestination): void {
      // Whether a body has anything to ask is the resolver's question, not
      // this form's: `needsForm` is what the palette and the completion
      // dropdown also ask, so a body they route here is one this can open
      // (AD-8). Counting visible fields here instead would make the form
      // decline a body whose only questions sit inside a block — which is
      // exactly the shape this task exists to support.
      if (!needsForm(s.body)) return
      setDestination(dest)
      setAnswers(new Map())
      setError('')
      setSnippet(s)
    },
    dispose(): void {
      close()
      dispose()
      disposeState()
    },
  }
}

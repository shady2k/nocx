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
import { For, Show, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Stack } from '../ui/stack'
import { StatusCard } from '../ui/status-card'
import { TextField } from '../ui/text-field'
import { visibleFields } from './resolve'
import { parse } from './parse'
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
  const [names, setNames] = createSignal<string[]>([])
  const [values, setValues] = createSignal<string[]>([])
  const [error, setError] = createSignal('')
  const [firing, setFiring] = createSignal(false)

  const close = (): void => {
    setSnippet(null)
    setError('')
    setFiring(false)
  }

  const submit = async (): Promise<void> => {
    const s = snippet()
    if (s === null || firing()) return
    setFiring(true)
    const answers = new Map<string, string>(names().map((name, i) => [name, values()[i] ?? '']))
    try {
      const refusal = await deps.fire(s, answers, destination())
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
              <For each={names()}>
                {(name, i) => (
                  <TextField
                    id={`snippet-ask-${name}`}
                    label={name}
                    value={values()[i()] ?? ''}
                    onInput={(v) =>
                      setValues((prev) => prev.map((old, at) => (at === i() ? v : old)))
                    }
                  />
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
      // With no answers yet, a field inside a condition is not visible:
      // the form opens on the ticks and what sits outside them (design §7
      // step 2). Revealing the rest as a tick goes on is nocx-0ow2b.
      const fields = visibleFields(parse(s.body), new Map())
      if (fields.length === 0) return
      setDestination(dest)
      setNames(fields.map((f) => f.name))
      // The defaults are the starting answers, so Enter on an untouched
      // form is the common case and costs nothing.
      setValues(fields.map((f) => f.defaultValue))
      setError('')
      setSnippet(s)
    },
    dispose(): void {
      close()
      dispose()
    },
  }
}

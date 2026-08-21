/**
 * CollisionDialog — the question asked when the file being sent already exists
 * where it is going.
 *
 * It knows nothing about uploads. No transport, no ticket, no destination
 * semantics: it takes three strings-and-a-number and answers with one of three
 * words plus whether that word covers the rest of the batch. The caller applies
 * the answer; this component stores no policy and remembers nothing between
 * openings, which is why it is mounted per question rather than kept alive with
 * an `open` flag.
 *
 * ## Composed, not painted
 *
 * This owns no markup and no CSS of its own — the panel, the rhythm, the tick
 * box and the three buttons are all kit components, exactly as `showConfirm`
 * and `showPrompt` are (see "Dialog body text is body text" in the kit README).
 * A collision question needs no appearance that `Dialog` does not already have,
 * so it declares no identity class and adds no stylesheet: a class nothing
 * paints is decoration, and an empty stylesheet is what `check-css-integrity`
 * calls unreachable.
 *
 * ## Why the answers sit where they do
 *
 * `Overwrite` destroys a file the user did not choose to lose, so it is
 * `danger` and it is FIRST — the actions row is `justify-content: flex-end`, so
 * first is furthest from the corner the pointer arrives at. `Skip` is last and
 * carries the autofocus, following `Dialog`'s own rule that on a destructive
 * question the explicitly focused control is the safe one. `Keep both` is the
 * primary: it is the only answer that loses nothing — neither the file already
 * there nor the one being sent — and a question with three answers still has a
 * recommendation.
 *
 * **Dismissal is Skip, never Overwrite.** Escape, the native cancel and a click
 * outside the panel all arrive as `Dialog`'s `onClose`, and all three answer
 * `skip`. Getting this backwards would let a stray keypress destroy a file on a
 * server, which is the single outcome this dialog exists to prevent.
 *
 * Focus is `Dialog`'s: a native modal `<dialog>` traps it, and `focusInitial`
 * picks the `autofocus` button. Nothing here builds a second trap.
 */
import { createSignal, Show } from 'solid-js'
import { Button } from './button'
import { Checkbox } from './checkbox'
import { Dialog } from './dialog'
import { Stack } from './stack'

/** What the user decided about one colliding file. */
export type CollisionAnswer = 'overwrite' | 'keepBoth' | 'skip'

/** The decision and its reach, in one value — the caller never has to read two. */
export interface CollisionResult {
  answer: CollisionAnswer
  /** Apply `answer` to every file still to come, without asking again. */
  applyToAll: boolean
}

/** The collision being asked about. Every string comes from the caller; this
 *  component invents no copy about hosts, sessions or protocols. */
export interface CollisionRequest {
  /** The file's name, as the user will recognise it. */
  name: string
  /** Where it is going, as the user will recognise it. */
  destination: string
  /**
   * How many files are still to be sent, THIS ONE INCLUDED. At 1 there is
   * nothing an "apply to all" could reach, so the checkbox is not drawn —
   * asking a question whose answer changes nothing is noise.
   */
  remaining: number
}

export interface CollisionDialogProps {
  request: CollisionRequest
  /** Called exactly once per user decision, with the answer and its reach. */
  onResolve: (result: CollisionResult) => void
}

export function CollisionDialog(props: CollisionDialogProps) {
  const [applyToAll, setApplyToAll] = createSignal(false)

  /** The files an "apply to all" would reach — the batch minus the one on screen. */
  const others = () => Math.max(0, props.request.remaining - 1)
  const asksAboutAll = () => others() > 0

  /**
   * The checkbox state is reported only while the checkbox is drawn. A caller
   * that shrinks `remaining` to 1 on a live instance would otherwise send a
   * `true` the user can no longer see, and "apply to all" is the half of the
   * answer with the blast radius.
   */
  const resolve = (answer: CollisionAnswer) =>
    props.onResolve({ answer, applyToAll: asksAboutAll() && applyToAll() })

  return (
    <Dialog
      open
      title="File already exists"
      onClose={() => resolve('skip')}
      footer={
        <>
          <Button variant="danger" onClick={() => resolve('overwrite')}>
            Overwrite
          </Button>
          <Button variant="primary" onClick={() => resolve('keepBoth')}>
            Keep both
          </Button>
          <Button variant="default" onClick={() => resolve('skip')} autofocus>
            Skip
          </Button>
        </>
      }
    >
      <Stack gap="loose">
        <p>
          <code>{props.request.name}</code> already exists in{' '}
          <code>{props.request.destination}</code>.
        </p>
        <Show when={asksAboutAll()}>
          <Checkbox
            checked={applyToAll()}
            onChange={(checked) => setApplyToAll(checked)}
            label={
              others() === 1
                ? 'Apply to the 1 remaining file'
                : `Apply to the ${others()} remaining files`
            }
          />
        </Show>
      </Stack>
    </Dialog>
  )
}

import { createSignal, type Component } from 'solid-js'
import { render } from 'solid-js/web'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { TextField } from './ui/text-field'
import { Stack } from './ui/stack'
import { SwatchPicker, type SwatchOption } from './ui/swatch-picker'
import { WORKSPACE_COLOURS, leastUsedWorkspaceColour } from './layout/workspace-colours'
import { t } from './i18n/locale-model'

/**
 * The name-and-colour dialog: one form, and every subject in the strip that
 * has both asks through it (nocx-2mipw, then the tab).
 *
 * IT REPLACED `showPrompt`, and the replacement is the point. A workspace was
 * created by a one-line text prompt, which is the kit's shape for "ask the
 * user for one thing" — and a workspace is not one thing. As soon as it has a
 * colour it needs a form, and building it as a form now is what makes the
 * field after this one an addition rather than a second rewrite.
 *
 * A TAB ASKS THE SAME QUESTION AND USED TO ASK IT TWICE OVER: a text prompt
 * for the name, and a run of colour words in the context menu for the colour
 * — "Green", "Amber", "Red", "Violet", one row each, with the palette buried
 * among Rename, Pin and Close. Two surfaces for one decision, and neither
 * showed the colour it was offering. One form, both subjects, and the menu
 * goes back to being a menu of actions.
 *
 * ONE PALETTE, TOO. The tab used to carry four THEME accents on the argument
 * that a decorated tab must still read after a theme change; the workspace
 * carries nine fixed hues because a container's identity may not shift under
 * it. Offering a person two palettes for the same act — "colour this thing in
 * the strip" — is a distinction they never asked for and cannot see the
 * reason for. The workspace's is the one that survives, because identity is
 * what a colour on a tab is for as well; the hues are declared identically in
 * every theme file (ADR-0013), so nothing loses its colour when the theme
 * changes.
 *
 * THE NAME IS PRE-FILLED, WHICH AMENDS §4.1 of the workspaces UX design. That
 * section says the name "is asked for and never invented" — a workspace,
 * unlike a tab, is always created deliberately, so it always has a name the
 * person typed. The owner chose Edge's behaviour instead, on sight: the field
 * opens with `Workspace N` selected, so Enter accepts it and the first
 * keystroke replaces it. The distinction §4.1 was protecting survives — the
 * name is still visible, still editable, and still refused when blank — but
 * "typed by a person" becomes "accepted by a person".
 *
 * THE COLOUR OFFERED FOR A NEW WORKSPACE IS THE LEAST-USED ONE
 * (layout/workspace-colours.ts), not a hash of the id and not the first
 * swatch. A default that collides on the second workspace is not a
 * suggestion, it is a chore.
 */

/** What the dialog answers with. `colour: null` is "no colour", which is a
 *  real answer for a subject that offers it and never reachable for one that
 *  does not. */
export interface NameColourDraft {
  readonly name: string
  readonly colour: string | null
}

/** Built when the dialog renders, not when this module loads: the labels are
 *  catalogue text, and a table made here would keep the starting language. */
const swatches = (): readonly SwatchOption[] =>
  WORKSPACE_COLOURS.map((c) => ({
    value: c.key,
    label: t(c.labelKey, c.key.charAt(0).toUpperCase() + c.key.slice(1)),
    // The token NAME, never a literal — see swatch-picker.tsx and ADR-0013.
    token: `--ws-${c.key}`,
  }))

const NameColourDialog: Component<{
  title: string
  submitLabel: string
  colourLabel: string
  /** Whether the subject may have no colour at all. A tab's ordinary state;
   *  never a workspace's. */
  allowNone: boolean
  /** Whether a blank name is refused. A workspace must have one — the backend
   *  refuses a blank and so does this — while a tab with no name falls back
   *  to what its panes call it (§4.5), so clearing the field is a real edit
   *  rather than a cancel. */
  nameRequired: boolean
  initialName: string
  initialColour: string | null
  onResolve: (draft: NameColourDraft | null) => void
}> = (props) => {
  const [name, setName] = createSignal(props.initialName)
  const [colour, setColour] = createSignal<string | null>(props.initialColour)
  const trimmed = () => name().trim()
  const submit = () => {
    // The backend refuses a blank name where one is required, and so does
    // this: a call that could be sent and refused is a round trip spent to
    // learn what the dialog already knew.
    if (props.nameRequired && trimmed() === '') return
    props.onResolve({ name: trimmed(), colour: colour() })
  }
  return (
    <Dialog
      open
      title={props.title}
      closeAria={t('ui.dialog.closeAria', 'Close dialog')}
      closeTitle={t('ui.dialog.closeTitle', 'Close')}
      onClose={() => props.onResolve(null)}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={() => props.onResolve(null)}>
            {t('nameColour.cancel', 'Cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={props.nameRequired && trimmed() === ''}
            onClick={submit}
          >
            {props.submitLabel}
          </Button>
        </>
      }
    >
      <Stack>
        <TextField
          label={t('nameColour.name', 'Name')}
          value={name()}
          onInput={setName}
          autoFocus
          selectOnFocus
        />
        <SwatchPicker
          options={swatches()}
          value={colour()}
          onChange={setColour}
          ariaLabel={props.colourLabel}
          allowNone={props.allowNone}
        />
      </Stack>
    </Dialog>
  )
}

/**
 * Put the dialog on screen and resolve with what the person answered, or with
 * null when they changed their mind — a different answer from an empty name,
 * and the reason this cannot return a bare string.
 */
function ask(props: {
  title: string
  submitLabel: string
  colourLabel: string
  allowNone: boolean
  nameRequired: boolean
  initialName: string
  initialColour: string | null
}): Promise<NameColourDraft | null> {
  return new Promise<NameColourDraft | null>((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)

    let dispose: (() => void) | null = null
    let settled = false

    const finish = (result: NameColourDraft | null) => {
      if (settled) return
      settled = true
      // Deferred for the same reason showPrompt defers: Dialog's own cleanup —
      // popOverlay and the focus restore — must run against a live root.
      queueMicrotask(() => {
        dispose?.()
        host.remove()
      })
      resolve(result)
    }

    dispose = render(() => <NameColourDialog {...props} onResolve={finish} />, host)
  })
}

/**
 * Ask for a new workspace's name and colour.
 *
 * `taken` is the colours the existing workspaces hold, so the offer can be one
 * of them is not using. Nulls and values this renderer does not know are
 * ignored rather than counted, which is the honest reading of both.
 */
export function showWorkspaceCreateDialog(
  existingCount: number,
  taken: readonly (string | null | undefined)[],
): Promise<NameColourDraft | null> {
  return ask({
    title: t('nameColour.newWorkspace', 'New workspace'),
    submitLabel: t('nameColour.submit.create', 'Create'),
    colourLabel: 'Workspace colour',
    // A workspace's colour is its identity in the strip (§5.5): every one of
    // them has one, so there is nothing here to decline.
    allowNone: false,
    nameRequired: true,
    // COUNT + 1, not "the number of workspaces you can see". The name is a
    // starting point the person will usually replace, and a suggestion that
    // had to be unique would have to grow a search — while the one thing it
    // must not do is be blank.
    initialName: `Workspace ${existingCount + 1}`,
    initialColour: leastUsedWorkspaceColour(taken),
  })
}

/**
 * Edit a workspace's name and colour — the SAME form the create used, which
 * is the point: a person who has met the create dialog has already learnt
 * this one, and recolouring stops being a thing you can only reach through a
 * menu you have to know is there.
 */
export function showWorkspaceEditDialog(
  name: string,
  colour: string | null,
): Promise<NameColourDraft | null> {
  return ask({
    title: t('nameColour.renameWorkspace', 'Rename workspace'),
    submitLabel: t('nameColour.submit.save', 'Save'),
    colourLabel: 'Workspace colour',
    allowNone: false,
    nameRequired: true,
    initialName: name,
    initialColour: colour,
  })
}

/**
 * Edit a tab's name and colour.
 *
 * A blank name is ACCEPTED here and it is not a cancel: a tab with no name of
 * its own falls back to what its panes call it (§4.5), so clearing the field
 * is how a person takes a name back off a tab. Only a workspace, which has no
 * such fallback, refuses it.
 */
export function showTabEditDialog(
  name: string,
  colour: string | null,
): Promise<NameColourDraft | null> {
  return ask({
    title: t('nameColour.renameTab', 'Rename tab'),
    submitLabel: t('nameColour.submit.save', 'Save'),
    colourLabel: 'Tab colour',
    allowNone: true,
    nameRequired: false,
    initialName: name,
    initialColour: colour,
  })
}

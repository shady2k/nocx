// The request form — one projection of the request file (design §6.4). The
// file is the truth; this and the HTTPie-style line that ships later are both
// projections of it, and neither is the owner.
//
// Every control is a kit component and this file places them; nothing here
// repaints one. The two things worth knowing before reading it:
//
// A SECRET IS NEVER A TEXT INPUT, and here it cannot be one. `auth.var` is a
// VARIABLE NAME — design §8 leaves no field in the whole contract where a
// secret value or a vault identifier could be spelled, so the attack is
// unspellable rather than guarded. The field edits that name; the chip beside
// it renders the BINDING (ADR-0021: the reference is what is stored, sent and
// resolved, and only the rendering is a chip). Both read the same draft, so
// they are two projections of one value and cannot disagree — the field is
// the only owner of the input.
//
// A DISABLED ROW IS A ROW THE USER KEEPS. Header and query rows carry
// `enabled`, and turning one off is a checkbox rather than deleting it:
// deleting a row to silence it loses the value they will want back.
//
// THE ENVIRONMENT IS NOT ON THE LINE. It governs a send, so it was put here
// first — and it belongs to the COLLECTION, not to one request, so it sits on
// the pane's header where the collection is named (api-pane.tsx). On the line
// it crushed the address field between two icon buttons and pushed its own
// pencil onto a second row, which is what a control in the wrong cell looks
// like before anybody argues about ownership.
//
// TWO COMPONENTS, ONE FORM. The line and the editor are exported separately
// because the workbench places them in different cells: the line spans the
// whole right half — a URL is the widest thing on the surface and it is what
// a person edits between one send and the next — while the editor is one
// column beside the response, which is the geometry every API client
// converges on and the one the owner asked for. They are not two forms: both
// project the same draft (§6.4) and both edit it through the same `onEdit`.
//
// THE FOUR SECTIONS ARE TABS, not a stack. Stacked, the four were a column
// two screens tall in which Auth was below the fold whatever you were doing,
// and every one of them was permanently on screen whether or not it had
// anything in it. A tab bar says the same four things in one line and puts
// the count where a person can see it without opening anything.

import { Show, createEffect, createSignal } from 'solid-js'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { IconButton } from '../ui/icon-button'
import { ArrowDownIcon } from '../ui/icons'
import { Select } from '../ui/select'
import { TextField, type TextFieldMark } from '../ui/text-field'
import { EditableRowList } from '../ui/row-list'
import { BodyEditor } from './body-editor'
import { Tabs } from '../ui/tabs'
import { layOutJSON } from '../ui/format-json'
import { showToast } from '../ui/toast'
import { createSecretChip } from '../ui/secret-chip'
import { applyTypedUrl, urlWithParams } from './api-url'
import { findVariables } from './variable-reference'
import type { ApiHeader, ApiParam, ApiRequest } from './api-model'

/** The verbs the picker offers. A file may hold anything the wire accepts,
 *  so whatever the request actually has is added to the list rather than
 *  replaced by the first entry — a picker that cannot show the current value
 *  is a picker that silently changes it. */
const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

const BODY_KINDS: Array<{ value: ApiRequest['body']['kind']; label: string }> = [
  { value: 'none', label: 'None' },
  { value: 'json', label: 'JSON' },
  { value: 'raw', label: 'Raw' },
  { value: 'form', label: 'Form' },
  { value: 'file', label: 'From a file in the collection' },
]

const AUTH_KINDS: Array<{ value: ApiRequest['auth']['kind']; label: string }> = [
  { value: 'none', label: 'None' },
  { value: 'bearer', label: 'Bearer' },
  { value: 'basic', label: 'Basic' },
  { value: 'apikey', label: 'API key' },
]

/** How many rows a tab is holding, for the tab's own label. A count that is
 *  zero is not shown: "Headers 0" is a longer way to write "Headers", and the
 *  point of the number is to be noticed. */
function counted(label: string, rows: readonly unknown[]): string {
  return rows.length > 0 ? `${label} ${rows.length}` : label
}

/** The kinds whose body is TEXT a person edits. `none` has no text and
 *  `file` names one instead, so both are a different control — and the
 *  editor must not be built for either, because building it mounts a CM6
 *  view. */
function isTextBody(kind: ApiRequest['body']['kind']): boolean {
  return kind === 'raw' || kind === 'json' || kind === 'form'
}

/** The kinds this surface can lay out for reading, which today is one.
 *
 *  A PREDICATE RATHER THAN A CHECK AT THE CALL SITE, because it is the thing
 *  the control's presence is derived from: the day a formatter arrives for
 *  another kind, the control appears for it by adding a case here rather than
 *  by somebody remembering there were two places to edit. `raw` and `form`
 *  are deliberately not in it — a form body is not JSON and laying it out
 *  would be this panel arguing with a person who already said what it is. */
function hasFormatter(kind: ApiRequest['body']['kind']): boolean {
  return kind === 'json'
}

/** Whether the body tab is holding anything. It has no rows to count, so the
 *  tab says so with a mark rather than a number — the same question the
 *  count answers for the two tables. */
function bodyLabel(request: ApiRequest | null): string {
  return request !== null && request.body.kind !== 'none' ? 'Body •' : 'Body'
}

function authLabel(request: ApiRequest | null): string {
  return request !== null && request.auth.kind !== 'none' ? 'Auth •' : 'Auth'
}

export interface RequestLineProps {
  request: ApiRequest | null
  dirty: boolean
  /** Whether a send is possible at all: there is a file behind this request. */
  sendable: boolean
  /** A run of THIS request is in flight. The one button becomes Stop while
   *  it is — one control for one exchange, because a second button that
   *  appeared beside Send would be a control that exists for two seconds and
   *  moves everything next to it when it does. */
  sending: boolean
  onEdit: (next: ApiRequest) => void
  onSend: () => void
  /** Stop the run that is in flight. Reached only while `sending`. */
  onStop: () => void
  /**
   * Ask for a curl command line to convert into this form.
   *
   * The door is HERE rather than in the collections menu because of what a
   * curl line becomes: one request, in this form, with no file behind it
   * until it is saved (api-store.ts). The menu beside the tree is about a
   * COLLECTION arriving — create, open, import — and a command line pasted
   * from somebody's terminal is not one. Optional, so a form mounted
   * without an owner for the ask simply does not offer it.
   */
  onImportCurl?: () => void
  /**
   * What the active environment says about a name: `bound` when it answers
   * it, `unbound` when it does not, and `unknown` when nobody has said —
   * which is not the same thing and must not be painted as `unbound`.
   *
   * Handed in rather than read here: which environment is active and what it
   * holds is the store's, and a form that fetched it would be a second
   * answer to a question that already has one.
   */
  variableState?: (name: string) => 'bound' | 'secret' | 'unbound' | 'unknown'
  /** Somebody clicked a variable in the address. The surface decides what to
   *  say about it — this line only knows where it was and what it is
   *  called. */
  onVariable?: (name: string, at: { x: number; y: number }) => void
}

/** The line: the verb, the address, what it goes out under, and Send. */
export function RequestLine(props: RequestLineProps) {
  const request = () => props.request
  const patch = (over: Partial<ApiRequest>): void => {
    const current = request()
    if (current) props.onEdit({ ...current, ...over })
  }

  // ── The URL field's own text ────────────────────────────────────────────
  //
  // The field shows the address WITH its enabled parameters (api-url.ts), so
  // adding a row in the table is visible where a person looks for it. That
  // makes the derived string and the typed string two versions of one value,
  // and the field needs its own copy for exactly one reason: while somebody
  // is typing, the model cannot represent what they have so far. Type the
  // `?` of `?page=2` and the model holds an address with no parameters,
  // whose derived form has no `?` in it — so the character would be erased
  // as it was typed, and a query could never be entered by hand at all.
  //
  // The rule, both ends named: from the moment the field takes the caret
  // until it loses it, the field's text is the truth and the model follows
  // every keystroke; at every other moment the model is the truth and the
  // text follows it. One caret means the two can never both be the owner.
  const [typedUrl, setTypedUrl] = createSignal('')

  createEffect(() => {
    const req = request()
    const derived = req === null ? '' : urlWithParams(req.url, req.query)
    if (document.activeElement?.id === 'api-url') return
    setTypedUrl(derived)
  })

  const editUrl = (typed: string): void => {
    setTypedUrl(typed)
    const current = request()
    if (current) props.onEdit(applyTypedUrl(current, typed))
  }

  /**
   * Which parts of the address are references rather than text.
   *
   * The scan is `variable-reference.ts`, which mirrors the backend's rule
   * character for character — that is what makes the mark honest: a
   * highlight over something the backend will send literally, or plain text
   * over something it will substitute, is worse than no highlight at all.
   * The kit paints them; nothing here says how they look.
   */
  const variableMarks = (): TextFieldMark[] =>
    findVariables(typedUrl()).map(({ from, to, name }) => ({
      from,
      to,
      // `unknown` is the kit's word for "a reference nothing answers", and it
      // is used ONLY when somebody can say so: an environment that has not
      // been read yet leaves every mark in the ordinary tone rather than
      // painting the address as broken while a listing is in flight.
      tone: markTone(props.variableState?.(name)),
    }))

  /** The kit's word for what this reference is. `unknown` is used ONLY when
   *  somebody can say so: an environment that has not been read yet leaves
   *  every mark in the ordinary tone rather than painting the address as
   *  broken while a listing is in flight. */
  const markTone = (state?: 'bound' | 'secret' | 'unbound' | 'unknown'): TextFieldMark['tone'] => {
    if (state === 'unbound') return 'unknown'
    if (state === 'secret') return 'secret'
    return 'reference'
  }

  /** Which variable a marked span is, by where it starts. The scan is the
   *  same one that produced the marks, so the two cannot disagree. */
  const variableAt = (from: number): string =>
    findVariables(typedUrl()).find((v) => v.from === from)?.name ?? ''

  const methodOptions = () => {
    const current = request()?.method ?? ''
    const all = current !== '' && !METHODS.includes(current) ? [current, ...METHODS] : METHODS
    return all.map((m) => ({ value: m, label: m }))
  }

  const sendTitle = (): string => {
    if (props.sending) return 'Stops the run that is in flight'
    if (!props.sendable) return 'Choose a request from a collection first'
    return props.dirty ? 'Saves this request, then sends it' : 'Send this request'
  }

  return (
    // The line is always here, and Send is refused rather than absent when
    // there is nothing to send: a control that disappears cannot tell a
    // person why it will not work, and the shape of the surface should not
    // change under them between one request and the next.
    <div class="api-request__line">
      <div class="api-request__method" data-api-field="method">
        <Select
          ariaLabel="Method"
          value={request()?.method ?? ''}
          onChange={(v) => patch({ method: v })}
          options={methodOptions()}
          disabled={request() === null}
        />
      </div>
      <div class="api-request__url">
        <TextField
          id="api-url"
          ariaLabel="URL"
          value={typedUrl()}
          disabled={request() === null}
          onInput={editUrl}
          marks={variableMarks()}
          onMarkClick={
            props.onVariable
              ? (mark, at) => {
                  const name = variableAt(mark.from)
                  if (name !== '') props.onVariable?.(name, at)
                }
              : undefined
          }
        />
      </div>
      {/* The import mark is the collections menu's own (ArrowDownIcon),
          because it is the same verb one level down: something written
          elsewhere arrives here. */}
      <Show when={props.onImportCurl}>
        <div class="api-request__import">
          <IconButton
            id="api-import-curl-open"
            size="sm"
            title="Import a curl command"
            ariaLabel="Import a curl command"
            onClick={() => props.onImportCurl?.()}
          >
            <ArrowDownIcon />
          </IconButton>
        </div>
      </Show>
      {/* SEND BECOMES STOP, and it stays ENABLED while it does. A disabled
          button was the only signal a request was in flight, which is
          exactly backwards: the moment there is something happening is the
          moment a person most needs a control, and what they want from it is
          to end it. `danger` because stopping is the destructive half of the
          pair — it discards an exchange that was under way. */}
      <div class="api-request__send">
        <Button
          variant={props.sending ? 'danger' : 'primary'}
          disabled={!props.sending && !props.sendable}
          title={sendTitle()}
          onClick={() => (props.sending ? props.onStop() : props.onSend())}
        >
          {props.sending ? 'Stop' : 'Send'}
        </Button>
      </div>
    </div>
  )
}

export interface RequestEditorProps {
  request: ApiRequest | null
  onEdit: (next: ApiRequest) => void
}

/** The editor: the four parts of a request, one at a time. */
export function RequestEditor(props: RequestEditorProps) {
  const request = () => props.request
  // WHICH TAB, held here. It is the surface's own state and not the store's:
  // it does not have to outlive the pane, and nothing else in the product
  // asks which part of a request somebody was last looking at.
  const [tab, setTab] = createSignal('params')

  const patch = (over: Partial<ApiRequest>): void => {
    const current = request()
    if (current) props.onEdit({ ...current, ...over })
  }

  const patchRow = <T extends ApiHeader | ApiParam>(
    rows: readonly T[],
    index: number,
    over: Partial<T>,
  ): T[] => rows.map((r, i) => (i === index ? { ...r, ...over } : r))

  // HOW MANY TIMES THIS BODY HAS BEEN LAID OUT — the editor's docKey carries
  // it, and that is the whole mechanism.
  //
  // BodyEditor re-fills from the draft only when its `docKey` changes, and
  // deliberately: pushing the draft back on every keystroke would move the
  // caret to the end of the document on every character. So the draft alone
  // cannot put formatted text on screen — the store would hold the laid-out
  // body while the editor went on showing the one line the person pressed the
  // control to be rid of. Bumping the counter says "the document in this
  // editor was replaced", which is what formatting is, and `fill` is already
  // a no-op when the text has not moved (body-editor.tsx).
  const [laidOut, setLaidOut] = createSignal(0)

  /**
   * Lay the body out, or say why not and change nothing.
   *
   * The refusals are the point. A body a person is about to send is the last
   * place for a best effort, so text that does not parse comes back untouched
   * with a sentence about it, and one too large to lay out cheaply says that
   * instead of freezing the pane it is being read in. Neither is silent: a
   * control that appears to do nothing is indistinguishable from a broken one.
   */
  const formatBody = (): void => {
    const current = request()
    if (!current) return
    const result = layOutJSON(current.body.text)
    if (result.kind === 'unreadable') {
      showToast({
        level: 'warning',
        message: 'This body is not valid JSON, so it was left exactly as it is.',
      })
      return
    }
    if (result.kind === 'too-large') {
      showToast({
        level: 'warning',
        message: `This body is over ${result.limit / 1024}K characters — too large to lay out here, so it was left exactly as it is.`,
      })
      return
    }
    patch({ body: { ...current.body, text: result.text } })
    setLaidOut((n) => n + 1)
  }

  return (
    <div class="api-request">
      <Show
        when={request()}
        fallback={
          <p class="api-request__idle">
            Choose a request from a collection, or import a curl line, and it appears here.
          </p>
        }
      >
        {(req) => (
          <Tabs
            orientation="horizontal"
            ariaLabel="This request"
            active={tab()}
            onChange={setTab}
            actions={
              <>
                {/* ABSENT WHERE THERE IS NO FORMATTER, which is the rule
                    this row already follows: the body kind is not offered on
                    the auth tab and the auth scheme is not offered on the
                    body tab, because a control that is present and inert
                    advertises something the surface cannot do. A `raw` or a
                    `form` body has no layout to be put into, so there is no
                    Format beside it — not a greyed one. */}
                <Show when={tab() === 'body' && hasFormatter(req().body.kind)}>
                  <Button onClick={formatBody}>Format</Button>
                </Show>
                <Show when={tab() === 'body'}>
                  <div data-api-field="body-kind">
                    <Select
                      ariaLabel="Body kind"
                      value={req().body.kind}
                      onChange={(v) =>
                        patch({ body: { ...req().body, kind: v as ApiRequest['body']['kind'] } })
                      }
                      options={BODY_KINDS}
                    />
                  </div>
                </Show>
                <Show when={tab() === 'auth'}>
                  <div data-api-field="auth-kind">
                    <Select
                      ariaLabel="Auth scheme"
                      value={req().auth.kind}
                      onChange={(v) =>
                        patch({ auth: { ...req().auth, kind: v as ApiRequest['auth']['kind'] } })
                      }
                      options={AUTH_KINDS}
                    />
                  </div>
                </Show>
              </>
            }
            items={[
              {
                id: 'params',
                label: counted('Params', req().query),
                content: () => (
                  <EditableRowList
                    variant="table"
                    ariaLabel="Query parameters"
                    columns={[
                      { label: 'Send', labelHidden: true },
                      { label: 'Name' },
                      { label: 'Value' },
                    ]}
                    rows={req().query}
                    addLabel="Add parameter"
                    emptyLabel="No query parameters."
                    removeLabel={(i) => `Remove parameter ${i + 1}`}
                    renderRow={(row, i) => (
                      <>
                        {/* The tick is FIRST, which is where a person looks
                            for "is this one on" — and it is a tick rather
                            than a delete, because a disabled row is a row
                            the user keeps. */}
                        <td>
                          <Checkbox
                            ariaLabel={`Send parameter ${i + 1}`}
                            checked={row().enabled}
                            onChange={(v) =>
                              patch({ query: patchRow(req().query, i, { enabled: v }) })
                            }
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-query-name-${i}`}
                            ariaLabel={`Parameter ${i + 1} name`}
                            value={row().name}
                            onInput={(v) => patch({ query: patchRow(req().query, i, { name: v }) })}
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-query-value-${i}`}
                            ariaLabel={`Parameter ${i + 1} value`}
                            value={row().value}
                            onInput={(v) =>
                              patch({ query: patchRow(req().query, i, { value: v }) })
                            }
                          />
                        </td>
                      </>
                    )}
                    onRemove={(i) => patch({ query: req().query.filter((_, j) => j !== i) })}
                    onAdd={() =>
                      patch({ query: [...req().query, { name: '', value: '', enabled: true }] })
                    }
                  />
                ),
              },
              {
                id: 'variables',
                label: counted('Variables', req().variables),
                content: () => (
                  <EditableRowList
                    variant="table"
                    ariaLabel="Request variables"
                    columns={[
                      { label: 'Send', labelHidden: true },
                      { label: 'Name' },
                      { label: 'Value' },
                    ]}
                    rows={req().variables}
                    addLabel="Add variable"
                    // The empty state says what the tab is FOR, because a
                    // person arriving here has usually just been sent by the
                    // panel behind a `{{name}}` they clicked: this is the
                    // scope that beats the environment, and that sentence is
                    // the whole reason to type a row here rather than there.
                    emptyLabel="No variables of this request's own. One here answers before the environment does."
                    removeLabel={(i) => `Remove variable ${i + 1}`}
                    renderRow={(row, i) => (
                      <>
                        <td>
                          <Checkbox
                            ariaLabel={`Use variable ${i + 1}`}
                            checked={row().enabled}
                            onChange={(v) =>
                              patch({ variables: patchRow(req().variables, i, { enabled: v }) })
                            }
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-variable-name-${i}`}
                            ariaLabel={`Variable ${i + 1} name`}
                            value={row().name}
                            onInput={(v) =>
                              patch({ variables: patchRow(req().variables, i, { name: v }) })
                            }
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-variable-value-${i}`}
                            ariaLabel={`Variable ${i + 1} value`}
                            value={row().value}
                            onInput={(v) =>
                              patch({ variables: patchRow(req().variables, i, { value: v }) })
                            }
                          />
                        </td>
                      </>
                    )}
                    onRemove={(i) =>
                      patch({ variables: req().variables.filter((_, j) => j !== i) })
                    }
                    onAdd={() =>
                      patch({
                        variables: [...req().variables, { name: '', value: '', enabled: true }],
                      })
                    }
                  />
                ),
              },
              {
                id: 'body',
                label: bodyLabel(req()),
                // THREE SIBLING SHOWS, NOT A SHOW WITH A FALLBACK. A JSX
                // element handed to a prop is a getter, and every access
                // builds the subtree again — which for the editor meant a
                // fresh CM6 view, mounted and filled, on every access. Each
                // fill reported a change, the change re-rendered the panel,
                // and the panel built another one. A boolean `when` per
                // branch is re-evaluated freely and only builds anything
                // when the branch actually flips.
                content: () => (
                  <>
                    <Show when={req().body.kind === 'none'}>
                      <p class="api-request__idle">No body is sent with this request.</p>
                    </Show>
                    <Show when={req().body.kind === 'file'}>
                      <TextField
                        id="api-body-file"
                        label="File in the collection"
                        description="A path WITHIN the collection, read on send under the handle's path rules."
                        value={req().body.fileRef}
                        onInput={(v) => patch({ body: { ...req().body, fileRef: v } })}
                      />
                    </Show>
                    <Show when={isTextBody(req().body.kind)}>
                      <BodyEditor
                        text={req().body.text}
                        // The request being edited, plus how many times its
                        // body has been laid out — see `laidOut` above.
                        docKey={`${req().id}#${laidOut()}`}
                        language={req().body.kind === 'json' ? 'json' : 'text'}
                        onChange={(text) => patch({ body: { ...req().body, text } })}
                      />
                    </Show>
                  </>
                ),
              },
              {
                id: 'headers',
                label: counted('Headers', req().headers),
                content: () => (
                  <EditableRowList
                    variant="table"
                    ariaLabel="Request headers"
                    columns={[
                      { label: 'Send', labelHidden: true },
                      { label: 'Name' },
                      { label: 'Value' },
                    ]}
                    rows={req().headers}
                    addLabel="Add header"
                    emptyLabel="No headers."
                    removeLabel={(i) => `Remove header ${i + 1}`}
                    renderRow={(row, i) => (
                      <>
                        <td>
                          <Checkbox
                            ariaLabel={`Send header ${i + 1}`}
                            checked={row().enabled}
                            onChange={(v) =>
                              patch({ headers: patchRow(req().headers, i, { enabled: v }) })
                            }
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-header-name-${i}`}
                            ariaLabel={`Header ${i + 1} name`}
                            value={row().name}
                            onInput={(v) =>
                              patch({ headers: patchRow(req().headers, i, { name: v }) })
                            }
                          />
                        </td>
                        <td>
                          <TextField
                            id={`api-header-value-${i}`}
                            ariaLabel={`Header ${i + 1} value`}
                            value={row().value}
                            onInput={(v) =>
                              patch({ headers: patchRow(req().headers, i, { value: v }) })
                            }
                          />
                        </td>
                      </>
                    )}
                    onRemove={(i) => patch({ headers: req().headers.filter((_, j) => j !== i) })}
                    onAdd={() =>
                      patch({ headers: [...req().headers, { name: '', value: '', enabled: true }] })
                    }
                  />
                ),
              },
              {
                id: 'auth',
                label: authLabel(req()),
                content: () => (
                  <Show
                    when={req().auth.kind !== 'none'}
                    fallback={
                      <p class="api-request__idle">No credential is sent with this request.</p>
                    }
                  >
                    <Show when={req().auth.kind === 'basic'}>
                      <TextField
                        id="api-auth-user"
                        label="User"
                        value={req().auth.user}
                        onInput={(v) => patch({ auth: { ...req().auth, user: v } })}
                      />
                    </Show>
                    <TextField
                      id="api-auth-var"
                      label="Secret variable"
                      description="The NAME of a variable. The value lives in the vault and never in this file — there is no field here it could be typed into."
                      value={req().auth.var}
                      onInput={(v) => patch({ auth: { ...req().auth, var: v } })}
                    />
                    <Show when={req().auth.var !== ''}>
                      <p class="api-request__binding">Sends {createSecretChip(req().auth.var)}</p>
                    </Show>
                  </Show>
                ),
              },
            ]}
          />
        )}
      </Show>
    </div>
  )
}

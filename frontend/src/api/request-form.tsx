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

import { Show } from 'solid-js'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Field } from '../ui/field'
import { Section } from '../ui/section'
import { Select } from '../ui/select'
import { Stack } from '../ui/stack'
import { TextField } from '../ui/text-field'
import { EditableRowList } from '../ui/row-list'
import { createSecretChip } from '../ui/secret-chip'
import type { ApiHeader, ApiParam, ApiRequest } from './api-model'

/** The verbs the picker offers. A file may hold anything the wire accepts,
 *  so whatever the request actually has is added to the list rather than
 *  replaced by the first entry — a picker that cannot show the current value
 *  is a picker that silently changes it. */
const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

const BODY_KINDS: Array<{ value: ApiRequest['body']['kind']; label: string }> = [
  { value: 'none', label: 'None' },
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

export interface RequestFormProps {
  request: ApiRequest | null
  /** True while the draft differs from the file — the Send button says so,
   *  because Send writes before it sends. */
  dirty: boolean
  /** False when the draft has no file behind it (a curl import that has not
   *  been saved): there is nothing on disk for api.request.send to send. */
  sendable: boolean
  sending: boolean
  onEdit: (next: ApiRequest) => void
  onSend: () => void
}

export function RequestForm(props: RequestFormProps) {
  const request = () => props.request
  const patch = (over: Partial<ApiRequest>): void => {
    const current = request()
    if (current) props.onEdit({ ...current, ...over })
  }

  const methodOptions = () => {
    const current = request()?.method ?? ''
    const all = current !== '' && !METHODS.includes(current) ? [current, ...METHODS] : METHODS
    return all.map((m) => ({ value: m, label: m }))
  }

  const sendTitle = (): string => {
    if (!props.sendable) return 'Save this request into a collection before sending it'
    return props.dirty ? 'Saves this request, then sends it' : 'Send this request'
  }

  const patchRow = <T extends ApiHeader | ApiParam>(
    rows: readonly T[],
    index: number,
    over: Partial<T>,
  ): T[] => rows.map((r, i) => (i === index ? { ...r, ...over } : r))

  return (
    <div class="api-request">
      {/* The line is always here, and Send is refused rather than absent
          when there is nothing to send: a control that disappears cannot
          tell a person why it will not work, and the shape of the surface
          should not change under them between one request and the next. */}
      <div class="api-request__line">
        <div class="api-request__method" data-api-field="method">
          <Select
            value={request()?.method ?? ''}
            onChange={(v) => patch({ method: v })}
            options={methodOptions()}
            disabled={request() === null}
          />
        </div>
        <div class="api-request__url">
          <TextField
            id="api-url"
            label="URL"
            value={request()?.url ?? ''}
            placeholder="{{baseUrl}}/users"
            disabled={request() === null}
            onInput={(v) => patch({ url: v })}
          />
        </div>
        <div class="api-request__send">
          <Button
            variant="primary"
            disabled={!props.sendable || props.sending}
            title={sendTitle()}
            onClick={() => props.onSend()}
          >
            Send
          </Button>
        </div>
      </div>

      <Show
        when={request()}
        fallback={
          <p class="api-request__idle">
            Choose a request from a collection, or import a curl line, and it appears here.
          </p>
        }
      >
        {(req) => (
          <Stack gap="loose">
            <Section title="Headers">
              <EditableRowList
                ariaLabel="Request headers"
                rows={req().headers}
                addLabel="Add header"
                emptyLabel="No headers."
                removeLabel={(i) => `Remove header ${i + 1}`}
                renderRow={(row, i) => (
                  <div class="api-request__row">
                    <TextField
                      id={`api-header-name-${i}`}
                      label="Name"
                      value={row().name}
                      onInput={(v) => patch({ headers: patchRow(req().headers, i, { name: v }) })}
                    />
                    <TextField
                      id={`api-header-value-${i}`}
                      label="Value"
                      value={row().value}
                      onInput={(v) => patch({ headers: patchRow(req().headers, i, { value: v }) })}
                    />
                    <Checkbox
                      label="Send"
                      checked={row().enabled}
                      onChange={(v) =>
                        patch({ headers: patchRow(req().headers, i, { enabled: v }) })
                      }
                    />
                  </div>
                )}
                onRemove={(i) => patch({ headers: req().headers.filter((_, j) => j !== i) })}
                onAdd={() =>
                  patch({ headers: [...req().headers, { name: '', value: '', enabled: true }] })
                }
              />
            </Section>

            <Section title="Query">
              <EditableRowList
                ariaLabel="Query parameters"
                rows={req().query}
                addLabel="Add parameter"
                emptyLabel="No query parameters."
                removeLabel={(i) => `Remove parameter ${i + 1}`}
                renderRow={(row, i) => (
                  <div class="api-request__row">
                    <TextField
                      id={`api-query-name-${i}`}
                      label="Name"
                      value={row().name}
                      onInput={(v) => patch({ query: patchRow(req().query, i, { name: v }) })}
                    />
                    <TextField
                      id={`api-query-value-${i}`}
                      label="Value"
                      value={row().value}
                      onInput={(v) => patch({ query: patchRow(req().query, i, { value: v }) })}
                    />
                    <Checkbox
                      label="Send"
                      checked={row().enabled}
                      onChange={(v) => patch({ query: patchRow(req().query, i, { enabled: v }) })}
                    />
                  </div>
                )}
                onRemove={(i) => patch({ query: req().query.filter((_, j) => j !== i) })}
                onAdd={() =>
                  patch({ query: [...req().query, { name: '', value: '', enabled: true }] })
                }
              />
            </Section>

            <Section title="Body">
              <Field for="api-body-kind" label="Body">
                <div data-api-field="body-kind">
                  <Select
                    value={req().body.kind}
                    onChange={(v) =>
                      patch({ body: { ...req().body, kind: v as ApiRequest['body']['kind'] } })
                    }
                    options={BODY_KINDS}
                  />
                </div>
              </Field>
              <Show when={req().body.kind === 'raw' || req().body.kind === 'form'}>
                <TextField
                  id="api-body-text"
                  label="Content"
                  multiline
                  value={req().body.text}
                  onInput={(v) => patch({ body: { ...req().body, text: v } })}
                />
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
            </Section>

            <Section title="Auth">
              <Field for="api-auth-kind" label="Scheme">
                <div data-api-field="auth-kind">
                  <Select
                    value={req().auth.kind}
                    onChange={(v) =>
                      patch({ auth: { ...req().auth, kind: v as ApiRequest['auth']['kind'] } })
                    }
                    options={AUTH_KINDS}
                  />
                </div>
              </Field>
              <Show when={req().auth.kind === 'basic'}>
                <TextField
                  id="api-auth-user"
                  label="User"
                  value={req().auth.user}
                  onInput={(v) => patch({ auth: { ...req().auth, user: v } })}
                />
              </Show>
              <Show when={req().auth.kind !== 'none'}>
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
            </Section>
          </Stack>
        )}
      </Show>
    </div>
  )
}

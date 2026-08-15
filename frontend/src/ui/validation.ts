/**
 * Form validation for the kit.
 *
 * `Field` and `TextField` already render an `error` and set `aria-invalid` — the
 * kit could *show* a validation failure from the day it was written. What it had
 * no vocabulary for was deciding there was one, so surfaces either skipped
 * validation entirely (the connections form let you save a profile with no host
 * and then produced a backend error on connect) or grew a private one (that same
 * file's `cm-form-error`, a single string for the whole form, in a colour token
 * that pointed at nothing).
 * Three pieces, deliberately separable:
 *
 * - **Validators** — `(value: string) => message | undefined`. Plain functions,
 *   no reactivity, trivially testable, composable with `combine`.
 * - **`createFormValidation`** — decides *when* a message is shown, which is a
 *   different question from whether the value is wrong.
 * - **`createSubmitGate`** — owns how a form refuses a submit: reveal every
 *   failing field, focus the first one, and announce how many need attention
 *   through the toast region. One owner, so surfaces stop each writing their
 *   own `valid() → revealAll() → showToast(firstError)` sequence — the count
 *   used to be lost, and the first invalid field used to sit unfocused. The
 *   gate lives in `submit-gate.ts`, its own module: it is browser-bound
 *   (announcing goes through the toast host), while this file stays pure and
 *   importable in a DOM-less test environment.
 *
 * ## When an error is shown
 *
 * Not while the user is still typing the first character of an empty field: a
 * form that turns red before you have finished answering it is reporting your
 * progress as failure. A message appears when the user has left the field
 * (`touch`), when they have typed something for it to judge (`answer`), or when
 * they have tried to submit (`revealAll`). `valid()`, `firstError()`,
 * `firstErrorField()` and `errorCount()` ignore all three and answer about the
 * values themselves, which is what a submit handler needs.
 *
 * The distinction that matters is between "you have not answered yet" and "what
 * you answered is wrong". The first must wait — the second should not. A host
 * with a space in it is wrong the moment the space is typed, and holding that
 * back until Create is pressed makes the form look like it accepted the value.
 */
import { createSignal } from 'solid-js'

export type Validator = (value: string) => string | undefined

/**
 * Non-empty after trimming. `label` names the field in the message, so the text
 * reads as a sentence about this field rather than a generic "Required".
 */
export function required(label: string): Validator {
  return (value) => (value.trim() === '' ? `${label} is required` : undefined)
}

/**
 * A hostname, IPv4 address, or bracketed IPv6 literal.
 *
 * Deliberately permissive about what a *name* may contain — underscores and
 * trailing dots appear in real internal DNS, and rejecting a host the user can
 * actually reach is a worse failure than accepting one they cannot. It rejects
 * what cannot be a host at all: whitespace, a scheme, a path, an embedded port
 * or userinfo. Those are the mistakes people actually make here — pasting
 * `ssh://box:22` or `user@box` into a field that already has User and Port
 * beside it.
 */
export function hostname(): Validator {
  return (value) => {
    const host = value.trim()
    if (host === '') return undefined
    if (/\s/.test(host)) return 'Host cannot contain spaces'
    if (host.includes('://')) return 'Enter a host name only, without a scheme'
    if (host.includes('/')) return 'Enter a host name only, without a path'
    if (host.includes('@')) return 'Enter a host name only — the user goes in the User field'
    // A bracketed IPv6 literal is the one form where colons are legal.
    if (host.startsWith('[')) {
      return /^\[[0-9a-fA-F:]+\]$/.test(host) ? undefined : 'Not a valid IPv6 address'
    }
    if (host.includes(':')) return 'Enter a host name only — the port goes in the Port field'
    if (!/^[A-Za-z0-9._-]+$/.test(host)) return 'Host contains characters that are not valid'
    return undefined
  }
}

/** A TCP port: an integer in 1..65535. Empty passes — compose with `required`. */
export function port(): Validator {
  return (value) => {
    const text = value.trim()
    if (text === '') return undefined
    if (!/^\d+$/.test(text)) return 'Port must be a whole number'
    const n = Number(text)
    if (n < 1 || n > 65535) return 'Port must be between 1 and 65535'
    return undefined
  }
}

/**
 * An absolute http(s) URL — the parse-level floor for an AI endpoint's base
 * URL (design §4.5, decision 3): only "is this an absolute http(s) URL" is
 * checked here. The loopback/private policy is enforced at dial time by the
 * HTTP client (nocx-edio) — a form-time check on that is decoration, for the
 * four reasons the design records. Empty passes — compose with `required`.
 */
export function absoluteHttpUrl(): Validator {
  return (value) => {
    const text = value.trim()
    if (text === '') return undefined
    let parsed: URL
    try {
      parsed = new URL(text)
    } catch {
      return 'Must be an absolute http(s) URL'
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'Must be an absolute http(s) URL'
    }
    if (parsed.hostname === '') return 'Must be an absolute http(s) URL'
    return undefined
  }
}

/**
 * A whole number ≥ 0. For the timeout and count fields where `0` is a legal
 * value meaning "off", so `required` is wrong and a range check is all there is.
 */
export function nonNegativeInteger(label: string): Validator {
  return (value) => {
    const text = value.trim()
    if (text === '') return undefined
    if (!/^\d+$/.test(text)) return `${label} must be a whole number`
    return undefined
  }
}

/** First failing validator wins, so rules read in the order they should fire. */
export function combine(...validators: Validator[]): Validator {
  return (value) => {
    for (const validate of validators) {
      const message = validate(value)
      if (message !== undefined) return message
    }
    return undefined
  }
}

export interface FormValidation<K extends string> {
  /** The message to render for a field — `undefined` until it is touched or revealed. */
  error(field: K): string | undefined
  /** Mark a field as answered. Call from the control's `onBlur`. */
  touch(field: K): void
  /**
   * Mark a field as answered *if there is an answer*. Call from the control's
   * `onInput`, passing what the user has typed so far.
   *
   * An empty value is ignored, which is the whole point: `required` still waits
   * for a blur or a submit, while a rule that judges content — a host with a
   * space, a port of `70000` — reports as soon as there is content to judge.
   */
  answer(field: K, value: string): void
  /** Show every failing field at once. Call when submit is attempted. */
  revealAll(): void
  /** Whether the values pass, regardless of what is currently shown. */
  valid(): boolean
  /** First failing message in declaration order — the one to put in a toast. */
  firstError(): string | undefined
  /** First failing field in declaration order — the one to focus. */
  firstErrorField(): K | undefined
  /** How many fields are failing now, regardless of what is shown. */
  errorCount(): number
  /** The DOM id of a field's control — the rule key by default, or the
   *  configured mapper's answer, which may be `undefined` for a field that
   *  has no focusable control. */
  controlId(field: K): string | undefined
  /** Forget every touch. Call when the form switches to a different record. */
  reset(): void
}

export interface FormValidationOptions<K extends string> {
  /**
   * Map a rule key to the DOM id of its control. Defaults to the key itself —
   * a form whose control ids are the logical field names needs nothing.
   * Return `undefined` for a field that has no focusable control, and the
   * submit gate will say it could not focus rather than pretend it did.
   */
  controlId?: (field: K) => string | undefined
}
/**
 * Wire a set of rules to the show-it-yet decision.
 *
 * Rules are accessors, not values, so they read whatever reactive state the
 * surface keeps its draft in — this makes no assumption about how the form is
 * stored, which is why it can serve both a store and a plain signal.
 *
 *     const v = createFormValidation({
 *       host: () => combine(required('Host'), hostname())(draft().host),
 *     })
 *     <TextField error={v.error('host')} onBlur={() => v.touch('host')} … />
 */
export function createFormValidation<K extends string>(
  rules: Record<K, () => string | undefined>,
  options: FormValidationOptions<K> = {},
): FormValidation<K> {
  const [touched, setTouched] = createSignal<ReadonlySet<K>>(new Set<K>())
  const [revealed, setRevealed] = createSignal(false)
  const keys = Object.keys(rules) as K[]
  const messageOf = (field: K) => rules[field]()
  const touch = (field: K) =>
    setTouched((current) => (current.has(field) ? current : new Set([...current, field])))
  const controlId = options.controlId ?? ((field: K) => field)

  return {
    error: (field) => (revealed() || touched().has(field) ? messageOf(field) : undefined),
    touch,
    answer: (field, value) => {
      if (value.trim() === '') return
      touch(field)
    },
    revealAll: () => setRevealed(true),
    valid: () => keys.every((key) => messageOf(key) === undefined),
    firstError: () => {
      for (const key of keys) {
        const message = messageOf(key)
        if (message !== undefined) return message
      }
      return undefined
    },
    firstErrorField: () => {
      for (const key of keys) {
        if (messageOf(key) !== undefined) return key
      }
      return undefined
    },
    errorCount: () =>
      keys.reduce((count, key) => (messageOf(key) !== undefined ? count + 1 : count), 0),
    controlId,
    reset: () => {
      setTouched(new Set<K>())
      setRevealed(false)
    },
  }
}

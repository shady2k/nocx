import { describe, expect, it } from 'vitest'
import { createRoot } from 'solid-js'
import {
  createFormValidation,
  required,
  hostname,
  port,
  nonNegativeInteger,
  combine,
} from './validation'

describe('required', () => {
  it('names the field in the message', () => {
    expect(required('Host')('')).toBe('Host is required')
  })

  it('treats whitespace as empty', () => {
    expect(required('Host')('   ')).toBe('Host is required')
  })

  it('passes any non-blank value', () => {
    expect(required('Host')('box')).toBeUndefined()
  })
})

describe('hostname', () => {
  it('passes an empty value — emptiness is `required`’s job, not this rule’s', () => {
    expect(hostname()('')).toBeUndefined()
  })

  it.each(['example.com', 'box', '10.0.0.1', 'db-01.internal', 'my_host', '[::1]', '[fe80::1]'])(
    'accepts %s',
    (host) => {
      expect(hostname()(host)).toBeUndefined()
    },
  )

  // The mistakes people actually make in a form that has User and Port beside it.
  it('rejects a value with the port pasted in', () => {
    expect(hostname()('box:22')).toBe('Enter a host name only — the port goes in the Port field')
  })

  it('rejects a value with the user pasted in', () => {
    expect(hostname()('root@box')).toBe('Enter a host name only — the user goes in the User field')
  })

  it('rejects a URL', () => {
    expect(hostname()('ssh://box')).toBe('Enter a host name only, without a scheme')
  })

  it('rejects a path', () => {
    expect(hostname()('box/srv')).toBe('Enter a host name only, without a path')
  })

  it('rejects spaces', () => {
    expect(hostname()('my box')).toBe('Host cannot contain spaces')
  })

  it('rejects a malformed IPv6 literal', () => {
    expect(hostname()('[not-an-address]')).toBe('Not a valid IPv6 address')
  })
})

describe('port', () => {
  it('accepts the ends of the range', () => {
    expect(port()('1')).toBeUndefined()
    expect(port()('65535')).toBeUndefined()
  })

  it('rejects 0 and anything past the top', () => {
    expect(port()('0')).toBe('Port must be between 1 and 65535')
    expect(port()('65536')).toBe('Port must be between 1 and 65535')
  })

  it('rejects a non-integer', () => {
    expect(port()('22.5')).toBe('Port must be a whole number')
    expect(port()('-1')).toBe('Port must be a whole number')
  })
})

describe('nonNegativeInteger', () => {
  it('accepts 0, because 0 means "off" for these fields', () => {
    expect(nonNegativeInteger('Ready timeout')('0')).toBeUndefined()
  })

  it('rejects a negative value', () => {
    expect(nonNegativeInteger('Ready timeout')('-5')).toBe('Ready timeout must be a whole number')
  })
})

describe('combine', () => {
  it('reports the first failure, so rules fire in declaration order', () => {
    expect(combine(required('Host'), hostname())('')).toBe('Host is required')
    expect(combine(required('Host'), hostname())('box:22')).toBe(
      'Enter a host name only — the port goes in the Port field',
    )
  })
})

describe('createFormValidation', () => {
  // The rule the whole "touched" mechanism exists for: a form must not turn red
  // before the user has finished answering it.
  it('shows nothing until a field is touched', () => {
    createRoot((dispose) => {
      const v = createFormValidation({ host: () => required('Host')('') })
      expect(v.error('host')).toBeUndefined()
      v.touch('host')
      expect(v.error('host')).toBe('Host is required')
      dispose()
    })
  })

  it('reveals every failing field at once on submit', () => {
    createRoot((dispose) => {
      const v = createFormValidation({
        host: () => required('Host')(''),
        user: () => required('User')(''),
      })
      v.revealAll()
      expect(v.error('host')).toBe('Host is required')
      expect(v.error('user')).toBe('User is required')
      dispose()
    })
  })

  it('answers valid() and firstError() regardless of what is shown', () => {
    createRoot((dispose) => {
      const v = createFormValidation({
        host: () => required('Host')(''),
        user: () => required('User')(''),
      })
      expect(v.error('host')).toBeUndefined()
      expect(v.valid()).toBe(false)
      expect(v.firstError()).toBe('Host is required')
      dispose()
    })
  })

  it('is valid when every rule passes', () => {
    createRoot((dispose) => {
      const v = createFormValidation({ host: () => required('Host')('box') })
      expect(v.valid()).toBe(true)
      expect(v.firstError()).toBeUndefined()
      dispose()
    })
  })

  it('re-reads the rule, so an error clears once the value is fixed', () => {
    createRoot((dispose) => {
      let host = ''
      const v = createFormValidation({ host: () => required('Host')(host) })
      v.touch('host')
      expect(v.error('host')).toBe('Host is required')
      host = 'box'
      expect(v.error('host')).toBeUndefined()
      dispose()
    })
  })

  // Without this, opening a fresh blank form inherits the previous record's
  // touches and greets the user with errors they have not caused yet.
  it('forgets touches and the reveal on reset', () => {
    createRoot((dispose) => {
      const v = createFormValidation({ host: () => required('Host')('') })
      v.revealAll()
      v.touch('host')
      v.reset()
      expect(v.error('host')).toBeUndefined()
      dispose()
    })
  })
})

// Reported from the running app: a host full of characters a host cannot
// contain sat there looking accepted until Create was pressed. "You have not
// answered yet" must wait; "what you answered is wrong" must not.
describe('answer', () => {
  it('reports a wrong value as soon as there is one, without a blur', () => {
    createRoot((dispose) => {
      let host = ''
      const v = createFormValidation({
        host: () => combine(required('Host'), hostname())(host),
      })

      host = 'фывфы'
      v.answer('host', host)
      expect(v.error('host')).toBe('Host contains characters that are not valid')
      dispose()
    })
  })

  it('leaves an empty field alone, so required does not fire mid-word', () => {
    createRoot((dispose) => {
      const v = createFormValidation({ host: () => required('Host')('') })
      v.answer('host', '')
      expect(v.error('host')).toBeUndefined()
      // …and a value of nothing but spaces is still no answer.
      v.answer('host', '   ')
      expect(v.error('host')).toBeUndefined()
      dispose()
    })
  })
})

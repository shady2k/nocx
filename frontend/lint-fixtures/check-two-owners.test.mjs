import { describe, it, expect } from 'vitest'
import { scanSource } from './check-two-owners.mjs'

/**
 * The two-owners checker's own tests.
 *
 * The rule: a value-bearing JSX prop (`value`, `checked`, `selected`,
 * `defaultValue`) whose expression is `lhs || literal` or `lhs ?? literal` —
 * a default invented at the render site, which by construction no validator or
 * model can see (nocx-a88r: the port input painted 22 while the validator
 * judged an empty draft). The absence-preserving forms are excluded:
 * `?? undefined` invents nothing and `?? null` / `?? ''` narrow absent to
 * absent, so the surface and the model agree. `|| ''` is NOT excluded — `||`
 * is falsy-triggered, so `0 || ''` paints empty where the raw value is 0, the
 * same shape as `|| 22`. Fixtures that must trip it, fixtures that must not.
 *
 * Every fixture is inlined source: the scanner parses text, so the tests are
 * hermetic and the cases read next to their assertions.
 */

function scan(source) {
  return scanSource('fixture.tsx', source)
}

/** The exact pre-fix line from d037c9d7^:frontend/src/connections.tsx. */
const PRE_FIX_PORT_INPUT = `
export function PortInput() {
  return <input type="number" value={fvNum('port') || 22} />
}
`

describe('must trip', () => {
  it('flags the historical defect verbatim: value={fvNum("port") || 22}', () => {
    const v = scan(PRE_FIX_PORT_INPUT)
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({
      prop: 'value',
      operator: '||',
      lhs: "fvNum('port')",
      fallback: '22',
    })
  })

  it('flags a string-literal fallback: value={fvStr("helperConsent") || "unknown"}', () => {
    const v = scan('<input value={fvStr("helperConsent") || "unknown"} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'value', operator: '||', fallback: '"unknown"' })
  })

  it('flags `|| ""` — falsy-triggered, so `0 || ""` paints empty where the raw value is 0', () => {
    // NOT excluded, unlike `?? ""`. `||` rewrites every falsy value (0, "",
    // false): `fvNum("port") || ""` paints empty while a validator reading
    // the raw value sees 0 — the same disagreement as `|| 22`. Nullish `??`
    // fires only on null/undefined, so `?? ""` narrows absent to absent and
    // passes (see "must NOT trip"). Keep the two cases separate branches;
    // "simplifying" them into one silently re-admits the defect shape.
    const v = scan('<input type="number" value={fvNum("port") || ""} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'value', operator: '||', fallback: '""' })
  })

  it('flags checked with a boolean fallback', () => {
    const v = scan('<input type="checkbox" checked={enabled || false} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'checked', operator: '||', fallback: 'false' })
  })

  it('flags selected with a fallback', () => {
    const v = scan('<option selected={current ?? false}>x</option>')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'selected', operator: '??', fallback: 'false' })
  })

  it('flags defaultValue with a numeric fallback', () => {
    const v = scan('<input defaultValue={count ?? 0} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'defaultValue', operator: '??', fallback: '0' })
  })

  it('flags a template-literal fallback with no substitutions', () => {
    const v = scan('<input value={name || `n/a`} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'value', operator: '||', fallback: '`n/a`' })
  })

  it('flags a parenthesised fallback (parens are not part of the AST)', () => {
    const v = scan('<input value={(fvNum("port") || 22)} />')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'value', operator: '||', fallback: '22' })
  })

  it('flags a multiline fallback', () => {
    const v = scan('<input\n  value={\n    fvNum("port")\n    || 22\n  }\n/>')
    expect(v).toHaveLength(1)
    expect(v[0]).toMatchObject({ prop: 'value', operator: '||', fallback: '22' })
  })

  it('reports a parse error as a violation (fail closed, never silent)', () => {
    const v = scan('<input value={')
    expect(v).toHaveLength(1)
    expect(v[0].prop).toBe('PARSE')
  })
})

describe('must NOT trip', () => {
  it('passes a resolver read with no fallback', () => {
    expect(scan('<input type="number" value={fvNum("port")} />')).toHaveLength(0)
  })

  it('passes a ternary default (a different shape from || / ??)', () => {
    expect(
      scan('<input value={row.bindPort != null ? String(row.bindPort) : "0"} />'),
    ).toHaveLength(0)
  })

  it('passes a fallback nested inside a conditional expression', () => {
    expect(
      scan(
        '<input value={props.inherit && props.auth === undefined ? INHERIT_AUTH : (props.auth ?? "")} />',
      ),
    ).toHaveLength(0)
  })

  it('passes a non-literal right side', () => {
    expect(scan('<input value={a || b} />')).toHaveLength(0)
    expect(scan('<input value={a || fallback()} />')).toHaveLength(0)
    expect(scan('<input value={a || `x${b}`} />')).toHaveLength(0)
  })

  it('passes ?? undefined (invents nothing)', () => {
    expect(scan('<input value={a ?? undefined} />')).toHaveLength(0)
  })

  it('passes ?? "" (narrows absent to absent — the render site and the model agree)', () => {
    // `??` fires only on null/undefined, so the fallback paints exactly the
    // state a validator reading the raw value sees: both empty. It is also the
    // idiomatic way to keep a text input controlled, so it will appear in
    // every new form field. Excluded on purpose; `|| ""` above is NOT excluded
    // — falsy-triggered `||` rewrites 0 and false too. Do not merge the cases.
    expect(scan('<input value={row.bindHost ?? ""} />')).toHaveLength(0)
    expect(scan('<input value={a ?? ""} />')).toHaveLength(0)
  })

  it('passes a fallback on a non-value-bearing prop', () => {
    expect(scan('<input placeholder={a || "x"} />')).toHaveLength(0)
  })

  it('passes a plain string attribute', () => {
    expect(scan('<input value="22" />')).toHaveLength(0)
  })

  it('passes expressions without a logical operator', () => {
    expect(scan('<input value={x + y} />')).toHaveLength(0)
    expect(scan('<input value={String(x)} />')).toHaveLength(0)
  })

  it('passes logical operators on non-JSX code', () => {
    expect(scan('const x = a || 22')).toHaveLength(0)
  })
})

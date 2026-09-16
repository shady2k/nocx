// @vitest-environment jsdom
// The vanilla Spinner is the Solid one's twin, and the pair is held together
// by this test rather than by care (spec 2026-09-14 §6.2): a variance added to
// one side only must fail here, not on the day a surface notices.
import { describe, it } from 'vitest'
import { render } from 'solid-js/web'
import { assertSameShape } from '../test-support/element-shape'
import { Spinner, type SpinnerProps } from './spinner'
import { createSpinner } from './spinner-element'

function fromSolid(props: SpinnerProps): Element {
  const host = document.createElement('div')
  const dispose = render(() => <Spinner {...props} />, host)
  const el = host.firstElementChild!
  dispose()
  return el
}

describe('createSpinner is <Spinner>’s twin', () => {
  const cases: SpinnerProps[] = [
    { label: 'Running' },
    { label: 'Running', size: 'sm' },
    { label: 'Loading', size: 'md' },
  ]
  for (const props of cases) {
    it(`matches for ${JSON.stringify(props)}`, () => {
      assertSameShape(createSpinner(props), fromSolid(props))
    })
  }
})

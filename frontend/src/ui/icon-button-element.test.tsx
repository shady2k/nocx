// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { assertSameShape } from '../test-support/element-shape'
import { CloseIcon, MoreIcon } from './icons'
import { IconButton, type IconButtonProps } from './icon-button'
import { createIconButton, type IconButtonElementOptions } from './icon-button-element'

afterEach(cleanup)

type SolidCase = Omit<IconButtonProps, 'children'> & Record<`data-${string}`, string>

/** Every variance IconButton has, plus the data-* passthrough the vanilla
 *  emitter exposes as `attrs`. */
const CASES: Array<[string, Omit<IconButtonElementOptions, 'icon'>, SolidCase]> = [
  ['default', { ariaLabel: 'Close' }, { ariaLabel: 'Close' }],
  ['xs', { ariaLabel: 'Close', size: 'xs' }, { ariaLabel: 'Close', size: 'xs' }],
  ['sm', { ariaLabel: 'Close', size: 'sm' }, { ariaLabel: 'Close', size: 'sm' }],
  ['lg', { ariaLabel: 'Close', size: 'lg' }, { ariaLabel: 'Close', size: 'lg' }],
  ['selected', { ariaLabel: 'Files', selected: true }, { ariaLabel: 'Files', selected: true }],
  [
    'primary appearance',
    { ariaLabel: 'Send', appearance: 'primary' },
    { ariaLabel: 'Send', appearance: 'primary' },
  ],
  ['square', { ariaLabel: 'Close', square: true }, { ariaLabel: 'Close', square: true }],
  [
    'rail indicator',
    { ariaLabel: 'Files', railIndicator: true },
    { ariaLabel: 'Files', railIndicator: true },
  ],
  ['disabled', { ariaLabel: 'Close', disabled: true }, { ariaLabel: 'Close', disabled: true }],
  [
    'title',
    { ariaLabel: 'Close', title: 'Close (Esc)' },
    { ariaLabel: 'Close', title: 'Close (Esc)' },
  ],
  ['tabIndex', { ariaLabel: 'Close', tabIndex: -1 }, { ariaLabel: 'Close', tabIndex: -1 }],
  [
    'data attrs',
    { ariaLabel: 'Block actions', attrs: { 'data-block-actions': '' } },
    { ariaLabel: 'Block actions', 'data-block-actions': '' },
  ],
]

function solid(props: SolidCase, icon: () => Element): Element {
  const { container } = render(() => <IconButton {...props}>{icon()}</IconButton>)
  return container.firstElementChild!
}

describe('createIconButton is the same element IconButton renders', () => {
  it.each(CASES)('%s', (_name, vanilla, props) => {
    const icon = () => MoreIcon({}) as Element
    assertSameShape(createIconButton({ ...vanilla, icon }), solid(props, icon))
  })

  it('the comparison fails when the two emitters disagree', () => {
    expect(() =>
      assertSameShape(
        createIconButton({ ariaLabel: 'Close', size: 'xs', icon: () => CloseIcon({}) as Element }),
        solid({ ariaLabel: 'Close', size: 'sm' }, () => CloseIcon({}) as Element),
      ),
    ).toThrow(/emitters disagree/)
  })

  it('fires onClick with the event', () => {
    const onClick = vi.fn()
    const btn = createIconButton({
      ariaLabel: 'Close',
      icon: () => CloseIcon({}) as Element,
      onClick,
    })
    btn.click()
    expect(onClick).toHaveBeenCalledTimes(1)
    expect(onClick.mock.calls[0][0]).toBeInstanceOf(MouseEvent)
  })
})

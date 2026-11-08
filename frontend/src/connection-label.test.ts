// How a saved connection is named where a person has to read BOTH what they
// called it and where it goes (nocx-ouru5.1.2): the tab strip, and the
// composer's place line. One function, because the two surfaces saying it two
// ways is how the tab and the prompt start naming different things.
import { describe, expect, it } from 'vitest'
import { connectionLabel } from './connection-label'

describe('a saved connection’s name', () => {
  it('reads the name first and the host beside it', () => {
    expect(connectionLabel('Production', '10.0.0.5')).toBe('Production · 10.0.0.5')
  })

  it('leaves the host alone when the connection has no name', () => {
    // The owner's own words: "a connection with no name keeps the host alone".
    expect(connectionLabel('', '10.0.0.5')).toBe('10.0.0.5')
    expect(connectionLabel(undefined, '10.0.0.5')).toBe('10.0.0.5')
    // A name of spaces is no name — printing " · host" would be a stray
    // separator for a connection somebody named with an empty field.
    expect(connectionLabel('   ', '10.0.0.5')).toBe('10.0.0.5')
  })

  it('says nothing when there is no host to say — a local pane is not a connection', () => {
    expect(connectionLabel(undefined, '')).toBe('')
    expect(connectionLabel('Production', '')).toBe('')
  })
})

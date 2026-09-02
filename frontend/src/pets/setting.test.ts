// The pet settings module (nocx-q4qeh.1).
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  applyPetsSettings,
  onPetsSettingsChanged,
  petHeight,
  petsEnabled,
  PETS_ENABLED_DEFAULT,
  PETS_SIZE_DEFAULT,
  resetPetsSettings,
} from './setting'

afterEach(() => resetPetsSettings())

describe('applyPetsSettings', () => {
  it('adopts the backend answers', () => {
    applyPetsSettings(false, 64)
    expect(petsEnabled()).toBe(false)
    expect(petHeight()).toBe(64)
  })

  it('keeps the declared default when a value is missing or the wrong type', () => {
    applyPetsSettings(undefined, 'big')
    expect(petsEnabled()).toBe(PETS_ENABLED_DEFAULT)
    expect(petHeight()).toBe(PETS_SIZE_DEFAULT)
  })

  it('clamps a size outside the declared bound', () => {
    // The terrain measures head clearance against this number; a value the
    // Go side would have refused must not reach the rules.
    applyPetsSettings(true, 5000)
    expect(petHeight()).toBe(96)
    applyPetsSettings(true, -3)
    expect(petHeight()).toBe(16)
  })

  it('rounds a fractional size to whole pixels', () => {
    applyPetsSettings(true, 40.6)
    expect(petHeight()).toBe(41)
  })
})

describe('onPetsSettingsChanged', () => {
  it('tells a listener when an answer actually changed', () => {
    const seen = vi.fn()
    onPetsSettingsChanged(seen)
    applyPetsSettings(false, PETS_SIZE_DEFAULT)
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('stays quiet when the snapshot says the same thing again', () => {
    // Every settings.changed notification refetches the WHOLE snapshot, so
    // this module is told about pets on every change to anything at all.
    const seen = vi.fn()
    onPetsSettingsChanged(seen)
    applyPetsSettings(PETS_ENABLED_DEFAULT, PETS_SIZE_DEFAULT)
    expect(seen).not.toHaveBeenCalled()
  })

  it('stops telling an unsubscribed listener', () => {
    const seen = vi.fn()
    onPetsSettingsChanged(seen)()
    applyPetsSettings(false, 64)
    expect(seen).not.toHaveBeenCalled()
  })
})

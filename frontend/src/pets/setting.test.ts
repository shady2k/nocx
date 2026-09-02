// The pet settings module (nocx-q4qeh.1).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  applyPetsSettings,
  onPetsSettingsChanged,
  petHeight,
  petsEnabled,
  PETS_ENABLED_DEFAULT,
  PETS_PACK_DEFAULT,
  PETS_SIZE_DEFAULT,
  petPack,
  petsSettingsKnown,
  resetPetsSettings,
} from './setting'

afterEach(() => resetPetsSettings())

describe('before the backend has answered', () => {
  it('there is no pet, whatever the declared default says', () => {
    // The window mounts its pet before the first snapshot arrives and the
    // declared default is ON, so a person who had switched pets off got the
    // sprite pack fetched anyway, every launch.
    expect(petsSettingsKnown()).toBe(false)
    expect(petsEnabled()).toBe(false)
  })

  it('and a failed fetch still counts as an answer', () => {
    // Otherwise the animal never appears at all.
    applyPetsSettings(undefined, undefined, undefined)
    expect(petsSettingsKnown()).toBe(true)
    expect(petsEnabled()).toBe(PETS_ENABLED_DEFAULT)
  })
})

describe('applyPetsSettings', () => {
  beforeEach(() => applyPetsSettings(PETS_ENABLED_DEFAULT, PETS_SIZE_DEFAULT, PETS_PACK_DEFAULT))

  it('adopts the backend answers', () => {
    applyPetsSettings(false, 64, 'cat-2')
    expect(petsEnabled()).toBe(false)
    expect(petHeight()).toBe(64)
    expect(petPack()).toBe('cat-2')
  })

  it('keeps the declared pack when the value is not a usable id', () => {
    applyPetsSettings(true, 34, 42)
    expect(petPack()).toBe(PETS_PACK_DEFAULT)
    applyPetsSettings(true, 34, '')
    expect(petPack()).toBe(PETS_PACK_DEFAULT)
  })

  it('keeps the declared default when a value is missing or the wrong type', () => {
    applyPetsSettings(undefined, 'big', 42)
    expect(petsEnabled()).toBe(PETS_ENABLED_DEFAULT)
    expect(petHeight()).toBe(PETS_SIZE_DEFAULT)
  })

  it('clamps a size outside the declared bound', () => {
    // The terrain measures head clearance against this number; a value the
    // Go side would have refused must not reach the rules.
    applyPetsSettings(true, 5000, 'cat-1')
    expect(petHeight()).toBe(96)
    applyPetsSettings(true, -3, 'cat-1')
    expect(petHeight()).toBe(16)
  })

  it('rounds a fractional size to whole pixels', () => {
    applyPetsSettings(true, 40.6, 'cat-1')
    expect(petHeight()).toBe(41)
  })
})

describe('onPetsSettingsChanged', () => {
  it('tells a listener when an answer actually changed', () => {
    const seen = vi.fn()
    onPetsSettingsChanged(seen)
    applyPetsSettings(false, PETS_SIZE_DEFAULT, PETS_PACK_DEFAULT)
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('stays quiet when the snapshot says the same thing again', () => {
    // Every settings.changed notification refetches the WHOLE snapshot, so
    // this module is told about pets on every change to anything at all.
    applyPetsSettings(PETS_ENABLED_DEFAULT, PETS_SIZE_DEFAULT, PETS_PACK_DEFAULT)
    const seen = vi.fn()
    onPetsSettingsChanged(seen)
    applyPetsSettings(PETS_ENABLED_DEFAULT, PETS_SIZE_DEFAULT, PETS_PACK_DEFAULT)
    expect(seen).not.toHaveBeenCalled()
  })

  it('the FIRST answer is news even when it matches the defaults', () => {
    // Nothing about the pet may happen before an answer arrives, so the
    // arrival itself has to be an event — otherwise a window whose settings
    // happen to equal the defaults never starts its animal.
    const seen = vi.fn()
    onPetsSettingsChanged(seen)
    applyPetsSettings(PETS_ENABLED_DEFAULT, PETS_SIZE_DEFAULT, PETS_PACK_DEFAULT)
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('stops telling an unsubscribed listener', () => {
    const seen = vi.fn()
    onPetsSettingsChanged(seen)()
    applyPetsSettings(false, 64, 'cat-2')
    expect(seen).not.toHaveBeenCalled()
  })
})

import { describe, expect, it } from 'vitest'
import { directionsNote } from './WiringTab'

describe('directionsNote', () => {
  it('says nothing when the directions went out or the firmware has no la_dir', () => {
    expect(directionsNote('sent')).toBeUndefined()
    expect(directionsNote(undefined)).toBeUndefined()
    expect(directionsNote("not sent: this pod's firmware (3.10.3) has no la_dir (3.10.4+)")).toBeUndefined()
  })

  it('says why when a driven pin kept them from going out', () => {
    const note = directionsNote('not sent: direction conflict: LA9 is driven by gpio output')
    expect(note).toBe('Saved, but the pin directions were not sent: direction conflict: LA9 is driven by gpio output. Release that pin, then save again.')
  })
})

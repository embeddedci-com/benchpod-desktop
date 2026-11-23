import { describe, expect, it } from 'vitest'
import { directionsNote, profileToSave } from './WiringTab'

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

describe('profileToSave', () => {
  it('writes the editor keys, pulls included, and leaves out what is not set', async () => {
    const { effectiveWiring } = await import('@bpui')
    const draft = { version: 2, uart_rx: 5, pulls: [1], loop_input_unit: 'mA' }
    const out = profileToSave(draft, effectiveWiring(draft))
    expect(out).toMatchObject({ version: 2, uart_rx: 5, uart_tx: null, pulls: [1], loop_input_unit: 'mA' })
    expect('la_mv' in out).toBe(false)
    expect('dut_dirs' in out).toBe(false)
    const none = profileToSave({ version: 2 }, effectiveWiring({ version: 2 }))
    expect('pulls' in none).toBe(false)
  })

  it('writes the JTAG pins, shared with SWD or not, and null for those left unwired', async () => {
    const { effectiveWiring } = await import('@bpui')
    const draft = { version: 2, swd_swclk: 11, swd_swdio: 12, jtag_tck: 11, jtag_tms: 12, jtag_tdi: 3, jtag_tdo: 4 }
    const out = profileToSave(draft, effectiveWiring(draft))
    expect(out).toMatchObject({ swd_swclk: 11, swd_swdio: 12, jtag_tck: 11, jtag_tms: 12, jtag_tdi: 3, jtag_tdo: 4, jtag_trst: null })
  })

  it('writes debug_port when set and leaves it out for automatic', async () => {
    const { effectiveWiring } = await import('@bpui')
    const draft = { version: 2, swd_swclk: 11, swd_swdio: 12, jtag_tck: 11, jtag_tms: 12, jtag_tdi: 3, jtag_tdo: 4, debug_port: 'jtag' }
    expect(profileToSave(draft, effectiveWiring(draft))).toMatchObject({ debug_port: 'jtag' })
    const auto = { ...draft, debug_port: null }
    expect('debug_port' in profileToSave(auto, effectiveWiring(auto))).toBe(false)
  })
})

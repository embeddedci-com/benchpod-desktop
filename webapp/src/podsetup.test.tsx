// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MantineProvider } from '@mantine/core'
import type { AppState, FoundPod } from './api'
import { PodSetup } from './PodSetup'

beforeAll(() => {
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as unknown as typeof window.matchMedia
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

const several: FoundPod = {
  connected: false,
  connection: '',
  la_voltage: null,
  tried: ['usb', 'your network'],
  error: 'usb: Error executing tool connect: ConnectionConfigError: 2 pods on USB ...',
  choices: [
    { name: 'benchpod-30a9f1', connection: 'usb:benchpod-30a9f1' },
    { name: 'benchpod-c2f662', connection: 'usb:benchpod-c2f662' },
  ],
}
const one: FoundPod = { connected: true, connection: 'usb:benchpod-c2f662', kind: 'usb_net', version: '4.0.0', la_voltage: 3.3, tried: ['usb:benchpod-c2f662'] }

describe('Find your pod with several pods', () => {
  it('lists them as buttons, and a button finds that pod alone', async () => {
    const asked: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        const { connection } = JSON.parse(String(init?.body ?? '{}')) as { connection: string }
        asked.push(connection)
        return reply(connection === '' ? several : one)
      }),
    )
    const state = { settings: { connection: '' } } as unknown as AppState
    render(
      <MantineProvider>
        <PodSetup state={state} onDone={() => {}} />
      </MantineProvider>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Find my pod' }))
    await screen.findByText('Several pods found')
    expect(screen.getByRole('button', { name: 'benchpod-30a9f1' })).toBeTruthy()
    // The raw error stays off the screen.
    expect(screen.queryByText(/ConnectionConfigError/)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'benchpod-c2f662' }))
    await screen.findByText('Found your pod')
    expect(asked).toEqual(['', 'usb:benchpod-c2f662'])
    await waitFor(() => expect((screen.getByLabelText(/Where/) as HTMLInputElement).value).toBe('usb:benchpod-c2f662'))
  })
})

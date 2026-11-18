// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MantineProvider } from '@mantine/core'
import type { BenchCall } from '@bpui'
import { WiringTab } from './WiringTab'

beforeAll(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as never
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false })) as never
  Element.prototype.scrollIntoView ??= () => {}
})
afterEach(cleanup)

const result = (data: unknown) => ({ text: JSON.stringify(data), structured: data }) as never

describe('WiringTab Visualize', () => {
  it('draws the profile on the header the MCP reports', async () => {
    const call: BenchCall = async (tool) =>
      tool === 'wiring'
        ? result({
            source: 'pod',
            profile: { version: 2, uart_rx: 3, uart_tx: 4 },
            la_voltage: 3.3,
            profile_name: 'k66',
            default_profile: 'k66',
            profiles: [{ name: 'k66', rev: 1 }],
            board_header: 'front',
          })
        : result({})
    render(
      <MantineProvider>
        <WiringTab call={call} refreshKey={0} />
      </MantineProvider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Visualize' }))
    expect((await screen.findByTestId('board-view-svg')).innerHTML).toContain('Facing the front')
    expect(screen.getByText('board TX')).toBeTruthy()
    expect(screen.queryByText('Pin headers (2x11, up to v3-r2)')).toBeNull()
  })
})

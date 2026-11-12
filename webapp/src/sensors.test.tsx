// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MantineProvider } from '@mantine/core'
import wasmInline from '@bpui/sensors/engine/sensor_def.wasm?inline'
import bmp280 from '@bpui/sensors/testdata/bmp280.yaml?raw'
import sht4x from '@bpui/sensors/testdata/sht4x.yaml?raw'
import { fakePod, type FakePod } from '@bpui/sensors/fakePod'
import { fromBase64, loadEngine, MemorySensorStore, setEngineSource, type Engine } from '@bpui/sensors'
import type { BenchCall } from '@bpui'
import type { AppState } from './api'
import { localFiles, localSensorPod, localSensorStore } from './sensors'
import { pinsFromWiring, SensorsView } from './SensorsView'

let engine: Engine
beforeAll(async () => {
  setEngineSource(fromBase64(wasmInline.slice(wasmInline.indexOf(',') + 1)))
  engine = await loadEngine()
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as unknown as typeof window.matchMedia
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as typeof HTMLCanvasElement.prototype.getContext
  Element.prototype.scrollIntoView ??= () => {}
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const reply = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(typeof body === 'string' ? body : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })

/**
 * benchpod-local in memory: the sensor routes over a MemorySensorStore (the reference store) and
 * the pod's sensor commands over the fake pod, so the page runs through the real HTTP mapping.
 */
function fakeBackend(pod: FakePod) {
  const mem = new MemorySensorStore({ version: '0.1.0', entries: [{ source: bmp280 }, { source: sht4x }] }, (json, kind) => {
    const r = kind === 'stimulus' ? engine.stimulusValidate(json) : engine.validate(json)
    return r.ok ? null : r.error
  }, { canChangeLibrary: true })
  const calls: { path: string; body?: unknown }[] = []
  const answer = async (p: () => Promise<unknown>, status = 200) => {
    try {
      return reply(status, (await p()) ?? {})
    } catch (e) {
      const err = e as Error & { code?: string }
      return reply(err.code === 'name_taken' ? 409 : err.code === 'not_found' ? 404 : 400, { error: err.message, code: err.code })
    }
  }
  const fetch = vi.fn(async (input: string, init?: RequestInit) => {
    const url = new URL(input, 'http://app')
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    calls.push({ path: `${method} ${url.pathname}${url.search}`, body })
    const m = url.pathname.match(/^\/api\/sensor-definitions\/([^/]+)$/)
    if (url.pathname === '/api/sensors/command') {
      try {
        return reply(200, { reply: await pod.send(body.command) })
      } catch (e) {
        return reply(502, { error: (e as Error).message })
      }
    }
    if (url.pathname === '/api/sensor-library') return answer(() => mem.library())
    if (url.pathname === '/api/sensor-library/settings') return answer(() => (method === 'PUT' ? mem.setLibraryVersion(body.version) : mem.librarySettings()))
    const e = url.pathname.match(/^\/api\/sensor-library\/entries\/([^/]+)$/)
    if (e) return answer(() => mem.libraryEntry(decodeURIComponent(e[1])))
    if (url.pathname === '/api/sensor-definitions') return method === 'POST' ? answer(() => mem.save(body.source), 201) : answer(() => mem.list())
    if (m && method === 'GET') return answer(() => mem.get(decodeURIComponent(m[1])))
    if (m && method === 'PUT') return answer(() => mem.save(body.source, decodeURIComponent(m[1])))
    return reply(404, { error: 'no route' })
  })
  vi.stubGlobal('fetch', fetch)
  return { mem, calls, fetch }
}

const state = (sensors: boolean | undefined): AppState =>
  ({
    pod: { connected: true, connection: 'usb', status: { capabilities: sensors === undefined ? {} : { sensors } } },
    settings: {},
  }) as unknown as AppState

const WIRING = { version: 1, la_mv: 3300, i2c_sda: 1, i2c_scl: 2, signals: [] }

function benchCall() {
  return vi.fn<BenchCall>(async (tool) => {
    if (tool === 'wiring') return { text: '', structured: { source: 'file', profile: WIRING, la_voltage: 3.3 } }
    return { text: '{}', structured: {} }
  })
}

describe('localSensorStore', () => {
  it('keeps the backend error code', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(409, { error: 'a definition named "x" already exists', code: 'name_taken' })))
    await expect(localSensorStore.save('format: 1')).rejects.toMatchObject({ code: 'name_taken', message: 'a definition named "x" already exists' })
  })

  it('uploads as a form with replace, downloads under the server file name', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      if (input.includes('/upload')) {
        expect(init?.body).toBeInstanceOf(FormData)
        return reply(200, { replaced: true, definition: { id: 'x' } })
      }
      return new Response('name: x\n', { headers: { 'Content-Disposition': 'attachment; filename="x.json"' } })
    })
    vi.stubGlobal('fetch', fetch)
    const r = await localSensorStore.upload(new Blob(['name: x']), 'x.yaml', { replace: true })
    expect(r.replaced).toBe(true)
    expect(fetch.mock.calls[0][0]).toBe('/api/sensor-definitions/upload?replace=1')
    const f = await localSensorStore.download('x', 'json')
    expect(fetch.mock.calls[1][0]).toBe('/api/sensor-definitions/x/download?format=json')
    expect(f.filename).toBe('x.json')
  })
})

describe('localSensorPod', () => {
  it('sends one command per request and throws the pod error', async () => {
    const fetch = vi.fn(async (_: string, init?: RequestInit) => {
      const { command } = JSON.parse(String(init?.body))
      if (command.cmd === 'sensor_set') return reply(502, { error: 'no instance bmp' })
      return reply(200, { reply: { instances: [] } })
    })
    vi.stubGlobal('fetch', fetch)
    expect((await localSensorPod.status()).instances).toEqual([])
    await expect(localSensorPod.set('bmp', { t: 1 })).rejects.toThrow('no instance bmp')
    expect(fetch.mock.calls[0][0]).toBe('/api/sensors/command')
  })
})

describe('localFiles', () => {
  it('saves through benchpod-local and says where', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(200, { path: '/Users/me/Downloads/bmp280.yaml' })))
    const saved = vi.fn()
    await localFiles(saved).save({ blob: new Blob(['x']), filename: 'bmp280.yaml' })
    expect(saved).toHaveBeenCalledWith('/Users/me/Downloads/bmp280.yaml')
  })

  it('falls back to a browser download', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(404, 'not found')))
    URL.createObjectURL = vi.fn(() => 'blob:x')
    URL.revokeObjectURL = vi.fn()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const saved = vi.fn()
    await localFiles(saved).save({ blob: new Blob(['x']), filename: 'a.yaml' })
    expect(click).toHaveBeenCalled()
    expect(saved).not.toHaveBeenCalled()
  })
})

describe('pinsFromWiring', () => {
  it('takes the I2C roles and named signals', () => {
    const w = { ...WIRING, signals: [{ name: 'GPS_RX', la: 7 }] } as never
    expect(pinsFromWiring(w, 'i2c')).toEqual({ sda: 1, scl: 2 })
    expect(pinsFromWiring(w, 'uart')).toEqual({ tx: 7 })
  })
})

describe('SensorsView', () => {
  const view = (sensors: boolean | undefined, call = benchCall(), onUpdateFirmware = vi.fn()) => {
    render(
      <MantineProvider>
        <SensorsView state={state(sensors)} call={call} onUpdateFirmware={onUpdateFirmware} />
      </MantineProvider>,
    )
    return { call, onUpdateFirmware }
  }

  it('shows the firmware notice when the pod lacks the sensors capability', async () => {
    fakeBackend(fakePod(engine))
    const { onUpdateFirmware } = view(undefined)
    fireEvent.click(await screen.findByText('Update firmware'))
    expect(onUpdateFirmware).toHaveBeenCalled()
    // The library still works without it.
    const library = await screen.findByLabelText('Library')
    await within(library).findByText('sht4x')
  })

  it('copies a library sensor and starts it on the pod with the wiring pins and pull-ups', async () => {
    const pod = fakePod(engine)
    const { mem, calls } = fakeBackend(pod)
    const { call } = view(true)
    const library = await screen.findByLabelText('Library')
    await within(library).findByText('bmp280')
    fireEvent.click(within(within(library).getByText('bmp280').closest('tr')!).getByText('Duplicate'))
    await screen.findByLabelText('Definition source')
    expect((await mem.list()).definitions.map((d) => d.name)).toEqual(['bmp280'])
    expect(calls.some((c) => c.path === 'POST /api/sensor-definitions')).toBe(true)

    await screen.findByLabelText('Validation result', {}, { timeout: 3000 })
    await waitFor(() => expect(screen.getByText('Start on the pod').closest('button')!.disabled).toBe(false))
    fireEvent.click(screen.getByText('Start on the pod'))
    await screen.findByLabelText('Instance bmp280', {}, { timeout: 3000 })
    expect(call).toHaveBeenCalledWith('set_pull', { las: [1, 2], enabled: true })
    expect(pod.commands).toContainEqual({ cmd: 'sensor_start', instance: 'bmp280', pins: { sda: 1, scl: 2 }, address: '0x76' })
    expect(pod.commands.filter((c) => c.cmd === 'sensor_define').length).toBeGreaterThan(1)
  })
})

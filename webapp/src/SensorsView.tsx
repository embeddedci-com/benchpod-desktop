import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Box, Button, Group, Modal, Stack, Text } from '@mantine/core'
import { benchData, benchError, effectiveWiring, laChannelLabel, type BenchCall, type Wiring } from '@bpui'
import { SensorsPage, type BenchHooks, type SensorKind, type SensorPins } from '@bpui/sensors'
import type { AppState } from './api'
import { localFiles, localSensorPod, localSensorStore, setSensorConfirm } from './sensors'

/**
 * The Sensors view: user-defined sensors (benchpod-ui src/sensors, shared with the web app) over
 * benchpod-local: definitions and stimuli in the data folder, the library built into the app, and
 * the pod's sensor commands through the BenchPod MCP server. The library and the editor work
 * without a pod and offline.
 */
export function SensorsView({ state, call, onUpdateFirmware }: { state: AppState; call: BenchCall; onUpdateFirmware: () => void }) {
  const connected = state.pod.connected
  const caps = (state.pod.status?.capabilities ?? null) as Record<string, unknown> | null
  const capable = !connected ? null : caps?.sensors === true
  const [wiring, setWiring] = useState<Wiring | null>(null)
  const [saved, setSaved] = useState<string | null>(null)
  // Starting an emulated sensor asks first (benchpod-local answers confirm): it can swap the pod's
  // FPGA image and drives the LA pins the sensor uses.
  const [asking, setAsking] = useState<Record<string, unknown> | null>(null)
  const answer = useRef<((ok: boolean) => void) | null>(null)
  useEffect(() => {
    setSensorConfirm(
      (command) =>
        new Promise<boolean>((resolve) => {
          answer.current?.(false)
          answer.current = resolve
          setAsking(command)
        }),
    )
    return () => {
      setSensorConfirm(null)
      answer.current?.(false)
      answer.current = null
    }
  }, [])
  const reply = (ok: boolean) => {
    answer.current?.(ok)
    answer.current = null
    setAsking(null)
  }

  const loadWiring = useCallback(async () => {
    const d = benchData<{ profile: Record<string, unknown> }>(await call('wiring', {}, { poll: true }))
    if (d) setWiring(effectiveWiring(d.profile))
  }, [call])
  useEffect(() => {
    if (connected) void loadWiring()
  }, [connected, loadWiring])

  const bench = useMemo<BenchHooks>(
    () => ({
      pinDefaults: (kind) => (wiring ? pinsFromWiring(wiring, kind) : {}),
      channelLabel: (la) => (wiring ? laChannelLabel(wiring, la) : `LA${la}`),
      // I2C needs pull-ups on SDA and SCL: switch them on (LA1-LA6 pull up) so the first start
      // works. Not at 1.8 V, where the pod has none. set_pull asks first, like every bench control.
      prepareStart: async (kind, pins) => {
        if (kind !== 'i2c' || wiring?.la_mv === 1800) return
        const las = [Number(pins.sda), Number(pins.scl)].filter((la) => la >= 1 && la <= 6)
        if (!las.length) return
        // Already on: no need to ask for them (the start itself still asks).
        const ps = benchData<{ channels?: { la: number; enabled: boolean; direction: string }[] }>(await call('pull_status', {}, { poll: true }))
        const on = (la: number) => !!ps?.channels?.some((c) => c.la === la && c.enabled && c.direction === 'up')
        if (las.every(on)) return
        const r = await call('set_pull', { las, enabled: true })
        if (r.declined) throw new Error('Not started: the pull-ups were declined.')
        const e = benchError(r)
        if (e) throw new Error(`Pull-ups: ${e}`)
      },
    }),
    [wiring, call],
  )
  const files = useMemo(() => localFiles(setSaved), [])

  return (
    <Box p="md" style={{ overflow: 'auto', height: '100%' }}>
      <Stack gap="md" maw={1200} mx="auto">
        {saved && (
          <Alert color="green" withCloseButton onClose={() => setSaved(null)}>
            Saved to {saved}
          </Alert>
        )}
        <Modal opened={!!asking} onClose={() => reply(false)} title="Start the emulated sensor?" centered>
          <Stack gap="sm">
            <Text size="sm">{startSummary(asking)}</Text>
            <Text size="xs" c="dimmed">
              The pod loads its sensor emulation image if another one runs (captures and the generator wait until the sensor stops) and
              drives the pins the sensor uses. You can turn these questions off in Settings.
            </Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => reply(false)}>
                Cancel
              </Button>
              <Button onClick={() => reply(true)} data-autofocus>
                Start
              </Button>
            </Group>
          </Stack>
        </Modal>
        <SensorsPage
          store={localSensorStore}
          pod={localSensorPod}
          podOnline={connected}
          capable={capable}
          onUpdateFirmware={onUpdateFirmware}
          bench={bench}
          files={files}
          libraryScope="computer"
        />
      </Stack>
    </Box>
  )
}

/** One line for the start dialog: which sensor, on which pins, at which address. */
export function startSummary(c: Record<string, unknown> | null): string {
  if (!c) return ''
  const pins = c.pins && typeof c.pins === 'object' ? (c.pins as Record<string, unknown>) : {}
  const pinText = Object.entries(pins)
    .map(([k, v]) => `${k.toUpperCase()} ${typeof v === 'number' ? `LA${v}` : String(v)}`)
    .join(', ')
  const addr = typeof c.address === 'string' ? c.address : typeof c.address === 'number' ? `0x${c.address.toString(16).padStart(2, '0')}` : ''
  return `Start "${String(c.instance ?? 'sensor')}"${pinText ? ` on ${pinText}` : ''}${addr ? `, address ${addr}` : ''}.`
}

const signalLa = (w: Wiring, ...names: string[]): number | undefined => {
  const want = names.map((n) => n.toLowerCase())
  return w.signals.find((s) => want.includes(s.name.toLowerCase()))?.la
}

/** Start pins from the bench's wiring profile: its I2C and SPI roles, and named signals (as the web app). */
export function pinsFromWiring(w: Wiring, kind: SensorKind): SensorPins {
  const out: SensorPins = {}
  const put = (k: string, v: number | null | undefined) => {
    if (typeof v === 'number') out[k] = v
  }
  if (kind === 'i2c') {
    put('sda', w.i2c_sda)
    put('scl', w.i2c_scl)
    put('drdy', signalLa(w, 'drdy', 'sensor_drdy', 'int'))
  } else if (kind === 'spi') {
    put('sclk', w.spi_sclk)
    put('mosi', w.spi_mosi)
    put('miso', w.spi_miso)
    put('cs', w.spi_cs)
    put('drdy', signalLa(w, 'drdy', 'sensor_drdy', 'int'))
  } else if (kind === 'uart') {
    // The DUT's console (uart_rx/uart_tx) is not a sensor: only a signal named for one.
    put('tx', signalLa(w, 'sensor_tx', 'gps_rx', 'gps'))
    put('rx', signalLa(w, 'sensor_rx', 'gps_tx'))
  }
  return out
}

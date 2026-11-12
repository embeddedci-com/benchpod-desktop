import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Box, Stack } from '@mantine/core'
import { benchData, benchError, effectiveWiring, laChannelLabel, type BenchCall, type Wiring } from '@bpui'
import { SensorsPage, type BenchHooks, type SensorKind, type SensorPins } from '@bpui/sensors'
import type { AppState } from './api'
import { localFiles, localSensorPod, localSensorStore } from './sensors'

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
        <SensorsPage
          store={localSensorStore}
          pod={localSensorPod}
          podOnline={connected}
          capable={capable}
          onUpdateFirmware={onUpdateFirmware}
          bench={bench}
          files={files}
        />
      </Stack>
    </Box>
  )
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

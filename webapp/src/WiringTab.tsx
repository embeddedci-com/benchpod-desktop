import { useCallback, useEffect, useState } from 'react'
import { Button, Loader, Stack, Text } from '@mantine/core'
import { WiringEditor, benchData, benchError, type BenchCall } from '@bpui'

type WiringResult = { source: string; profile: Record<string, unknown>; la_voltage: number; directions?: string | null }

/**
 * What to tell the user about the pin directions set_wiring sent to the pod (la_dir): nothing when
 * they went out or the firmware is too old for them, else why not (a driven pin refused its mark).
 */
export function directionsNote(directions: string | null | undefined): string | undefined {
  if (!directions || !directions.startsWith('not sent') || directions.includes('firmware')) return undefined
  return `Saved, but the pin directions were ${directions}. Release that pin, then save again.`
}

const SOURCES: Record<string, string> = {
  file: 'this computer',
  server: 'embeddedci.com (this pod is registered there)',
  dict: 'set in this session',
  defaults: 'the defaults: nothing saved yet',
}

/**
 * The Wiring tab: the profile every tool reads, edited by hand. Save applies it to the pod session
 * with set_wiring (through the policy) and benchpod-local keeps it in the data folder; a changed I/O
 * voltage is set on the pod too.
 */
export function WiringTab({ call, refreshKey }: { call: BenchCall; refreshKey: unknown }) {
  const [w, setW] = useState<WiringResult | null>(null)
  const [error, setError] = useState<string | undefined>()

  const load = useCallback(async () => {
    const r = await call('wiring', {}, { poll: true })
    const d = benchData<WiringResult>(r)
    if (d) {
      setW(d)
      setError(undefined)
    } else setError(benchError(r))
  }, [call])
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  if (!w)
    return (
      <Stack p="sm" gap="xs">
        {error ? (
          <>
            <Text size="sm" c="red">
              {error}
            </Text>
            <Button size="xs" variant="light" w="fit-content" onClick={() => void load()}>
              Try again
            </Button>
          </>
        ) : (
          <Loader size="sm" />
        )}
      </Stack>
    )

  const save = async (profile: Record<string, unknown>) => {
    const r = await call('set_wiring', { profile })
    if (r.declined) return 'Not saved: you declined it.'
    const e = benchError(r)
    if (e) return e
    // set_wiring also sends each pin's direction to the pod, so it never drives against the board.
    const dirs = directionsNote(benchData<WiringResult>(r)?.directions)
    const volts = profile.la_mv === 1800 ? 1.8 : 3.3
    if (volts !== w.la_voltage) {
      const v = await call('set_la_voltage', { voltage: volts })
      const ve = benchError(v)
      if (ve) return `Saved, but the I/O voltage was not set: ${ve}`
    }
    await load()
    return dirs
  }

  return (
    <WiringEditor
      profile={w.profile}
      source={SOURCES[w.source] ?? w.source}
      onSave={save}
      note={
        <Text size="xs" c="dimmed">
          Which board signal is on which LA channel, and which way each one goes. The console, captures, decoding, flashing and the
          agent all read it, and the pod never drives against a board output. It is kept on this computer.
        </Text>
      }
    />
  )
}

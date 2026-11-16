import { useCallback, useEffect, useState } from 'react'
import { Button, Loader, Stack, Text } from '@mantine/core'
import { WiringEditor, WiringProfilePicker, benchData, benchError, type BenchCall, type WiringProfileItem } from '@bpui'

type WiringResult = {
  source: string
  profile: Record<string, unknown>
  la_voltage: number | null
  directions?: string | null
  profile_name?: string
  default_profile?: string | null
  profiles?: { name: string; rev: number }[]
}

/**
 * What to tell the user about the pin directions set_wiring sent to the pod (la_dir): nothing when
 * they went out or the firmware is too old for them, else why not (a driven pin refused its mark).
 */
export function directionsNote(directions: string | null | undefined): string | undefined {
  if (!directions || !directions.startsWith('not sent') || directions.includes('firmware')) return undefined
  return `Saved, but the pin directions were ${directions}. Release that pin, then save again.`
}

const SOURCES: Record<string, string> = {
  pod: 'stored on the pod',
  file: 'this computer',
  server: 'embeddedci.com (this pod is registered there)',
  dict: 'set in this session, not saved',
  preset: 'the reference bench preset',
  undefined: 'nothing wired yet',
  defaults: 'the defaults: nothing saved yet',
}

/**
 * The Wiring tab: the wiring profiles stored on the pod (current firmware), the diagram, and the
 * editor. Picking a profile switches this session to it (load_wiring); saving stores it on the pod
 * (save_wiring), where the web app, CI and the agent see it too. The I/O voltage is set when the
 * board is powered on with the profile (the Power tab), not here. Older firmware keeps one profile:
 * saving applies it to the session (set_wiring), as before.
 */
export function WiringTab({ call, refreshKey }: { call: BenchCall; refreshKey: unknown }) {
  const [w, setW] = useState<WiringResult | null>(null)
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [newName, setNewName] = useState<string | null>(null)

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

  const stored = Array.isArray(w.profiles) && (w.profiles.length > 0 || w.source === 'pod')
  const items: WiringProfileItem[] = (w.profiles ?? []).map((p) => ({ name: p.name, rev: p.rev, isDefault: p.name === w.default_profile }))
  const selected = newName ?? (w.profile_name || null)

  const run = async (tool: string, args: Record<string, unknown>) => {
    setBusy(true)
    try {
      const r = await call(tool, args)
      if (r.declined) return 'Not done: you declined it.'
      const e = benchError(r)
      if (e) return e
      await load()
      return undefined
    } finally {
      setBusy(false)
    }
  }

  const save = async (profile: Record<string, unknown>) => {
    if (!stored) {
      const r = await call('set_wiring', { profile })
      if (r.declined) return 'Not saved: you declined it.'
      const e = benchError(r)
      if (e) return e
      await load()
      return directionsNote(benchData<WiringResult>(r)?.directions)
    }
    const e = await run('save_wiring', { profile, ...(selected ? { name: selected } : {}) })
    if (!e) setNewName(null)
    return e
  }

  const pick = async (name: string) => {
    setNewName(null)
    const e = await run('load_wiring', { name })
    if (e) setError(e)
  }

  return (
    <Stack gap="xs">
      {stored ? (
        <Stack p="sm" pb={0} gap={4}>
          <WiringProfilePicker
            profiles={newName ? [...items, { name: newName, rev: 0, isDefault: false }] : items}
            selected={selected}
            onSelect={(n) => void pick(n)}
            onMakeDefault={(n) => void run('set_default_wiring', { name: n }).then((e) => e && setError(e))}
            onDelete={(n) => void run('delete_wiring', { name: n }).then((e) => e && setError(e))}
            onCreate={(n) => setNewName(n)}
            busy={busy}
          />
          {newName ? (
            <Text size="xs" c="dimmed">
              New profile {newName}: it starts as a copy of the wiring below. Change it and save to store it on the pod.
            </Text>
          ) : null}
          {error ? (
            <Text size="xs" c="red">
              {error}
            </Text>
          ) : null}
        </Stack>
      ) : null}
      <WiringEditor
        profile={w.profile}
        source={SOURCES[w.source] ?? w.source}
        onSave={save}
        title={selected ?? undefined}
        saveLabel={stored ? (newName ? `Save as ${newName}` : 'Save to the pod') : 'Save wiring'}
        note={
          <Text size="xs" c="dimmed">
            Which board signal is on which LA channel, and which way each one goes. The console, captures, decoding, flashing and
            the agent all read it, and the pod never drives against a board output.{' '}
            {stored
              ? 'Profiles are stored on the pod: the web app, CI and the agent see the same ones. Powering the board on sets the profile’s I/O voltage.'
              : 'It is kept on this computer (this pod’s firmware stores no profiles; update it to keep them on the pod).'}
          </Text>
        }
      />
    </Stack>
  )
}

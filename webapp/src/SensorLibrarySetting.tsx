import { useEffect, useState } from 'react'
import { Button, Group, Select, Stack, Text, TextInput } from '@mantine/core'
import type { LibrarySettings } from '@bpui/sensors'
import { localSensorStore } from './sensors'

const VERSION_RE = /^v?\d+\.\d+\.\d+$/

/** Settings: which version of the public sensor library the Sensors view uses. */
export function SensorLibrarySetting({ offline }: { offline: boolean }) {
  const [s, setS] = useState<LibrarySettings | null>(null)
  const [custom, setCustom] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ color: string; text: string } | null>(null)
  useEffect(() => {
    localSensorStore.librarySettings().then(setS, (e: Error) => setMsg({ color: 'red', text: e.message }))
  }, [])
  if (!s) return msg ? <Text size="xs" c={msg.color}>{msg.text}</Text> : null

  const choice = s.override === '' ? 'pinned' : s.override === 'latest' ? 'latest' : 'version'
  const set = async (v: string) => {
    setBusy(true)
    setMsg(null)
    try {
      const r = await localSensorStore.setLibraryVersion(v)
      setS(r)
      setCustom('')
      if (r.warnings?.length) setMsg({ color: 'orange', text: r.warnings.join(' ') })
    } catch (e) {
      setMsg({ color: 'red', text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Stack gap={6}>
      <Group gap="xs" align="flex-end">
        <Select
          size="xs"
          w={220}
          aria-label="Sensor library version"
          data={[
            { value: 'pinned', label: `Built in (${s.pinned_version})` },
            { value: 'latest', label: 'Latest release' },
            { value: 'version', label: 'A specific version' },
          ]}
          value={custom !== '' ? 'version' : choice}
          disabled={busy}
          onChange={(v) => {
            if (v === 'pinned') void set('')
            else if (v === 'latest') void set('latest')
            else setCustom(choice === 'version' ? s.override : '0.')
          }}
        />
        {(choice === 'version' || custom !== '') && (
          <>
            <TextInput size="xs" w={90} aria-label="Sensor library version number" placeholder="0.1.0" value={custom || s.override} onChange={(e) => setCustom(e.currentTarget.value)} />
            <Button size="xs" variant="light" loading={busy} disabled={!VERSION_RE.test(custom)} onClick={() => void set(custom)}>
              Use
            </Button>
          </>
        )}
      </Group>
      <Text size="xs" c="dimmed">
        The library of ready-made sensors in the Sensors view. Other versions download from GitHub once and stay cached{offline ? '; offline, only cached versions work' : ''}.
      </Text>
      {msg && (
        <Text size="xs" c={msg.color}>
          {msg.text}
        </Text>
      )}
    </Stack>
  )
}

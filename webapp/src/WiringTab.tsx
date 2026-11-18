import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Button, Group, Loader, Menu, Stack, Text } from '@mantine/core'
import {
  PinsEditor,
  WiringDiagram,
  WiringProfilePicker,
  benchData,
  benchError,
  changedChannels,
  effectiveWiring,
  validateWiring,
  wiringFieldErrors,
  type BenchCall,
  type NewProfileFrom,
  type Wiring,
  type WiringProfileItem, HelpTip } from '@bpui'

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

/** The profile keys the Pins editor writes, as stored: what is not set is left out. */
export function profileToSave(draft: Record<string, unknown>, w: Wiring): Record<string, unknown> {
  const out: Record<string, unknown> = { ...draft, version: 2 }
  for (const k of ['la_mv', 'efuse', 'uart_rx', 'uart_tx', 'uart_baud', 'i2c_sda', 'i2c_scl', 'i2c_addr', 'swd_swclk', 'swd_swdio', 'swd_nreset', 'swd_target', 'spi_sclk', 'spi_mosi', 'spi_miso', 'spi_cs', 'signals', 'dut_dirs', 'pulls'] as const)
    out[k] = w[k]
  for (const k of ['la_mv', 'efuse', 'i2c_addr'] as const) if (!out[k]) delete out[k]
  if (out.pulls === null) delete out.pulls
  if (out.dut_dirs && Object.keys(out.dut_dirs as object).length === 0) delete out.dut_dirs
  return out
}

/**
 * The Pins tab: the wiring profiles stored on the pod and the pins of the one on screen. Picking a
 * profile switches this session to it (load_wiring); Make active makes it the pod's default (what
 * powering the board on uses); saving stores it on the pod (save_wiring), where the web app, CI and
 * the agent see it too. Older firmware keeps one profile: saving applies it to the session
 * (set_wiring) and keeps it on this computer.
 */
export function WiringTab({ call, refreshKey }: { call: BenchCall; refreshKey: unknown }) {
  const [w, setW] = useState<WiringResult | null>(null)
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [newName, setNewName] = useState<string | null>(null)
  const [draft, setDraft] = useState<Record<string, unknown>>({})
  const [saved, setSaved] = useState(false)
  const [diagram, setDiagram] = useState(false)

  const load = useCallback(async () => {
    const r = await call('wiring', {}, { poll: true })
    const d = benchData<WiringResult>(r)
    if (d) {
      setW(d)
      setDraft(d.profile)
      setError(undefined)
    } else setError(benchError(r))
  }, [call])
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const wiring = useMemo(() => effectiveWiring(draft), [draft])
  const issues = useMemo(() => validateWiring(draft), [draft])
  const fieldErr = useMemo(() => wiringFieldErrors(wiring, issues.errors), [wiring, issues])
  const changed = useMemo(() => (w ? changedChannels(wiring, effectiveWiring(w.profile)) : new Set<number>()), [wiring, w])

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
  const dirty = newName != null || JSON.stringify(draft) !== JSON.stringify(w.profile)

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

  const save = async () => {
    setError(undefined)
    setSaved(false)
    const profile = profileToSave(draft, wiring)
    let e: string | undefined
    if (!stored) {
      setBusy(true)
      try {
        const r = await call('set_wiring', { profile })
        e = r.declined ? 'Not saved: you declined it.' : benchError(r)
        if (!e) {
          await load()
          e = directionsNote(benchData<WiringResult>(r)?.directions)
        }
      } finally {
        setBusy(false)
      }
    } else {
      e = await run('save_wiring', { profile, ...(selected ? { name: selected } : {}) })
      if (!e) setNewName(null)
    }
    if (e) setError(e)
    else setSaved(true)
  }

  const pick = async (name: string) => {
    setNewName(null)
    setSaved(false)
    const e = await run('load_wiring', { name })
    if (e) setError(e)
  }

  const create = (name: string, from: NewProfileFrom) => {
    setNewName(name)
    setSaved(false)
    if (from === 'empty') setDraft({ version: 2 })
  }

  const menu = (
    <>
      <Menu.Item onClick={() => void navigator.clipboard?.writeText(JSON.stringify(profileToSave(draft, wiring), null, 2))}>Copy as JSON</Menu.Item>
      <Menu.Item onClick={() => setDiagram((d) => !d)}>{diagram ? 'Hide the diagram' : 'Show the diagram'}</Menu.Item>
    </>
  )

  return (
    <Stack gap="sm" p="sm">
      {stored ? (
        <WiringProfilePicker
          profiles={newName ? [...items, { name: newName, rev: 0, isDefault: false }] : items}
          selected={selected}
          onSelect={(n) => void pick(n)}
          onMakeDefault={(n) => void run('set_default_wiring', { name: n }).then((e) => e && setError(e))}
          onDelete={(n) => void run('delete_wiring', { name: n }).then((e) => e && setError(e))}
          onCreate={create}
          menu={menu}
          busy={busy}
        />
      ) : (
        <Group justify="space-between" gap="xs">
          <Text size="xs" c="dimmed">
            Older firmware: the pins are kept on this computer.{' '}
            <HelpTip label="profiles on the pod" docs="/docs/benchpod-firmware-update">
              Update the firmware to keep the profiles on the pod.
            </HelpTip>
          </Text>
          <Menu position="bottom-end">
            <Menu.Target>
              <Button size="compact-xs" variant="subtle" color="gray" aria-label="More profile actions">
                ⋯
              </Button>
            </Menu.Target>
            <Menu.Dropdown>{menu}</Menu.Dropdown>
          </Menu>
        </Group>
      )}
      {newName ? (
        <Text size="xs" c="dimmed">
          New profile {newName}: save it to store it on the pod.
        </Text>
      ) : null}

      <PinsEditor
        wiring={wiring}
        onChange={(patch) => {
          setSaved(false)
          setDraft((d) => ({ ...d, ...patch }))
        }}
        errors={fieldErr}
        warnings={issues.warnings}
        disabled={busy}
      />

      {diagram ? <WiringDiagram wiring={wiring} changed={changed} title={selected ?? undefined} /> : null}

      {error ? (
        <Alert color="red" variant="light" p="xs" withCloseButton onClose={() => setError(undefined)}>
          <Text size="xs" style={{ whiteSpace: 'pre-wrap' }}>
            {error}
          </Text>
        </Alert>
      ) : null}
      {issues.errors.length ? (
        <Text size="xs" c="red">
          {issues.errors.length === 1 ? issues.errors[0].message : `Fix the ${issues.errors.length} problems marked in red to save.`}
        </Text>
      ) : null}
      <Group gap="xs">
        <Button size="xs" onClick={() => void save()} loading={busy} disabled={!dirty || issues.errors.length > 0}>
          {stored ? (newName ? `Save ${newName} to the pod` : 'Save to the pod') : 'Save'}
        </Button>
        <Button
          size="xs"
          variant="default"
          onClick={() => {
            setNewName(null)
            setDraft(w.profile)
          }}
          disabled={!dirty || busy}
        >
          Undo changes
        </Button>
        {saved && !dirty ? (
          <Text size="xs" c="teal">
            Saved
          </Text>
        ) : null}
      </Group>
    </Stack>
  )
}

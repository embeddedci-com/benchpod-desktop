import { useEffect, useState } from 'react'
import {
  Alert, Anchor, Autocomplete, Button, Divider, Group, NumberInput, PasswordInput, SegmentedControl, Select, Stack, Switch, Text, Textarea, TextInput,
} from '@mantine/core'
import { api, type AppState, type ModelInfo, type Settings } from './api'

/** Model source, key, pod connection and safety limits. Also the first-run setup. */
export function SettingsPanel({ state, onSaved }: { state: AppState; onSaved: (s: AppState) => void }) {
  const [s, setS] = useState<Settings>(state.settings)
  const [key, setKey] = useState('')
  const [models, setModels] = useState<ModelInfo[]>(state.recommended)
  const [busy, setBusy] = useState<string | null>(null)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const preset = state.presets.find((p) => p.id === s.provider)

  useEffect(() => setS(state.settings), [state.settings])

  const set = (patch: Partial<Settings>) => setS((prev) => ({ ...prev, ...patch }))

  const pickProvider = (id: string | null) => {
    const p = state.presets.find((x) => x.id === id)
    if (!p) return
    set({ provider: p.id, base_url: p.base_url || '', model: p.default_model ?? '' })
    setModels(p.id === 'openrouter' ? state.recommended : [])
  }

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    setMsg(null)
    try {
      await fn()
    } catch (e) {
      setMsg({ ok: false, text: String(e instanceof Error ? e.message : e) })
    } finally {
      setBusy(null)
    }
  }

  const save = () =>
    run('save', async () => {
      if (key.trim()) {
        await api.setKey(s.provider, key.trim())
        setKey('')
      }
      onSaved(await api.saveSettings(s))
      setMsg({ ok: true, text: 'Saved.' })
    })

  const loadModels = () =>
    run('models', async () => {
      await api.saveSettings(s)
      const r = await api.models()
      setModels(r.models)
      setMsg({ ok: true, text: `${r.models.length} models with tool calling.` })
    })

  const test = () =>
    run('test', async () => {
      if (key.trim()) {
        await api.setKey(s.provider, key.trim())
        setKey('')
      }
      onSaved(await api.saveSettings(s))
      const r = await api.testModel()
      setMsg({ ok: r.ok, text: `${r.message} (${(r.ms / 1000).toFixed(1)} s)` })
    })

  const keyHint =
    state.key_source === 'keychain'
      ? 'A key is stored in the system keychain. Type a new one to replace it.'
      : state.key_source === 'env'
        ? 'Using the key from the environment or .env file.'
        : 'Stored in the system keychain, never in a file.'

  return (
    <Stack gap="sm">
      <Text fw={600}>Model</Text>
      <Select
        label="Where the model runs"
        data={state.presets.map((p) => ({ value: p.id, label: p.name }))}
        value={s.provider}
        onChange={pickProvider}
        allowDeselect={false}
      />
      {preset?.local ? (
        <Text size="xs" c="dimmed">
          Runs on your computer: nothing you type leaves it. Start the server first (for Ollama: <code>ollama serve</code> and <code>ollama pull</code> a model).
        </Text>
      ) : (
        <Text size="xs" c="dimmed">
          What you type and what the tools return (logs, captures) are sent to {preset?.id === 'custom' ? 'that server' : (preset?.name ?? 'this provider')} to run the model.
        </Text>
      )}
      {preset?.id === 'custom' || preset?.local ? (
        <TextInput label="Base URL" description="The address before /chat/completions" value={s.base_url} onChange={(e) => set({ base_url: e.currentTarget.value })} />
      ) : null}
      {preset?.need_key || preset?.id === 'custom' ? (
        <PasswordInput
          label="API key"
          description={
            <>
              {keyHint}
              {preset?.key_url ? (
                <>
                  {' '}
                  <Anchor href={preset.key_url} target="_blank" size="xs">
                    Get a key
                  </Anchor>
                </>
              ) : null}
            </>
          }
          value={key}
          onChange={(e) => setKey(e.currentTarget.value)}
          placeholder={state.key_source ? '••••••••' : 'paste your key'}
        />
      ) : null}
      <Group align="flex-end" gap="xs" wrap="nowrap">
        <Autocomplete
          style={{ flex: 1 }}
          label="Model"
          value={s.model}
          onChange={(v) => set({ model: v })}
          data={models.map((m) => ({ value: m.id, label: m.note ? `${m.id} · ${m.note}` : m.id }))}
          limit={50}
        />
        <Button variant="default" onClick={loadModels} loading={busy === 'models'}>
          List
        </Button>
      </Group>
      {state.model_warning ? (
        <Alert color="yellow" p="xs">
          <Text size="xs">{state.model_warning}</Text>
        </Alert>
      ) : null}
      <Group gap="xs">
        <Button variant="light" onClick={test} loading={busy === 'test'}>
          Test model
        </Button>
        <Text size="xs" c="dimmed">
          one small tool call, to see the model handles tools
        </Text>
      </Group>

      <Divider my="xs" />
      <Text fw={600}>BenchPod</Text>
      <TextInput
        label="Connection"
        description="Empty finds the pod on your network. Or: an address (192.168.1.50), usb, usb:<name>, embeddedci:<name>"
        value={s.connection}
        onChange={(e) => set({ connection: e.currentTarget.value })}
      />
      <div>
        <Text size="sm" fw={500} mb={4}>
          Board I/O voltage
        </Text>
        <SegmentedControl
          size="xs"
          value={String(s.la_voltage ?? 0)}
          onChange={(v) => set({ la_voltage: Number(v) })}
          data={[
            { value: '0', label: 'keep pod setting' },
            { value: '1.8', label: '1.8 V' },
            { value: '3.3', label: '3.3 V' },
          ]}
        />
      </div>
      <Textarea
        label="About this board"
        description="Pinout, expected rails, firmware notes: the agent reads this before every answer."
        autosize
        minRows={3}
        maxRows={10}
        value={s.dut_notes}
        onChange={(e) => set({ dut_notes: e.currentTarget.value })}
        placeholder="NUCLEO-F446RE thermostat node. UART2 115200 on LA4/LA5. Boots to 'node ready>'."
      />

      <Divider my="xs" />
      <Text fw={600}>Safety</Text>
      <Switch
        label="Ask before power, flash and output changes"
        checked={s.confirm_risky}
        onChange={(e) => set({ confirm_risky: e.currentTarget.checked })}
      />
      <Group grow>
        <NumberInput
          label="Max DAC output (V)"
          min={0}
          max={12}
          step={0.1}
          decimalScale={2}
          value={s.limits.max_dac_volts}
          onChange={(v) => set({ limits: { ...s.limits, max_dac_volts: Number(v) || 0 } })}
        />
        <NumberInput
          label="Max loop current (mA)"
          min={0}
          max={24}
          step={0.5}
          value={s.limits.max_current_out * 1000}
          onChange={(v) => set({ limits: { ...s.limits, max_current_out: (Number(v) || 0) / 1000 } })}
        />
      </Group>
      <Text size="xs" c="dimmed">
        Limits are checked by the app before each call, whatever the model asks for. 0 = only the pod's own range.
      </Text>

      {msg ? (
        <Alert color={msg.ok ? 'green' : 'red'} p="xs">
          <Text size="sm">{msg.text}</Text>
        </Alert>
      ) : null}
      <Group justify="flex-end">
        <Button onClick={save} loading={busy === 'save'}>
          Save
        </Button>
      </Group>
      <Text size="xs" c="dimmed">
        Settings, the conversation and the action log are kept in {state.data_dir}.
      </Text>
    </Stack>
  )
}

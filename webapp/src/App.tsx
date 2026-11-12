import { useCallback, useEffect, useMemo, useState } from 'react'
import { ActionIcon, Badge, Box, Button, Card, Drawer, Group, Loader, Stack, Text, Title, Tooltip } from '@mantine/core'
import { CaptureView, ChatView, ConsoleTerminal, GeneratorControls, PowerControls, useBenchCall, useChat, useTools, type FetchCapture, type WaveformLibrary } from '@bpui'
import { api, type AppState } from './api'
import { SettingsPanel } from './SettingsPanel'

const SUGGESTIONS = [
  'What is on my bench? Check the pod status and the wiring.',
  'Power-cycle the board and show me its boot log.',
  'Capture the UART and I2C lines for 200 ms and tell me what the firmware does.',
  'Measure the target rail current while the board idles.',
]

/** The last capture in full, from benchpod-local (the MCP server's capture resources). */
const fetchCapture = (async (kind: string) => {
  const r = await fetch(`/api/capture/${kind}`)
  if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`)
  return r.json()
}) as FetchCapture

/** Tools after which the capture view loads the last capture again. */
const CAPTURE_TOOLS = /^(capture_la|capture_adc|capture_correlated|decode_la)$/

function needsSetup(s: AppState): boolean {
  const p = s.presets.find((x) => x.id === s.settings.provider)
  return !s.settings.model || (!!p?.need_key && !s.key_source)
}

export function App() {
  const [state, setState] = useState<AppState | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const chat = useChat({ base: '/api/chat' })
  const tools = useTools('/api/chat')
  // The bench controls beside the chat: the same tools as the agent, through the same policy.
  const bench = useBenchCall('/api/tool')
  // The waveform library in the data folder; a waveform plays through the replay tool (and the policy).
  const library = useMemo<WaveformLibrary>(
    () => ({
      list: async () => (await api.waveforms()).waveforms,
      create: api.createWaveform,
      fromCapture: api.waveformFromCapture,
      rename: api.renameWaveform,
      remove: api.deleteWaveform,
      play: (id, opts) => bench.call('replay', { local_waveform: id, ...opts }),
    }),
    [bench.call],
  )
  // The agent's captures show in the capture view: it reloads after each capture tool result.
  const agentCaptures = useMemo(
    () => chat.items.filter((it) => it.kind === 'tool' && !!it.result && CAPTURE_TOOLS.test(it.call.name)).length,
    [chat.items],
  )

  const refresh = useCallback(async () => setState(await api.state()), [])
  useEffect(() => {
    void refresh()
  }, [refresh])
  // The pod connects on the first tool call, the agent's or a bench control's: follow it, quickly
  // while the agent runs.
  useEffect(() => {
    void refresh()
    const t = setInterval(() => void refresh(), chat.running ? 2000 : 5000)
    return () => clearInterval(t)
  }, [chat.running, refresh])

  if (!state) {
    return (
      <Group justify="center" mt="xl">
        <Loader />
      </Group>
    )
  }

  const preset = state.presets.find((p) => p.id === state.settings.provider)
  const setup = needsSetup(state)

  const connect = async () => {
    setConnecting(true)
    try {
      await api.connectPod()
    } finally {
      setConnecting(false)
      await refresh()
    }
  }

  const podBadge = state.pod.connected ? (
    <Badge color="green" variant="light">
      pod connected{state.pod.connection ? ` · ${state.pod.connection}` : ''}
    </Badge>
  ) : state.mcp.state === 'installing' || state.mcp.state === 'starting' || connecting ? (
    <Badge color="blue" variant="light" leftSection={<Loader size={10} />}>
      {state.mcp.state === 'installing' ? 'installing the BenchPod tools…' : connecting ? 'connecting…' : 'starting the BenchPod tools…'}
    </Badge>
  ) : (
    <Tooltip label={state.pod.error || state.mcp.error || 'Connects on the first tool call, or click to connect now'} multiline w={320}>
      <Badge color={state.pod.error || state.mcp.error ? 'red' : 'gray'} variant="light" style={{ cursor: 'pointer' }} onClick={connect}>
        {state.pod.error || state.mcp.error ? 'pod not connected · retry' : 'pod: connect'}
      </Badge>
    </Tooltip>
  )

  const cost = chat.usage?.cost ? ` · $${chat.usage.cost.toFixed(4)} this message` : ''
  const footer = (
    <Tooltip
      multiline
      w={320}
      withArrow
      label={
        preset?.local
          ? 'Runs on this computer: nothing you type leaves it.'
          : `Your messages and the tool results (logs, captures) are sent to ${preset?.id === 'custom' ? 'your server' : (preset?.name ?? 'the provider')} to run the model.`
      }
    >
      <Text size="xs" c="dimmed" style={{ cursor: 'help' }}>
        {state.settings.model || 'no model'} · {preset?.name ?? state.settings.base_url} ⓘ{cost}
      </Text>
    </Tooltip>
  )

  return (
    <Stack gap={0} h="100vh">
      <Group justify="space-between" px="md" py={8} style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}>
        <Group gap="sm">
          <Title order={5} ff="monospace">
            benchpod
          </Title>
          {podBadge}
        </Group>
        <Group gap="xs">
          <Badge variant="default" style={{ cursor: 'pointer', textTransform: 'none' }} onClick={() => setSettingsOpen(true)}>
            {state.settings.model || 'pick a model'}
          </Badge>
          <Button size="compact-sm" variant="subtle" onClick={() => void chat.reset()} disabled={chat.running}>
            New chat
          </Button>
          <ActionIcon variant="subtle" onClick={() => setSettingsOpen(true)} aria-label="Settings">
            ⚙
          </ActionIcon>
        </Group>
      </Group>

      <Box style={{ flex: 1, minHeight: 0 }}>
        {setup ? (
          <Box maw={560} mx="auto" p="md" style={{ overflow: 'auto', height: '100%' }}>
            <Card withBorder p="lg">
              <Title order={4} mb={4}>
                Pick a model
              </Title>
              <Text size="sm" c="dimmed" mb="md">
                BenchPod Desktop works with any model that speaks the OpenAI API: one on your own computer (Ollama, LM Studio), your own account with a provider, or a server your team runs.
              </Text>
              <SettingsPanel state={state} onSaved={setState} />
            </Card>
          </Box>
        ) : (
          <ChatView
            items={chat.items}
            running={chat.running}
            onSend={(t) => void chat.send(t)}
            onStop={() => void chat.stop()}
            onConfirm={(id, ok) => void chat.confirm(id, ok)}
            suggestions={SUGGESTIONS}
            tools={tools}
            helpHref="https://www.embeddedci.com/docs/benchpod-ai-agent"
            toolsReferenceHref="https://www.embeddedci.com/docs/benchpod-mcp#tools"
            intro={
              <Stack gap={4}>
                <Title order={3} ff="monospace">
                  benchpod agent
                </Title>
                <Text size="sm" c="dimmed">
                  Ask it to test or debug your board. It asks before changing the hardware.
                </Text>
              </Stack>
            }
            footer={footer}
            panelTabs={[{ value: 'generator', label: 'Generator', content: <GeneratorControls call={bench.call} library={library} /> }]}
            panelControls={{
              captures: <CaptureView call={bench.call} fetchCapture={fetchCapture} refreshKey={agentCaptures} />,
              console: <ConsoleTerminal call={bench.call} logBase="/api/console" agentRunning={chat.running} />,
              power: <PowerControls call={bench.call} />,
            }}
          />
        )}
      </Box>

      {bench.dialog}

      <Drawer opened={settingsOpen} onClose={() => setSettingsOpen(false)} position="right" size="md" title="Settings">
        <SettingsPanel
          state={state}
          onSaved={(s) => {
            setState(s)
          }}
        />
      </Drawer>
    </Stack>
  )
}

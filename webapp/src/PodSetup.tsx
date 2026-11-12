import { useState } from 'react'
import { Alert, Button, Group, Loader, SegmentedControl, Stack, Text, TextInput } from '@mantine/core'
import { api, type AppState, type FoundPod } from './api'

const KINDS: Record<string, string> = {
  usb_net: 'USB (network adapter)',
  serial: 'USB (console only: capture, UART and flash need the network or USB networking)',
  tcp: 'your network',
  discover: 'your network',
}

/**
 * Find your pod: the first start, with no account. Looks on USB, then on the LAN (mDNS), shows the
 * pod it found, asks for the board's I/O voltage, and saves both. Nothing is registered anywhere.
 */
export function PodSetup({ state, onDone }: { state: AppState; onDone: (s: AppState) => void }) {
  const [finding, setFinding] = useState(false)
  const [found, setFound] = useState<FoundPod | null>(null)
  const [conn, setConn] = useState(state.settings.connection)
  const [volts, setVolts] = useState<string>(state.settings.la_voltage ? String(state.settings.la_voltage) : '3.3')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | undefined>()

  const find = async () => {
    setFinding(true)
    setError(undefined)
    setFound(null)
    try {
      const f = await api.findPod(conn)
      setFound(f)
      if (f.connected && f.la_voltage) setVolts(String(f.la_voltage))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setFinding(false)
    }
  }

  const use = async () => {
    if (!found?.connected) return
    setSaving(true)
    setError(undefined)
    try {
      // The app sets this voltage on the pod every time it connects (the MCP server's --la-voltage).
      onDone(await api.saveSettings({ connection: found.connection, la_voltage: Number(volts), pod_setup_done: true }))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Stack gap="sm">
      <Text size="sm" c="dimmed">
        Plug the pod into this computer over USB-C, or into your network. No account and no registration: the app talks to the pod
        directly.
      </Text>
      <Group gap="xs" align="flex-end">
        <TextInput
          style={{ flex: 1 }}
          size="sm"
          label="Where"
          placeholder="empty: look on USB, then on your network"
          description="Or an address (192.168.1.50), usb, or usb:<name> with several pods on USB"
          value={conn}
          onChange={(e) => setConn(e.currentTarget.value)}
        />
        <Button onClick={() => void find()} loading={finding}>
          Find my pod
        </Button>
      </Group>
      {finding ? (
        <Group gap="xs">
          <Loader size="xs" />
          <Text size="sm" c="dimmed">
            Looking on USB, then on your network… (the first start also installs the BenchPod tools)
          </Text>
        </Group>
      ) : null}
      {found && !found.connected ? (
        <Alert color="orange" variant="light" title="No pod found">
          <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
            {found.error}
          </Text>
          <Text size="xs" c="dimmed" mt="xs">
            Check the pod is powered and the USB cable carries data. On a network, mDNS does not cross routers: type the pod's address
            instead.
          </Text>
        </Alert>
      ) : null}
      {found?.connected ? (
        <Stack gap="xs">
          <Alert color="green" variant="light" title="Found your pod">
            <Text size="sm">
              {found.board ? `BenchPod (board ${found.board})` : 'BenchPod'}, firmware {found.version || 'unknown'}, over {KINDS[found.kind ?? ''] ?? found.kind}
              {found.ip ? ` · network address ${found.ip}` : ''}
            </Text>
          </Alert>
          <div>
            <Text size="sm" fw={500}>
              Your board's I/O voltage
            </Text>
            <Text size="xs" c="dimmed" mb={4}>
              The logic levels on the LA pins (UART, I2C, SWD). Most boards are 3.3 V; pick 1.8 V only when the board's I/O is 1.8 V.
            </Text>
            <SegmentedControl
              value={volts}
              onChange={setVolts}
              data={[
                { value: '3.3', label: '3.3 V' },
                { value: '1.8', label: '1.8 V' },
              ]}
            />
          </div>
          <Group>
            <Button onClick={() => void use()} loading={saving}>
              Use this pod
            </Button>
          </Group>
        </Stack>
      ) : null}
      {error ? (
        <Alert color="red" variant="light">
          <Text size="sm">{error}</Text>
        </Alert>
      ) : null}
    </Stack>
  )
}

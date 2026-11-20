import { useState } from 'react'
import { HelpTip } from '@bpui'
import { Alert, Button, Group, Loader, Stack, Text, TextInput } from '@mantine/core'
import { api, type AppState, type FoundPod } from './api'
import { PodPairing } from './PodPairing'

const KINDS: Record<string, string> = {
  usb_net: 'USB (network adapter)',
  serial: 'USB (console only: capture, UART and flash need the network or USB networking)',
  tcp: 'your network',
  discover: 'your network',
}

/**
 * Find your pod: the first start, with no account. Looks on USB, then on the LAN (mDNS), shows the
 * pod it found, and saves it. The board's I/O voltage is in the pod's wiring profile (the Pins tab).
 * Nothing is registered anywhere.
 */
export function PodSetup({ state, onDone, onUpdateFirmware }: { state: AppState; onDone: (s: AppState) => void; onUpdateFirmware?: () => void }) {
  const [finding, setFinding] = useState(false)
  const [found, setFound] = useState<FoundPod | null>(null)
  const [conn, setConn] = useState(state.settings.connection)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | undefined>()

  const find = async () => {
    setFinding(true)
    setError(undefined)
    setFound(null)
    try {
      const f = await api.findPod(conn)
      setFound(f)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setFinding(false)
    }
  }

  // Firmware 3.x: the pod answered, but the app drives firmware 4.0 or later. Save it and update it.
  const tooOld = found?.compat === 'firmware_too_old'
  // Firmware 4.0.1 over the network: this computer is not paired with the pod. Pair it, then use it.
  const notPaired = found?.compat === 'not_paired'

  const use = async () => {
    if (!found?.connected && !tooOld) return
    setSaving(true)
    setError(undefined)
    try {
      // The I/O voltage comes from the pod's wiring profile; la_voltage 0 clears an older app's override.
      onDone(await api.saveSettings({ connection: found!.connection, la_voltage: 0, pod_setup_done: true }))
      if (tooOld) onUpdateFirmware?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Stack gap="sm">
      <Text size="sm" c="dimmed">
        Plug the pod into this computer over USB-C, or into your network.{' '}
        <HelpTip label="no account needed" docs="/docs/benchpod-getting-started#1-plug-in-the-pod">
          No account and no registration: the app talks to the pod directly.
        </HelpTip>
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
      {tooOld ? (
        <Stack gap="xs">
          <Alert color="orange" variant="light" title="Found your pod: it needs firmware 4.0">
            <Text size="sm">
              Firmware {found?.version || '3.x'}{' '}
              <HelpTip label="firmware 4.0" docs="/docs/benchpod-firmware-update">
                The app drives pods on firmware 4.0 or later. It updates this one for you, over the same connection.
              </HelpTip>
            </Text>
          </Alert>
          <Group>
            <Button onClick={() => void use()} loading={saving}>
              Use this pod and update it
            </Button>
          </Group>
        </Stack>
      ) : notPaired ? (
        <PodPairing
          onPaired={(pod) => {
            // Paired and connected: look again, for what the pod is.
            if (pod.connected) void find()
          }}
        />
      ) : found && !found.connected ? (
        <Alert color="orange" variant="light" title="No pod found">
          <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>
            {found.error}
          </Text>
          <Text size="xs" c="dimmed" mt="xs">
            Check the pod is powered and the USB cable carries data.{' '}
            <HelpTip label="no pod found" docs="/docs/benchpod-troubleshooting">
              On a network, discovery (mDNS) does not cross routers: type the pod&apos;s address instead.
            </HelpTip>
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
          <Text size="xs" c="dimmed">
            Set your board&apos;s I/O voltage in the Pins tab.{' '}
            <HelpTip label="I/O voltage" docs="/docs/benchpod-getting-started#board-io-voltage">
              3.3 V or 1.8 V, part of the pins profile: set it once and the pod uses it from then on.
            </HelpTip>
          </Text>
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

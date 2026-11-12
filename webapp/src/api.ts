// The benchpod-local API (local/app.go).

export type Limits = { max_dac_volts: number; max_current_out: number }

export type Settings = {
  provider: string
  base_url: string
  model: string
  connection: string
  la_voltage?: number
  confirm_risky: boolean
  limits: Limits
  dut_notes: string
  offline?: boolean
}

export type Preset = {
  id: string
  name: string
  base_url: string
  need_key: boolean
  local: boolean
  default_model?: string
  key_url?: string
  key_env?: string
}

export type ModelInfo = { id: string; name: string; note?: string; input_per_m?: number; output_per_m?: number }

export type AppState = {
  version: string
  settings: Settings
  key_source: '' | 'keychain' | 'env'
  presets: Preset[]
  recommended: ModelInfo[]
  model_warning: string
  mcp: { state: 'stopped' | 'installing' | 'starting' | 'ready' | 'failed'; error: string }
  pod: { connected: boolean; connection?: string; error?: string; status?: Record<string, unknown> }
  data_dir: string
  offline: boolean
  offline_forced: boolean
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`)
  if (r.status === 204) return undefined as T
  return (await r.json()) as T
}

export type WaveformMeta = {
  id: string
  name: string
  unit: 'V' | 'mA'
  sample_rate_hz: number
  sample_count: number
  min: number
  max: number
  created: string
}

export const api = {
  state: () => req<AppState>('GET', '/api/state'),
  saveSettings: (s: Partial<Settings>) => req<AppState>('PUT', '/api/settings', s),
  setKey: (provider: string, key: string) => req<void>('PUT', '/api/key', { provider, key }),
  deleteKey: (provider: string) => req<void>('DELETE', `/api/key?provider=${encodeURIComponent(provider)}`),
  models: () => req<{ models: ModelInfo[] }>('GET', '/api/models'),
  testModel: () => req<{ ok: boolean; message: string; ms: number }>('POST', '/api/test-model'),
  connectPod: () => req<AppState['pod']>('POST', '/api/pod/connect'),
  mcpLog: () => req<{ lines: string[] }>('GET', '/api/mcp/log'),
  firmware: () => req<{ files: { name: string; path: string; size: number; modified: string }[] }>('GET', '/api/firmware'),
  uploadFirmware: async (file: File) => {
    const r = await fetch(`/api/firmware?name=${encodeURIComponent(file.name)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: file,
    })
    if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`)
    return (await r.json()) as { name: string; path: string; size: number; modified: string }
  },
  waveforms: () => req<{ waveforms: WaveformMeta[] }>('GET', '/api/waveforms'),
  createWaveform: (w: { name: string; unit: 'V' | 'mA'; sample_rate_hz: number; samples: number[] }) => req<WaveformMeta>('POST', '/api/waveforms', w),
  waveformFromCapture: (name: string) => req<WaveformMeta>('POST', '/api/waveforms/from-capture', { name }),
  renameWaveform: (id: string, name: string) => req<WaveformMeta>('PATCH', `/api/waveforms/${encodeURIComponent(id)}`, { name }),
  deleteWaveform: (id: string) => req<void>('DELETE', `/api/waveforms/${encodeURIComponent(id)}`),
}

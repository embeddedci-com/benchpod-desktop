import {
  browserFiles,
  commandSensorPod,
  SensorStoreError,
  type DefinitionKind,
  type DefinitionSummary,
  type FileBlob,
  type FileHost,
  type LibraryEntryFull,
  type LibraryListing,
  type LibrarySettings,
  type SensorPod,
  type SensorStore,
  type SourceFormat,
  type StoredDefinition,
} from '@bpui/sensors'

// The Sensors view's store, pod and files over benchpod-local (local/sensors.go): definitions and
// stimuli in the data folder, the sensor library built into the app (or a version from GitHub),
// and sensor commands to the pod through the BenchPod MCP server.

const query = (q: Record<string, string | undefined>) => {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) if (v) p.set(k, v)
  const s = p.toString()
  return s ? `?${s}` : ''
}

/** A request; a refusal becomes a SensorStoreError with the backend's code (name_taken, ...). */
async function call(path: string, init?: RequestInit): Promise<Response> {
  const r = await fetch(path, init)
  if (!r.ok) {
    const text = (await r.text()).trim()
    let msg = text || `HTTP ${r.status}`
    let code: string | undefined
    try {
      const j = JSON.parse(text) as { error?: string; code?: string }
      msg = j.error || msg
      code = j.code
    } catch {
      // plain text
    }
    throw new SensorStoreError(msg, code ?? (r.status === 404 ? 'not_found' : undefined))
  }
  return r
}

const json = async <T>(path: string, method = 'GET', body?: unknown): Promise<T> => {
  const r = await call(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return (r.status === 204 ? undefined : await r.json()) as T
}

/** The file name of a download: the Content-Disposition's, else `fallback`. */
function fileName(r: Response, fallback: string): string {
  const m = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(r.headers.get('Content-Disposition') ?? '')
  return m ? decodeURIComponent(m[1]) : fallback
}

async function download(path: string, fallback: string): Promise<FileBlob> {
  const r = await call(path)
  return { blob: await r.blob(), filename: fileName(r, fallback) }
}

const D = '/api/sensor-definitions'
const L = '/api/sensor-library'
const id = encodeURIComponent

export const localSensorStore: SensorStore = {
  list: (kind?: DefinitionKind) => json<{ definitions: DefinitionSummary[]; full_validation?: boolean }>(`${D}${query({ kind })}`),
  get: (i: string) => json<StoredDefinition>(`${D}/${id(i)}`),
  save: (source: string, i?: string) => (i ? json<StoredDefinition>(`${D}/${id(i)}`, 'PUT', { source }) : json<StoredDefinition>(D, 'POST', { source })),
  delete: (i: string) => json<void>(`${D}/${id(i)}`, 'DELETE'),
  duplicate: (i: string, name?: string) => json<StoredDefinition>(`${D}/${id(i)}/duplicate`, 'POST', name ? { name } : {}),
  upload: async (file: Blob, filename: string, opts: { replace?: boolean } = {}) => {
    const form = new FormData()
    form.append('file', file, filename)
    const r = await call(`${D}/upload${query({ replace: opts.replace ? '1' : undefined })}`, { method: 'POST', body: form })
    return (await r.json()) as { replaced: boolean; definition: StoredDefinition }
  },
  download: (i: string, format?: SourceFormat) => download(`${D}/${id(i)}/download${query({ format })}`, `${i}.${format ?? 'yaml'}`),
  zip: (opts: { kind?: DefinitionKind; format?: SourceFormat } = {}) => download(`${D}/download${query(opts)}`, 'sensor-definitions.zip'),
  library: (version?: string) => json<LibraryListing>(`${L}${query({ version })}`),
  libraryEntry: (name: string, version?: string) => json<LibraryEntryFull>(`${L}/entries/${id(name)}${query({ version })}`),
  libraryDownload: (name: string, opts: { format?: SourceFormat; version?: string } = {}) =>
    download(`${L}/entries/${id(name)}/download${query(opts)}`, `${name}.${opts.format ?? 'yaml'}`),
  librarySettings: () => json<LibrarySettings>(`${L}/settings`),
  setLibraryVersion: (version: string) => json<LibrarySettings>(`${L}/settings`, 'PUT', { version }),
}

/** The connected pod's sensors, one command at a time through benchpod-local. */
export const localSensorPod: SensorPod = commandSensorPod(async (command) => {
  const r = await fetch('/api/sensors/command', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ command }),
  })
  const body = (await r.json().catch(() => ({}))) as { reply?: Record<string, unknown>; error?: string }
  if (!r.ok) throw new Error(body.error || `HTTP ${r.status}`)
  return body.reply ?? {}
})

/**
 * Files for the desktop app. The window has no Tauri APIs (the page is served by benchpod-local),
 * so: open is the file input, which the window shows as the system's file dialog; save goes
 * through benchpod-local, which writes the file into the Downloads folder and answers its path.
 * In a browser tab on an older benchpod-local, save falls back to the browser's download.
 */
export function localFiles(onSaved: (path: string) => void): FileHost {
  return {
    async save(file) {
      try {
        const r = await fetch(`/api/files/save?name=${encodeURIComponent(file.filename)}`, { method: 'POST', body: file.blob })
        if (!r.ok) throw new Error((await r.text()).trim())
        onSaved(((await r.json()) as { path: string }).path)
      } catch {
        browserFiles.save(file)
      }
    },
    open: (opts) => browserFiles.open(opts),
  }
}

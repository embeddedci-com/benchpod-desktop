import { combinedSequenceStore, httpSequenceStore, type ApiRequest, type SequenceStore } from '@bpui'

// The bus console's saved sequences in the desktop app: the local file (/api/bus-sequences, no
// account needed), plus the embeddedci.com account's (/api/account/bus-sequences, passed on by
// benchpod-local with the EmbeddedCI API key) when signed in, which is what sharing needs.

const via =
  (prefix: string): ApiRequest =>
  (method, path, body) =>
    fetch(prefix + path.replace(/^\/benchpod\//, '/'), {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })

export async function desktopSequenceStore(): Promise<SequenceStore> {
  let signedIn = false
  try {
    const r = await fetch('/api/account')
    signedIn = r.ok && !!((await r.json()) as { signed_in?: boolean }).signed_in
  } catch {
    /* the local API is down: local only */
  }
  const local = httpSequenceStore(via('/api'), { canShare: false })
  const account = signedIn ? httpSequenceStore(via('/api/account')) : null
  return combinedSequenceStore(local, account)
}

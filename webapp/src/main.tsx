import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MantineProvider, createTheme } from '@mantine/core'
import '@mantine/core/styles.css'
import { App } from './App'

const theme = createTheme({
  primaryColor: 'teal',
  fontFamilyMonospace: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
})

const DARK = '(prefers-color-scheme: dark)'

/**
 * The computer's light or dark setting, followed live. The app forces it on Mantine rather than
 * using "auto": with "auto", components that read the scheme (the capture canvas, the waveform
 * views) see "auto" and draw their light palette in dark mode.
 */
export function useSystemScheme(): 'light' | 'dark' {
  const query = () => (typeof window !== 'undefined' && window.matchMedia?.(DARK).matches ? 'dark' : 'light')
  const [scheme, setScheme] = useState<'light' | 'dark'>(query)
  useEffect(() => {
    const m = window.matchMedia?.(DARK)
    if (!m) return
    const on = () => setScheme(m.matches ? 'dark' : 'light')
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [])
  return scheme
}

function Root() {
  const scheme = useSystemScheme()
  return (
    <MantineProvider theme={theme} forceColorScheme={scheme}>
      <App />
    </MantineProvider>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
)

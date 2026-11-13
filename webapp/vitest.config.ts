import { defineConfig, mergeConfig } from 'vitest/config'
import viteConfig from './vite.config'

// The page's tests: the Vite config (the @bpui alias, deduped singletons) plus the engine's .wasm
// as bytes (`?inline`), as benchpod-ui's own tests load it.
export default mergeConfig(viteConfig, defineConfig({ assetsInclude: ['**/*.wasm'] }))

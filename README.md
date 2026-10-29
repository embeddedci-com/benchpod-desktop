# BenchPod Desktop

A free, open source desktop app to test and debug your board with an AI agent and an
[EmbeddedCI BenchPod](https://www.embeddedci.com/pytest-hil). Ask it in plain words: it powers
the board, reads its UART console, captures logic and analog signals, emulates the sensor the
firmware expects, drives voltages and flashes firmware, and shows you the logs and captures next
to the conversation.

It works with the model you choose:

- **A model on your own computer** (Ollama, LM Studio, llama.cpp, vLLM): no account, no cost,
  nothing leaves your machine.
- **Your own account** with a provider (OpenRouter, Anthropic, OpenAI, ...): paste your key, pay
  the provider directly.
- **A server your team runs**: any endpoint that speaks the OpenAI chat-completions API.

The app has no EmbeddedCI key inside and needs no EmbeddedCI account for a pod on your network or
USB.

## How it works

```
 window (webapp)  ──HTTP──▶  benchpod-local  ──stdio MCP──▶  embeddedci-mcp  ──LAN/USB/cloud──▶  BenchPod ──▶ your board
                              │ agent loop
                              └──OpenAI API──▶ your model
```

- `benchpod-local` (Go) is the app: the agent loop, the settings, the HTTP API and the page.
  The desktop app is a [Tauri](https://tauri.app) window around it; you can also run it on its
  own and use it in a browser tab.
- The BenchPod tools are the [BenchPod MCP server](https://pypi.org/project/embeddedci-mcp/),
  the same one the Claude and Codex plugins run. Every change to it reaches the app with no change
  here. The app finds it on your `PATH`, runs it with [uv](https://docs.astral.sh/uv/), or
  installs a private copy of uv on first start.
- The agent loop (`agent/`) is provider-neutral: one OpenAI-compatible client, tools from MCP, and
  a policy that holds the safety rules.

## Safety

An agent driving real hardware gets limits it cannot talk its way past. They are enforced in the
app, before a tool runs, never only in the prompt:

- **Confirmations**: power, flashing, outputs and pod settings wait for your yes (on by default).
- **Limits**: a ceiling for the DAC output voltage and the 4-20 mA loop current.
- **Caps**: tool calls and model turns per message.
- **Action log**: every tool call, its arguments and its result, in `actions.jsonl` in the data
  folder.

Your model key is kept in the system keychain (macOS Keychain, Windows Credential Manager, the
Secret Service on Linux), never in a file the app writes.

## Install

Release builds are on the [releases page](https://github.com/embeddedci-com/benchpod-desktop/releases).

## Build from source

Check out [benchpod-ui](https://github.com/embeddedci-com/benchpod-ui) (the chat and capture
views, shared with the EmbeddedCI website) next to this repository:

```bash
git clone https://github.com/embeddedci-com/benchpod-ui
```

```bash
git clone https://github.com/embeddedci-com/benchpod-desktop
```

You need Go 1.26+ and Node 22+; the desktop app also needs [Rust](https://rustup.rs).

```bash
cd benchpod-desktop && make run-local
```

builds `bin/benchpod-local` and opens the app in your browser.

```bash
make desktop
```

builds the desktop installer for your machine.

### Development keys

Copy `.env.example` to `.env` (it is git-ignored) for keys during development, for example
`BENCHPOD_MODEL_API_KEY` or `OPENROUTER_API_KEY`, and `BENCHPOD_MCP_COMMAND` to run the MCP
server from a checkout. A key typed in the app goes to the keychain instead.

```bash
make dev
```

runs `benchpod-local` and the webapp with hot reload on http://localhost:5176.

```bash
make test
```

## Repository layout

| Path | What |
| --- | --- |
| `agent/` | the agent loop: OpenAI-compatible model client, MCP tools, policy enforcement |
| `chat/` | one conversation over HTTP and server-sent events (the protocol benchpod-ui speaks) |
| `policy/` | which tools need a confirmation, the hardware limits |
| `providers/` | model endpoint presets and recommended models |
| `local/` | the app's backend: settings, keys, the MCP server, the HTTP API |
| `cmd/benchpod-local/` | the binary |
| `webapp/` | the page |
| `desktop/` | the Tauri shell |

## License

Apache License 2.0. See [LICENSE](LICENSE).

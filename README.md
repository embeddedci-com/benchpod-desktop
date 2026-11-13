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

## Bench controls

The panel beside the chat drives the pod by hand, without the agent:

- **Console**: the board's UART console. Open it (the channels and baud come from the wiring, or
  pick them), type a command and press Enter (LF, CRLF or no line ending; up and down arrows go
  through what you typed). The agent's console reads and writes show here too, in order; while the
  agent runs, the terminal leaves the reading to it.
- **Captures**: logic (14 channels), analog, or both on one time axis, at the length and rate you
  pick, optionally waiting for an edge or level on an LA channel. The capture is drawn in full:
  drag to zoom, double-click to reset, pick which channels to show. Decode UART, I2C or SPI from it
  (the channels and baud come from the wiring) as a table and as labels on the waveform. The
  agent's captures and decodes show here too. Needs embeddedci-mcp 2.13.10 or later.
  **Sequences**: tick rows in the bus console's log and save them under a name, then run the
  sequence again (each step asks the same confirmations as by hand, and a read that differs from
  the recording is flagged) or export it as a pytest file for the embeddedci SDK, with an assert
  per read. Sequences are kept on this computer, in `bus-sequences.json` in the data folder. With
  an EmbeddedCI API key (signed in to embeddedci.com, not offline) you can also share one in your
  organization; shared ones are kept in your embeddedci.com account.
- **Generator**: pick the output (3.3 V, 5 V, ±12 V or the 4-20 mA output), then drive a DC level,
  play a sine, square or sawtooth, or play a waveform from your library, until **Stop**. Also:
  - **Segments**: build a signal from ramps, holds and steps (durations in ms, levels in volts or
    mA), play it in a loop, or save it to the library.
  - **Fault**: replay a library waveform with a window (start and width in % of it) replaced by a
    flatline or spike at a level you pick, or stuck at the value just before the window.
  - **Control loop**: the FPGA loop. Each tick it reads an input, looks it up in a curve (a droop
    with a knee, like a solar panel's I-V, constant, rising or falling), moves the output toward it
    and holds it inside a clamp. The input is the ADC (closed loop), a held point you move with a
    slider, or a sweep. The live output and input show while it runs. Presets, the input sense chain
    and an output stage between the DAC and the board are in the web app only for now.
- **Waveform library**: kept on this computer, in the data folder's `waveforms` folder. Save the
  last analog capture (yours or the agent's) under a name, or import a CSV (one value per line, or
  time and value columns, which give the rate). Play one as recorded or scaled to fill the
  output; rename or delete it.
- **Flash**: flash your board's firmware from a file (.elf, .hex or .bin) over SWD, with the OpenOCD
  target, the reset line and the power rail from the wiring, and read OpenOCD's log. Then
  power-cycle the board and see its boot log, and keep a target that worked in the wiring. The
  file is copied into the data folder's `firmware` folder (the last 10 stay, to flash again).
  OpenOCD runs on your computer: install it first (`brew install open-ocd` on a Mac).
  Below it, **SPI flash chip**: identify, program (erase, write, verify) and read back a 25-series
  SPI NOR flash on the wiring's SPI pins, optionally holding the board in reset. A read of up to
  4 KB shows as hex; a longer one is saved as a file in the data folder.
- **Wiring**: which board signal is on which LA channel (UART, I2C, SWD and SPI roles, and named
  signals), the target power rail, the I/O voltage, the UART baud and the OpenOCD target for
  flashing, with each channel's pull resistor and any conflict. Pin directions: which way each wired
  board signal goes (board output, board input or bidirectional), with each role's and signal's
  default shown. Saving sends them to the pod (firmware 3.10.4+), which then never drives a board
  output with a push-pull driver. Every control and the agent read it. It is kept on this computer, in `wiring.json` in the data folder; a profile the agent saves
  with `set_wiring` lands there too.
- **Power**: switch the internal 5 V rail or the external rail on and off, and read the voltage
  and the current the board draws, live, with the last minute as a chart.

**Pod firmware** (in Settings): the pod's firmware and gateware version, a note in the header
when a newer release is out, and an update: the latest release from GitHub, or, without internet
(offline mode), the release files you downloaded elsewhere (`bench_pod_stm32.bin`, its `.sig`,
`blobs-manifest.json` and the `blob-*.bin` files with their `.sig`). The pod checks the image and
its signature, restarts into it, then gets the gateware and Wi-Fi bridge images. The app runs the
bundled `benchpod` CLI for this (`benchpod flash-self --network`), over USB or your network; a
pod on embeddedci.com updates from the web app.

**This pod** (in Settings, under Pod firmware): the pod's board, firmware, connection, network
address and USB network adapter at a glance; Wi-Fi (show the network, scan, join, forget); network
access on Ethernet and Wi-Fi (open, locked or off) and HTTPS-only; the USB network adapter on or
off; the firmware signature policy; and, with offline mode off and `benchpod login` done,
deregistering the pod from embeddedci.com. The bundled `benchpod` CLI does the settings no
BenchPod tool covers, over the pod's USB console. The Wi-Fi password goes to it on stdin and is
never logged.

**Calibration** (in Settings): what the pod has stored, the ADC offset (nothing on J8), each DAC
output's line from two readings of your DMM, the ADC gains measured against those outputs, and
going back to the built-in values (a second click confirms it).

The controls use the same tools and the same safety settings as the agent: power, flashing and
outputs ask first (unless you turned that off), the output limits apply, and every change goes to
the action log. The agent reads the pod's state before it acts, so it sees what you changed.

## Sensors

The **Sensors** view (the switch at the top) makes the pod act as a sensor your firmware reads:
an I2C, SPI, UART or analog part, described in a YAML file
([format](https://github.com/embeddedci-com/benchpod-sensors/blob/main/docs/format.md)).

- **Library**: ready-made sensors (BME280, BMP280, MPU-6050, SHT4x, a GPS receiver). Open one to
  preview it or run it as is; duplicate it to change it.
- **My sensors** and **Stimuli**: your own definitions and stimuli (values that change over
  time, with faults), with an editor that checks the file as you type and previews what the DUT
  reads. They are files on this computer, in the data folder's `sensors` folder
  (`sensors/<kind>/<name>.yaml`, `stimuli/<name>.yaml`), kept as you wrote them. Upload a file,
  or download one or all of them (a zip): downloads go to your Downloads folder.
- **On the pod**: start a sensor on the LA channels from the wiring (an I2C start switches on
  the pull-ups, after asking), change its values while the firmware runs, inject faults, read
  back its registers, play a stimulus.

Running sensors needs pod firmware that plays them; with older firmware the view says so and
the update is in Settings. The library and the editor work without a pod and offline.

**Sensor library** (in Settings): the library version built into the app, the latest release,
or a specific version. Other versions download from GitHub once and are cached in the folder the
BenchPod Python SDK and CLI share (`embeddedci/benchpod-sensors` in your user cache folder, or
`BENCHPOD_SENSOR_CACHE`). Offline, or when GitHub cannot be reached, a cached version is used,
else the built-in one, with a note.

## Safety

An agent driving real hardware gets limits it cannot talk its way past. They are enforced in the
app, before a tool runs, never only in the prompt:

- **Confirmations**: power, flashing, outputs and pod settings wait for your yes (on by default).
- **Limits**: a ceiling for the DAC output voltage and the 4-20 mA loop current, for DC levels,
  generated waveforms and replays alike (a replay scaled to fill an output counts as that output's
  full range), a fault's level, and the control loop's clamp. The loop drives the output last
  routed, so the app follows which one that is: a loop is refused until an output is routed
  (`dac_output` with the path and 0 V, which the Generator does for you), and moving a running loop
  onto an output where its clamp is above the limit is refused too. Moving a running loop's input
  (`loop_input`) asks nothing: its output stays inside the clamp it was checked with.
- **Caps**: tool calls and model turns per message.
- **Action log**: every tool call, its arguments and its result, in `actions.jsonl` in the data
  folder, with who made it: the agent, or you from the bench controls (`"source": "manual"`).

Your model key is kept in the system keychain (macOS Keychain, Windows Credential Manager, the
Secret Service on Linux), never in a file the app writes.

## Install

On a Mac, with [Homebrew](https://brew.sh):

```bash
brew trust embeddedci-com/tap
```

```bash
brew install --cask embeddedci-com/tap/benchpod-desktop
```

Recent versions of Homebrew ask you to trust a tap before they load anything from it.

Windows and Linux installers are on the [releases page](https://github.com/embeddedci-com/benchpod-desktop/releases).
They are not code-signed yet: on Windows, SmartScreen asks you to confirm the first start. On a Mac,
the cask removes the download quarantine for you.

## First start

No account and no registration: the app talks to the pod directly.

1. **Find your pod**: plug it in over USB-C or into your network and press **Find my pod**. The
   app looks on USB first, then on your network (mDNS); or type its address. Pick your board's
   I/O voltage (3.3 V or 1.8 V): the app sets it on the pod every time it connects.
2. **Pick a model**: a local one (Ollama is preselected: start it first), or a provider such as
   OpenRouter, with your key. Or skip it: the bench controls work without the agent.
3. Ask, or use the bench controls beside the chat. Both ask before they change power, firmware or
   outputs.

A pod in the cloud (`embeddedci:<name>`, after `benchpod login`) goes in Settings → Connection.

## Offline use

Turn on **Offline** at the top of Settings and the app keeps everything on your computer and
your own network:

- only a model on this computer or a server on your network (hosted providers are refused, also
  a custom URL that points at one);
- only pods on your network or USB: no `embeddedci:` devices, no builds from embeddedci.com, and
  the BenchPod tools run without your `benchpod login` session or API key;
- no downloads: the app does not install uv, and uv runs from its cache only.

The BenchPod tools must then already be installed. Either start the app once with internet
access (uv keeps its copy), or install the tools from your own package mirror so
`embeddedci-mcp` is on your `PATH`:

```bash
pipx install embeddedci-mcp
```

To turn offline mode on for good on a machine, so it cannot be switched off in the app, put
`BENCHPOD_OFFLINE=1` in a `.env` file in the app's data folder (its path is at the bottom of
Settings), or set it in the environment.

Offline mode covers the app. A pod registered to embeddedci.com connects to it by itself
whenever its network reaches the internet. For a pod that should stay local, stop that with
`benchpod deregister --connection <pod address>` (its data stays on the account), or keep it on
a network without internet access or on USB.

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

`BPUI_SRC=/path/to/benchpod-ui/src` builds and tests the webapp against another benchpod-ui
checkout (a worktree) instead of `../benchpod-ui`. Refresh the built-in sensor library from a
benchpod-sensors checkout with `go run ./sensors/gen -src ../benchpod-sensors -version X.Y.Z`.

## Release

```bash
scripts/release.sh 0.1.0
```

tags `v0.1.0`. CI runs the tests, builds every installer and makes a draft release; publishing the
draft updates the Homebrew cask (the repository needs the `TAP_GITHUB_TOKEN` secret).

## Repository layout

| Path | What |
| --- | --- |
| `agent/` | the agent loop: OpenAI-compatible model client, MCP tools, policy enforcement |
| `chat/` | one conversation over HTTP and server-sent events (the protocol benchpod-ui speaks) |
| `policy/` | which tools need a confirmation, the hardware limits |
| `providers/` | model endpoint presets and recommended models |
| `local/` | the app's backend: settings, keys, the MCP server, the HTTP API |
| `sensors/` | sensor definitions (parse, convert) and the public sensor library, with its built-in copy |
| `cmd/benchpod-local/` | the binary |
| `webapp/` | the page |
| `desktop/` | the Tauri shell |

## License

Apache License 2.0. See [LICENSE](LICENSE).

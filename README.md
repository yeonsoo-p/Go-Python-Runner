# Go Python Runner

A native desktop application that orchestrates bundled Python scripts through a Go backend. Users interact with task cards in a React frontend — never touching the underlying Python code. Builds to a single executable with a bundled Python runtime.

## Architecture

```
┌──────────────────────────────────┐
│  React Frontend (Task Cards)     │  Wails v3 webview
│  - Auto-generated TS bindings    │
│  - Real-time events              │
├──────────────────────────────────┤
│  Go Backend (Wails Services)     │
│  ├─ ScriptService (registry)     │  Wails bindings -> frontend
│  ├─ RunnerService (lifecycle)    │  Wails events -> frontend
│  ├─ LogService (unified logs)    │  All sources -> slog -> UI
│  └─ gRPC Server                  │  gRPC <-> Python
├──────────────────────────────────┤
│  Python Scripts (bundled)        │
│  - gRPC client (generated)       │
│  - Helper library                │
└──────────────────────────────────┘
```

**Go ↔ Frontend** — Wails v3 auto-generated TypeScript bindings + typed events for real-time updates.

**Go ↔ Python** — gRPC bidirectional streaming with Protobuf as the single source of truth. Each script runs as an independent subprocess with its own gRPC stream.

## Features

- **Task cards** — each Python script is a card with parameters, output, progress, and status
- **Parallel execution** — run multiple scripts concurrently with isolated message channels
- **Shared cache** — scripts share Python objects (dicts, numpy arrays, etc.) via OS shared memory
- **Native file dialogs** — Python scripts can open/save files through Wails native dialogs
- **SQLite database** — Python scripts can read/write to a shared SQLite database via gRPC
- **Plugin system** — drop scripts into a user directory to extend or override built-in scripts
- **Unified logging** — frontend, backend, and Python errors all funnel into one structured log system
- **Single executable** — ships with a bundled Python runtime, no user setup required

## Tech Stack

| Layer | Technology |
|---|---|
| Desktop framework | [Wails v3](https://v3alpha.wails.io/) |
| Backend | Go |
| Frontend | React 19 + TypeScript + Vite + Tailwind |
| Go ↔ Python | gRPC bidirectional streaming |
| Schema | Protobuf with `oneof` |
| Python runtime | [python-build-standalone](https://github.com/indygreg/python-build-standalone) via `uv` |
| Python tooling | [uv](https://docs.astral.sh/uv/) |
| Logging | `log/slog` + lumberjack |
| Testing | Go `testing` + pytest + vitest |

## Getting Started

### Prerequisites

- **Go 1.25+** — [go.dev/dl](https://go.dev/dl)
- **Node.js 22+** — [nodejs.org](https://nodejs.org)
- **uv** — manages Python, deps, and protobuf codegen (no separate `protoc` needed)
  - Linux: `curl -LsSf https://astral.sh/uv/install.sh | sh`
  - Windows: `powershell -c "irm https://astral.sh/uv/install.ps1 | iex"`
- **Wails v3** — `go install github.com/wailsapp/wails/v3/cmd/wails3@latest`

**Platform dependencies:**

- **Windows:** No C toolchain required — SQLite uses the pure-Go [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) driver and Wails v3 talks to WebView2 via DLL bindings (no CGO).
- **Linux (Ubuntu/Debian):** `sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev` — Wails v3 links GTK + webkit2gtk via CGO on Linux.

Run `wails3 doctor` to verify your environment is ready.

### Setup

```bash
# Python venv + deps
uv sync

# Node deps
cd frontend && npm install && cd ..

# Generate protobuf code
make proto

# Generate Wails bindings
make bindings
```

### Development

```bash
# Hot-reload dev mode
wails3 dev
```

### Build

```bash
# Bundle portable Python runtime
make bundle-python

# Build native executable
make build
```

### Test

```bash
# All tests
make test

# By layer
make test-go           # Go unit + service tests
make test-python       # Python tests
make test-frontend     # Frontend tests
make test-integration  # End-to-end Go ↔ Python tests
```

### Lint

```bash
make lint              # All linters
make lint-go           # go vet
make lint-python       # ruff + mypy
make lint-frontend     # tsc --noEmit
```

## Sample Scripts

| Script | What it demonstrates |
|---|---|
| **Hello World** | Basic output and progress |
| **Data Processor** | Iterative text analysis with configurable depth |
| **Numpy Stats** | Pre-installed package usage + data results |
| **Cache Produce/Consume** | Cross-process shared memory caching |
| **File Export** | Native save dialog + file writing |
| **DB Todo** | SQLite CRUD operations via gRPC |
| **DB Key-Value** | Read/write operations on the built-in `kv` table |
| **DB Run History** | Query past runs from the SQLite history table |
| **Parallel Worker** | Concurrent workers chaining data through the shared cache |
| **Error Stages** | Partial completion + error propagation |

## Plugin System

Add or override scripts by placing them in the plugin directory:

- **Windows**: `%APPDATA%/go-python-runner/scripts/`
- **Linux**: `~/.go-python-runner/scripts/`

Each plugin follows the same structure: `<script-name>/script.json` + `main.py`.

## License

[MIT](LICENSE)

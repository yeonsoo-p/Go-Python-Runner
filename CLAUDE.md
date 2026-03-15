# Go Python Runner

A native desktop application that orchestrates bundled Python scripts through a Go backend. Users interact with task cards in a React frontend — never seeing the underlying Python code. Builds to a single executable.

## Architecture Decisions

1. **Desktop app**: Wails v3 — single native executable with embedded webview
2. **Go <-> Frontend**: Wails auto-generated TypeScript bindings + Wails typed events (real-time)
3. **Go <-> Python**: Plain gRPC with bidirectional streaming, own process lifecycle management
4. **Schema**: Protobuf `.proto` file as single source of truth; `oneof` for compile-time type safety
5. **Go concurrency**: Typed channels + interface types + type switches (idiomatic Go)
6. **Scripts**: Bundled with app in the repository
7. **Frontend**: React 18 + TypeScript + Vite + Tailwind (via Wails v3)
8. **Python runtime**: Dev: `uv` manages interpreter + venv + deps. Distribution: bundled portable interpreter via python-build-standalone (no user setup)
9. **Python deps**: Shared base (`grpcio`, `protobuf`, `numpy`) — managed by `uv` in dev, pre-installed at build time for distribution
10. **Logging**: Unified structured logging via Go `log/slog` — frontend, backend, and Python errors all funnel into one system
11. **Plugin system**: User-writable script directory that can override built-in scripts or add new ones post-build
12. **Testing**: Three tiers — unit tests (isolated, fast), service tests (real deps, no full app), integration tests (end-to-end Go-Python)
13. **Shared cache**: Parallel scripts share any Python object via `multiprocessing.shared_memory` + pickle. Go manages lifecycle (registry, ref counting, cleanup).

## Architecture Layers

```text
┌──────────────────────────────────┐
│  React Frontend (Task Cards)     │  Wails v3 webview
│  - Auto-generated TS bindings    │
│  - Wails events for real-time    │
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

## Project Structure

```text
go-python-runner/
├── main.go                        # Wails v3 app entry
├── go.mod / go.sum
├── pyproject.toml                 # Python version pin + dev deps (uv)
├── uv.lock                        # Reproducible Python lockfile
├── proto/
│   └── runner.proto               # Protobuf service + message definitions
├── internal/
│   ├── services/
│   │   ├── script_service.go      # Wails service: list/get scripts
│   │   ├── script_service_test.go
│   │   ├── runner_service.go      # Wails service: start/cancel/status
│   │   ├── runner_service_test.go
│   │   ├── log_service.go         # Wails service: receive frontend errors, expose logs
│   │   └── log_service_test.go
│   ├── runner/
│   │   ├── manager.go             # Process lifecycle, typed channels
│   │   ├── manager_test.go
│   │   ├── process.go             # Single subprocess: spawn, gRPC, wait
│   │   ├── process_test.go
│   │   ├── grpc_server.go         # gRPC server for Python clients
│   │   ├── grpc_server_test.go
│   │   ├── cache.go               # Shared memory cache manager (registry + lifecycle)
│   │   └── cache_test.go
│   ├── registry/
│   │   ├── registry.go            # Discover scripts from builtin + plugin dirs
│   │   └── registry_test.go
│   ├── logging/
│   │   ├── logger.go              # slog multi-handler: file + ring buffer
│   │   ├── logger_test.go
│   │   ├── ring.go                # In-memory ring buffer for UI access
│   │   └── ring_test.go
│   └── gen/                       # Generated protobuf Go code
│       ├── runner.pb.go
│       └── runner_grpc.pb.go
├── scripts/
│   ├── _lib/
│   │   ├── runner.py              # Python helper wrapping gRPC client
│   │   ├── gen/                   # Generated protobuf Python code
│   │   │   ├── runner_pb2.py
│   │   │   └── runner_pb2_grpc.py
│   │   └── tests/                 # Python tests
│   │       ├── test_runner.py     # Unit tests for helper lib
│   │       ├── test_grpc_client.py # Service tests for gRPC client
│   │       └── test_integration.py # Integration with Go server
│   ├── hello_world/               # Simple greeting (output, progress)
│   │   ├── script.json
│   │   └── main.py
│   ├── data_processor/            # String processing (real work + progress)
│   │   ├── script.json
│   │   └── main.py
│   ├── numpy_stats/               # Numpy stats (pre-installed pkg + data_result)
│   │   ├── script.json
│   │   └── main.py
│   ├── cache_produce/             # Shared cache producer (cache_set + numpy + hold)
│   │   ├── script.json
│   │   └── main.py
│   ├── cache_consume/             # Shared cache consumer (cache_get + numpy)
│   │   ├── script.json
│   │   └── main.py
│   ├── file_export/               # File export via native save dialog
│   │   ├── script.json
│   │   └── main.py
│   └── error_stages/              # Partial failure (error propagation path)
│       ├── script.json
│       └── main.py
├── frontend/
│   ├── package.json
│   ├── vite.config.ts
│   ├── bindings/                  # Auto-generated by wails3
│   └── src/
│       ├── main.tsx
│       ├── App.tsx
│       ├── components/
│       │   ├── TaskCard.tsx
│       │   ├── TaskCard.test.tsx
│       │   ├── TaskGrid.tsx
│       │   ├── TaskGrid.test.tsx
│       │   ├── RunOutput.tsx
│       │   ├── ParamForm.tsx
│       │   ├── ParamForm.test.tsx
│       │   ├── LogViewer.tsx        # Unified log panel with filters
│       │   └── LogViewer.test.tsx
│       └── hooks/
│           ├── useScripts.ts      # Wails bindings + events
│           └── useScripts.test.ts
├── python/
│   ├── README.md                  # How to set up portable Python
│   └── requirements.txt           # Shared base deps (grpcio, protobuf)
├── tests/
│   └── integration/               # End-to-end Go->Python->Go tests
│       ├── full_run_test.go
│       ├── parallel_test.go
│       ├── plugin_test.go
│       └── testdata/              # Fixture scripts for tests
│           ├── echo_script/
│           ├── crash_script/
│           └── slow_script/
├── build/
│   └── bundle_python.sh           # Download + package portable Python
├── Makefile                       # Build + test orchestration
└── CLAUDE.md
```

## Protobuf Contract

Single source of truth: `proto/runner.proto`. Both Go and Python use generated code from this file. The `oneof` fields provide compile-time type safety — no loose `"type"` string field with arbitrary JSON.

One bidirectional streaming RPC (`Execute`). Message direction follows gRPC client/server roles:

- **ClientMessage** (Python → Go): `Output`, `Progress`, `Status`, `Error`, `DataResult`, `CacheCreate`, `CacheLookup`, `CacheRelease`
- **ServerMessage** (Go → Python): `StartRequest` (with params map), `CancelRequest`, `CacheInfo`

## Wails v3 Services

### ScriptService

Exposed to frontend via auto-generated TypeScript bindings:

- `ListScripts() []Script` — returns all registered scripts with metadata
- `GetScript(id string) Script` — single script details including parameter schema

### RunnerService

Exposed via bindings (methods) + events (real-time updates):

- `StartRun(scriptID string, params map[string]string) string` — returns runID
- `CancelRun(runID string) error` — graceful cancellation
- `GetRunHistory() []RunRecord` — past executions

Emits Wails events:

- `run:output` — stdout text from script
- `run:progress` — progress updates (current/total/label)
- `run:status` — state transitions (running/completed/failed)
- `run:error` — error messages with traceback

## Go Concurrency Model

```text
StartRun() ->  spawn Python subprocess
           ->  goroutine: gRPC stream reader -> typed chan Message
           ->  goroutine: chan Message -> Wails events (to frontend)
           ->  goroutine: cmd.Wait() -> cleanup + final status event
```

- `Message` is a Go interface; concrete types per proto message (`OutputMsg`, `ProgressMsg`, etc.)
- Type switches for dispatch — compile-time safe, no reflection
- Each run is independent: own OS process, own goroutines, own channel
- `sync.Mutex`-protected maps in the manager for tracking active runs

## Unified Logging

Central structured logger in Go using `log/slog`. All three error sources funnel into one system.

```text
┌─────────────┐     Wails binding      ┌──────────────────────┐
│  Frontend    │ ──────────────────────>│                      │
│  (React)     │  LogService.Error()    │   Go Central Logger  │
└─────────────┘                        │   (log/slog)         │
                                       │                      │──> Log file (rotating)
┌─────────────┐     direct slog calls  │   Fields:            │──> In-memory ring buffer
│  Go Backend  │ ──────────────────────>│   - source           │──> Wails events -> UI
│  (services)  │                       │   - level            │
└─────────────┘                        │   - runID            │
                                       │   - scriptID         │
┌─────────────┐     gRPC Error msg     │   - timestamp        │
│  Python      │ ──────────────────────>│   - message          │
│  Scripts     │     + stderr capture   │   - traceback        │
└─────────────┘                        └──────────────────────┘
```

### Error sources

| Source | How errors reach Go | Details |
| --- | --- | --- |
| Frontend (React) | `LogService.Error(msg, context)` Wails binding | JS errors, component errors, unhandled rejections. Frontend installs a global error handler that calls the binding. |
| Go backend | Direct `slog` calls | Service errors, gRPC server errors, process lifecycle failures |
| Python scripts | gRPC `Error` message (structured) + stderr capture (unstructured) | gRPC errors carry message + traceback. Stderr catches crashes before gRPC connects or after it disconnects. |

### LogService (Wails service)

- `LogError(source string, message string, context map[string]string)` — frontend calls this to report JS errors
- `GetLogs(filter LogFilter) []LogEntry` — UI fetches filtered log entries
- Emits Wails event `log:entry` for real-time log streaming to the UI

### Log levels

Using `slog` levels: DEBUG, INFO, WARN, ERROR.

- Python output/progress messages = INFO
- Python `Error` gRPC message = ERROR
- Python stderr (crash output) = ERROR
- Frontend JS errors = ERROR
- Go service errors = ERROR

### Log output

- **File**: OS-appropriate app data dir (`~/.go-python-runner/logs/` or `%APPDATA%/go-python-runner/logs/`). JSON lines format (structured, parseable). Rotating via `lumberjack` package.
- **Ring buffer**: In-memory, capped at last 1000 entries. Serves `GetLogs()` calls from the UI.
- **Wails events**: `log:entry` events stream to the frontend LogViewer in real-time.

### LogViewer component (`frontend/src/components/LogViewer.tsx`)

- Unified log panel showing all sources
- Filters by source (frontend/backend/python), level, scriptID, runID
- Real-time updates via `log:entry` Wails event
- Collapsible traceback display for Python errors

## Python Script Structure

Each script lives in `scripts/<name>/` with two files:

### script.json (metadata)

JSON file with fields: `id`, `name`, `description`, `params[]` (each with `name`, `type`, `required`, `default`, `description`). See `scripts/hello_world/script.json` for a working example.

### main.py (entry point)

Scripts add `_lib` to `sys.path`, then import from the `runner` helper module. The lifecycle is:

1. `connect()` — establishes a bidirectional gRPC stream, waits for `StartRequest` from Go, returns the params dict
2. Script logic — calls `output()`, `progress()`, `data_result()`, `cache_set()`/`cache_get()` as needed
3. `complete()` or `fail()` — closes the send stream and drains until Go confirms receipt (EOF), guaranteeing all messages are delivered before the process exits

See `scripts/hello_world/main.py` for the simplest example, `scripts/numpy_stats/main.py` for one using pre-installed packages, `scripts/cache_produce/main.py` and `scripts/cache_consume/main.py` for shared memory caching across parallel scripts, and `scripts/file_export/main.py` for native file dialogs.

## Python Runtime

Two modes: `uv`-managed for development, bundled portable Python for distribution.

### Dev prerequisites

- **`uv`** — the only Python-related tool needed. No system Python required; `uv` downloads the interpreter automatically.
- Install: `curl -LsSf https://astral.sh/uv/install.sh | sh` (or `powershell -c "irm https://astral.sh/uv/install.ps1 | iex"` on Windows)

### Dev workflow (uv)

`pyproject.toml` at the project root defines the Python version, shared deps (grpcio, protobuf, numpy), dev tools (grpcio-tools, pytest, mypy, ruff), and linter configuration. See `pyproject.toml` for the full list.

Commands:

- `uv sync` — downloads Python 3.12+, creates `.venv/`, installs all deps (first time setup)
- `uv run pytest scripts/_lib/tests/` — run Python tests
- `uv run python -m grpc_tools.protoc ...` — protobuf codegen
- Never call `pip` or `python` directly — always use `uv run`

### Distribution (bundled Python)

End users get a bundled portable Python interpreter — no `uv`, no `pip`, no system Python needed.

- **Source**: [python-build-standalone](https://github.com/indygreg/python-build-standalone) — pre-built portable Python for Windows/Linux
- **Location**: `python/` directory next to the executable
- **Shared deps**: `grpcio`, `protobuf`, `numpy` — pre-installed into `site-packages` at build time (see `python/requirements.txt`)
- **Build script**: `build/bundle_python.sh` downloads portable Python + installs deps from `python/requirements.txt`

### How Go finds Python

Fallback order (checked at startup):

1. `.venv/Scripts/python.exe` (Windows) or `.venv/bin/python3` (Linux) — **dev mode** (`uv`-managed venv)
2. `python/python.exe` (Windows) or `python/bin/python3` (Linux) relative to executable — **distribution mode**

### Build flow

1. `uv run python build/bundle_python.py` — download portable Python, install deps from `python/requirements.txt`
2. `wails3 build` — build Go + React into native executable
3. Distribute: executable + `python/` directory + `scripts/` (or bundle into platform installer)

## Plugin System (Script Override)

Users can override built-in scripts or add new ones after the binary is built and distributed, by placing scripts in a user-writable plugin directory.

### Resolution order

```text
1. Scan built-in scripts/       (embedded alongside binary)
2. Scan user plugin directory    (~/.go-python-runner/scripts/)
3. Matching IDs                  -> plugin OVERRIDES built-in
4. New IDs                       -> ADDED as additional scripts
```

### Plugin directory

- **Linux**: `~/.go-python-runner/scripts/`
- **Windows**: `%APPDATA%/go-python-runner/scripts/`
- Configurable via config file or environment variable `PYRUNNER_PLUGIN_DIR`
- Same structure as built-in: `<plugin-dir>/<script-name>/script.json + main.py`

### Registry behavior (`internal/registry/registry.go`)

`Script` struct holds ID, Name, Description, Params, Source ("builtin"/"plugin"), and Dir (absolute path). `Param` holds name, type, required, default, description.

1. `LoadBuiltin(scriptsDir string)` — load bundled scripts into the registry
2. `LoadPlugins(pluginDir string)` — scan user plugin directory
3. For matching IDs: plugin replaces built-in (log a warning via unified logging)
4. For new IDs: add to registry as `Source: "plugin"`

### Frontend behavior

- TaskCard displays a badge/indicator distinguishing plugin scripts from built-in
- `ScriptService.GetPluginDir() string` — exposes the plugin directory path so users know where to place scripts

### Safety

- Plugin scripts use the same gRPC protocol — no special runtime treatment
- Plugin scripts have access to the same shared Python deps (`grpcio`, `protobuf`)
- Malformed `script.json` in plugin dir: skip the script and log a warning (never crash)
- Missing `main.py` in plugin dir: skip the script and log a warning

## Shared Cache

Parallel Python scripts can share **any picklable Python object** (dicts, DataFrames, numpy arrays, model weights, images, custom classes) via OS-level shared memory. Go manages the cache lifecycle; Python scripts access the data directly.

```text
Go Backend (Cache Manager)
├── Tracks named blocks: key -> {shm_name, size, owner_run, ref_count}
├── gRPC messages: CacheCreate, CacheLookup, CacheRelease, CacheInfo
└── Cleanup: releases blocks when all referencing runs complete

Python Script A                         Python Script B
├── runner.cache_set("features", obj)   ├── runner.cache_get("features")
│   1. pickle.dumps(obj)                │   1. gRPC CacheLookup -> shm_name + size
│   2. SharedMemory.create(size)        │   2. SharedMemory.open(shm_name)
│   3. Write pickled bytes to block     │   3. pickle.loads(shm.buf) -> obj
│   4. gRPC CacheCreate -> register     │   (shared memory, no Go intermediary)
└── Continues execution                 └── Continues execution
```

### How it works

- **`cache_set(key, obj)`**: Pickles the object, creates a `SharedMemory` block, writes the serialized bytes, registers the block with Go via gRPC.
- **`cache_get(key)`**: Looks up block metadata via Go (gRPC), opens the `SharedMemory` by name, unpickles and returns the original Python object.
- **Data path**: Python -> shared memory -> Python. Go never touches the blob data — it only tracks metadata (name, size, ref count).
- **Any picklable type**: dicts, lists, DataFrames, numpy arrays, sklearn models, PIL images, custom classes.

### Python helper API (`scripts/_lib/runner.py`)

Three functions: `cache_set(key, obj)`, `cache_get(key)`, `cache_release(key)`. Internally uses `pickle.dumps`/`loads` with `multiprocessing.shared_memory.SharedMemory`. Each function sends the corresponding gRPC message (CacheCreate/CacheLookup/CacheRelease) and manages local SharedMemory handles. See `scripts/_lib/runner.py` for the full typed implementation and `scripts/cache_produce/main.py` for a working example.

### Go Cache Manager (`internal/runner/cache.go`)

`CacheManager` tracks blocks in a `sync.RWMutex`-protected map. Each `CacheBlock` stores Key, ShmName, Size, OwnerRun (creator's runID), and Refs (slice of runIDs referencing it).

- `Register(key, shmName, size, ownerRunID)` — add block to registry
- `Lookup(key) -> (shmName, size, found)` — return block info for Python to open
- `Release(key, runID)` — remove ref; if zero refs remain, delete block
- `CleanupRun(runID)` — release all blocks owned by / referenced by a terminated run

### Cleanup and safety

- When a run completes or crashes, Go calls `CleanupRun(runID)` to remove the run's references. Blocks persist in the registry for future consumers.
- Blocks are deleted only via explicit `Release` calls or app shutdown.
- **Windows limitation**: The OS reclaims shared memory when the last handle closes. Cross-process sharing requires concurrent runs — a producer that exits before its consumer starts will leave the Go registry intact but the underlying memory gone. The `cache_produce` script holds its handle alive for a configurable duration so `cache_consume` can retrieve the data while it's still running.
- On Linux: shared memory blocks persist in `/dev/shm/` until explicitly unlinked.

## Testing

Three tiers of tests across all layers. Go integration tests are gated behind a build tag so `go test ./...` stays fast.

### Unit Tests

Isolated, no external dependencies, fast.

**Go** (`go test ./internal/...`):

| Package | What to test | Approach |
| --- | --- | --- |
| `internal/registry/` | Script discovery, JSON parsing, plugin override logic | Temp dirs with test `script.json` + `main.py`. Verify `LoadBuiltin`, `LoadPlugins`, override by ID, malformed JSON skipped. |
| `internal/logging/` | Ring buffer overflow, slog handler routing | `ring_test.go`: push beyond capacity, verify FIFO eviction. `logger_test.go`: verify multi-handler dispatches to file + ring. |
| `internal/runner/` | Protobuf message serialization | Round-trip each `oneof` variant through marshal/unmarshal. |

**Python** (`pytest scripts/_lib/tests/`):

| Module | What to test | Approach |
| --- | --- | --- |
| `runner.py` | Helper functions produce correct protobuf messages | Mock the gRPC stub. Call `output()`, `progress()`, `complete()` — verify protobuf messages sent. |

**Frontend** (`vitest` + React Testing Library):

| Component | What to test | Approach |
| --- | --- | --- |
| `TaskCard` | Renders name, description, status badge, plugin indicator | Pass props, assert DOM output. |
| `ParamForm` | Generates inputs from param schema, validates required fields | Render with param definitions, simulate user input, verify form data. |
| `LogViewer` | Filters by source/level, renders collapsible tracebacks | Pass log entries, toggle filters, assert visibility. |

### Service Tests

Test each service layer with real dependencies (gRPC, filesystem) but without the full Wails app.

**Go gRPC service** (`internal/runner/grpc_server_test.go`):

- Start real gRPC server on `localhost:0` (random port)
- Use a minimal Python test script that connects and sends all message types
- Verify Go receives correctly typed messages through the channel
- Test cancel flow: send cancel, verify Python process exits

**Go Wails services** (`internal/services/*_test.go`):

- `ScriptService`: real registry with temp script dirs, verify `ListScripts`/`GetScript` return correct data
- `RunnerService`: mock the manager interface, verify `StartRun` returns runID, `CancelRun` propagates
- `LogService`: real logger + ring buffer, call `LogError`, verify `GetLogs` returns the entry with correct fields

**Python gRPC client** (`scripts/_lib/tests/test_grpc_client.py`):

- Start a mock gRPC server in-process
- Call helper functions (`output`, `progress`, `complete`)
- Verify the server receives correct protobuf messages

### Integration Tests

Full end-to-end: Go spawns Python subprocess, gRPC connects, typed messages flow through channels.

Located in `tests/integration/`, gated with `//go:build integration` build tag.

| Test | What it validates |
| --- | --- |
| `TestFullRun` | Go manager starts a real Python script, collects all messages from typed channel, asserts output -> progress -> status sequence |
| `TestParallelRuns` | Start 3 scripts concurrently, verify each gets independent message streams with no cross-talk |
| `TestPluginOverride` | Load builtin + plugin with same ID, run it, verify the plugin version executed |
| `TestScriptCrash` | Run a script that raises an exception — verify stderr captured, error logged, status = "failed" |
| `TestCancelMidRun` | Start a long-running script, cancel it mid-execution, verify process terminated and status = "failed" |
| `TestCacheShareObject` | Script A caches a dict via `cache_set`, Script B retrieves it via `cache_get` — verify object equality |
| `TestCacheConcurrentReaders` | 3 scripts reading the same cached object simultaneously — verify no corruption |
| `TestCacheCleanupOnCrash` | Kill script that owns a cache block — verify Go releases shared memory |

Test fixture scripts live in `tests/integration/testdata/` (echo, crash, slow variants).

### Test Commands

```bash
# Go unit + service tests
go test ./internal/...

# Go integration tests (requires Python)
go test ./tests/integration/ -tags=integration

# Python tests
cd scripts/_lib && pytest tests/

# Frontend tests
cd frontend && npx vitest run

# All tests
make test
```

## Tech Stack

| Layer | Choice | Reason |
| --- | --- | --- |
| Desktop framework | Wails v3 | Single binary, native webview, Go bindings |
| Go <-> Frontend | Wails bindings + events | Zero boilerplate, typed |
| Go <-> Python | gRPC bidirectional streaming | Typed protobuf contract, parallel |
| Schema | Protobuf with `oneof` | Cross-language validation, compile-time safe |
| Frontend | React 18 + TypeScript + Vite + Tailwind | Wails v3 supported |
| Go concurrency | Typed channels + type switches | Idiomatic, compile-time safe |
| Python dev tooling | `uv` | Downloads Python, manages venv + deps, no system Python needed |
| Python runtime (dist) | python-build-standalone | Bundled portable Python for end users |
| Python deps | grpcio + protobuf (shared base) | `uv sync` in dev, pre-installed at build time for dist |
| Logging | `log/slog` (stdlib) + `lumberjack` | Structured, rotating file output |
| Plugin system | OS app data dir + registry overlay | Override/extend scripts post-build |
| Go testing | `testing` (stdlib) + `testify/assert` | Standard + readable assertions |
| Python testing | `pytest` | De facto Python test runner |
| Frontend testing | `vitest` + React Testing Library | Fast, Vite-native, component testing |

## Implementation Phases

1. **Scaffold** — `wails3 init` with React template, set up protobuf toolchain
2. **Proto + codegen** — Define `runner.proto`, generate Go and Python code
3. **Go core** — Registry (builtin + plugin loading), process manager, gRPC server, typed channels
4. **Unified logging** — `slog` multi-handler, ring buffer, lumberjack file rotation
5. **Wails services** — ScriptService + RunnerService + LogService with events
6. **Python helper** — `runner.py` wrapping generated gRPC client
7. **React frontend** — TaskCard, TaskGrid, RunOutput, ParamForm, LogViewer components
8. **Plugin system** — Plugin directory scanning, override logic, frontend badges
9. **Unit + service tests** — Go `*_test.go` files, Python `pytest`, frontend `vitest`
10. **Integration** — Wire everything, test with sample scripts + plugin overrides
11. **Integration tests** — `tests/integration/` with build tag, fixture scripts in `testdata/`
12. **Build** — Single executable via `wails3 build` + bundled Python

## Verification

- `wails3 dev` — run in dev mode, verify task cards load
- Start a sample Python script, verify real-time output/progress in UI
- Run multiple scripts in parallel, verify independent channels
- `wails3 build` — verify single executable works standalone
- Place a script in plugin dir with same ID as built-in — verify it overrides
- Place a script in plugin dir with new ID — verify it appears as additional task card
- Trigger errors in all three layers — verify they appear in LogViewer with correct source labels

## Development Commands

```bash
# First-time setup
uv sync                                          # Python venv + deps
cd frontend && npm install                       # Node deps

# Dev mode (hot reload)
wails3 dev

# Code generation
make proto                                       # Protobuf Go + Python
make bindings                                    # Wails TypeScript bindings

# Run all tests
make test

# Run tests by layer
go test ./internal/...                           # Go unit + service
go test ./tests/integration/ -tags=integration   # Go integration
uv run pytest scripts/_lib/tests/                # Python
cd frontend && npx vitest run                    # Frontend

# Lint all layers
make lint                                        # All linters
go vet ./...                                     # Go
uv run ruff check scripts/ tests/                # Python style
uv run mypy scripts/_lib/runner.py               # Python types
cd frontend && npx tsc --noEmit                  # TypeScript

# Bundle Python runtime for distribution
bash build/bundle_python.sh

# Production build
wails3 build
```

# Go Python Runner

A native desktop application that orchestrates bundled Python scripts through a Go backend. Users interact with task cards in a React frontend — never seeing the underlying Python code. Builds to a single executable.

## Architecture Decisions

1. **Desktop app**: Wails v3 — single native executable with embedded webview
2. **Go <-> Frontend**: Wails auto-generated TypeScript bindings + Wails typed events (real-time)
3. **Go <-> Python**: Plain gRPC with bidirectional streaming, own process lifecycle management
4. **Schema**: Protobuf `.proto` file as single source of truth; `oneof` for compile-time type safety
5. **Go concurrency**: Typed channels + interface types + type switches (idiomatic Go)
6. **Scripts**: Bundled with app in the repository
7. **Frontend**: React 19 + TypeScript + Vite + Tailwind (via Wails v3)
8. **Python runtime**: Dev: `uv` manages interpreter + venv + deps. Distribution: bundled portable interpreter via python-build-standalone (no user setup)
9. **Python deps**: Shared base (`grpcio`, `protobuf`, `numpy`) — managed by `uv` in dev, pre-installed at build time for distribution
10. **Logging**: Unified structured logging via Go `log/slog` — frontend, backend, and Python errors all funnel into one system
11. **Plugin system**: User-writable script directory that can override built-in scripts or add new ones post-build
12. **Testing**: Tiered — unit (isolated, fast), service (real deps, no full app), integration (end-to-end Go ↔ Python, build-tagged), stress (high-volume / concurrency, build-tagged)
13. **Shared cache**: Parallel scripts share any Python object via `multiprocessing.shared_memory` + pickle. Go manages lifecycle (registry, ref counting, cleanup).
14. **Error handling**: Every failure flows through one ingress — `internal/notify.Reservoir.Report(Event)` — which routes by severity × persistence to slog (file + ring buffer + LogViewer) AND a UI surface (toast, banner, streamed pane, or full-screen pane). Services hold *only* `notify.Reservoir`; `*slog.Logger` exists only in `main.go` and `internal/notify`. See § Error Handling for the four-part contract and the routing matrix.
15. **Plugin authoring loop**: Catalog hot-reloads on filesystem changes via fsnotify; malformed plugins surface as `LoadIssue` records, not silent skips. Scripts can be opened in the OS default handler from the UI.

## Architecture Layers

```text
┌──────────────────────────────────┐
│  React Frontend (Task Cards)     │  Wails v3 webview
│  - Auto-generated TS bindings    │
│  - useNotifyChannel subscribes   │
│    to notify:* + run:* events    │
├──────────────────────────────────┤
│  Go Backend (Wails Services)     │
│  ├─ ScriptService (registry)     │  Wails bindings -> frontend
│  ├─ RunnerService (lifecycle)    │  Wails events -> frontend
│  ├─ LogService (ring buffer +    │  GetLogs / log:entry events
│  │   LogError shim into          │
│  │   reservoir for frontend JS)  │
│  ├─ EnvService (venv inspector)  │  Install/uninstall + streaming
│  ├─ notify.Reservoir             │  *Single ingress* for every
│  │                               │   error → slog + Wails routing
│  └─ gRPC Server                  │  gRPC <-> Python
├──────────────────────────────────┤
│  Python Scripts (bundled)        │
│  - gRPC client (generated)       │
│  - Helper library (fail/warn/    │
│    info carry Severity enum)     │
└──────────────────────────────────┘
```

## Protobuf Contract

Single source of truth: `proto/runner.proto`. Both Go and Python use generated code from this file. The `oneof` fields provide compile-time type safety — no loose `"type"` string field with arbitrary JSON.

One bidirectional streaming RPC (`Execute`). Message direction follows gRPC client/server roles:

- **ClientMessage** (Python → Go): `Output`, `Progress`, `Status`, `Error`, `DataResult`, `CacheCreate`, `CacheLookup`, `CacheRelease`, `FileDialogRequest`, `DbExecute`, `DbQuery`
- **ServerMessage** (Go → Python): `StartRequest` (with params map), `CancelRequest`, `CacheInfo`, `FileDialogResponse`, `DbResult`, `DbQueryResult`

## Wails v3 Services

### Architecture philosophy: Frontend shows, Go manages, Python does

| Layer | Role |
| --- | --- |
| React (frontend) | **Shows.** Renders state from events. No state-machine logic, no dedup guards. |
| Go (backend) | **Manages.** Sole authority on run lifecycle and `run:status` events. Owns process supervision, cache registry, DB. |
| Python (scripts) | **Does.** Performs the actual work. Reports outcomes (output, progress, error) to Go. |
| Wails / gRPC | **Propagates.** Pure transport. The gRPC handler updates Manager-internal flags from Python's `Status` messages but never forwards them to the frontend — Manager is the single emitter. |

### ScriptService

Exposed to frontend via auto-generated TypeScript bindings:

- `ListScripts() []Script` — returns all registered scripts with metadata, in deterministic order (builtin first, then by Name)

### RunnerService

Exposed via bindings (methods) + events (real-time updates):

- `StartRun(scriptID string, params map[string]string) string` — returns runID
- `StartParallelRuns(scriptID string, params map[string]string, workerCount int) []string` — fan-out via the script's parallel config
- `CancelRun(runID string) error` — graceful cancellation; returns error if runID is unknown (this is the canonical "did the run terminate?" signal)

Run history is exposed as a Python script (`db_run_history`) that queries the SQLite `runs` table directly, not as a Wails binding. The Go side writes the table; consumers read it via the same DB path.

Emits Wails events:

- `run:output` — stdout text from script
- `run:progress` — progress updates (current/total/label)
- `run:status` — state transitions (running → completed | failed). Emitted exactly once at the terminal state by `Manager.waitForExit`. Python's `complete()` / `fail()` calls feed flags into Manager's derivation but do not produce frontend events on their own.
- `run:error` — error messages with traceback (content; status is set by `run:status`). Emitted by `notify.Reservoir` for in-flight errors (severity ≥ Error, has runID); routes to the per-run pane.
- `run:data` — structured data results (key + value bytes from `data_result()`)
- `notify:toast` — one-shot user-visible error/warn (auto-dismissing). Emitted by `notify.Reservoir` for `Severity:Error`/`Warn` × `Persistence:OneShot`.
- `notify:banner` — sticky ongoing condition (e.g. plugin load issue). Emitted for `Persistence:Ongoing`. Banners dedupe by `Key`; same-key re-emission replaces the prior entry.
- `notify:banner:dismiss` — Go signals a known ongoing condition cleared (paired with banner Key).
- `notify:banners:list` — full banner snapshot, used for atomic replacement (`ReplaceBannersByPrefix`) and reconnect/refresh.
- `notify:critical` — full-screen pane for unrecoverable failures (`Persistence:Catastrophic` or `Severity:Critical`).

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

All three error sources funnel through one Go-side ingress: `notify.Reservoir.Report(Event)`. The reservoir owns the slog write AND the Wails routing decision — callers cannot pick "slog without UI" or "UI without slog" by accident.

```text
┌─────────────┐     LogService.LogError()  ┌──────────────────────┐
│  Frontend    │ ─────────────────────────>│                      │
│  (React)     │     (thin shim → Report)  │   notify.Reservoir   │
└─────────────┘                            │                      │
                                           │   1. Always slog ──> File (lumberjack-rotated app.log)
┌─────────────┐     reservoir.Report(...)  │              ──> Ring buffer (1000 entries)
│  Go Backend  │ ─────────────────────────>│              ──> log:entry Wails event
│  (services)  │                           │
└─────────────┘                            │   2. Route by Severity × Persistence:
                                           │      Error  + OneShot   → notify:toast
┌─────────────┐     gRPC Error msg         │      Error  + Ongoing   → notify:banner
│  Python      │ ─────────────────────────>│      Error  + InFlight  → run:error
│  Scripts     │     + stderr capture      │      Critical/Catastrophic → notify:critical
└─────────────┘                            │      Warn/Info + OneShot   → log-only
                                           └──────────────────────┘
```

### Error sources

| Source | How errors reach the reservoir | Details |
| --- | --- | --- |
| Frontend (React) | `LogService.LogError(source, msg, context)` Wails binding | Becomes a `Source:Frontend` event. ErrorBoundary + global handlers wired in `main.tsx` / `App.tsx` call into this. |
| Go backend | `reservoir.Report(notify.Event{...})` directly | Services hold `notify.Reservoir` only — `*slog.Logger` is internal to the reservoir. |
| Python scripts | gRPC `Error` message (structured) + stderr capture (unstructured) | gRPC errors carry `Severity` (proto enum) + traceback. Manager reservoir-reports stderr-synthesized errors when Python crashes pre-`fail()`. |

### LogService (Wails service)

- `LogError(source, message, context)` — thin shim that calls `reservoir.Report(Severity:Error, Persistence:OneShot, Source:<arg>)`. The optional `runID` / `scriptID` / `traceback` keys in `context` carry through.
- `GetLogs() []LogEntry` — returns ring buffer entries; client filters.
- Emits Wails event `log:entry` for real-time log streaming.

### Log levels

`notify.Severity` (Info / Warn / Error / Critical) maps 1:1 to slog levels (Info / Warn / Error / Error). `Severity:Info` + `Persistence:OneShot` is the trace pattern — what used to be `slog.Info(...)` becomes `reservoir.Report(...)` with those values, which routes to log-only (no toast).

### Log output

- **File**: OS-appropriate app data dir (`~/.go-python-runner/logs/` or `%APPDATA%/go-python-runner/logs/`). JSON lines format (structured, parseable). Rotating via `lumberjack` package.
- **Ring buffer**: In-memory, capped at last 1000 entries. Serves `GetLogs()` calls from the UI.
- **Wails events**: `log:entry` events stream to the frontend LogViewer in real-time.

### LogViewer component (`frontend/src/components/LogViewer.tsx`)

- Unified log panel showing all sources
- Filters by source (frontend/backend/python), level, scriptID, runID
- Real-time updates via `log:entry` Wails event
- Collapsible traceback display for Python errors

## Error Handling

**Every failure must surface. No silent failures, no log-only failures, no toast-only failures. This is a hard rule. PRs that violate it should not pass review.**

### The four-part contract

For every operation that can fail:

1. **Return the error.** Functions that can fail return `error` (Go) or reject the Promise (TS). Discarding with `_ =` or empty `catch {}` is forbidden.
2. **Record durably.** A single `reservoir.Report(Event)` call writes to slog (file + ring buffer + LogViewer) and emits the routed Wails event. The reservoir owns step 2 by construction; callers cannot record durably without surfacing.
3. **Surface to the user.** Same `reservoir.Report` call — the routing matrix below picks the surface from `Severity × Persistence`. There is no second call.
4. **Don't leave torn state.** If the failure happened mid-operation, recover or roll back before returning. Half-applied state is worse than a clean rollback.

Parts 2 and 3 used to be two separate things to remember (slog + Wails Emit). They are now satisfied by a single `Report` call, which is why the orthodoxy is non-negotiable: services hold *only* `notify.Reservoir`, never a logger.

### Orthodox dependency pattern (non-negotiable)

1. **Reservoir is the sole observability dependency.** Every service that emits anything observable (error, warning, info trace) holds exactly one field: `reservoir notify.Reservoir`. No `*slog.Logger` field.
2. **Trace events go through `reservoir.Report` too.** What used to be `s.logger.Info(...)` is now `s.reservoir.Report(notify.Event{Severity: SeverityInfo, Persistence: PersistenceOneShot, ...})`. The routing maps Info+OneShot to log-only — no UI noise.
3. **Reservoir is constructor-only.** Every service takes `notify.Reservoir` as a constructor parameter. No `SetReservoir`, no two-phase init, no nil-tolerant fallback.
4. **Banner publication uses `Reservoir.ReplaceBannersByPrefix` directly.** No per-service `publishIssues` wrapper. Whoever owns the keyspace (`loadIssue:*`, etc.) calls `ReplaceBannersByPrefix` itself.
5. **`*slog.Logger` exists only in `main.go` and `internal/notify`.** Importing `log/slog` in a service breaks the rule.
6. **One Report per failure.** A failure that crosses the Go→frontend boundary is `Report`'d by the Go side only. The frontend catch is silent (or `console.warn` for debugging) — never `addNotification`. Pure-frontend failures (input validation, transport-layer where Wails itself broke) still go through `LogService.LogError` or `addNotification`, which routes through the reservoir. The mechanical test: *"does this catch follow an `await bindings.X.Y(...)`?"* If yes, do not addNotification — Go already did.

### Three orthogonal axes (not a hierarchy)

| Axis | Question it answers |
| --- | --- |
| **Severity** (info / warn / error / critical) | *How bad?* |
| **Persistence** (one-shot / ongoing / in-flight / catastrophic) | *Does the user need to take action that persists across renders?* |
| **Source** (backend / python / frontend) | *Who reported it?* |

Surface is **determined by** Severity × Persistence inside `notify.Reservoir.routeWails`:

| Persistence | Severity | Wails event | UI surface |
| --- | --- | --- | --- |
| Ongoing | warn / error | `notify:banner` (key-deduped) | Sticky banner |
| One-shot | error | `notify:toast` | Auto-dismissing toast |
| One-shot | warn / info | (none — log-only) | LogViewer only |
| In-flight | error (with runID) | `run:error` | Per-run streamed pane |
| In-flight | warn / info | `notify:toast` | Toast (so it isn't lost in a collapsed card) |
| Catastrophic | any | `notify:critical` | Full-screen pane |
| any | critical | `notify:critical` | Full-screen pane (severity overrides persistence) |

`ReplaceBannersByPrefix` additionally emits `notify:banners:list` with the full ongoing-banner snapshot — the frontend's central router (`useNotifyChannel`) atomically swaps its ongoing-banner set on each one.

### Forbidden patterns

| Anti-pattern | Why forbidden |
| --- | --- |
| `_ = mgr.CancelRun(id)` | Discarded errors. If you genuinely don't care, comment why and use `//nolint:errcheck`. |
| Holding `*slog.Logger` in a service | Violates the orthodoxy. Logger is internal to `notify.Reservoir`. |
| `s.logger.Error(...)` then `app.Event.Emit("foo:error", ...)` | Two-call pattern; one of them will rot. Use a single `Report`. |
| `addNotification({severity:'error', ...})` from a frontend path that has no Go-side log | Toast vanishes in 6s; tomorrow's bug report has no trace. Frontend errors should go through `LogService.LogError` (which routes through the reservoir). |
| `addNotification(...)` in the catch block of an `await bindings.X.Y(...)` call | The Go side's `reservoir.Report` already produced the toast via `notify:toast`. Adding another via `addNotification` creates a near-identical duplicate with a different React id. Frontend catch is control flow only. |
| Calling `reservoir.Report` on user cancellation (closed dialog, declined prompt) | Cancel is not a failure. Use `runner.ErrDialogCancelled` (or an operation-specific sentinel) and short-circuit the Report path. See § Cancellation vs failure. |
| `try { ... } catch { return null }` | Silent swallow; downstream can't tell "no data" from "broken." |
| Returning success with a `Severity:Warn` `Report` of "partial failure" | If part failed, the operation failed. Period. |
| Default-to-fallback values on error | Hides failures. The caller can decide on a fallback if they want one. |

### Templates

Wails-bound method (orthodox):

```go
func (s *FooService) DoThing(arg string) error {
    if err := s.validate(arg); err != nil {
        wrapped := fmt.Errorf("validate %q: %w", arg, err)
        s.reservoir.Report(notify.Event{
            Severity:    notify.SeverityError,
            Persistence: notify.PersistenceOneShot,
            Source:      notify.SourceBackend,
            Title:       "DoThing failed",
            Message:     wrapped.Error(),
            Err:         wrapped,
        })
        return wrapped
    }
    s.reservoir.Report(notify.Event{
        Severity:    notify.SeverityInfo,
        Persistence: notify.PersistenceOneShot,
        Source:      notify.SourceBackend,
        Message:     fmt.Sprintf("doThing succeeded: %s", arg),
    })
    return nil
}
```

Frontend caller:

```ts
try {
  await bindings.FooService.DoThing(arg)
} catch {
  // Go's reservoir.Report already surfaced this via notify:toast.
  // Frontend catch is control flow only — no addNotification, no LogError.
}
```

Pure-frontend validation (no binding involved) still surfaces via `addNotification`, since Go never sees these and would not otherwise log them:

```ts
if (!spec.trim()) {
  addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message: 'Package spec cannot be empty' })
  return
}
```

Background goroutine:

```go
go func() {
    if err := s.longRun(...); err != nil {
        s.reservoir.Report(notify.Event{
            Severity:    notify.SeverityError,
            Persistence: notify.PersistenceOneShot,
            Source:      notify.SourceBackend,
            Title:       "longRun failed",
            Message:     err.Error(),
            Err:         err,
        })
        return
    }
}()
```

### Cascading failures: `errors.Join`, not exceptions

When a primary operation fails and rollback fires, the rollback itself may partially fail. Don't carve out an exception ("user already saw the primary failure"). Use `errors.Join` so all errors propagate:

```go
runIDs, primaryErr := s.startAll(...)
if primaryErr != nil {
    s.reservoir.Report(notify.Event{
        Severity: notify.SeverityError, Persistence: notify.PersistenceOneShot,
        Source: notify.SourceBackend, Title: "Parallel run failed",
        Message: primaryErr.Error(), Err: primaryErr,
    })
    var cleanup []error
    for _, id := range runIDs {
        if cancelErr := s.manager.CancelRun(id); cancelErr != nil {
            rollbackErr := fmt.Errorf("rollback %s: %w", id, cancelErr)
            s.reservoir.Report(notify.Event{
                Severity: notify.SeverityError, Persistence: notify.PersistenceOneShot,
                Source: notify.SourceBackend, RunID: id,
                Title: "Parallel rollback incomplete", Message: rollbackErr.Error(), Err: rollbackErr,
            })
            cleanup = append(cleanup, rollbackErr)
        }
    }
    return nil, errors.Join(append([]error{primaryErr}, cleanup...)...)
}
```

Every error is durably reported at its layer; the joined error becomes the binding's rejection on the frontend. No exemption, no carve-out.

### Cancellation vs failure

User-driven non-completion (closed dialog, declined prompt, cooperative cancel) is **not** a failure. It must not call `reservoir.Report`. Use a sentinel error at the boundary and short-circuit the Report path:

```go
// internal/runner/dialog.go
var ErrDialogCancelled = errors.New("dialog cancelled by user")

// main.go wailsDialogHandler — translate empty-path to the sentinel
path, err := d.PromptForSingleSelection()
if path == "" {
    return "", runner.ErrDialogCancelled
}
return path, err

// internal/runner/grpc_server.go handleFileDialog
switch {
case errors.Is(err, ErrDialogCancelled):
    resp.Cancelled = true                  // silent — not a failure
case err != nil:
    s.reservoir.Report(notify.Event{...})  // real OS failure
    resp.Cancelled = true
    resp.Error = err.Error()
}
```

Sentinel + `errors.Is` keeps future call sites uniform. The boundary (the dialog handler in `main.go`) is responsible for normalizing platform-specific cancel signals (empty-path, specific error types, etc.) into `ErrDialogCancelled`; downstream layers only check the sentinel.

### Tests must enforce

Every new method gets at least one negative test asserting all four parts of the contract via `notify.AssertContract` (defined in `internal/notify/testing.go`):

- Method returns a non-nil error when its dep fails.
- A `notify.RecordingReservoir` recorded an `Event` with the expected `Severity`, `Persistence`, `Source` (Phase B already proves Report always slog-writes — no need to re-assert that per service).
- Internal state remains coherent (active runs map, cache registry, registry issues — service-specific assertions).

For tests that *also* need to verify the slog record itself (e.g. attribute round-trip), construct the reservoir as `notify.New(testLogger)` where `testLogger` wraps a capturing `slog.Handler` — see `internal/services/log_service_test.go::reservoirAndRing` for the pattern.

### Severity & Persistence reference

Single source of truth across the four layers. Translation between layers is by formatting alone.

| Layer | Severity | Persistence |
| --- | --- | --- |
| Proto (`runner.proto`) | `SEVERITY_INFO` / `SEVERITY_WARN` / `SEVERITY_ERROR` / `SEVERITY_CRITICAL` (+ `SEVERITY_UNSPECIFIED` = ERROR for back-compat) | n/a (UI concept) |
| Go (`internal/notify`) | `notify.SeverityInfo` / `SeverityWarn` / `SeverityError` / `SeverityCritical` | `notify.PersistenceOneShot` / `PersistenceOngoing` / `PersistenceInFlight` / `PersistenceCatastrophic` |
| Python (`scripts/_lib/runner.py`) | `SEVERITY_INFO` / `SEVERITY_WARN` / `SEVERITY_ERROR` / `SEVERITY_CRITICAL` | n/a |
| TypeScript (`useNotifications`) | `'info' \| 'warn' \| 'error' \| 'critical'` | `'one-shot' \| 'ongoing' \| 'in-flight' \| 'catastrophic'` |

Source string (`'backend' \| 'python' \| 'frontend'`) is identical across all layers.

## Python Script Structure

Each script lives in `scripts/<name>/` with two files:

### script.json (metadata)

JSON file with fields: `id`, `name`, `description`, `params[]` (each with `name`, `required`, `default`, `description`). See `scripts/hello_world/script.json` for a working example.

### main.py (entry point)

Scripts import from the `runner` helper module — Go sets `PYTHONPATH` to include `scripts/_lib` when spawning each subprocess, so plugin scripts placed in `~/.go-python-runner/scripts/` import the same way without `sys.path` manipulation.

The standard entrypoint is `runner.run(main_func)`, which connects, calls `main_func(params)`, and translates `KeyboardInterrupt` / `SystemExit` / `Exception` into a structured `fail()`. A minimal script:

```python
from runner import run, output, complete

def main(params):
    output(f"Hello, {params['name']}")
    complete()

if __name__ == "__main__":
    run(main)
```

Inside `main`, scripts call any combination of:

- `output()`, `progress()`, `data_result()` — send results to frontend
- `complete()` — signal successful completion to Go
- `fail(msg, tb=None)` — signal failure with optional explicit traceback (defaults to `traceback.format_exc()`)
- `cache_set()`/`cache_get()`/`cache_release()` — shared memory between scripts
- `dialog_open()`/`dialog_save()` — native OS file pickers
- `db_execute()`/`db_query()` — SQLite database access
- `is_cancelled()` — non-blocking check for Go-initiated cancellation (cooperative stop for long-running loops)

`runner.run(main)` ensures `complete()` or `fail()` is invoked exactly once before the process exits, which closes the send stream and drains until Go confirms receipt (EOF) — this guarantees all messages are delivered before the process terminates.

See `scripts/hello_world/main.py` for the simplest example, `scripts/numpy_stats/main.py` for pre-installed packages, `scripts/cache_produce/main.py` and `scripts/cache_consume/main.py` for shared memory caching, `scripts/file_export/main.py` for native file dialogs, and `scripts/db_todo/main.py` for database access.

## Python Runtime

Two modes: `uv`-managed for development, bundled portable Python for distribution.

### Dev prerequisites

- **`uv`** — the only Python-related tool needed. No system Python required; `uv` downloads the interpreter automatically.
- Install: `curl -LsSf https://astral.sh/uv/install.sh | sh` (or `powershell -c "irm https://astral.sh/uv/install.ps1 | iex"` on Windows)

### Dev workflow (uv)

`pyproject.toml` at the project root defines the Python version, shared deps (grpcio, protobuf, numpy), dev tools (grpcio-tools, pytest, mypy, ruff), and linter configuration. See `pyproject.toml` for the full list.

Commands:

- `uv sync` — downloads Python 3.13+, creates `.venv/`, installs all deps (first time setup)
- `uv run pytest scripts/_lib/tests/` — run Python tests
- `uv run python -m grpc_tools.protoc ...` — protobuf codegen
- Never call `pip` or `python` directly — always use `uv run`

### Distribution (bundled Python)

End users get a bundled portable Python interpreter — no `uv`, no `pip`, no system Python needed.

- **Source**: [python-build-standalone](https://github.com/indygreg/python-build-standalone) — pre-built portable Python for Windows/Linux
- **Location**: `python/` directory next to the executable
- **Shared deps**: `grpcio`, `protobuf`, `numpy` — pre-installed into `site-packages` at build time (see `python/requirements.txt`)
- **Build script**: `build/bundle_python.py` downloads portable Python + installs deps from `python/requirements.txt`

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
- Override via environment variable `PYRUNNER_PLUGIN_DIR` (resolved by `registry.DefaultPluginDir`)
- Same structure as built-in: `<plugin-dir>/<script-name>/script.json + main.py`

### Registry behavior (`internal/registry/registry.go`)

`Script` struct holds ID, Name, Description, Params, Source ("builtin"/"plugin"), and Dir (absolute path). `Param` holds name, type, required, default, description.

1. `LoadBuiltin(scriptsDir string)` — load bundled scripts into the registry
2. `LoadPlugins(pluginDir string)` — scan user plugin directory
3. For matching IDs: plugin replaces built-in (log a warning via unified logging)
4. For new IDs: add to registry as `Source: "plugin"`

### Frontend behavior

- TaskCard displays a badge/indicator distinguishing plugin scripts from built-in
- The plugin directory path is documented (Windows / Linux paths above) but is not surfaced via a Wails method today; if a UI affordance for "open plugin folder" is needed, add a `ScriptService.GetPluginDir()` method and an `Open Folder` button

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

## Run Lifecycle State Machine

The run lifecycle is governed by a **distributed state machine** spanning 4 layers. Typed status constants and transition guards were added to formalize the critical paths.

### Composite State

A run's true state is the combination of multiple independent variables across layers:

| Layer | State Variables | Location |
| --- | --- | --- |
| Go Manager | `RunState.Status` (typed `RunStatus`: `StatusRunning` / `StatusCompleted` / `StatusFailed`), presence in `activeRuns` map | `internal/runner/manager.go` |
| gRPC RunChannel | `stream` (nil/set), `connected` (chan open/closed), `closed` (bool), `gotError` (`atomic.Bool`) | `internal/runner/grpc_server.go` |
| Python client | `_stream` (None/set), `_msg_iter._done` (bool), `_cache_refs` (dict of SharedMemory handles) | `scripts/_lib/runner.py` |
| React frontend | `RunState.status` (typed `RunStatus` union), `output[]`, `progress`, `error` | `frontend/src/hooks/useScripts.ts` |

### State Transitions

```text
REGISTERED
│  Go: GRPCServer.RegisterRun() creates RunChannel (stream=nil, connected=open)
│
├─ proc.Start() succeeds
▼
PROCESS_STARTED
│  Go: Status="running", added to activeRuns map
│  RunChannel: stream=nil, waiting for Python to connect
│  React: status="running" (set immediately on startRun)
│
├─ Python connects via gRPC ──────────────── or ── connect timeout → FAILED
▼
CONNECTED
│  RunChannel: stream=set, connected=closed (via sync.Once)
│
├─ SendStart() delivers params
▼
EXECUTING
│  Messages flowing: Output, Progress, DataResult, Cache*, FileDialog, Db*
│  Python calls output(), progress(), cache_set(), etc.
│
├─ exit code 0 ──────┬── exit code != 0 or err ──┬── CancelRun() called
▼                     ▼                           ▼
COMPLETED          ERRORED                     CANCELLED
Status="completed"  Status="failed"             gRPC cancel + proc.Kill
                    GotError flag tracks         Status="failed"
                    whether structured error
                    arrived via gRPC vs stderr
│                     │                           │
└─────────────────────┴───────────────────────────┘
                      │
                      ▼
               CLEANING UP
               1. cache.CleanupRun(runID)    — release cache refs
               2. grpc.UnregisterRun(runID)  — close Messages chan, remove RunChannel
               3. delete from activeRuns      — remove from live tracking
               4. append to history[]         — persist as RunRecord
```

Authoritative implementation: `Manager.StartRun` / `Manager.waitForExit` in `internal/runner/manager.go`, `GRPCServer.RegisterRun` in `internal/runner/grpc_server.go`.

### Safeguards

| Safeguard | Detail |
| --- | --- |
| **Typed `RunStatus`** | Go: `RunStatus` string type with `StatusRunning`/`StatusCompleted`/`StatusFailed` constants (`manager.go`). TypeScript: `RunStatus` union type (`useScripts.ts`). Python: `STATUS_COMPLETED`/`STATUS_FAILED` constants (`runner.py`). Proto wire format remains `string` — typed boundary at gRPC handler via `RunStatus(m.Status.State)`. |
| **Transition guards** | Go Manager: `IsTerminal()` prevents overwriting a terminal status (`manager.go`). Frontend: `onStatus` and `onError` handlers skip updates when status is already `"completed"` or `"failed"` (`useScripts.ts`). |
| **Atomic `gotError`** | `RunChannel.gotError` uses `atomic.Bool` for lock-free, race-free access across goroutines (`grpc_server.go`). |

### Remaining Architectural Notes

| Item | Detail |
| --- | --- |
| **Hidden sub-states** | `RunChannel` has its own state machine (`stream=nil` → `stream=set` → `closed=true`) invisible to the `Status` field. A run can be `StatusRunning` while Python hasn't connected or the gRPC stream is dead. This is by design — these are internal to gRPC plumbing and guarded by `closedMu`/`connectOnce`. |
| **Multiple status sources** | Go Manager (process exit), Python (gRPC `StatusMsg`), and React (error events) can all set status. Transition guards prevent terminal→non-terminal overwrites, but the Go Manager is the authoritative final-status source via `waitForExit`. |
| **Implicit cleanup ordering** | Cache cleanup → gRPC unregister → map removal → history append must happen in sequence (`manager.go`). Ordering is enforced by code sequencing in the `waitForExit` goroutine. |

### Future Work

- Introduce a `RunPhase` type capturing the full composite state (registered/starting/connected/executing/closing/done) if sub-state visibility becomes needed
- Centralize all transitions in Manager with a validated `transition(runID, from, to)` method for full formalization
- Consider promoting `RunStatus` to a proto enum if the typed boundary at the gRPC handler proves insufficient

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

- `ScriptService`: real registry with temp script dirs, verify `ListScripts` returns scripts in deterministic order
- `RunnerService`: mock the manager interface, verify `StartRun` returns runID, `CancelRun` propagates
- `LogService`: real logger + ring buffer, call `LogError`, verify `GetLogs()` (no filter — backend returns all, client filters) returns the entry with correct fields

**Python gRPC client** — covered by the Go integration tests in `tests/integration/` (which spawn real Python subprocesses against a real Go gRPC server). The Python helper itself is exercised through unit tests in `scripts/_lib/tests/test_runner.py`.

### Integration Tests

Full end-to-end: Go spawns Python subprocess, gRPC connects, typed messages flow through channels.

Located in `tests/integration/`, gated with `//go:build integration` build tag.

Coverage spans the contracts that span both the Go and Python sides:

- **Happy-path message flow** — manager spawns Python, typed messages flow output → progress → status, run reaches `completed`.
- **Parallel isolation** — concurrent runs get independent streams; no cross-talk in messages, cache refs, or status.
- **Plugin override** — same-ID plugin replaces the builtin at execution time, not just in the registry list.
- **Crash & cancel** — Python exception, Go-side cancel, and SIGTERM-ignoring child each end at `failed` with the right traceback / stderr captured.
- **Trust order** — the Manager treats Python's `complete()`/`fail()` and the OS exit code as independent inputs and resolves disagreement deterministically (`tests/integration/trust_order_test.go`).
- **Shared cache** — `cache_set` / `cache_get` round-trip arbitrary picklables; producer crash releases blocks; pickling errors surface as structured failures.
- **Stress-extra** — progress bursts, huge single messages, concurrent DB writes, and run-history scaling.

Fixture scripts live in `tests/integration/testdata/` and are added per scenario rather than enumerated in the doc.

### Test Commands

```bash
# Go unit + service tests
make test-go                                     # go test -race ./internal/...

# Go end-to-end (requires Python)
make test-integration                            # -tags=integration

# Stress tests (high-volume / concurrency, longer timeout)
make test-stress                                 # -tags=stress

# Python tests
make test-python                                 # uv run pytest scripts/_lib/tests/

# Frontend tests
make test-frontend                               # cd frontend && npx vitest run

# Default suite (go + python + frontend + integration; stress is opt-in)
make test
```

## Tech Stack

| Layer | Choice | Reason |
| --- | --- | --- |
| Desktop framework | Wails v3 | Single binary, native webview, Go bindings |
| Go <-> Frontend | Wails bindings + events | Zero boilerplate, typed |
| Go <-> Python | gRPC bidirectional streaming | Typed protobuf contract, parallel |
| Schema | Protobuf with `oneof` | Cross-language validation, compile-time safe |
| Frontend | React 19 + TypeScript + Vite + Tailwind | Wails v3 supported |
| Go concurrency | Typed channels + type switches | Idiomatic, compile-time safe |
| Python dev tooling | `uv` | Downloads Python, manages venv + deps, no system Python needed |
| Python runtime (dist) | python-build-standalone | Bundled portable Python for end users |
| Python deps | grpcio + protobuf (shared base) | `uv sync` in dev, pre-installed at build time for dist |
| Logging | `log/slog` (stdlib) + `lumberjack` | Structured, rotating file output |
| Plugin system | OS app data dir + registry overlay | Override/extend scripts post-build |
| Go testing | `testing` (stdlib) + `testify/assert` | Standard + readable assertions |
| Python testing | `pytest` | De facto Python test runner |
| Frontend testing | `vitest` + React Testing Library | Fast, Vite-native, component testing |

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
go test -race ./internal/...                                     # Go unit + service
go test -race ./tests/integration/ -tags=integration             # Go integration
go test -race ./tests/stress/... -tags=stress -timeout=300s      # Go stress (opt-in)
uv run pytest scripts/_lib/tests/                                # Python
cd frontend && npx vitest run                                    # Frontend

# Lint all layers
make lint                                        # All linters
go vet ./...                                     # Go
uv run ruff check scripts/ tests/                # Python style
uv run mypy scripts/_lib/runner.py               # Python types
cd frontend && npx tsc --noEmit                  # TypeScript

# Bundle Python runtime for distribution
uv run python build/bundle_python.py

# Production build
wails3 build
```

# Portable Python Runtime

This directory holds the bundled portable Python interpreter for distribution.
End users do not need Python, `uv`, or `pip` installed — everything is self-contained.

## How it works

- **Source**: `uv python install` downloads a compact Python from [python-build-standalone](https://github.com/indygreg/python-build-standalone) (~50MB after cleanup, vs ~134MB for the raw tarball).
- **Shared deps**: `grpcio`, `protobuf`, and `numpy` are pre-installed via `uv pip install` at build time.
- **Location**: The `python/` directory sits next to the application executable.

## Building

From the project root (requires `uv`):

```bash
uv run python build/bundle_python.py
```

This uses `uv python install` to download a portable Python, installs deps from `requirements.txt`, and removes unnecessary directories (tcl, test, idlelib, pip, etc.).

## Directory structure (after bundling)

```text
python/
├── requirements.txt    # Shared deps (grpcio, protobuf, numpy)
├── README.md           # This file
└── python/             # Portable Python install (created by bundle script)
    ├── bin/python3      # Linux
    ├── python.exe       # Windows
    └── lib/site-packages/
```

## How Go finds Python

Go checks these paths in order:

1. `.venv/Scripts/python.exe` (Windows) or `.venv/bin/python3` (Linux) — dev mode
2. `python/python.exe` (Windows) or `python/bin/python3` (Linux) relative to executable — distribution mode

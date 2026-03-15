"""Bundle a portable Python interpreter using uv and install shared dependencies.

Creates a self-contained python/python/ directory that ships alongside the
executable. Uses `uv python install` for a smaller footprint (~70MB vs ~134MB
from raw python-build-standalone tarballs).

Usage: uv run python build/bundle_python.py
"""

from __future__ import annotations

import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

PYTHON_VERSION = "3.13"
PROJECT_DIR = Path(__file__).resolve().parent.parent
PYTHON_DIR = PROJECT_DIR / "python"
INSTALL_DIR = PYTHON_DIR / "python"  # final location: python/python/python.exe
REQUIREMENTS = PYTHON_DIR / "requirements.txt"

# Directories safe to remove from the bundled Python — not needed at runtime
CLEANUP_DIRS = [
    "tcl",
    "include",
    "libs",
    "Lib/test",
    "Lib/tests",
    "Lib/idlelib",
    "Lib/tkinter",
    "Lib/turtledemo",
    "Lib/ensurepip",
    "Lib/lib2to3",
    "share",
]


def find_uv() -> str:
    """Find the uv binary."""
    uv = shutil.which("uv")
    if uv is None:
        print("ERROR: uv not found. Install: https://docs.astral.sh/uv/", file=sys.stderr)
        sys.exit(1)
    return uv


def rmtree_robust(path: Path) -> None:
    """Remove a directory tree, handling Windows file locks."""
    def on_error(_func: object, fpath: str, _exc: object) -> None:
        os.chmod(fpath, 0o777)
        os.remove(fpath)

    try:
        shutil.rmtree(path, onexc=on_error)
    except PermissionError:
        print(f"ERROR: Could not remove {path}. Close programs using it.", file=sys.stderr)
        sys.exit(1)


def main() -> None:
    uv = find_uv()
    system = platform.system().lower()

    print(f"==> Bundling portable Python {PYTHON_VERSION} using uv")

    # Use a temp dir for uv install, then flatten into INSTALL_DIR
    uv_staging = PYTHON_DIR / "_uv_staging"

    # Clean up previous
    for d in [INSTALL_DIR, uv_staging]:
        if d.exists():
            print(f"==> Removing {d}")
            rmtree_robust(d)

    # Install Python via uv into staging dir
    print(f"==> uv python install {PYTHON_VERSION}")
    subprocess.check_call([
        uv, "python", "install", PYTHON_VERSION,
        "--install-dir", str(uv_staging),
        "--no-bin",  # don't try to symlink into ~/.local/bin
    ])

    # uv creates: staging/cpython-3.12.x-<platform>/python.exe
    # Find the versioned subdirectory and move it to INSTALL_DIR
    subdirs = [d for d in uv_staging.iterdir() if d.is_dir() and d.name.startswith("cpython-")]
    if not subdirs:
        print(f"ERROR: No cpython-* directory found in {uv_staging}", file=sys.stderr)
        sys.exit(1)

    # Pick the most specific (non-symlink) directory
    src = max(subdirs, key=lambda d: len(d.name))
    print(f"==> Moving {src.name} -> {INSTALL_DIR}")
    src.rename(INSTALL_DIR)
    rmtree_robust(uv_staging)

    # Find the python binary
    if system == "windows":
        python_bin = INSTALL_DIR / "python.exe"
    else:
        python_bin = INSTALL_DIR / "bin" / "python3"

    if not python_bin.exists():
        print(f"ERROR: Python binary not found at {python_bin}", file=sys.stderr)
        sys.exit(1)

    print(f"    Binary: {python_bin}")

    # Install shared dependencies
    print(f"==> Installing dependencies from {REQUIREMENTS}")
    subprocess.check_call([
        uv, "pip", "install",
        "--python", str(python_bin),
        "--break-system-packages",
        "-r", str(REQUIREMENTS),
    ])

    # Post-install cleanup — remove unnecessary directories
    cleaned = 0
    for dirname in CLEANUP_DIRS:
        target = INSTALL_DIR / dirname
        if target.exists():
            shutil.rmtree(target, ignore_errors=True)
            cleaned += 1

    # Remove __pycache__ directories
    for pycache in INSTALL_DIR.rglob("__pycache__"):
        shutil.rmtree(pycache, ignore_errors=True)

    # Remove pip (no longer needed after deps are installed)
    pip_dir = INSTALL_DIR / "Lib" / "site-packages" / "pip"
    if pip_dir.exists():
        shutil.rmtree(pip_dir, ignore_errors=True)
        cleaned += 1
    for dist in (INSTALL_DIR / "Lib" / "site-packages").glob("pip-*.dist-info"):
        shutil.rmtree(dist, ignore_errors=True)

    if cleaned:
        print(f"==> Cleaned {cleaned} unnecessary directories")

    # Report final size
    total = sum(f.stat().st_size for f in INSTALL_DIR.rglob("*") if f.is_file())
    print(f"==> Done. Portable Python at: {INSTALL_DIR} ({total / 1024 / 1024:.0f} MB)")


if __name__ == "__main__":
    main()

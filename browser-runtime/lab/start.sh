#!/usr/bin/env bash
set -euo pipefail
LAB_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$LAB_SCRIPT_DIR/lab.py" start "$@"

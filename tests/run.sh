#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON_BIN="${PYTHON_BIN:-python3}"

"$PYTHON_BIN" -m unittest discover -s "$ROOT/tests" -p 'test_*.py' -v
bash "$ROOT/tests/test-bootstrap-voice-trace-secrets.sh"

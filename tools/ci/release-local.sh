#!/usr/bin/env bash
# Real local end-to-end validation; remote model calls are disabled for the load test.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"
DM_LOCAL_LLM_MODE=off make local
python3 -m venv .cache/audit-venv
.cache/audit-venv/bin/python -m pip install --quiet --upgrade 'pip>=26.2,<27'
.cache/audit-venv/bin/python -m pip install --quiet -e sdks/python 'PyYAML>=6,<7'
.cache/audit-venv/bin/python tools/load/check.py
.cache/audit-venv/bin/python tools/demos/test_verify_results.py testdata/golden/vantara-twin/expected.yaml scratch/audit/load.snapshot.json
DM_E2E_CHANNEL="${DM_E2E_CHANNEL:-chrome}" pnpm --dir apps/web exec playwright test -c playwright.local.config.ts

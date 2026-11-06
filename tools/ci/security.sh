#!/usr/bin/env bash
# Local dependency scans. Missing scanners or a nonzero scanner result fail the gate.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"
for cmd in pnpm cargo govulncheck gitleaks trivy; do
  command -v "$cmd" >/dev/null || { echo "Scanner ausente: $cmd (instale o scanner no host; veja docs/auditoria-local.md)" >&2; exit 1; }
done
pnpm audit --audit-level low
(cd tools/codegen && pnpm audit --audit-level low)
for module in services/api tools/installer sdks/go; do
  (cd "$module" && govulncheck ./...)
done
(cd services/harness && cargo audit)
(cd sdks/rust && cargo audit)
for lock in apps/android/app/gradle.lockfile apps/android/core/gradle.lockfile; do
  trivy fs --scanners vuln --include-dev-deps --exit-code 1 --no-progress "$lock"
done
# Audit the SDK with a patched pip in an isolated environment.
python3 -m venv .cache/audit-venv
.cache/audit-venv/bin/python -m pip install --quiet --upgrade 'pip>=26.2,<27'
.cache/audit-venv/bin/python -m pip install --quiet -e sdks/python -r tools/ci/requirements.txt
.cache/audit-venv/bin/pip-audit
# Full Git history, redacted. Uncommitted changes are also checked by the commit hook.
gitleaks git --log-opts=--all --redact --no-banner

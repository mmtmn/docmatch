#!/usr/bin/env bash
# Runner local de CI: roda o alvo `ci` (ou `ci-full`) de cada subsistema e imprime um resumo
# para colar no PR. Uso: tools/ci/run.sh [ci|ci-full]
set -uo pipefail
root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$root"
target="${1:-ci}"

subsystems=(
  packages/contracts
  services/api
  services/harness
  apps/web
  apps/extensions
  apps/admin
  infra
  sdks/python
  sdks/typescript
  sdks/go
  sdks/rust
  apps/android
  apps/ios
  tools/installer
  tools/terminal
)

declare -a names statuses times
overall=0
start_all=$(date +%s)

run_step() {
  local name="$1"; shift
  local t0=$(date +%s)
  if "$@" > "/tmp/docmatch-ci-${name//\//_}.log" 2>&1; then
    statuses+=("ok"); rc=0
  else
    statuses+=("FALHOU"); rc=1; overall=1
    echo "--- $name falhou; últimas linhas:" >&2
    tail -n 40 "/tmp/docmatch-ci-${name//\//_}.log" >&2
  fi
  names+=("$name"); times+=("$(( $(date +%s) - t0 ))s")
  return 0
}

run_step "lint/hardcode" tools/lint/hardcode.sh
run_step "lint/commits" tools/ci/check-commits.sh
run_step "lint/links" python3 -I docs/scripts/check-links.py

# Escopo: só roda os subsistemas tocados desde a base (CI_BASE_REF, senão origin/main). Mudanças em
# arquivos compartilhados (raiz, tools/, config/, packages/, .github/) rodam tudo; `ci-full` e
# DM_CI_ALL=1 também. Sem base resolvível, roda tudo.
base="${CI_BASE_REF:-origin/main}"
all="${DM_CI_ALL:-0}"
[[ "$target" == "ci-full" ]] && all=1
changed=""
if [[ "$all" != "1" ]]; then
  if mb="$(git merge-base "$base" HEAD 2>/dev/null)"; then
    changed="$( { git diff --name-only "$mb" HEAD; git status --porcelain --untracked-files=all | cut -c4- | sed 's/^.* -> //'; } 2>/dev/null )"
    if grep -qE '^(Makefile|package\.json|pnpm-workspace\.yaml|pnpm-lock\.yaml|\.nvmrc|tools/|config/|packages/|\.github/)' <<<"$changed"; then
      all=1
    fi
  else
    all=1
  fi
fi

for s in "${subsystems[@]}"; do
  [[ -f "$s/Makefile" ]] || continue
  # Skip only a subsystem without the target. A Makefile that does not parse still declares it,
  # so the step runs and fails loudly instead of disappearing from the summary.
  if ! make -C "$s" -n "$target" >/dev/null 2>&1 && ! grep -qE "^${target}:" "$s/Makefile"; then
    continue
  fi
  if [[ "$all" == "1" ]] || grep -q "^$s/" <<<"$changed"; then
    run_step "$s" make -C "$s" "$target"
  else
    names+=("$s"); statuses+=("pulado (sem mudanças)"); times+=("0s")
  fi
done

if [[ "$target" == "ci-full" ]]; then
  run_step "security" bash tools/ci/security.sh
  run_step "local-flow-load" bash tools/ci/release-local.sh
fi

echo
echo "## make $target — resumo ($(( $(date +%s) - start_all ))s)"
echo
echo "| etapa | resultado | tempo |"
echo "| --- | --- | --- |"
for i in "${!names[@]}"; do
  echo "| ${names[$i]} | ${statuses[$i]} | ${times[$i]} |"
done
echo
if [[ $overall -ne 0 ]]; then
  echo "resultado: FALHOU" ; exit 1
fi
echo "resultado: ok"

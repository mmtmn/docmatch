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
  apps/admin
  infra
  sdks/python
  sdks/typescript
  sdks/go
  sdks/rust
  apps/android
  apps/ios
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

for s in "${subsystems[@]}"; do
  if [[ -f "$s/Makefile" ]] && make -C "$s" -n "$target" >/dev/null 2>&1; then
    run_step "$s" make -C "$s" "$target"
  fi
done

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

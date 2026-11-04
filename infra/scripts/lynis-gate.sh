#!/usr/bin/env bash
# Runs a Lynis system audit and fails when the hardening index is below the threshold. Used as
# the last gate of the Packer build (the AMI is not produced if it fails) and usable on a running
# instance through SSM.
#
# Uso: sudo lynis-gate.sh            (LYNIS_MIN_SCORE, padrão 90; LYNIS_REPORT, padrão /var/log/lynis-report.dat)
set -euo pipefail

min_score="${LYNIS_MIN_SCORE:-90}"
report="${LYNIS_REPORT:-/var/log/lynis-report.dat}"
log="${LYNIS_LOG:-/var/log/lynis.log}"

log() { printf 'lynis-gate: %s\n' "$*" >&2; }
die() { log "erro: $*"; exit 1; }

[[ $EUID -eq 0 ]] || die "precisa rodar como root"
command -v lynis >/dev/null 2>&1 || die "lynis não instalado"
[[ "$min_score" =~ ^[0-9]+$ ]] || die "LYNIS_MIN_SCORE inválido: $min_score"

log "executando lynis audit system --quick (mínimo exigido: $min_score)"
# Lynis returns non-zero when it finds warnings; the verdict here is the hardening index only.
lynis audit system --quick --quiet --no-colors --report-file "$report" --log-file "$log" >/dev/null 2>&1 || true

[[ -r "$report" ]] || die "relatório não gerado em $report"
index="$(grep -E '^hardening_index=' "$report" | tail -1 | cut -d= -f2 | tr -d '[:space:]')"
[[ "$index" =~ ^[0-9]+$ ]] || die "índice de endurecimento não encontrado em $report"

warnings="$(grep -cE '^warning\[\]=' "$report" || true)"
suggestions="$(grep -cE '^suggestion\[\]=' "$report" || true)"
log "índice de endurecimento: $index (avisos: $warnings, sugestões: $suggestions)"

if (( index < min_score )); then
  log "abaixo do mínimo ($min_score). Avisos e sugestões:"
  grep -E '^(warning|suggestion)\[\]=' "$report" | sed -E 's/^([a-z]+)\[\]=/  \1: /' | cut -c1-200 >&2
  exit 1
fi
log "aprovado"

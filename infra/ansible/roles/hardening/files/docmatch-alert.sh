#!/usr/bin/env bash
# docmatch-alert: sends an operational alert by e-mail through the Resend API (via the egress
# proxy). Used by Falco (--falco reads one JSON event per line), cron (sendmail shim), timers and
# scripts. Without RESEND_API_KEY it only writes to the journal, never fails the caller.
#
# Uso: docmatch-alert --subject "texto" --stdin < corpo
#      docmatch-alert --falco < evento-json
#      echo corpo | docmatch-alert --subject "texto" --stdin
set -uo pipefail

env_file="/run/docmatch/env"
subject=""
mode="text"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --subject) subject="$2"; shift ;;
    --stdin) mode="text" ;;
    --falco) mode="falco" ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "docmatch-alert: argumento desconhecido: $1" >&2; exit 2 ;;
  esac
  shift
done

body="$(cat)"
if [[ "$mode" == "falco" ]]; then
  # One JSON event per line; the subject carries priority and rule.
  priority="$(printf '%s' "$body" | jq -r '.priority // "Warning"' 2>/dev/null | head -1)"
  rule="$(printf '%s' "$body" | jq -r '.rule // "regra desconhecida"' 2>/dev/null | head -1)"
  subject="Falco ${priority}: ${rule}"
  body="$(printf '%s' "$body" | jq -r '.output // .' 2>/dev/null || printf '%s' "$body")"
fi
[[ -n "$subject" ]] || subject="docmatch: alerta em $(hostname)"

logger -t docmatch-alert -p daemon.warning -- "$subject"

if [[ ! -r "$env_file" ]]; then
  logger -t docmatch-alert -p daemon.notice -- "ambiente não renderizado; alerta só no journal"
  exit 0
fi
get() { sed -nE "s/^$1=\"(.*)\"$/\1/p" "$env_file" | sed -e 's/\\\\/\\/g' -e 's/\\"/"/g'; }
api_key="$(get RESEND_API_KEY)"
to="$(get DM_ALERT_EMAIL)"
from="$(get DM_EMAIL_FROM)"
if [[ -z "$api_key" || "$api_key" == "__UNSET__" || -z "$to" ]]; then
  logger -t docmatch-alert -p daemon.notice -- "RESEND_API_KEY ou DM_ALERT_EMAIL ausentes; alerta só no journal"
  exit 0
fi
[[ -n "$from" ]] || from="docmatch <alertas@${HOSTNAME:-localhost}>"

payload="$(jq -cn --arg from "$from" --arg to "$to" --arg subject "[docmatch $(hostname)] $subject" --arg text "$body" \
  '{from: $from, to: [$to], subject: $subject, text: $text}')"
if ! curl -fsS --max-time 20 --retry 2 -o /dev/null \
  -H "Authorization: Bearer ${api_key}" -H "Content-Type: application/json" \
  -X POST --data-binary "$payload" "https://api.resend.com/emails"; then
  logger -t docmatch-alert -p daemon.err -- "falha ao enviar o alerta pelo Resend"
fi
exit 0

#!/usr/bin/env bash
# sendmail shim: there is no MTA on the box. cron, AIDE, rkhunter, fail2ban and unattended-upgrades
# hand their mail here; the headers are parsed for a subject and the body goes to docmatch-alert.
set -uo pipefail
subject=""
body=""
in_headers=1
while IFS= read -r line || [[ -n "$line" ]]; do
  if [[ $in_headers -eq 1 ]]; then
    if [[ -z "$line" ]]; then in_headers=0; continue; fi
    case "$line" in
      Subject:*) subject="${line#Subject:}"; subject="${subject# }" ;;
    esac
    continue
  fi
  body+="$line"$'\n'
done
[[ -n "$subject" ]] || subject="mensagem do sistema para root"
printf '%s' "$body" | /usr/local/sbin/docmatch-alert --subject "$subject" --stdin
exit 0

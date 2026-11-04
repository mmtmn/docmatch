#!/usr/bin/env bash
# Lint de hardcode: falha se encontrar URLs, e-mails, IPs públicos, ARNs ou strings com cara de
# chave fora da lista de permissões. Uso: tools/lint/hardcode.sh [arquivos...]  (sem args: repo todo)
set -euo pipefail
root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$root"

allow_file="tools/lint/hardcode-allow.txt"
allow_paths="$(grep -E '^path:' "$allow_file" | sed 's/^path://' | paste -sd'|' -)"
allow_literals="$(grep -E '^literal:' "$allow_file" | sed 's/^literal://' | paste -sd'|' -)"

if [[ $# -gt 0 ]]; then
  files="$(printf '%s\n' "$@")"
else
  files="$(git ls-files)"
fi

# Só arquivos de texto existentes, fora dos caminhos permitidos.
files="$(echo "$files" | while read -r f; do
  [[ -f "$f" ]] || continue
  if [[ -n "$allow_paths" ]] && echo "$f" | grep -qE "^($allow_paths)"; then continue; fi
  if file --mime "$f" | grep -q 'charset=binary'; then continue; fi
  echo "$f"
done)"
[[ -z "$files" ]] && exit 0

patterns=(
  'https?://[A-Za-z0-9./_%?=&#:-]+'
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z0-9.-]+\.[a-z]{2,}'
  '\b(?!(10|127|0)\.)(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b'
  'arn:aws:[a-z0-9-]+:[a-z0-9-]*:[0-9]{12}:'
  'AKIA[0-9A-Z]{16}'
  '\b(sk|rk)_(live|test)_[A-Za-z0-9]{10,}'
  '\bwhsec_[A-Za-z0-9]{10,}'
  '\bsk-or-v1-[A-Za-z0-9]{10,}'
  '\bre_[A-Za-z0-9]{20,}'
  '\bgh[pousr]_[A-Za-z0-9]{30,}'
  '-----BEGIN [A-Z ]*PRIVATE KEY-----'
)
pattern="$(IFS='|'; echo "${patterns[*]}")"

status=0
while read -r f; do
  hits="$(grep -nP "$pattern" "$f" 2>/dev/null || true)"
  [[ -z "$hits" ]] && continue
  if [[ -n "$allow_literals" ]]; then
    hits="$(echo "$hits" | grep -vP "$allow_literals" || true)"
  fi
  [[ -z "$hits" ]] && continue
  echo "hardcode: $f" >&2
  echo "$hits" | sed 's/^/  /' >&2
  status=1
done <<< "$files"

if [[ $status -ne 0 ]]; then
  echo "hardcode: mova o valor para variável de ambiente ou config, ou registre a exceção em $allow_file" >&2
fi
exit $status

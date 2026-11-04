#!/usr/bin/env bash
# Lê um arquivo .env e imprime `export CHAVE='valor'` para cada variável, com
# aspas seguras. Diferente de `source`, aceita valores com espaços, "<" e ">"
# (ex.: DM_EMAIL_FROM=docmatch <no-reply@example.com>) e ignora comentários
# em linha ("CHAVE=valor   # comentário").
#
# Uso: eval "$(tools/dev/env.sh .env)"
set -euo pipefail

file="${1:-.env}"
if [[ ! -f "$file" ]]; then
  echo "env.sh: arquivo $file não existe (cp .env.example .env)" >&2
  exit 1
fi

while IFS= read -r line || [[ -n "$line" ]]; do
  line="${line#"${line%%[![:space:]]*}"}"          # trim à esquerda
  [[ -z "$line" || "$line" == \#* ]] && continue
  line="${line#export }"
  key="${line%%=*}"
  value="${line#*=}"
  [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
  if [[ "$value" == \"*\" ]]; then
    value="${value#\"}"; value="${value%%\"*}"
  elif [[ "$value" == \'*\' ]]; then
    value="${value#\'}"; value="${value%%\'*}"
  else
    # Remove comentário em linha (precedido de espaço) e espaços nas pontas.
    value="$(printf '%s' "$value" | sed -E 's/[[:space:]]+#.*$//; s/[[:space:]]+$//; s/^[[:space:]]+//')"
  fi
  printf "export %s='%s'\n" "$key" "${value//\'/\'\\\'\'}"
done < "$file"

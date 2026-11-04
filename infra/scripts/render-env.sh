#!/usr/bin/env bash
# Renders /run/docmatch/env (tmpfs, mode 0400, owner docmatch) from SSM Parameter Store using
# GetParametersByPath on the instance's path (/docmatch/<env>). Runs as root from
# docmatch-env.service before any application service starts. Also writes the small subset Caddy
# needs (origin secret, domain) as /run/docmatch/env.caddy, owned by caddy.
#
# The output is a systemd EnvironmentFile: KEY="value" with backslashes and double quotes escaped.
# systemd does not translate C escapes, so real newlines in a value (PEM keys) are stored as the
# two characters \n, which is the format .env.example documents for APPLE_PRIVATE_KEY.
#
# Uso: render-env.sh [--path /docmatch/prod] [--out /run/docmatch/env] [--owner docmatch]
#                    [--allow-placeholders]
# Lê DM_SSM_PATH e AWS_REGION de /etc/docmatch/instance.env quando não informados.
set -euo pipefail
umask 077

ssm_path="${DM_SSM_PATH:-}"
out="/run/docmatch/env"
owner="docmatch"
placeholder="${DM_PLACEHOLDER:-__UNSET__}"
allow_placeholders=0
instance_env="/etc/docmatch/instance.env"
# Subsets for services that run as other users: <user>:<file>:<KEY,KEY,...>. A subset is only
# written when the user exists on the box.
subsets=(
  "caddy:/run/docmatch/env.caddy:DM_ORIGIN_SECRET,DM_ORIGIN_SECRET_HEADER,DM_DOMAIN,DM_PUBLIC_BASE_URL,DM_ROUTE53_ZONE_ID,DM_ALERT_EMAIL,AWS_REGION"
  "postgres:/run/docmatch/env.postgres:DM_S3_BACKUPS_BUCKET,DM_BACKUPS_PGBACKREST_KEY,AWS_REGION"
)

log() { printf 'render-env: %s\n' "$*" >&2; }
die() { log "erro: $*"; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --path) ssm_path="$2"; shift ;;
    --out) out="$2"; shift ;;
    --owner) owner="$2"; shift ;;
    --subset) subsets+=("$2"); shift ;;
    --allow-placeholders) allow_placeholders=1 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) die "argumento desconhecido: $1" ;;
  esac
  shift
done

if [[ -z "$ssm_path" || -z "${AWS_REGION:-}" ]] && [[ -r "$instance_env" ]]; then
  # shellcheck disable=SC1090
  source "$instance_env"
  ssm_path="${ssm_path:-${DM_SSM_PATH:-}}"
fi
[[ -n "$ssm_path" ]] || die "caminho SSM indefinido (--path ou DM_SSM_PATH em $instance_env)"
[[ -n "${AWS_REGION:-}" ]] || die "AWS_REGION indefinida"
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION" AWS_PAGER=""
[[ $EUID -eq 0 ]] || die "precisa rodar como root para definir dono e modo dos arquivos"
for c in aws jq; do command -v "$c" >/dev/null 2>&1 || die "comando necessário não encontrado: $c"; done
id "$owner" >/dev/null 2>&1 || die "usuário inexistente: $owner"

run_dir="$(dirname "$out")"
install -d -m 0750 -o root -g "$owner" "$run_dir"

# One JSON document with every parameter under the path (the CLI paginates on its own).
params="$(aws ssm get-parameters-by-path --path "$ssm_path" --recursive --with-decryption --output json)"
count="$(jq -r '.Parameters | length' <<<"$params")"
[[ "$count" -gt 0 ]] || die "nenhum parâmetro em $ssm_path (terraform apply ainda não rodou?)"

unset_keys="$(jq -r --arg p "$placeholder" '.Parameters[] | select(.Value == $p) | .Name | split("/") | last' <<<"$params" | sort)"
if [[ -n "$unset_keys" ]]; then
  log "segredos ainda com o valor de espaço reservado do Terraform:"
  printf '  %s\n' "$unset_keys" >&2
  [[ $allow_placeholders -eq 1 ]] || die "preencha-os com aws ssm put-parameter --overwrite (ver infra/README.md)"
fi

# KEY="value": real newlines become the two characters \n, then \ and " are escaped.
render() {
  jq -r "$1"'
    | (.Name | split("/") | last) as $k
    | (.Value | gsub("\n"; "\\n") | gsub("\\\\"; "\\\\\\\\") | gsub("\""; "\\\"")) as $v
    | "\($k)=\"\($v)\""' <<<"$params"
}

write_atomic() { # write_atomic <path> <owner> <content>
  local path="$1" who="$2" content="$3" tmp
  tmp="$(mktemp "$(dirname "$path")/.env.XXXXXX")"
  printf '%s\n' "$content" > "$tmp"
  chmod 0400 "$tmp"
  chown "$who:$who" "$tmp"
  mv -f "$tmp" "$path"
}

write_atomic "$out" "$owner" "$(render '.Parameters[]')"
log "escrito $out ($count variáveis, dono $owner, modo 0400)"

for spec in "${subsets[@]}"; do
  IFS=: read -r sub_user sub_file sub_keys <<<"$spec"
  [[ -n "$sub_user" && -n "$sub_file" && -n "$sub_keys" ]] || die "subset inválido: $spec"
  id "$sub_user" >/dev/null 2>&1 || continue
  keys_json="$(printf '%s\n' "$sub_keys" | tr ',' '\n' | jq -R . | jq -sc .)"
  write_atomic "$sub_file" "$sub_user" "$(render ".Parameters[] | select((.Name | split(\"/\") | last) as \$n | ${keys_json} | index(\$n))")"
  log "escrito $sub_file (dono $sub_user)"
done

#!/usr/bin/env bash
# Renders /run/docmatch/env (tmpfs, mode 0400, owner docmatch) from SSM Parameter Store using
# GetParametersByPath on the instance's path (/docmatch/<env>). Runs as root from
# docmatch-env.service before any application service starts. Also writes the small subset Caddy
# needs (origin secret, domain) as /run/docmatch/env.caddy, owned by caddy.
#
# The output is a systemd EnvironmentFile: KEY="value". Inside double quotes systemd unescapes
# only \\ and \" (any other backslash is kept as is) and a raw newline ends the value, so a real
# newline in a value (PEM keys) becomes the two characters \n, which is the format .env.example
# documents for APPLE_PRIVATE_KEY. See render() for the exact order of the escapes.
#
# With DM_SSM_NAMES (or --names A,B,C) only those parameters under the path are read, with
# GetParameters: the burst workers' role may read exactly that list (no DATABASE_URL, no other
# secret), so a GetParametersByPath on the whole path would be refused anyway. DM_ENV_EXCLUDE
# (A,B) keeps those keys out of the main file: on a burst worker the pool secret and the internal
# endpoint go only to the burst user's subset, never to the harness's environment.
#
# Uso: render-env.sh [--path /docmatch/prod] [--names A,B] [--out /run/docmatch/env]
#                    [--owner docmatch] [--allow-placeholders]
# Lê DM_SSM_PATH, DM_SSM_NAMES e AWS_REGION de /etc/docmatch/instance.env quando não informados.
set -euo pipefail
umask 077

log() { printf 'render-env: %s\n' "$*" >&2; }
die() { log "erro: $*"; exit 1; }

# KEY="value" lines for every parameter selected by the jq filter in $1, read from $params.
# Escapes, in this order: a real newline becomes the two characters \n; then every backslash
# (including the one just written) is doubled; then double quotes are escaped. systemd turns \\
# back into one backslash and leaves \n alone, so the service receives single backslashes, real
# quotes and \n as two characters. Doubling the backslash twice (an earlier bug) delivered \\n.
# SSM refuses empty values, so a parameter holding the sentinel $DM_EMPTY_VALUE (__EMPTY__) is
# rendered as an empty value: the operator's explicit "not configured yet" for an optional
# secret (a provider key not issued yet), unlike the placeholder, which blocks the boot.
render() {
  jq -r --arg empty "${DM_EMPTY_VALUE:-__EMPTY__}" "$1"'
    | (.Name | split("/") | last) as $k
    | (if .Value == $empty then "" else .Value end
       | gsub("\n"; "\\n") | gsub("\\\\"; "\\\\") | gsub("\""; "\\\"")) as $v
    | "\($k)=\"\($v)\""' <<<"$params"
}

# fetch_params <path> [names]: one JSON document {"Parameters": [...]} with every parameter under
# the path (GetParametersByPath, paginated by the CLI) or only the listed names (GetParameters, ten
# per call, the API's limit). A listed name that does not exist fails: a worker without one of its
# parameters must not start half configured.
fetch_params() {
  local path="$1" names="${2:-}" out part missing i n
  local -a list chunk
  if [[ -z "$names" ]]; then
    aws ssm get-parameters-by-path --path "$path" --recursive --with-decryption --output json
    return
  fi
  read -r -a list <<<"${names//,/ }"
  out='{"Parameters":[]}'
  for ((i = 0; i < ${#list[@]}; i += 10)); do
    chunk=()
    for n in "${list[@]:i:10}"; do
      [[ "$n" =~ ^[A-Z][A-Z0-9_]{0,127}$ ]] || die "nome inválido em DM_SSM_NAMES: $n"
      chunk+=("$path/$n")
    done
    part="$(aws ssm get-parameters --names "${chunk[@]}" --with-decryption --output json)"
    missing="$(jq -r '(.InvalidParameters // [])[]' <<<"$part" | paste -sd ' ' -)"
    [[ -z "$missing" ]] || die "parâmetros inexistentes: $missing"
    out="$(jq -c --argjson p "$part" '{Parameters: (.Parameters + ($p.Parameters // []))}' <<<"$out")"
  done
  printf '%s\n' "$out"
}

# unset_keys_of <params json> <placeholder>: the parameters still holding the Terraform
# placeholder, which block the boot. The empty sentinel (__EMPTY__) of an optional secret is not
# one of them: its feature just stays off.
unset_keys_of() {
  jq -r --arg p "$2" '.Parameters[] | select(.Value == $p) | .Name | split("/") | last' <<<"$1" | sort
}

# keys_json <A,B,C>: the list as a JSON array of strings.
keys_json() {
  printf '%s\n' "$1" | tr ',' '\n' | sed '/^$/d' | jq -R . | jq -sc .
}

write_atomic() { # write_atomic <path> <owner> <content>
  local path="$1" who="$2" content="$3" tmp
  tmp="$(mktemp "$(dirname "$path")/.env.XXXXXX")"
  printf '%s\n' "$content" > "$tmp"
  chmod 0400 "$tmp"
  chown "$who:$who" "$tmp"
  mv -f "$tmp" "$path"
}

prepare_runtime_dir() {
  # Each service must traverse the parent to read its own 0400 subset. Directory search
  # permission grants neither listing nor access to another service's environment file.
  install -d -m 0711 -o root -g "$2" "$1"
}

main() {
  local out owner placeholder allow_placeholders instance_env count unset_keys empty_keys ssm_names
  local spec sub_user sub_file sub_keys exclude
  ssm_path="${DM_SSM_PATH:-}"
  ssm_names="${DM_SSM_NAMES:-}"
  exclude="${DM_ENV_EXCLUDE:-}"
  out="/run/docmatch/env"
  owner="docmatch"
  placeholder="${DM_PLACEHOLDER:-__UNSET__}"
  allow_placeholders=0
  instance_env="/etc/docmatch/instance.env"
  # Subsets for services that run as other users: <user>:<file>:<KEY,KEY,...>. A subset is only
  # written when the user exists on the box.
  local -a subsets=(
    "caddy:/run/docmatch/env.caddy:DM_ORIGIN_SECRET,DM_ORIGIN_SECRET_HEADER,DM_VIEWER_IP_HEADER,DM_DOMAIN,DM_PUBLIC_BASE_URL,DM_ROUTE53_ZONE_ID,DM_ALERT_EMAIL,AWS_REGION"
    "postgres:/run/docmatch/env.postgres:DM_S3_BACKUPS_BUCKET,DM_BACKUPS_PGBACKREST_KEY,AWS_REGION"
    "docmatch-burst:/run/docmatch/env.burst:DM_ENV,AWS_REGION,DM_BURST_API_URL,DM_BURST_POOL_SECRET"
  )

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --path) ssm_path="$2"; shift ;;
      --names) ssm_names="$2"; shift ;;
      --out) out="$2"; shift ;;
      --owner) owner="$2"; shift ;;
      --subset) subsets+=("$2"); shift ;;
      --allow-placeholders) allow_placeholders=1 ;;
      -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
      *) die "argumento desconhecido: $1" ;;
    esac
    shift
  done

  if [[ -z "$ssm_path" || -z "${AWS_REGION:-}" ]] && [[ -r "$instance_env" ]]; then
    # shellcheck disable=SC1090
    source "$instance_env"
    ssm_path="${ssm_path:-${DM_SSM_PATH:-}}"
    ssm_names="${ssm_names:-${DM_SSM_NAMES:-}}"
    exclude="${exclude:-${DM_ENV_EXCLUDE:-}}"
  fi
  [[ -n "$ssm_path" ]] || die "caminho SSM indefinido (--path ou DM_SSM_PATH em $instance_env)"
  [[ -n "${AWS_REGION:-}" ]] || die "AWS_REGION indefinida"
  export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION" AWS_PAGER=""
  [[ $EUID -eq 0 ]] || die "precisa rodar como root para definir dono e modo dos arquivos"
  for c in aws jq; do command -v "$c" >/dev/null 2>&1 || die "comando necessário não encontrado: $c"; done
  id "$owner" >/dev/null 2>&1 || die "usuário inexistente: $owner"

  run_dir="$(dirname "$out")"
  prepare_runtime_dir "$run_dir" "$owner"

  # One JSON document with the parameters of this instance (all under the path, or the list).
  params="$(fetch_params "$ssm_path" "$ssm_names")"
  count="$(jq -r '.Parameters | length' <<<"$params")"
  [[ "$count" -gt 0 ]] || die "nenhum parâmetro em $ssm_path (terraform apply ainda não rodou?)"

  unset_keys="$(unset_keys_of "$params" "$placeholder")"
  if [[ -n "$unset_keys" ]]; then
    log "segredos ainda com o valor de espaço reservado do Terraform:"
    printf '  %s\n' "$unset_keys" >&2
    [[ $allow_placeholders -eq 1 ]] || die "preencha-os com aws ssm put-parameter --overwrite (ver infra/README.md)"
  fi

  empty_keys="$(jq -r --arg e "${DM_EMPTY_VALUE:-__EMPTY__}" '.Parameters[] | select(.Value == $e) | .Name | split("/") | last' <<<"$params" | sort | paste -sd ' ' -)"
  [[ -z "$empty_keys" ]] || log "vazios por escolha (${DM_EMPTY_VALUE:-__EMPTY__}): $empty_keys"

  write_atomic "$out" "$owner" "$(render ".Parameters[] | select((.Name | split(\"/\") | last) as \$n | $(keys_json "$exclude") | index(\$n) | not)")"
  log "escrito $out ($count variáveis, dono $owner, modo 0400${exclude:+, sem $exclude})"

  for spec in "${subsets[@]}"; do
    IFS=: read -r sub_user sub_file sub_keys <<<"$spec"
    [[ -n "$sub_user" && -n "$sub_file" && -n "$sub_keys" ]] || die "subset inválido: $spec"
    id "$sub_user" >/dev/null 2>&1 || continue
    write_atomic "$sub_file" "$sub_user" "$(render ".Parameters[] | select((.Name | split(\"/\") | last) as \$n | $(keys_json "$sub_keys") | index(\$n))")"
    log "escrito $sub_file (dono $sub_user)"
  done
}

# Sourced by infra/tests/render-env.test.sh, which exercises render() without AWS.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

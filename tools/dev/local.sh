#!/usr/bin/env bash
# docmatch on this machine, with no provider key and no login step: Postgres (and Mailpit) in
# Docker, migrations, the real harness (dm-server), the API, the worker and the site built for
# localhost. The site signs in by itself through the development login (DM_DEV_LOGIN, which the
# API refuses outside DM_ENV=dev) and that account gets daily local credits, so upload ->
# estimate -> run -> results works end to end. Uploads stay in a local folder served by the API.
#
# Uso: tools/dev/local.sh up | down | status       (make local, make local-down)
# Logs em .cache/local/logs/; o .env é criado a partir do .env.example quando não existe.
set -euo pipefail

root="$(git rev-parse --show-toplevel 2>/dev/null || { cd "$(dirname "$0")/../.." && pwd; })"
cd "$root"
state="$root/.cache/local"
logs="$state/logs"
pids="$state/pids"
bin="$state/bin"
compose=(docker compose -f tools/dev/compose.yaml)

log() { printf '==> %s\n' "$*"; }
die() { printf 'erro: %s\n' "$*" >&2; exit 1; }

# The secrets the API refuses to boot without are generated here: they are local keys, not
# provider keys, and never leave this machine.
ensure_env() {
  if [[ ! -f .env ]]; then
    cp .env.example .env
    log ".env criado a partir do .env.example"
  fi
  chmod 600 .env
  local name value
  for name in DM_SESSION_SECRET DM_QUOTE_HMAC_KEY DM_APIKEY_PEPPER DM_WEBHOOK_SIGNING_KEY; do
    if grep -qE "^${name}=[[:space:]]*(#.*)?$" .env; then
      if [[ "$name" == DM_SESSION_SECRET ]]; then value="$(openssl rand -base64 48 | tr -d '\n')"; else value="$(openssl rand -hex 32)"; fi
      # -i.bak (suffix attached) works with GNU and BSD sed; a bare -i is GNU only (macOS).
      sed -i.bak "s|^${name}=.*|${name}=${value}|" .env && rm -f .env.bak
      log "$name gerado no .env"
    fi
  done
}

port_of() { # port_of <url or host:port>
  local v="${1%/}"
  v="${v##*:}"
  printf '%s' "${v%%/*}"
}

wait_for() { # wait_for <description> <command...>
  local what="$1"
  shift
  for _ in $(seq 1 120); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  die "$what não respondeu em 120 s (veja $logs)"
}

start() { # start <name> <command...>: background process with log and pid file
  local name="$1"
  shift
  stop_one "$name"
  nohup "$@" >"$logs/$name.log" 2>&1 &
  echo $! >"$pids/$name"
}

stop_one() {
  local f="$pids/$1"
  if [[ -f "$f" ]]; then
    local pid
    pid="$(cat "$f")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$f"
  fi
}

up() {
  if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    die "Docker com Compose v2 é necessário"
  fi
  for c in go cargo pnpm openssl curl; do command -v "$c" >/dev/null 2>&1 || die "comando necessário: $c"; done
  mkdir -p "$logs" "$pids" "$bin" "$state/media"
  ensure_env
  eval "$(tools/dev/env.sh .env --defaults)"
  node tools/dev/check-local.mjs || die "configuração local recusada; ajuste .env sem usar o banco de produção"

  # Provider use is explicit: DM_LOCAL_LLM_MODE=local|openrouter (off by default).
  export DM_ENV=dev DM_DEV_LOGIN=1 DM_LLM_MODE="${DM_LOCAL_LLM_MODE:-off}"
  case "$DM_LLM_MODE" in
    off|local|openrouter) ;;
    *) die "DM_LOCAL_LLM_MODE deve ser off, local ou openrouter" ;;
  esac
  export DM_S3_UPLOADS_BUCKET="" DM_S3_ENDPOINT="" AWS_ACCESS_KEY_ID="" AWS_SECRET_ACCESS_KEY=""
  export DM_MEDIA_LOCAL_DIR="$state/media" DM_HARNESS_SOCKET="$state/harness.sock" DM_LLM_CACHE_DIR="$state/llm"
  export DM_PRICING_FILE="$root/config/pricing.yaml" DM_MODELS_FILE="$root/config/models.yaml" DM_THROUGHPUT_FILE="$root/config/throughput.yaml"
  local api_port web_port
  api_port="$(port_of "$DM_API_LISTEN")"
  web_port="$(port_of "$DM_PUBLIC_BASE_URL")"

  if [[ "$DATABASE_URL" =~ ^postgres(ql)?://([^:/@]+):([^@]*)@[^:/]+(:([0-9]+))?/([^?]+) ]]; then
    export DM_DEV_PG_USER="${BASH_REMATCH[2]}" DM_DEV_PG_PASSWORD="${BASH_REMATCH[3]}" DM_DEV_PG_PORT="${BASH_REMATCH[5]:-5432}" DM_DEV_PG_DB="${BASH_REMATCH[6]}"
  else
    die "DATABASE_URL precisa ter o formato postgres://usuario:senha@host:porta/banco"
  fi
  log "Postgres e Mailpit (Docker)"
  "${compose[@]}" up -d --wait postgres mailpit >"$logs/compose.log" 2>&1 || die "docker compose falhou (veja $logs/compose.log)"

  log "migrações"
  (cd services/api && go run ./cmd/migrate up) >"$logs/migrate.log" 2>&1 || die "migrações falharam (veja $logs/migrate.log)"

  log "compilando API, worker e harness"
  (cd services/api && go build -o "$bin/" ./cmd/api ./cmd/worker) >"$logs/build.log" 2>&1 || die "build Go falhou (veja $logs/build.log)"
  local target="${CARGO_TARGET_DIR:-$HOME/.cache/docmatch/cargo-target}"
  (cd services/harness && CARGO_TARGET_DIR="$target" cargo build -q -p dm-server) >>"$logs/build.log" 2>&1 || die "build do harness falhou (veja $logs/build.log)"

  rm -f "$DM_HARNESS_SOCKET"
  start harness "$target/debug/dm-server" --socket "$DM_HARNESS_SOCKET"
  wait_for "o harness" test -S "$DM_HARNESS_SOCKET"
  start api "$bin/api"
  start worker "$bin/worker"
  wait_for "a API" curl -fsS "http://127.0.0.1:${api_port}/v1/version"

  log "site para ${DM_PUBLIC_BASE_URL} (API em ${DM_API_BASE_URL})"
  # Keep the running local app independent from production/E2E builds in apps/web/dist.
  DM_PUBLIC_BASE_URL="$DM_PUBLIC_BASE_URL" PUBLIC_DM_API_BASE_URL="$DM_API_BASE_URL" PUBLIC_DM_FEATURE_RESULTS=false \
    pnpm --dir apps/web exec astro build --outDir "$state/web" >"$logs/web-build.log" 2>&1 || die "build do site falhou (veja $logs/web-build.log)"
  start web node apps/web/tools/serve-dist.mjs --dir "$state/web" --host 127.0.0.1 --port "$web_port"
  wait_for "o site" curl -fsS "http://127.0.0.1:${web_port}/"

  cat <<EOF

docmatch local no ar (modelo: ${DM_LLM_MODE}):
  site     ${DM_PUBLIC_BASE_URL}/app    (entra sozinho com o login local)
  API      ${DM_API_BASE_URL}/v1/version
  Mailpit  http://127.0.0.1:${DM_DEV_MAILPIT_PORT:-8025}   (códigos do login por e-mail)
  logs     $logs
  parar    make local-down
EOF
}

down() {
  local name
  for name in web worker api harness; do stop_one "$name"; done
  "${compose[@]}" stop postgres mailpit >/dev/null 2>&1 || true
  log "docmatch local parado (o banco fica no volume do Docker)"
}

status() {
  local name pid
  for name in harness api worker web; do
    pid="$(cat "$pids/$name" 2>/dev/null || true)"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then echo "$name: rodando (pid $pid)"; else echo "$name: parado"; fi
  done
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  status) status ;;
  -h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' ;;
  *) die "uso: tools/dev/local.sh up | down | status" ;;
esac

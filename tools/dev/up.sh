#!/usr/bin/env bash
# Sobe o ambiente local: carrega o .env, inicia o Docker Compose (Postgres com
# pgvector, MinIO, Mailpit, stripe-mock e, com --llm, o proxy do Ollama),
# espera o Postgres, aplica as migrações (cmd/migrate) e imprime as URLs.
#
# Uso: tools/dev/up.sh [--llm] [--no-migrate] [--down]
set -euo pipefail

root="$(git rev-parse --show-toplevel 2>/dev/null || { cd "$(dirname "$0")/../.." && pwd; })"
cd "$root"

profiles=()
migrate=1
for arg in "$@"; do
  case "$arg" in
    --llm) profiles+=(--profile llm) ;;
    --no-migrate) migrate=0 ;;
    --down)
      docker compose -f tools/dev/compose.yaml --profile llm down
      echo "ambiente local parado (volumes preservados; use 'docker compose -f tools/dev/compose.yaml down -v' para apagar)"
      exit 0 ;;
    -h|--help)
      sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "argumento desconhecido: $arg" >&2; exit 2 ;;
  esac
done

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  echo "erro: Docker com o plugin Compose v2 é necessário (docker compose version)" >&2
  exit 1
fi

if [[ ! -f .env ]]; then
  echo "erro: .env não existe. Rode: cp .env.example .env" >&2
  exit 1
fi
eval "$(tools/dev/env.sh .env)"

: "${DATABASE_URL:?DATABASE_URL precisa estar no .env}"

# Deriva usuário, senha, porta e banco do DATABASE_URL para o compose.
# postgres://usuario:senha@host:porta/banco?params
if [[ "$DATABASE_URL" =~ ^postgres(ql)?://([^:/@]+)(:([^@]*))?@([^:/]+)(:([0-9]+))?/([^?]+) ]]; then
  export DM_DEV_PG_USER="${BASH_REMATCH[2]}"
  export DM_DEV_PG_PASSWORD="${BASH_REMATCH[4]:-}"
  export DM_DEV_PG_PORT="${BASH_REMATCH[7]:-5432}"
  export DM_DEV_PG_DB="${BASH_REMATCH[8]}"
else
  echo "erro: DATABASE_URL precisa ter o formato postgres://usuario:senha@host:porta/banco" >&2
  exit 1
fi
if [[ -z "$DM_DEV_PG_PASSWORD" ]]; then
  echo "erro: DATABASE_URL precisa ter senha (o Postgres do compose exige uma)" >&2
  exit 1
fi

# Porta do S3 local a partir do DM_S3_ENDPOINT, quando definido.
if [[ "${DM_S3_ENDPOINT:-}" =~ :([0-9]+)/?$ ]]; then
  export DM_DEV_S3_PORT="${BASH_REMATCH[1]}"
fi
# Porta do Ollama a partir de DM_LOCAL_LLM_BASE_URL, quando definida.
if [[ "${DM_LOCAL_LLM_BASE_URL:-}" =~ :([0-9]+)(/|$) ]]; then
  export DM_DEV_OLLAMA_UPSTREAM_PORT="${BASH_REMATCH[1]}"
fi

compose=(docker compose -f tools/dev/compose.yaml "${profiles[@]}")

echo "==> subindo contêineres"
"${compose[@]}" up -d --wait --remove-orphans

echo "==> esperando o Postgres em 127.0.0.1:${DM_DEV_PG_PORT}"
for _ in $(seq 1 60); do
  if "${compose[@]}" exec -T postgres pg_isready -U "$DM_DEV_PG_USER" -d "$DM_DEV_PG_DB" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
"${compose[@]}" exec -T postgres pg_isready -U "$DM_DEV_PG_USER" -d "$DM_DEV_PG_DB" >/dev/null

if [[ $migrate -eq 1 ]]; then
  echo "==> aplicando migrações (services/api/cmd/migrate)"
  (cd services/api && go run ./cmd/migrate up)
fi

s3_port="${DM_DEV_S3_PORT:-9000}"
s3_console="${DM_DEV_S3_CONSOLE_PORT:-9001}"
mailpit_port="${DM_DEV_MAILPIT_PORT:-8025}"
stripe_port="${DM_DEV_STRIPE_MOCK_PORT:-12111}"

cat <<EOF

ambiente local pronto:
  Postgres      postgres://${DM_DEV_PG_USER}:***@127.0.0.1:${DM_DEV_PG_PORT}/${DM_DEV_PG_DB}
  S3 local      http://127.0.0.1:${s3_port}   console: http://127.0.0.1:${s3_console}  (chave ${DM_DEV_S3_ACCESS_KEY:-docmatch}; RustFS, API compatível com MinIO)
  Mailpit       http://127.0.0.1:${mailpit_port}   SMTP em 127.0.0.1:${DM_DEV_MAILPIT_SMTP_PORT:-1025}
  stripe-mock   http://127.0.0.1:${stripe_port}
EOF
if [[ ${#profiles[@]} -gt 0 ]]; then
  echo "  Ollama proxy  127.0.0.1:${DM_DEV_OLLAMA_PROXY_PORT:-11435} -> 127.0.0.1:${DM_DEV_OLLAMA_UPSTREAM_PORT:-11434}"
fi
cat <<EOF

próximos passos:
  make -C services/api run-api      # API em ${DM_API_LISTEN:-127.0.0.1:8080}
  make -C services/api run-worker   # worker (River)
  tools/dev/up.sh --down            # para tudo
EOF

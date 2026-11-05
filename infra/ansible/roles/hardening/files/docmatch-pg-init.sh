#!/usr/bin/env bash
# First-boot database initialisation (idempotent): creates the application role and database
# from DATABASE_URL and the pgBackRest stanza. Runs as root from docmatch-pg-init.service with
# the rendered environment loaded.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL ausente no ambiente renderizado}"

# postgres://user:password@host:port/dbname?params
if [[ ! "$DATABASE_URL" =~ ^postgres(ql)?://([^:/@]+):([^@]*)@([^:/]+)(:([0-9]+))?/([^?]+) ]]; then
  echo "docmatch-pg-init: DATABASE_URL fora do formato postgres://usuario:senha@host:porta/banco" >&2
  exit 1
fi
db_user="${BASH_REMATCH[2]}"
db_pass="$(printf '%b' "${BASH_REMATCH[3]//%/\\x}")"
db_name="${BASH_REMATCH[7]}"

psql_admin() { sudo -u postgres psql -v ON_ERROR_STOP=1 -qAt "$@"; }

for _ in $(seq 1 30); do
  if sudo -u postgres pg_isready -q; then break; fi
  sleep 2
done

if [[ "$(psql_admin -c "SELECT 1 FROM pg_roles WHERE rolname = '${db_user//\'/\'\'}'")" != "1" ]]; then
  psql_admin -c "CREATE ROLE \"${db_user//\"/\"\"}\" LOGIN PASSWORD '${db_pass//\'/\'\'}' CONNECTION LIMIT 30"
  echo "docmatch-pg-init: papel ${db_user} criado"
else
  psql_admin -c "ALTER ROLE \"${db_user//\"/\"\"}\" WITH PASSWORD '${db_pass//\'/\'\'}'"
fi

if [[ "$(psql_admin -c "SELECT 1 FROM pg_database WHERE datname = '${db_name//\'/\'\'}'")" != "1" ]]; then
  psql_admin -c "CREATE DATABASE \"${db_name//\"/\"\"}\" OWNER \"${db_user//\"/\"\"}\" ENCODING 'UTF8' LC_COLLATE 'C.UTF-8' LC_CTYPE 'C.UTF-8' TEMPLATE template0"
  psql_admin -d "$db_name" -c "REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT ALL ON SCHEMA public TO \"${db_user//\"/\"\"}\""
  psql_admin -d "$db_name" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
  echo "docmatch-pg-init: banco ${db_name} criado"
fi

# pgBackRest stanza (needs the bucket; skipped quietly until the env carries it).
if [[ -r /run/docmatch/env.postgres ]]; then
  if ! sudo -u postgres /usr/local/bin/docmatch-pgbackrest --stanza=docmatch info --output=json 2>/dev/null | grep -q '"status":{"code":0'; then
    sudo -u postgres /usr/local/bin/docmatch-pgbackrest --stanza=docmatch stanza-create && echo "docmatch-pg-init: stanza criada" || \
      echo "docmatch-pg-init: stanza-create falhou (verifique o bucket de backups e o papel da instância)" >&2
  fi
fi
echo "docmatch-pg-init: concluído"

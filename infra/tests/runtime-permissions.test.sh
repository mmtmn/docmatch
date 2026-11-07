#!/usr/bin/env bash
# Verify Linux DAC with real, separate service users. The container has no network or AWS access.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "${1:-}" != --inside ]]; then
  command -v docker >/dev/null || { echo 'runtime-permissions.test: Docker is required' >&2; exit 1; }
  # Read the install task, so a regression in its group/mode also fails the execution test.
  read -r backup_group backup_mode < <(python3 - "$root" <<'PY'
import pathlib, re, sys
text = (pathlib.Path(sys.argv[1]) / 'infra/ansible/roles/hardening/tasks/services.yml').read_text()
task = text.split('- name: Install the pgBackRest schedule wrapper\n', 1)[1].split('\n- name:', 1)[0]
print(re.search(r'^    group: (\w+)$', task, re.M)[1],
      re.search(r'^    mode: "(\d+)"$', task, re.M)[1])
PY
  )
  # Stream the three test inputs: Docker VM hosts do not always mount macOS temporary paths.
  tar -C "$root" -cf - infra/scripts/render-env.sh infra/tests/runtime-permissions.test.sh \
    infra/ansible/roles/hardening/templates/docmatch-pgbackrest-backup.sh.j2 |
    docker run --rm -i --network none --entrypoint bash \
      -e BACKUP_GROUP="$backup_group" -e BACKUP_MODE="$backup_mode" debian:bookworm-slim \
      -c 'mkdir /repo; tar -xf - -C /repo; exec bash /repo/infra/tests/runtime-permissions.test.sh --inside'
  exit 0
fi

# shellcheck source-path=SCRIPTDIR
# shellcheck source=../scripts/render-env.sh
source "$root/infra/scripts/render-env.sh"
: "${BACKUP_GROUP:?container install group missing}"
: "${BACKUP_MODE:?container install mode missing}"
for user in docmatch postgres caddy bystander; do useradd --no-create-home "$user"; done
prepare_runtime_dir /run/docmatch docmatch
write_atomic /run/docmatch/env docmatch 'MAIN_SECRET="test-main"'
write_atomic /run/docmatch/env.postgres postgres 'DM_S3_BACKUPS_BUCKET="test-backups"'
write_atomic /run/docmatch/env.caddy caddy 'DM_ORIGIN_SECRET="test-origin"'

# Neither a fresh directory nor a previously provisioned 0750 parent may strand a subset.
chmod 0750 /run/docmatch
prepare_runtime_dir /run/docmatch docmatch
for user in docmatch postgres caddy; do
  own="env.$user"; [[ "$user" == docmatch ]] && own='env'
  runuser -u "$user" -- test -r "/run/docmatch/$own"
  if runuser -u "$user" -- ls /run/docmatch >/dev/null 2>&1; then
    echo "unexpected directory listing permission for $user" >&2; exit 1
  fi
  for file in env env.postgres env.caddy; do
    [[ "$file" == "$own" ]] && continue
    if runuser -u "$user" -- test -r "/run/docmatch/$file"; then
      echo "$user can read $file" >&2; exit 1
    fi
  done
done
for file in env env.postgres env.caddy; do
  if runuser -u bystander -- test -r "/run/docmatch/$file"; then exit 1; fi
done

# Install the real schedule template with the Ansible task's owner/mode; only the backup
# binary is substituted. The actual wrapper must run as postgres and read its own subset.
install -m "$BACKUP_MODE" -o root -g "$BACKUP_GROUP" \
  "$root/infra/ansible/roles/hardening/templates/docmatch-pgbackrest-backup.sh.j2" \
  /usr/local/sbin/docmatch-pgbackrest-backup
cat > /usr/local/bin/docmatch-pgbackrest <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
source /run/docmatch/env.postgres
[[ "$DM_S3_BACKUPS_BUCKET" == test-backups ]]
printf '%s\n' 'backup permissions ok'
STUB
chmod 0755 /usr/local/bin/docmatch-pgbackrest
runuser -u postgres -- /usr/local/sbin/docmatch-pgbackrest-backup
if runuser -u bystander -- test -x /usr/local/sbin/docmatch-pgbackrest-backup; then exit 1; fi
echo 'runtime-permissions.test: isolated service subsets and backup execution passed'

#!/usr/bin/env bash
# Builds the application for the instance (linux/arm64), publishes it to the releases bucket and
# installs it on the app instance over SSM Run Command (no SSH anywhere): binaries in the role's
# bin dir, pricing.yaml and models.yaml in its config dir, migrations, then a restart of the
# harness, the API and the worker with a health check. The bucket comes from the Terraform
# outputs, the instance from its tags and the paths from the hardening role's defaults, so
# nothing here is typed twice.
#
# A release is a commit: the tree must be clean (or pass --release to reinstall one already
# published). Rollback = --release <older sha> --skip-build.
#
# Uso: infra/scripts/deploy-app.sh [--env prod] [--release <sha>] [--skip-build] [--dry-run]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib.sh
source "$here/lib.sh"

env="prod"
release=""
skip_build=0
dry_run=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) env="$2"; shift ;;
    --release) release="$2"; shift ;;
    --skip-build) skip_build=1 ;;
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) dm_die "argumento desconhecido: $1" ;;
  esac
  shift
done

dm_require aws terraform git jq sha256sum make
root="$(dm_repo_root)"
env_dir="$root/infra/terraform/envs/$env"
[[ -d "$env_dir" ]] || dm_die "ambiente desconhecido: $env"
region="$(dm_region)"
export AWS_PAGER=""

# Paths and names of the box, from the role that built the AMI (plain scalars only).
defaults="$root/infra/ansible/roles/hardening/defaults/main.yml"
role_default() {
  local v
  v="$(sed -n "s/^$1:[[:space:]]*//p" "$defaults" | tr -d "\"'")"
  [[ -n "$v" && "$v" != *"{{"* ]] || dm_die "padrão $1 ausente ou não literal em $defaults"
  printf '%s' "$v"
}
bin_dir="$(role_default hardening_app_bin_dir)"
config_dir="$(role_default hardening_app_config_dir)"
app_home="$(role_default hardening_app_home)"
app_user="$(role_default hardening_app_user)"
app_group="$(role_default hardening_app_group)"
env_file="$(role_default hardening_env_file)"
api_listen="$(role_default hardening_api_listen)"

if [[ -z "$release" ]]; then
  [[ -z "$(git -C "$root" status --porcelain)" ]] ||
    dm_die "árvore com mudanças não commitadas: uma release é um commit (ou use --release <sha>)"
  release="$(git -C "$root" rev-parse --short=12 HEAD)"
fi
[[ "$release" =~ ^[0-9a-f]{7,40}$ ]] || dm_die "release inválida: $release"
out="$root/dist/release/$release"

if [[ $skip_build -eq 0 ]]; then
  dm_require go cargo rustup
  rm -rf "$out"
  mkdir -p "$out"
  dm_log "compilando a release $release (linux/arm64)"
  make -s -C "$root/services/api" release RELEASE_DIR="$out" VERSION="$release"
  make -s -C "$root/services/harness" release RELEASE_DIR="$out"
  install -m 0644 "$root/config/pricing.yaml" "$root/config/models.yaml" "$out/"
  (cd "$out" && sha256sum docmatch-api docmatch-worker docmatch-migrate docmatch-harness pricing.yaml models.yaml > SHA256SUMS)
fi

bucket="$(terraform -chdir="$env_dir" output -raw releases_bucket_name)"
[[ -n "$bucket" ]] || dm_die "output releases_bucket_name vazio; o ambiente já foi aplicado?"
prefix="releases/$release"

if [[ -f "$out/SHA256SUMS" ]]; then
  if [[ $dry_run -eq 1 ]]; then
    dm_log "simulação: enviaria $out para s3://$bucket/$prefix/"
  else
    aws s3 cp --recursive --only-show-errors "$out/" "s3://$bucket/$prefix/" | cat
    dm_log "release publicada em s3://$bucket/$prefix/"
  fi
elif [[ $skip_build -eq 1 ]]; then
  dm_log "sem build local de $release: instala a que já está em s3://$bucket/$prefix/"
else
  dm_die "build de $release não encontrado em $out"
fi

instances="$(aws ec2 describe-instances --region "$region" \
  --filters "Name=tag:docmatch:role,Values=app" "Name=tag:docmatch:env,Values=$env" "Name=instance-state-name,Values=running" \
  --query 'Reservations[].Instances[].InstanceId' --output text | cat)"
read -r -a ids <<<"$instances"
[[ ${#ids[@]} -eq 1 && "${ids[0]}" =~ ^i-[0-9a-f]+$ ]] ||
  dm_die "esperava uma instância da aplicação em execução em $env, achei: '${instances}'"
instance_id="${ids[0]}"

# The script that runs as root on the instance. Values are expanded here, on the operator's side.
remote="$(cat <<EOF
set -euo pipefail
export AWS_REGION='$region' AWS_DEFAULT_REGION='$region' AWS_PAGER=''
work="\$(mktemp -d)"
trap 'rm -rf "\$work"' EXIT
aws s3 cp --recursive --only-show-errors 's3://$bucket/$prefix/' "\$work/"
cd "\$work"
sha256sum --check --strict --quiet SHA256SUMS
install -d -m 0755 -o root -g root '$bin_dir' '$config_dir'
for b in docmatch-api docmatch-worker docmatch-migrate docmatch-harness; do
  install -m 0755 -o root -g root "\$b" '$bin_dir/'"\$b.new"
  mv -f '$bin_dir/'"\$b.new" '$bin_dir/'"\$b"
done
install -m 0644 -o root -g root pricing.yaml models.yaml '$config_dir/'
systemctl restart docmatch-env.service
systemd-run --wait --pipe --quiet --collect --uid='$app_user' --gid='$app_group' \\
  -p EnvironmentFile='$env_file' -p WorkingDirectory='$app_home' '$bin_dir/docmatch-migrate' up
systemctl restart docmatch-harness.service docmatch-api.service docmatch-worker.service
ok=0
for _ in \$(seq 1 30); do
  if curl -fsS --max-time 2 'http://$api_listen/v1/version'; then ok=1; break; fi
  sleep 1
done
echo
systemctl --no-pager --lines=0 status docmatch-harness.service docmatch-api.service docmatch-worker.service || true
systemctl is-active --quiet docmatch-harness.service docmatch-api.service docmatch-worker.service
[[ \$ok -eq 1 ]]
echo '$release' > '$bin_dir/../RELEASE'
echo "release $release instalada"
EOF
)"

if [[ $dry_run -eq 1 ]]; then
  dm_log "simulação: rodaria em $instance_id:"
  printf '%s\n' "$remote"
  exit 0
fi

params="$(jq -n --arg s "$remote" '{commands: [$s], executionTimeout: ["900"]}')"
command_id="$(aws ssm send-command --region "$region" --instance-ids "$instance_id" \
  --document-name AWS-RunShellScript --comment "docmatch $env release $release" \
  --timeout-seconds 600 --parameters "$params" --query Command.CommandId --output text | cat)"
dm_log "instalando em $instance_id (comando $command_id)"

status="InProgress"
for _ in $(seq 1 180); do
  status="$(aws ssm get-command-invocation --region "$region" --command-id "$command_id" \
    --instance-id "$instance_id" --query Status --output text 2>/dev/null | cat || true)"
  case "$status" in
    Pending|InProgress|Delayed|"") sleep 5 ;;
    *) break ;;
  esac
done
aws ssm get-command-invocation --region "$region" --command-id "$command_id" --instance-id "$instance_id" \
  --query '[StandardOutputContent, StandardErrorContent]' --output text | cat
[[ "$status" == "Success" ]] || dm_die "instalação terminou com $status"
dm_log "release $release no ar em $env"

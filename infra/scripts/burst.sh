#!/usr/bin/env bash
# Raises, lowers or shows the pool of on-demand (burst) workers by hand, through the same path the
# main worker uses: SetDesiredCapacity and DescribeAutoScalingGroups on the pool's Auto Scaling
# group (docs/escala.md, docs/runbooks/burst.md). The main worker stays the owner of the size: an
# `up` without work goes back to zero at the end of the idle period (DM_BURST_IDLE_MINUTES), a
# `down` with work queued is undone at its next reconcile, and instances holding a job are
# protected from scale-in, so `down` never takes one of them.
#
# Uso: infra/scripts/burst.sh [--env prod] [--asg <nome>] up [N] | down | status
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib.sh
source "$here/lib.sh"

env="prod"
asg=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) env="$2"; shift 2 ;;
    --asg) asg="$2"; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) break ;;
  esac
done
cmd="${1:-status}"
shift || true

dm_require aws jq terraform
region="$(dm_region)"
export AWS_PAGER=""
root="$(dm_repo_root)"
env_dir="$root/infra/terraform/envs/$env"
[[ -d "$env_dir" ]] || dm_die "ambiente desconhecido: $env"
if [[ -z "$asg" ]]; then
  asg="$(terraform -chdir="$env_dir" output -raw burst_asg_name 2>/dev/null || true)"
fi
[[ -n "$asg" ]] || asg="docmatch-$env-worker"
[[ "$asg" =~ ^docmatch-[a-z0-9-]+$ ]] || dm_die "grupo inválido: $asg"

# Every aws call is piped through cat: the CLI exits 120 without output when stdout is a file.
describe() {
  aws autoscaling describe-auto-scaling-groups --region "$region" --auto-scaling-group-names "$asg" --output json | cat
}

group="$(describe)"
[[ "$(jq '.AutoScalingGroups | length' <<<"$group")" -eq 1 ]] || dm_die "grupo $asg não encontrado em $region"
max_size="$(jq -r '.AutoScalingGroups[0].MaxSize' <<<"$group")"

set_desired() {
  local n="$1"
  aws autoscaling set-desired-capacity --region "$region" --auto-scaling-group-name "$asg" \
    --desired-capacity "$n" --no-honor-cooldown --output json | cat
  dm_log "capacidade desejada de $asg: $n"
}

show() {
  local g="$1"
  jq -r '.AutoScalingGroups[0] | "grupo \(.AutoScalingGroupName): desejado \(.DesiredCapacity), mínimo \(.MinSize), máximo \(.MaxSize)"' <<<"$g"
  jq -r '.AutoScalingGroups[0].Instances[]? | "  \(.InstanceId)  \(.InstanceType // "?")  \(.LifecycleState)  \(if .ProtectedFromScaleIn then "protegida (com trabalho)" else "livre" end)"' <<<"$g"
}

case "$cmd" in
  up)
    n="${1:-1}"
    [[ "$n" =~ ^[0-9]+$ ]] || dm_die "uso: burst.sh up [N]"
    if (( n > max_size )); then
      dm_log "pedido de $n acima do máximo do grupo ($max_size); usando $max_size"
      n="$max_size"
    fi
    set_desired "$n"
    dm_log "o worker principal devolve o pool a zero depois do período ocioso, se não houver trabalho"
    ;;
  down)
    set_desired 0
    busy="$(jq -r '[.AutoScalingGroups[0].Instances[]? | select(.ProtectedFromScaleIn) | .InstanceId] | join(" ")' <<<"$group")"
    if [[ -n "$busy" ]]; then
      dm_log "instâncias com trabalho (protegidas) terminam o trabalho antes de sair: $busy"
    fi
    ;;
  status)
    show "$group"
    url="$(aws ssm get-parameter --region "$region" --name "/docmatch/$env/DM_BURST_API_URL" --query Parameter.Value --output text 2>/dev/null | cat || true)"
    enabled="$(aws ssm get-parameter --region "$region" --name "/docmatch/$env/DM_BURST_ENABLED" --query Parameter.Value --output text 2>/dev/null | cat || true)"
    echo "DM_BURST_ENABLED=${enabled:-?}  endpoint interno: ${url:-?}"
    echo "atividades recentes:"
    aws autoscaling describe-scaling-activities --region "$region" --auto-scaling-group-name "$asg" --max-items 5 --output json | cat |
      jq -r '.Activities[]? | "  \(.StartTime)  \(.StatusCode)  \(.Description)"'
    ;;
  *)
    dm_die "comando desconhecido: $cmd (use up, down ou status)"
    ;;
esac

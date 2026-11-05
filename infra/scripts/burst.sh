#!/usr/bin/env bash
# Raises, lowers or shows the pool of on-demand (burst) workers by hand, through the same path the
# main worker uses: SetDesiredCapacity, SetInstanceProtection and DescribeAutoScalingGroups on the
# pool's Auto Scaling group (docs/escala.md, docs/runbooks/burst.md).
#
#   up [N]  only with the pool on (DM_BURST_ENABLED=1). The scaler treats a raise it did not make as
#           work: the instances stay warm for DM_BURST_IDLE_MINUTES from when it sees the raise,
#           then the group goes back to zero.
#   down    desired 0 and scale-in protection off on every instance: also the ones holding a job,
#           whose runs go back to the queue from their checkpoint. With the pool on, the scaler
#           brings the group back up if work is queued: switch the pool off first to stop it.
#   status  size, instances, protection, the internal endpoint and the latest activities.
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
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
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

param() {
  aws ssm get-parameter --region "$region" --name "/docmatch/$env/$1" --query Parameter.Value --output text 2>/dev/null | cat || true
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
    enabled="$(param DM_BURST_ENABLED)"
    [[ "$enabled" == 1 ]] || dm_die "pool desligado (DM_BURST_ENABLED=${enabled:-?}): ligue-o antes de subir instâncias (docs/runbooks/burst.md)"
    if (( n > max_size )); then
      dm_log "pedido de $n acima do máximo do grupo ($max_size); usando $max_size"
      n="$max_size"
    fi
    set_desired "$n"
    dm_log "aquecido por DM_BURST_IDLE_MINUTES a partir de quando o worker principal vir a subida; depois, sem trabalho, volta a zero"
    ;;
  down)
    set_desired 0
    mapfile -t protected < <(jq -r '.AutoScalingGroups[0].Instances[]? | select(.ProtectedFromScaleIn and (.LifecycleState == "InService" or (.LifecycleState | startswith("Pending")))) | .InstanceId' <<<"$group")
    if (( ${#protected[@]} > 0 )); then
      aws autoscaling set-instance-protection --region "$region" --auto-scaling-group-name "$asg" \
        --instance-ids "${protected[@]}" --no-protected-from-scale-in --output json | cat
      dm_log "proteção retirada de ${protected[*]}: as execuções delas voltam à fila a partir do último evento"
    fi
    [[ "$(param DM_BURST_ENABLED)" != 1 ]] || dm_log "o pool está ligado: com trabalho na fila, o worker principal sobe o grupo de novo"
    ;;
  status)
    show "$group"
    echo "DM_BURST_ENABLED=$(param DM_BURST_ENABLED)  endpoint interno: $(param DM_BURST_API_URL)"
    echo "atividades recentes:"
    aws autoscaling describe-scaling-activities --region "$region" --auto-scaling-group-name "$asg" --max-items 5 --output json | cat |
      jq -r '.Activities[]? | "  \(.StartTime)  \(.StatusCode)  \(.Description)"'
    ;;
  *)
    dm_die "comando desconhecido: $cmd (use up, down ou status)"
    ;;
esac

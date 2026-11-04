#!/usr/bin/env bash
# Shared helpers for infra/scripts. Source it; never execute it.
# shellcheck shell=bash

dm_log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }
dm_die() {
  dm_log "erro: $*"
  exit 1
}

dm_require() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || dm_die "comando necessário não encontrado: $c"
  done
}

# Region from the environment, then from the CLI configuration. No default on purpose.
dm_region() {
  local r="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
  [[ -z "$r" ]] && r="$(aws configure get region 2>/dev/null || true)"
  [[ -z "$r" ]] && dm_die "região indefinida: exporte AWS_REGION"
  echo "$r"
}

# Account id: ACCOUNT_ID override first, then IAM, then EC2 (sts get-caller-identity is flaky on
# the snap build of the CLI, so it is the last resort).
dm_account_id() {
  if [[ -n "${ACCOUNT_ID:-}" ]]; then
    [[ "$ACCOUNT_ID" =~ ^[0-9]{12}$ ]] || dm_die "ACCOUNT_ID inválido: $ACCOUNT_ID"
    echo "$ACCOUNT_ID"
    return
  fi
  local arn id
  arn="$(aws iam get-user --query 'User.Arn' --output text 2>/dev/null || true)"
  if [[ "$arn" =~ :([0-9]{12}): ]]; then
    echo "${BASH_REMATCH[1]}"
    return
  fi
  id="$(aws ec2 describe-security-groups --max-items 1 --query 'SecurityGroups[0].OwnerId' --output text 2>/dev/null || true)"
  if [[ "$id" =~ ^[0-9]{12}$ ]]; then
    echo "$id"
    return
  fi
  id="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
  if [[ "$id" =~ ^[0-9]{12}$ ]]; then
    echo "$id"
    return
  fi
  dm_die "não foi possível descobrir o id da conta; exporte ACCOUNT_ID=<12 dígitos>"
}

dm_repo_root() {
  local here
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  git -C "$here" rev-parse --show-toplevel 2>/dev/null || cd "$here/../.." && pwd
}

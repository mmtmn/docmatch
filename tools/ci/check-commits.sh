#!/usr/bin/env bash
# Verifica as mensagens dos commits do branch atual contra o hook commit-msg
# (Conventional Commits, sem atribuição a assistentes de IA).
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"

base="${CI_BASE_REF:-origin/main}"
if ! git rev-parse --verify -q "$base" >/dev/null; then
  base="$(git rev-list --max-parents=0 HEAD | tail -1)"
fi
range="$base..HEAD"
commits="$(git rev-list "$range" 2>/dev/null || true)"
[[ -z "$commits" ]] && { echo "nenhum commit novo em $range"; exit 0; }

status=0
tmp="$(mktemp)"
for c in $commits; do
  git log -1 --format=%B "$c" > "$tmp"
  if ! tools/git-hooks/commit-msg "$tmp" 2>/dev/null; then
    echo "commit $c reprovado:" >&2
    sed -n '1p' "$tmp" | sed 's/^/  /' >&2
    status=1
  fi
done
rm -f "$tmp"
exit $status

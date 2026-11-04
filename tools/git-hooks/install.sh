#!/usr/bin/env bash
# Ativa os hooks versionados em tools/git-hooks sem dependências externas.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
chmod +x "$root"/tools/git-hooks/commit-msg "$root"/tools/git-hooks/pre-commit "$root"/tools/git-hooks/pre-push
git -C "$root" config core.hooksPath tools/git-hooks
echo "hooks ativados: core.hooksPath=tools/git-hooks"

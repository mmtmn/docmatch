# Sourced by release.sh and publish-images.sh (bash): what the origin remote says about the
# repository, so neither script types an owner, a registry or a URL.
#
#   docmatch_repo_info <repo root>
#
# Sets repo_host, repo_owner and repo_name from `git remote get-url origin` (https, ssh:// or
# scp-like git@host:owner/repo), and, for GitHub only, registry (ghcr.io/<owner in lower case>,
# where the installer pulls the images from) and repo_url (the https address of the repository).
# Everything stays empty when the remote is unknown or is not GitHub.

# shellcheck disable=SC2034 # the variables are the result, read by the sourcing script
docmatch_repo_info() {
  local remote
  remote="$(git -C "$1" remote get-url origin 2>/dev/null || true)"
  remote="${remote%.git}"
  repo_host="" repo_owner="" repo_name="" registry="" repo_url=""
  if [[ "$remote" =~ ^[a-z]+://([^/@]+@)?([^/:]+)(:[0-9]+)?/([^/]+)/([^/]+)$ ]]; then
    repo_host="${BASH_REMATCH[2]}" repo_owner="${BASH_REMATCH[4]}" repo_name="${BASH_REMATCH[5]}"
  elif [[ "$remote" =~ ^[^@]+@([^:]+):([^/]+)/([^/]+)$ ]]; then
    repo_host="${BASH_REMATCH[1]}" repo_owner="${BASH_REMATCH[2]}" repo_name="${BASH_REMATCH[3]}"
  fi
  if [[ "$repo_host" == "github.com" ]]; then
    registry="ghcr.io/$(tr '[:upper:]' '[:lower:]' <<<"$repo_owner")"
    repo_url="https://${repo_host}/${repo_owner}/${repo_name}"
  fi
}

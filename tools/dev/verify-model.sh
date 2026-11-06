#!/usr/bin/env bash
# Opt-in real model + golden-data validation. Expected answers never go to the model.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
if [[ $# != 3 || ( "$1" != local && "$1" != openrouter ) ]]; then
  echo 'usage: tools/dev/verify-model.sh local|openrouter DATA_DIRECTORY EXPECTED_YAML' >&2
  exit 2
fi
mode="$1"
export DM_DATA_PACK="$(cd "$2" && pwd)"
export DM_EXPECTED_PATH="$(cd "$(dirname "$3")" && pwd)/$(basename "$3")"
cd "$root"
if [[ -f .env ]]; then eval "$(tools/dev/env.sh .env --defaults)"; fi
export DM_LLM_MODE="$mode" DM_LLM_CACHE_MODE=off DM_MODELS_FILE="$root/config/models.yaml"
if [[ "$mode" == local ]]; then
  export DM_LOCAL_LLM_BASE_URL="${DM_LOCAL_LLM_BASE_URL:-http://localhost:11434/v1}"
  export DM_MODEL_EXTRACTION_LOCAL="${DM_MODEL_EXTRACTION_LOCAL:-qwen3.5:0.8b}"
  export DM_LOCAL_LLM_REASONING_EFFORT="${DM_LOCAL_LLM_REASONING_EFFORT:-none}"
fi
export CARGO_TARGET_DIR="${CARGO_TARGET_DIR:-$HOME/.cache/docmatch/cargo-target}"
cargo test --manifest-path services/harness/Cargo.toml -p dm-server --test grpc \
  real_provider_data_pack_scores_100 -- --ignored --nocapture

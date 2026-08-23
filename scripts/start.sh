#!/usr/bin/env bash
# llama-swap launcher for Linux / macOS
# usage: ./scripts/start.sh [path/to/config.yaml] [listen-addr]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:-$SCRIPT_DIR/../config.starter.yaml}"
LISTEN="${2:-127.0.0.1:8080}"

for var in LLAMACPP_SERVER LLM_MODELS_DIR; do
    if [[ -z "${!var:-}" ]]; then
        echo "Required environment variable $var is not set. Example:" >&2
        echo "  export LLAMACPP_SERVER=/opt/llama.cpp/build/bin/llama-server" >&2
        echo "  export LLM_MODELS_DIR=\"/mnt/ai-ssd/LLM Models\"" >&2
        echo "(persist it in ~/.bashrc, ~/.zshrc or your systemd unit)" >&2
        exit 1
    fi
done

EXE=""
for candidate in "$SCRIPT_DIR/../build/llama-swap-linux-amd64" \
                 "$SCRIPT_DIR/../build/llama-swap-darwin-arm64" \
                 "$SCRIPT_DIR/../build/llama-swap"; do
    [[ -x "$candidate" ]] && EXE="$candidate" && break
done
if [[ -z "$EXE" ]]; then
    EXE="$(command -v llama-swap || true)"
fi
if [[ -z "$EXE" ]]; then
    echo "llama-swap binary not found. Build it first (see SETUP.md) or install it in PATH." >&2
    exit 1
fi

echo "starting: $EXE --config $CONFIG --listen $LISTEN"
exec "$EXE" --config "$CONFIG" --listen "$LISTEN" "${@:3}"

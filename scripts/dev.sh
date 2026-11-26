#!/usr/bin/env bash
# Runs the UI against a pretend Deco mesh with hot reload:
#   atlantis-fake (SSH) -> atlantis serve (:18081) -> Vite dev server (:5173)
# Open http://localhost:5173. On first run, create an account and connect
# the pretend Deco with the details atlantis-fake prints.
set -euo pipefail
cd "$(dirname "$0")/.."

DEV_DIR="${ATLANTIS_DEV_DIR:-$(mktemp -d)}"
FAKE_PORT="${FAKE_PORT:-20601}"
API_PORT="${API_PORT:-18081}"
mkdir -p "$DEV_DIR"

go build -o "$DEV_DIR/atlantis" ./cmd/atlantis
go build -o "$DEV_DIR/atlantis-fake" ./cmd/atlantis-fake

pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

"$DEV_DIR/atlantis-fake" -listen "127.0.0.1:$FAKE_PORT" -churn "${FAKE_CHURN:-40s}" &
pids+=($!)

"$DEV_DIR/atlantis" serve --server-listen "127.0.0.1:$API_PORT" --store-path "$DEV_DIR/atlantis.db" &
pids+=($!)

echo "dev data in $DEV_DIR"
cd web && ATLANTIS_API="http://127.0.0.1:$API_PORT" npx vite

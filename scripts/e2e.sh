#!/usr/bin/env bash
# Runs the Playwright smoke tests against a fresh `atlantis serve` backed by
# the pretend Deco mesh. Expects bin/atlantis (with the UI built in) and
# bin/atlantis-fake; `make e2e` builds both first.
set -euo pipefail
cd "$(dirname "$0")/.."

DIR="$(mktemp -d)"
FAKE_PORT="${FAKE_PORT:-20602}"
API_PORT="${API_PORT:-18081}"

pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; rm -rf "$DIR"; }
trap cleanup EXIT INT TERM

# SEED=1 starts with a day of history (for README screenshots).
seed=()
[ -n "${SEED:-}" ] && seed=(-seed-history "$DIR/atlantis.db")
bin/atlantis-fake -listen "127.0.0.1:$FAKE_PORT" -churn "${FAKE_CHURN:-20s}" "${seed[@]}" &
pids+=($!)
for _ in $(seq 100); do # the fake listens once any seeding is done
  (exec 3<>"/dev/tcp/127.0.0.1/$FAKE_PORT") 2>/dev/null && break
  sleep 0.1
done

ATLANTIS_SECRET_KEY="$(bin/atlantis keygen)" \
ATLANTIS_SERVER_LISTEN="127.0.0.1:$API_PORT" \
ATLANTIS_STORE_PATH="$DIR/atlantis.db" \
ATLANTIS_DECO_HOST=127.0.0.1 ATLANTIS_DECO_PORT="$FAKE_PORT" \
ATLANTIS_DECO_EMAIL=demo@example.com ATLANTIS_DECO_PASSWORD=demo-password \
ATLANTIS_POLL_FAST=10s ATLANTIS_LOG_FORMAT=text \
  bin/atlantis serve >"$DIR/serve.log" 2>&1 &
pids+=($!)

for _ in $(seq 50); do
  bin/atlantis healthcheck --url "http://127.0.0.1:$API_PORT/health" 2>/dev/null && break
  sleep 0.2
done

cd web && ATLANTIS_URL="http://127.0.0.1:$API_PORT" npx playwright test "$@" || {
  echo "--- serve log ---"; tail -50 "$DIR/serve.log"; exit 1
}

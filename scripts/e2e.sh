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
bin/atlantis-fake -listen "127.0.0.1:$FAKE_PORT" -churn "${FAKE_CHURN:-20s}" ${seed[@]+"${seed[@]}"} &
pids+=($!)
for _ in $(seq 100); do # the fake listens once any seeding is done
  (exec 3<>"/dev/tcp/127.0.0.1/$FAKE_PORT") 2>/dev/null && break
  sleep 0.1
done

# Nothing is configured: the key file is created next to the database and
# the tests add the pretend Deco from the UI.
bin/atlantis serve --server-listen "127.0.0.1:$API_PORT" --store-path "$DIR/atlantis.db" >"$DIR/serve.log" 2>&1 &
pids+=($!)

up=
for _ in $(seq 50); do
  bin/atlantis healthcheck --url "http://127.0.0.1:$API_PORT/health" 2>/dev/null && { up=1; break; }
  sleep 0.2
done
[ -n "$up" ] || { echo "atlantis serve did not start:"; cat "$DIR/serve.log"; exit 1; }

cd web && ATLANTIS_URL="http://127.0.0.1:$API_PORT" DECO_PORT="$FAKE_PORT" npx playwright test "$@" || {
  echo "--- serve log ---"; tail -50 "$DIR/serve.log"; exit 1
}

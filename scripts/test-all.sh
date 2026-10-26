#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${TEST_PORT:-16160}"
TEMP_DIR="$(mktemp -d)"
MASTER_PID=""
cleanup() {
  if [[ -n "$MASTER_PID" ]]; then
    kill -TERM "$MASTER_PID" 2>/dev/null || true
    wait "$MASTER_PID" 2>/dev/null || true
  fi
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT
"$ROOT_DIR/dist/master" -listen "127.0.0.1:$PORT" -data-dir "$TEMP_DIR/data" -log-dir "$TEMP_DIR/logs" -plugin-dir "$ROOT_DIR/dist/plugins" >"$TEMP_DIR/master.out" 2>&1 &
MASTER_PID=$!
export BASE_URL="http://127.0.0.1:$PORT"
for _ in {1..40}; do
  curl -fsS "$BASE_URL/api/monitor/overview" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -fsS "$BASE_URL/api/monitor/overview" >/dev/null
tests=(test-lifecycle.sh test-communication.sh test-crash-recovery.sh test-update.sh test-concurrency.sh)
names=(Lifecycle Communication "Crash Recovery" Update Concurrency)
passed=0
printf '================================\nPlugin Manager Test Summary\n================================\n'
for index in "${!tests[@]}"; do
  if bash "$ROOT_DIR/scripts/${tests[$index]}"; then
    printf '%-16s PASS\n' "${names[$index]}"
    passed=$((passed+1))
  else
    printf '%-16s FAIL\n' "${names[$index]}"
  fi
done
printf '================================\nTotal: %d\nPassed: %d\nFailed: %d\n' "${#tests[@]}" "$passed" "$(( ${#tests[@]} - passed ))"
[[ "$passed" -eq "${#tests[@]}" ]]

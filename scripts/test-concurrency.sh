#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ID1="timestamp-concurrent"
ID2="heartbeat-concurrent"
trap 'cleanup_plugin "$ID1"; cleanup_plugin "$ID2"; cleanup_response' EXIT
register_plugin "$ID1" timestamp 1.0.0 "$ROOT_DIR/dist/plugins/timestamp/v1.0.0/timestamp-plugin"
register_plugin "$ID2" heartbeat 1.0.0 "$ROOT_DIR/dist/plugins/heartbeat/v1.0.0/heartbeat-plugin"
curl -sS -X POST "$BASE_URL/api/plugins/$ID1/start" >/tmp/gppm-start-1.json & p1=$!
curl -sS -X POST "$BASE_URL/api/plugins/$ID2/start" >/tmp/gppm-start-2.json & p2=$!
wait "$p1" "$p2"
wait_state "$ID1" RUNNING
wait_state "$ID2" RUNNING
for _ in {1..8}; do
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"message_type":"timestamp.generate","payload":{}}' "$BASE_URL/api/plugins/$ID1/messages" >/dev/null &
done
wait
PID="$(curl -fsS "$BASE_URL/api/plugins/$ID1" | jq -r '.runtime.pid')"
[[ "$(pgrep -P "$(lsof -tiTCP:"${BASE_URL##*:}" -sTCP:LISTEN | head -1)" | grep -c "^$PID$" || true)" -eq 1 ]]
rm -f /tmp/gppm-start-1.json /tmp/gppm-start-2.json
printf 'PASS concurrency\n'

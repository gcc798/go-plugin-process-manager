#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ID="timestamp-lifecycle"
trap 'cleanup_plugin "$ID"; cleanup_response' EXIT
register_plugin "$ID" timestamp 1.0.0 "$ROOT_DIR/dist/plugins/timestamp/v1.0.0/timestamp-plugin"
request GET /api/plugins 200
jq -e --arg id "$ID" '.plugins[] | select(.id==$id)' "$RESPONSE_FILE" >/dev/null
request POST "/api/plugins/$ID/start" 200
PID1="$(jq -er '.runtime.pid | select(. > 0)' "$RESPONSE_FILE")"
wait_state "$ID" RUNNING
kill -0 "$PID1"
request POST "/api/plugins/$ID/start" 409
request POST "/api/plugins/$ID/stop" 200
wait_state "$ID" STOPPED
! kill -0 "$PID1" 2>/dev/null
request POST "/api/plugins/$ID/restart" 200
PID2="$(jq -er '.runtime.pid | select(. > 0)' "$RESPONSE_FILE")"
[[ "$PID1" != "$PID2" ]]
printf 'PASS lifecycle\n'

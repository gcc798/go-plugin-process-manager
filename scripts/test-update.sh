#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ID="timestamp-update"
trap 'cleanup_plugin "$ID"; cleanup_response' EXIT
register_plugin "$ID" timestamp 1.0.0 "$ROOT_DIR/dist/plugins/timestamp/v1.0.0/timestamp-plugin"
request POST "/api/plugins/$ID/start" 200
OLD_PID="$(jq -r '.runtime.pid' "$RESPONSE_FILE")"
MASTER_PID="$(lsof -tiTCP:"${BASE_URL##*:}" -sTCP:LISTEN | head -1)"
request POST "/api/plugins/$ID/update" 200 "$(jq -nc --arg path "$ROOT_DIR/dist/plugins/timestamp/v2.0.0/timestamp-plugin" '{version:"2.0.0",binary_path:$path}')"
NEW_PID="$(jq -r '.runtime.pid' "$RESPONSE_FILE")"
[[ "$NEW_PID" != "$OLD_PID" ]]
[[ "$MASTER_PID" == "$(lsof -tiTCP:"${BASE_URL##*:}" -sTCP:LISTEN | head -1)" ]]
! kill -0 "$OLD_PID" 2>/dev/null
request POST "/api/plugins/$ID/messages" 200 '{"message_type":"timestamp.generate","payload":{}}'
jq -e '.payload_json | fromjson | .version == "2.0.0" and has("formatted_time")' "$RESPONSE_FILE" >/dev/null
request POST "/api/plugins/$ID/update" 400 '{"version":"9.0.0","binary_path":"/does/not/exist"}'
request GET "/api/plugins/$ID" 200
jq -e '.runtime.version == "2.0.0" and .runtime.process_alive == true' "$RESPONSE_FILE" >/dev/null
printf 'PASS update\n'

#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ID="timestamp-crash"
trap 'cleanup_plugin "$ID"; cleanup_response' EXIT
register_plugin "$ID" timestamp 1.0.0 "$ROOT_DIR/dist/plugins/timestamp/v1.0.0/timestamp-plugin"
request POST "/api/plugins/$ID/start" 200
OLD_PID="$(jq -r '.runtime.pid' "$RESPONSE_FILE")"
kill -9 "$OLD_PID"
for _ in {1..60}; do
  body="$(curl -fsS "$BASE_URL/api/plugins/$ID")"
  NEW_PID="$(jq -r '.runtime.pid' <<<"$body")"
  COUNT="$(jq -r '.runtime.restart_count' <<<"$body")"
  [[ "$NEW_PID" -gt 0 && "$NEW_PID" != "$OLD_PID" && "$COUNT" -ge 1 ]] && break
  sleep 0.25
done
[[ "$NEW_PID" -gt 0 && "$NEW_PID" != "$OLD_PID" && "$COUNT" -ge 1 ]]
request GET "/api/plugins/$ID/events?limit=100" 200
jq -e '.events | any(.event_type == "PLUGIN_CRASHED")' "$RESPONSE_FILE" >/dev/null
request GET "/api/plugins/$ID/restarts" 200
jq -e '.restarts | any(.success == true)' "$RESPONSE_FILE" >/dev/null
request POST "/api/plugins/$ID/messages" 200 '{"message_type":"timestamp.generate","payload":{}}'
request POST "/api/plugins/$ID/stop" 200
sleep 4
request GET "/api/plugins/$ID" 200
jq -e '.runtime.observed_state == "STOPPED" and .runtime.pid == 0' "$RESPONSE_FILE" >/dev/null
printf 'PASS crash recovery\n'

#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
TIMESTAMP_ID="timestamp-communication"
HEARTBEAT_ID="heartbeat-communication"
trap 'cleanup_plugin "$TIMESTAMP_ID"; cleanup_plugin "$HEARTBEAT_ID"; cleanup_response' EXIT
SSE_OUTPUT="$(curl -sN --max-time 1 "$BASE_URL/api/monitor/stream" || true)"
grep -q '^event:snapshot$' <<<"$SSE_OUTPUT"
register_plugin "$TIMESTAMP_ID" timestamp 1.0.0 "$ROOT_DIR/dist/plugins/timestamp/v1.0.0/timestamp-plugin"
request POST "/api/plugins/$TIMESTAMP_ID/start" 200
request POST "/api/plugins/$TIMESTAMP_ID/messages" 200 '{"message_type":"timestamp.generate","payload":{}}'
jq -er '.success == true and (.payload_json | fromjson | .timestamp_ms > 0)' "$RESPONSE_FILE" >/dev/null
request GET "/api/plugins/$TIMESTAMP_ID/interactions?kind=REQUEST_RESPONSE" 200
jq -e '.interactions | length >= 1 and all(.kind == "REQUEST_RESPONSE" and .initiator == "MASTER")' "$RESPONSE_FILE" >/dev/null
register_plugin "$HEARTBEAT_ID" heartbeat 1.0.0 "$ROOT_DIR/dist/plugins/heartbeat/v1.0.0/heartbeat-plugin"
request POST "/api/plugins/$HEARTBEAT_ID/start" 200
for _ in {1..30}; do
  reports="$(curl -fsS "$BASE_URL/api/plugins/$HEARTBEAT_ID/interactions?kind=ACTIVE_REPORT" | jq '.interactions | length')"
  [[ "$reports" -ge 1 ]] && break
  sleep 0.25
done
[[ "$reports" -ge 1 ]]
request POST "/api/plugins/$HEARTBEAT_ID/messages" 200 '{"message_type":"heartbeat.set_interval","payload":{"interval_seconds":1}}'
before="$reports"
sleep 3
request GET "/api/plugins/$HEARTBEAT_ID/interactions?kind=ACTIVE_REPORT" 200
after="$(jq '.interactions | length' "$RESPONSE_FILE")"
[[ $((after-before)) -ge 2 ]]
jq -e '.interactions | all(.direction == "PLUGIN_TO_MASTER" and .initiator == "PLUGIN" and .response_payload == "{\"ack\":true}")' "$RESPONSE_FILE" >/dev/null
printf 'PASS communication\n'

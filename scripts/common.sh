#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:16060}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

request() {
  local method="$1" path="$2" expected="$3" data="${4:-}"
  RESPONSE_FILE="$(mktemp)"
  if [[ -n "$data" ]]; then
    HTTP_STATUS="$(curl -sS -o "$RESPONSE_FILE" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' --data "$data" "$BASE_URL$path")"
  else
    HTTP_STATUS="$(curl -sS -o "$RESPONSE_FILE" -w '%{http_code}' -X "$method" "$BASE_URL$path")"
  fi
  if [[ "$HTTP_STATUS" != "$expected" ]]; then
    printf 'FAIL %s %s: HTTP %s, body=%s\n' "$method" "$path" "$HTTP_STATUS" "$(cat "$RESPONSE_FILE")" >&2
    rm -f "$RESPONSE_FILE"
    exit 1
  fi
}

cleanup_response() {
  rm -f "${RESPONSE_FILE:-}"
}

wait_state() {
  local id="$1" state="$2"
  for _ in {1..40}; do
    local current
    current="$(curl -fsS "$BASE_URL/api/plugins/$id" 2>/dev/null | jq -r '.runtime.observed_state' || true)"
    [[ "$current" == "$state" ]] && return 0
    sleep 0.25
  done
  printf 'FAIL plugin %s did not reach %s\n' "$id" "$state" >&2
  return 1
}

cleanup_plugin() {
  local id="$1"
  curl -sS -X POST "$BASE_URL/api/plugins/$id/stop" >/dev/null 2>&1 || true
  curl -sS -X DELETE "$BASE_URL/api/plugins/$id" >/dev/null 2>&1 || true
}

register_plugin() {
  local id="$1" type="$2" version="$3" binary="$4"
  cleanup_plugin "$id"
  request POST /api/plugins 201 "$(jq -nc --arg id "$id" --arg type "$type" --arg version "$version" --arg binary "$binary" '{id:$id,type:$type,version:$version,binary_path:$binary,restart:{enabled:true,max_retries:5,initial_delay_seconds:1,max_delay_seconds:4,stable_reset_seconds:10}}')"
  cleanup_response
}

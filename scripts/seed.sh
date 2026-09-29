#!/usr/bin/env bash
# End-to-end demo: generate a JWT and fire one of each channel + a small batch.
set -euo pipefail

API_URL="${API_URL:-http://localhost:8080}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOKEN="$("$SCRIPT_DIR/gen_jwt.sh" 3600)"

echo "using token: ${TOKEN:0:20}..."
echo

hit() {
  local endpoint="$1"; local body="$2"; local method="${3:-POST}"
  curl -sS -X "$method" "$API_URL$endpoint" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Idempotency-Key: $(uuidgen)" \
    -H "Content-Type: application/json" \
    -d "$body" | jq .
  echo
}

echo "--- single SMS ---"
hit /v1/notifications '{
  "recipient":"+905551234567",
  "channel":"sms",
  "priority":"high",
  "content":{"text":"hello from notifd"}
}'

echo "--- single email ---"
hit /v1/notifications '{
  "recipient":"demo@example.com",
  "channel":"email",
  "priority":"normal",
  "content":{"subject":"hello","body":"from notifd"}
}'

echo "--- batch of 3 push ---"
hit /v1/notifications/batch '{
  "channel":"push",
  "priority":"low",
  "content":{"title":"hello","body":"from notifd batch"},
  "items":[
    {"recipient":"push-token-0000000000000000abcd1"},
    {"recipient":"push-token-0000000000000000abcd2"},
    {"recipient":"push-token-0000000000000000abcd3"}
  ]
}'

echo "--- list most recent ---"
curl -sS -H "Authorization: Bearer $TOKEN" "$API_URL/v1/notifications?limit=10" | jq .

#!/usr/bin/env bash
# Smoke-test the rate limiter by submitting N notifications on a channel.
# Usage: ./scripts/load_test.sh [count] [channel]
# Env:   API_URL (default http://localhost:8080), JWT (auto-generated if empty)
set -euo pipefail

COUNT="${1:-500}"
CHANNEL="${2:-sms}"
API_URL="${API_URL:-http://localhost:8080}"
TOKEN="${JWT:-}"

if [[ -z "$TOKEN" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  TOKEN="$("$SCRIPT_DIR/gen_jwt.sh" 3600)"
fi

echo "POSTing $COUNT $CHANNEL notifications to $API_URL ..."

case "$CHANNEL" in
  sms)   CONTENT='{"text":"load-test hi"}' ;;
  email) CONTENT='{"subject":"load","body":"load-test hi"}' ;;
  push)  CONTENT='{"title":"load","body":"load-test hi"}' ;;
  *) echo "unknown channel: $CHANNEL"; exit 2 ;;
esac

case "$CHANNEL" in
  sms)   RECIPIENT='+905551234567' ;;
  email) RECIPIENT='loadtest@example.com' ;;
  push)  RECIPIENT='push-token-demo-0000000000000000' ;;
esac

START=$(date +%s)
for i in $(seq 1 "$COUNT"); do
  IDEM="load-$(date +%s%N)-$i"
  curl -sS -o /dev/null \
    -X POST "$API_URL/v1/notifications" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Idempotency-Key: $IDEM" \
    -H "Content-Type: application/json" \
    -d "$(jq -n \
      --arg r "$RECIPIENT" --arg c "$CHANNEL" --argjson content "$CONTENT" \
      '{recipient:$r, channel:$c, priority:"normal", content:$content}')" &
  if (( i % 50 == 0 )); then
    wait
    echo "  queued $i / $COUNT"
  fi
done
wait
END=$(date +%s)

echo "done in $((END - START))s"
echo "scrape /metrics to confirm throttle:"
echo "  curl -s $API_URL/metrics | grep notifd_notifications_sent_total"

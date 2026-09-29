#!/usr/bin/env bash
# Generate a short-lived HS256 JWT for local demos.
# Usage: JWT_SECRET=dev-secret-change-me SUBJECT=mahir ./scripts/gen_jwt.sh [ttl_seconds]
#
# Prints just the token to stdout so you can do:
#   export TOKEN="$(./scripts/gen_jwt.sh)"
#   curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/...
set -euo pipefail

TTL="${1:-3600}"
SECRET="${JWT_SECRET:-dev-secret-change-me}"
SUBJECT="${SUBJECT:-demo}"
ISSUER="${JWT_ISSUER:-notifd}"

b64() { openssl base64 -e -A | tr '+/' '-_' | tr -d '='; }

NOW=$(date +%s)
EXP=$(( NOW + TTL ))

HEADER_JSON='{"alg":"HS256","typ":"JWT"}'
PAYLOAD_JSON=$(printf '{"iss":"%s","sub":"%s","iat":%d,"exp":%d}' "$ISSUER" "$SUBJECT" "$NOW" "$EXP")

HEADER=$(printf '%s' "$HEADER_JSON" | b64)
PAYLOAD=$(printf '%s' "$PAYLOAD_JSON" | b64)
UNSIGNED="${HEADER}.${PAYLOAD}"
SIG=$(printf '%s' "$UNSIGNED" | openssl dgst -sha256 -hmac "$SECRET" -binary | b64)

echo "${UNSIGNED}.${SIG}"

#!/usr/bin/env sh
set -eu

base_url="${API_BASE_URL:-http://localhost:8080}"
api_key="${API_KEY:-local-api-key}"

curl --fail --silent --show-error "$base_url/healthz"
curl --fail --silent --show-error \
  -H "X-API-Key: $api_key" \
  -H "Content-Type: application/json" \
  -d '{"customer_id":"smoke-customer","message":"Where is my order?","order_id":"smoke-123"}' \
  "$base_url/v1/tickets"


#!/usr/bin/env bash
# deploy/seed_channels.sh
# Idempotently provisions a fresh SV Gateway instance:
#   - Sets system options (frontend theme, groups, self-use mode)
#   - Creates access tokens
#   - Creates upstream channels
#
# Usage:
#   bash deploy/seed_channels.sh
#   GATEWAY_URL=https://gateway.storyverseai.art bash deploy/seed_channels.sh
#
# Reads all secrets from deploy/.env.prod (or from the current env).
# Re-running is safe — existing tokens/channels are skipped.

set -euo pipefail

# ── Load .env.prod if present ────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env.prod"
if [[ -f "${ENV_FILE}" ]]; then
  # Export variables, skip comment lines and blank lines
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
  echo "[seed] Loaded env from ${ENV_FILE}"
else
  echo "[seed] Warning: ${ENV_FILE} not found — relying on existing environment."
fi

# ── Config ───────────────────────────────────────────────────────────────────
GATEWAY_URL="${GATEWAY_URL:-http://localhost:3000}"
GATEWAY_ROOT_USERNAME="${GATEWAY_ROOT_USERNAME:-root}"
FAL_API_KEY="${FAL_API_KEY:-${FAL_KEY:-}}"
BYTEPLUS_SEEDANCE_ENDPOINT="${BYTEPLUS_SEEDANCE_ENDPOINT:-https://ark.ap-southeast.bytepluses.com/api/v3/contents/generations/tasks}"
SEEDANCE_25_ENDPOINT_ID="${SEEDANCE_25_ENDPOINT_ID:-dreamina-seedance-2-5-260628}"
COOKIE_JAR="$(mktemp /tmp/sv-seed-cookies.XXXXXX)"
trap 'rm -f "${COOKIE_JAR}"' EXIT

echo "[seed] Gateway URL: ${GATEWAY_URL}"

# ── Validate required keys ───────────────────────────────────────────────────
MISSING=()
for var in \
  TOKENROUTER_API_KEY \
  APIMART_API_KEY \
  BYTEPLUS_ARK_API_KEY \
  BYTEPLUS_ACCESS_KEY \
  BYTEPLUS_SECRET_KEY \
  BYTEPLUS_GROUP_ID \
  SEEDREAM_LITE_ENDPOINT_ID \
  SEEDANCE_20_ENDPOINT_ID \
  FAL_API_KEY; do
  if [[ -z "${!var:-}" ]]; then
    MISSING+=("${var}")
  fi
done

# Need at least one of these auth methods
if [[ -z "${GATEWAY_ROOT_PASSWORD:-}" && -z "${GATEWAY_ROOT_ACCESS_TOKEN:-}" ]]; then
  MISSING+=("GATEWAY_ROOT_PASSWORD or GATEWAY_ROOT_ACCESS_TOKEN")
fi

if [[ ${#MISSING[@]} -gt 0 ]]; then
  echo "[seed] ERROR: The following required env vars are not set:" >&2
  for v in "${MISSING[@]}"; do echo "  - ${v}" >&2; done
  exit 1
fi

# ── Auth helpers ─────────────────────────────────────────────────────────────
json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

# Build the common curl options (auth header or cookie jar)
AUTH_ARGS=()
if [[ -n "${GATEWAY_ROOT_ACCESS_TOKEN:-}" ]]; then
  echo "[seed] Authenticating via GATEWAY_ROOT_ACCESS_TOKEN"
	AUTH_ARGS=(-H "Authorization: Bearer ${GATEWAY_ROOT_ACCESS_TOKEN}" -H "New-Api-User: 1")
else
  echo "[seed] Authenticating via password login as ${GATEWAY_ROOT_USERNAME}"
  LOGIN_BODY="{\"username\":\"$(json_escape "${GATEWAY_ROOT_USERNAME}")\",\"password\":\"$(json_escape "${GATEWAY_ROOT_PASSWORD}")\"}"
  LOGIN_RESP=$(curl -s -w "\n%{http_code}" \
    -c "${COOKIE_JAR}" \
    -X POST "${GATEWAY_URL}/api/user/login" \
    -H "Content-Type: application/json" \
    -H "New-Api-User: 1" \
    -d "${LOGIN_BODY}")
  HTTP_CODE="${LOGIN_RESP##*$'\n'}"
  LOGIN_RESP_BODY="${LOGIN_RESP%$'\n'*}"
  if [[ "${HTTP_CODE}" != "200" ]]; then
    echo "[seed] ERROR: Login failed (HTTP ${HTTP_CODE}). Check GATEWAY_ROOT_PASSWORD." >&2
    exit 1
  fi
  if ! echo "${LOGIN_RESP_BODY}" | grep -q '"success":true'; then
    echo "[seed] ERROR: Login failed. Check GATEWAY_ROOT_USERNAME and GATEWAY_ROOT_PASSWORD." >&2
    echo "[seed] Response: ${LOGIN_RESP_BODY}" >&2
    exit 1
  fi
  AUTH_ARGS=(-b "${COOKIE_JAR}" -H "New-Api-User: 1")
  echo "[seed] Login successful."
fi

# Wrapper: authenticated GET, returns response body
api_get() {
  local path="$1"
  curl -s "${AUTH_ARGS[@]}" "${GATEWAY_URL}${path}"
}

# Wrapper: authenticated PUT, takes JSON body string
api_put() {
  local path="$1"
  local body="$2"
  curl -s -X PUT "${AUTH_ARGS[@]}" \
    -H "Content-Type: application/json" \
    -d "${body}" \
    "${GATEWAY_URL}${path}"
}

api_put_expect_success() {
  local description="$1"
  local path="$2"
  local body="$3"
  local resp
  resp="$(api_put "${path}" "${body}")"
  if ! echo "${resp}" | grep -q '"success":true'; then
    echo "[seed] ERROR: Failed to ${description}: ${resp}" >&2
    exit 1
  fi
  echo "${resp}" | grep -o '"message":"[^"]*"' || true
}

# Wrapper: authenticated POST, takes JSON body string, returns response body
api_post() {
  local path="$1"
  local body="$2"
  curl -s -X POST "${AUTH_ARGS[@]}" \
    -H "Content-Type: application/json" \
    -d "${body}" \
    "${GATEWAY_URL}${path}"
}

# ── 1. Set system options ─────────────────────────────────────────────────────
echo ""
echo "[seed] === Setting system options ==="

# SelfUseModeEnabled — bypasses per-model pricing check (required for channels to work)
echo "[seed] Setting SelfUseModeEnabled=true ..."
api_put_expect_success "set SelfUseModeEnabled" "/api/option/" '{"key":"SelfUseModeEnabled","value":"true"}'

# Frontend theme — SV deployment uses the newer default UI; routing/admin
# panels such as /models/routing are maintained there first.
echo "[seed] Setting theme.frontend=default ..."
api_put_expect_success "set theme.frontend" "/api/option/" '{"key":"theme.frontend","value":"default"}'

# GroupRatio — register groups with ratio 1
echo "[seed] Setting GroupRatio for sv-monorepo, bragi-canvas ..."
api_put_expect_success "set GroupRatio" "/api/option/" '{"key":"GroupRatio","value":"{\"sv-monorepo\":1,\"bragi-canvas\":1}"}'

# UserUsableGroups — make both groups available to users
echo "[seed] Setting UserUsableGroups ..."
api_put_expect_success "set UserUsableGroups" "/api/option/" '{"key":"UserUsableGroups","value":"{\"sv-monorepo\":\"sv-monorepo\",\"bragi-canvas\":\"bragi-canvas\"}"}'

echo "[seed] System options set."

# ── 2. Create tokens ─────────────────────────────────────────────────────────
echo ""
echo "[seed] === Creating access tokens ==="

# Fetch existing tokens to enable idempotency
EXISTING_TOKENS=$(api_get "/api/token/?p=0&size=100")

create_token_if_missing() {
  local name="$1"
  local body="$2"
  if echo "${EXISTING_TOKENS}" | grep -q "\"name\":\"${name}\""; then
    echo "[seed] Token '${name}' already exists — skipping."
  else
    RESP=$(api_post "/api/token/" "${body}")
    if echo "${RESP}" | grep -q '"success":true'; then
      echo "[seed] Token '${name}' created."
    else
      echo "[seed] WARNING: Failed to create token '${name}': ${RESP}" >&2
    fi
  fi
}

# sv-monorepo-token: unlimited quota, no expiry, group sv-monorepo
create_token_if_missing "sv-monorepo-token" '{
  "name": "sv-monorepo-token",
  "remain_quota": -1,
  "unlimited_quota": true,
  "expired_time": -1,
  "group": "sv-monorepo"
}'

# bragi-canvas-token: unlimited quota, no expiry, group bragi-canvas
create_token_if_missing "bragi-canvas-token" '{
  "name": "bragi-canvas-token",
  "remain_quota": -1,
  "unlimited_quota": true,
  "expired_time": -1,
  "group": "bragi-canvas"
}'

# ── 3. Create channels ────────────────────────────────────────────────────────
echo ""
echo "[seed] === Creating channels ==="

# Fetch existing channels to enable idempotency
EXISTING_CHANNELS=$(api_get "/api/channel/?p=0&size=100")

get_channel_id_by_name() {
  local name="$1"
  local row
  row="$(
    printf '%s' "${EXISTING_CHANNELS}" |
      tr '{' '\n' |
      grep "\"name\":\"${name}\"" |
      head -n 1 || true
  )"
  if [[ -z "${row}" ]]; then
    return 0
  fi
  printf '%s' "${row}" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p'
}

upsert_channel() {
  local name="$1"
  local legacy_names="$2"
  local channel_json="$3"
  local id
  id="$(get_channel_id_by_name "${name}")"
  if [[ -z "${id}" && -n "${legacy_names}" ]]; then
    local legacy_name
    IFS=',' read -ra legacy_name_array <<< "${legacy_names}"
    for legacy_name in "${legacy_name_array[@]}"; do
      legacy_name="${legacy_name// /}"
      if [[ -z "${legacy_name}" ]]; then
        continue
      fi
      id="$(get_channel_id_by_name "${legacy_name}")"
      if [[ -n "${id}" ]]; then
        echo "[seed] Channel '${legacy_name}' exists — updating to '${name}'."
        break
      fi
    done
  fi

  if [[ -n "${id}" ]]; then
    local update_body="{\"id\":${id},${channel_json#\{}"
    RESP=$(api_put "/api/channel/" "${update_body}")
    if echo "${RESP}" | grep -q '"success":true'; then
      echo "[seed] Channel '${name}' updated."
    else
      echo "[seed] WARNING: Failed to update channel '${name}': ${RESP}" >&2
    fi
  else
    RESP=$(api_post "/api/channel/" "{\"mode\":\"single\",\"channel\":${channel_json}}")
    if echo "${RESP}" | grep -q '"success":true'; then
      echo "[seed] Channel '${name}' created."
    else
      echo "[seed] WARNING: Failed to create channel '${name}': ${RESP}" >&2
    fi
  fi
}

disable_legacy_channels() {
  local canonical_name="$1"
  local legacy_names="$2"

  EXISTING_CHANNELS=$(api_get "/api/channel/?p=0&size=100")

  local legacy_name id resp
  IFS=',' read -ra legacy_name_array <<< "${legacy_names}"
  for legacy_name in "${legacy_name_array[@]}"; do
    legacy_name="${legacy_name// /}"
    if [[ -z "${legacy_name}" || "${legacy_name}" == "${canonical_name}" ]]; then
      continue
    fi

    id="$(get_channel_id_by_name "${legacy_name}")"
    if [[ -z "${id}" ]]; then
      continue
    fi

    resp="$(api_put "/api/channel/" "{\"id\":${id},\"status\":2}")"
    if echo "${resp}" | grep -q '"success":true'; then
      echo "[seed] Legacy channel '${legacy_name}' disabled after '${canonical_name}' migration."
    else
      echo "[seed] WARNING: Failed to disable legacy channel '${legacy_name}': ${resp}" >&2
    fi
  done
}

# Channel 1: tokenrouter (type=1, OpenAI-compatible)
upsert_channel "tokenrouter" "tokenrouter-gpt55" "{
  \"name\": \"tokenrouter\",
  \"type\": 1,
  \"key\": \"${TOKENROUTER_API_KEY}\",
  \"base_url\": \"https://api.tokenrouter.com\",
  \"models\": \"openai/gpt-5.5,sv-gpt-5.5\",
  \"model_mapping\": \"{\\\"sv-gpt-5.5\\\":\\\"openai/gpt-5.5\\\"}\",
  \"group\": \"sv-monorepo,bragi-canvas\",
  \"priority\": 100,
  \"weight\": 100,
  \"status\": 1
}"
disable_legacy_channels "tokenrouter" "tokenrouter-gpt55"

# Channel 2: byteplus (type=45, BytePlus Ark Seedance)
# `setting` carries the BytePlus asset-library credentials (AK/SK + group) used by the
# /v1/assets proxy to register real-person/live-action reference media as asset://. The
# `key` above is the Ark Bearer token for generation; the asset API is signed with AK/SK.
upsert_channel "byteplus" "byteplus-seedance-2,byteplus-seedream-lite" "{
  \"name\": \"byteplus\",
  \"type\": 45,
  \"key\": \"${BYTEPLUS_ARK_API_KEY}\",
  \"base_url\": \"https://ark.ap-southeast.bytepluses.com\",
  \"models\": \"${SEEDREAM_LITE_ENDPOINT_ID},sv-seedream-5.0-lite,${SEEDANCE_20_ENDPOINT_ID},sv-seedance-2.0,${SEEDANCE_25_ENDPOINT_ID},sv-seedance-2.5\",
  \"model_mapping\": \"{\\\"sv-seedream-5.0-lite\\\":\\\"${SEEDREAM_LITE_ENDPOINT_ID}\\\",\\\"sv-seedance-2.0\\\":\\\"${SEEDANCE_20_ENDPOINT_ID}\\\",\\\"sv-seedance-2.5\\\":\\\"${SEEDANCE_25_ENDPOINT_ID}\\\"}\",
  \"setting\": \"{\\\"byteplus_access_key\\\":\\\"${BYTEPLUS_ACCESS_KEY}\\\",\\\"byteplus_secret_key\\\":\\\"${BYTEPLUS_SECRET_KEY}\\\",\\\"byteplus_asset_group_id\\\":\\\"${BYTEPLUS_GROUP_ID}\\\",\\\"byteplus_seedance_endpoint\\\":\\\"${BYTEPLUS_SEEDANCE_ENDPOINT}\\\"}\",
  \"group\": \"sv-monorepo,bragi-canvas\",
  \"priority\": 110,
  \"weight\": 100,
  \"status\": 1
}"
disable_legacy_channels "byteplus" "byteplus-seedance-2,byteplus-seedream-lite"

# Channel 3: apimart (type=58, APIMart image generation)
upsert_channel "apimart" "apimart-images" "{
  \"name\": \"apimart\",
  \"type\": 58,
  \"key\": \"${APIMART_API_KEY}\",
  \"base_url\": \"https://api.apimart.ai\",
  \"models\": \"gpt-image-2,gpt-image-2-official,gpt-image-2.5-flare,gpt-image-2.5-sunburst,gemini-3-pro-image-preview,doubao-seedream-5-0-lite,sv-gpt-image-2,sv-gpt-image-2-official,sv-gpt-image-2.5-flare,sv-gpt-image-2.5-sunburst,sv-nano-banana-pro,sv-seedream-5.0-lite\",
  \"model_mapping\": \"{\\\"sv-gpt-image-2\\\":\\\"gpt-image-2\\\",\\\"sv-gpt-image-2-official\\\":\\\"gpt-image-2-official\\\",\\\"sv-gpt-image-2.5-flare\\\":\\\"gpt-image-2.5-flare\\\",\\\"sv-gpt-image-2.5-sunburst\\\":\\\"gpt-image-2.5-sunburst\\\",\\\"sv-nano-banana-pro\\\":\\\"gemini-3-pro-image-preview\\\",\\\"sv-seedream-5.0-lite\\\":\\\"doubao-seedream-5-0-lite\\\"}\",
  \"group\": \"sv-monorepo,bragi-canvas\",
  \"priority\": 100,
  \"weight\": 100,
  \"status\": 1
}"
disable_legacy_channels "apimart" "apimart-images"

# Optional Reactor bridge: upstream credentials remain in the official SDK worker.
if [[ -n "${REACTOR_WORKER_SECRET:-}" ]]; then
  upsert_channel "reactor" "" "{
    \"name\": \"reactor\",
    \"type\": 60,
    \"key\": \"${REACTOR_WORKER_SECRET}\",
    \"base_url\": \"${REACTOR_WORKER_URL:-http://reactor-worker:8091}\",
    \"models\": \"reactor/fast-h3-omni,sv-fast-h3-omni\",
    \"model_mapping\": \"{\\\"sv-fast-h3-omni\\\":\\\"reactor/fast-h3-omni\\\"}\",
    \"group\": \"sv-monorepo,bragi-canvas\",
    \"priority\": 100,
    \"weight\": 100,
    \"status\": 1
  }"
else
  echo "[seed] Reactor skipped: REACTOR_WORKER_SECRET is not configured."
fi


# Channel 4: fal (type=59, FAL audio + async video)
upsert_channel "fal" "fal-media" "{
  \"name\": \"fal\",
  \"type\": 59,
  \"key\": \"${FAL_API_KEY}\",
  \"base_url\": \"https://fal.run\",
  \"models\": \"sv-kling-3.0,fal-ai/kling-video/v3/pro,sv-seedance-2.0,fal-ai/seedance-2/reference-to-video,sv-sora-2,fal-ai/sora-2/image-to-video,sv-grok-video,xai/grok-imagine-video,sv-veo-3.1,fal-ai/veo3.1,sv-elevenlabs-tts-v3,fal-ai/elevenlabs/tts/eleven-v3,sv-minimax-tts,fal-ai/minimax/speech-2.8-hd,sv-elevenlabs-sfx,fal-ai/elevenlabs/sound-effects/v2\",
  \"model_mapping\": \"{\\\"sv-kling-3.0\\\":\\\"fal-ai/kling-video/v3/pro\\\",\\\"sv-seedance-2.0\\\":\\\"fal-ai/seedance-2/reference-to-video\\\",\\\"sv-sora-2\\\":\\\"fal-ai/sora-2/image-to-video\\\",\\\"sv-grok-video\\\":\\\"xai/grok-imagine-video\\\",\\\"sv-veo-3.1\\\":\\\"fal-ai/veo3.1\\\",\\\"sv-elevenlabs-tts-v3\\\":\\\"fal-ai/elevenlabs/tts/eleven-v3\\\",\\\"sv-minimax-tts\\\":\\\"fal-ai/minimax/speech-2.8-hd\\\",\\\"sv-elevenlabs-sfx\\\":\\\"fal-ai/elevenlabs/sound-effects/v2\\\"}\",
  \"group\": \"sv-monorepo,bragi-canvas\",
  \"priority\": 100,
  \"weight\": 100,
  \"status\": 1
}"
disable_legacy_channels "fal" "fal-media"

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "[seed] =============================="
echo "[seed] Seeding complete."
echo "[seed] Run the following to verify the gateway is working:"
echo ""
echo "  # Get a token key from the admin panel and test:"
echo "  curl -s ${GATEWAY_URL}/v1/chat/completions \\"
echo "    -H 'Authorization: Bearer <sv-monorepo-token-key>' \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"sv-gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}'"
echo ""
echo "  # fal audio smoke test:"
echo "  curl -i ${GATEWAY_URL}/v1/audio/speech \\"
echo "    -H 'Authorization: Bearer <sv-monorepo-token-key>' \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"sv-minimax-tts\",\"input\":\"ping from local gateway\",\"voice\":\"female-shaonv\"}'"
echo "[seed] =============================="

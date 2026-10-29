#!/usr/bin/env bash
# deploy/seed_billing_rules.sh
# Idempotently writes provider/model billing rules into a running gateway.
#
# Usage:
#   GATEWAY_URL=http://localhost:3000 GATEWAY_ROOT_PASSWORD=... bash deploy/seed_billing_rules.sh
#   GATEWAY_URL=https://gateway... GATEWAY_ROOT_ACCESS_TOKEN=... bash deploy/seed_billing_rules.sh
#
# Optional:
#   BILLING_RULES_FILE=deploy/my-billing-rules.json bash deploy/seed_billing_rules.sh
#
# The override file shape is:
#   {
#     "rules": {"channel_name:fal:sv-veo-3.1": "per_second"},
#     "params": {"channel_name:fal:sv-veo-3.1": {"default_seconds": 8}}
#   }

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env.prod"
if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
  echo "[billing-seed] Loaded env from ${ENV_FILE}"
else
  echo "[billing-seed] Warning: ${ENV_FILE} not found — relying on existing environment."
fi

GATEWAY_URL="${GATEWAY_URL:-http://localhost:3000}"
GATEWAY_ROOT_USERNAME="${GATEWAY_ROOT_USERNAME:-root}"
COOKIE_JAR="$(mktemp /tmp/sv-billing-seed-cookies.XXXXXX)"
trap 'rm -f "${COOKIE_JAR}"' EXIT

command -v curl >/dev/null || { echo "[billing-seed] ERROR: curl is required." >&2; exit 1; }
command -v jq >/dev/null || { echo "[billing-seed] ERROR: jq is required." >&2; exit 1; }

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

AUTH_ARGS=()
if [[ -n "${GATEWAY_ROOT_ACCESS_TOKEN:-}" ]]; then
  echo "[billing-seed] Authenticating via GATEWAY_ROOT_ACCESS_TOKEN"
  AUTH_ARGS=(-H "Authorization: Bearer ${GATEWAY_ROOT_ACCESS_TOKEN}" -H "New-Api-User: 1")
elif [[ -n "${GATEWAY_ROOT_PASSWORD:-}" ]]; then
  echo "[billing-seed] Authenticating via password login as ${GATEWAY_ROOT_USERNAME}"
  LOGIN_BODY="{\"username\":\"$(json_escape "${GATEWAY_ROOT_USERNAME}")\",\"password\":\"$(json_escape "${GATEWAY_ROOT_PASSWORD}")\"}"
  LOGIN_RESP="$(curl -s -w "\n%{http_code}" \
    -c "${COOKIE_JAR}" \
    -X POST "${GATEWAY_URL}/api/user/login" \
    -H "Content-Type: application/json" \
    -H "New-Api-User: 1" \
    -d "${LOGIN_BODY}")"
  HTTP_CODE="${LOGIN_RESP##*$'\n'}"
  LOGIN_RESP_BODY="${LOGIN_RESP%$'\n'*}"
  if [[ "${HTTP_CODE}" != "200" ]] || ! printf '%s' "${LOGIN_RESP_BODY}" | grep -q '"success":true'; then
    echo "[billing-seed] ERROR: login failed (HTTP ${HTTP_CODE}): ${LOGIN_RESP_BODY}" >&2
    exit 1
  fi
  AUTH_ARGS=(-b "${COOKIE_JAR}" -H "New-Api-User: 1")
  echo "[billing-seed] Login successful."
else
  echo "[billing-seed] ERROR: set GATEWAY_ROOT_PASSWORD or GATEWAY_ROOT_ACCESS_TOKEN." >&2
  exit 1
fi

api_put() {
  local path="$1"
  local body="$2"
  curl -s -X PUT "${AUTH_ARGS[@]}" \
    -H "Content-Type: application/json" \
    -d "${body}" \
    "${GATEWAY_URL}${path}"
}

put_option() {
  local key="$1"
  local value_json="$2"
  local body resp
  body="$(jq -cn --arg key "${key}" --arg value "${value_json}" '{key:$key,value:$value}')"
  resp="$(api_put "/api/option/" "${body}")"
  if ! printf '%s' "${resp}" | grep -q '"success":true'; then
    echo "[billing-seed] ERROR: failed to set ${key}: ${resp}" >&2
    exit 1
  fi
  echo "[billing-seed] Set ${key}"
}

if [[ -n "${BILLING_RULES_FILE:-}" ]]; then
  [[ -f "${BILLING_RULES_FILE}" ]] || { echo "[billing-seed] ERROR: ${BILLING_RULES_FILE} not found." >&2; exit 1; }
  RULES_JSON="$(jq -c '.rules' "${BILLING_RULES_FILE}")"
  PARAMS_JSON="$(jq -c '.params' "${BILLING_RULES_FILE}")"
else
  RULES_JSON="$(jq -cn '{
    "channel_name:byteplus:sv-seedream-5.0-lite": "fixed_image",
    "channel_name:byteplus:sv-seedance-2.0": "byteplus_seedance2",
    "channel_name:byteplus:sv-seedance-2.5": "byteplus_seedance2",
    "channel_name:apimart:sv-gpt-image-2": "fixed_image",
    "channel_name:apimart:sv-gpt-image-2-official": "image_quality_resolution",
    "channel_name:apimart:sv-nano-banana-pro": "fixed_image",
    "channel_name:apimart:sv-seedream-5.0-lite": "fixed_image",
    "channel_name:fal:sv-kling-3.0": "per_second",
    "channel_name:fal:sv-seedance-2.0": "per_second",
    "channel_name:fal:sv-sora-2": "per_second",
    "channel_name:fal:sv-grok-video": "per_second",
    "channel_name:fal:sv-veo-3.1": "per_second",
    "channel_name:fal:sv-elevenlabs-tts-v3": "per_character",
    "channel_name:fal:sv-minimax-tts": "per_character",
    "channel_name:fal:sv-elevenlabs-sfx": "per_second"
  }')"

  PARAMS_JSON="$(jq -cn \
    --argjson seedream "${SEEDREAM_LITE_PRICE_PER_IMAGE:-0.03}" \
    --argjson gpt_image "${APIMART_GPT_IMAGE_PRICE_PER_IMAGE:-0.08}" \
    --argjson banana "${APIMART_BANANA_PRO_PRICE_PER_IMAGE:-0.039}" \
    --argjson klingv3 "${FAL_KLING_V3_PRICE_PER_SECOND:-0.14}" \
    --argjson seedance "${FAL_SEEDANCE_PRICE_PER_SECOND:-0.10}" \
    --argjson sora "${FAL_SORA2_PRICE_PER_SECOND:-0.10}" \
    --argjson grok480 "${FAL_GROK_480P_PRICE_PER_SECOND:-0.05}" \
    --argjson grok720 "${FAL_GROK_720P_PRICE_PER_SECOND:-0.07}" \
    --argjson veo_off "${FAL_VEO_AUDIO_OFF_PRICE_PER_SECOND:-0.20}" \
    --argjson veo_on "${FAL_VEO_AUDIO_ON_PRICE_PER_SECOND:-0.40}" \
    --argjson veo4k_off "${FAL_VEO_4K_AUDIO_OFF_PRICE_PER_SECOND:-0.40}" \
    --argjson veo4k_on "${FAL_VEO_4K_AUDIO_ON_PRICE_PER_SECOND:-0.60}" \
    --argjson elevenlabs "${FAL_ELEVENLABS_PRICE_PER_1K_CHARACTERS:-0.10}" \
    --argjson minimax "${FAL_MINIMAX_PRICE_PER_1K_CHARACTERS:-0.10}" \
    --argjson sfx "${FAL_ELEVENLABS_SFX_PRICE_PER_SECOND:-0.002}" \
    '{
      "channel_name:byteplus:sv-seedream-5.0-lite": {
        "price_per_image": $seedream
      },
      "channel_name:byteplus:sv-seedance-2.0": {
        "default_seconds": 5,
        "default_resolution": "720p",
        "fps": 24
      },
      "channel_name:byteplus:sv-seedance-2.5": {
        "default_seconds": 5,
        "default_resolution": "720p",
        "fps": 24,
        "width_480p": 854,
        "height_480p": 480,
        "standard_720p_no_video_rate_per_m_tokens": 10.7,
        "standard_1080p_no_video_rate_per_m_tokens": 11.7,
        "standard_720p_video_input_rate_per_m_tokens": 6.4,
        "standard_1080p_video_input_rate_per_m_tokens": 7.0,
        "settle_by_completion_tokens": true
      },
      "channel_name:apimart:sv-gpt-image-2": {
        "price_per_image": $gpt_image
      },
      "channel_name:apimart:sv-gpt-image-2-official": {
        "default_quality": "medium",
        "default_resolution": "1k",
        "price_low_1k": 0.00304,
        "price_low_2k": 0.00392,
        "price_low_4k": 0.00904,
        "price_medium_1k": 0.026,
        "price_medium_2k": 0.03408,
        "price_medium_4k": 0.08024,
        "price_high_1k": 0.1036,
        "price_high_2k": 0.13576,
        "price_high_4k": 0.32032
      },
      "channel_name:apimart:sv-nano-banana-pro": {
        "price_per_image": $banana
      },
      "channel_name:apimart:sv-seedream-5.0-lite": {
        "price_per_image": $seedream
      },
      "channel_name:fal:sv-kling-3.0": {
        "default_seconds": 5,
        "price_per_second": $klingv3
      },
      "channel_name:fal:sv-seedance-2.0": {
        "default_seconds": 5,
        "default_generate_audio": true,
        "price_per_second": $seedance
      },
      "channel_name:fal:sv-sora-2": {
        "default_seconds": 4,
        "price_per_second": $sora
      },
      "channel_name:fal:sv-grok-video": {
        "default_seconds": 6,
        "default_resolution": "720p",
        "480p_price_per_second": $grok480,
        "720p_price_per_second": $grok720
      },
      "channel_name:fal:sv-veo-3.1": {
        "default_seconds": 8,
        "default_generate_audio": true,
        "audio_off_price_per_second": $veo_off,
        "audio_on_price_per_second": $veo_on,
        "audio_off_720p_price_per_second": $veo_off,
        "audio_on_720p_price_per_second": $veo_on,
        "audio_off_1080p_price_per_second": $veo_off,
        "audio_on_1080p_price_per_second": $veo_on,
        "audio_off_4k_price_per_second": $veo4k_off,
        "audio_on_4k_price_per_second": $veo4k_on
      },
      "channel_name:fal:sv-elevenlabs-tts-v3": {
        "price_per_1k_characters": $elevenlabs
      },
      "channel_name:fal:sv-minimax-tts": {
        "price_per_1k_characters": $minimax
      },
      "channel_name:fal:sv-elevenlabs-sfx": {
        "default_seconds": 1,
        "price_per_second": $sfx
      }
    }')"
fi

echo "[billing-seed] Gateway URL: ${GATEWAY_URL}"
echo "[billing-seed] Rules:"
printf '%s\n' "${RULES_JSON}" | jq .
echo "[billing-seed] Params:"
printf '%s\n' "${PARAMS_JSON}" | jq .

put_option "billing_setting.billing_rule" "${RULES_JSON}"
put_option "billing_setting.billing_rule_params" "${PARAMS_JSON}"

echo "[billing-seed] Done."

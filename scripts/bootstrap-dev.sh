#!/usr/bin/env bash
# DEV bootstrap: создаёт первого пользователя, получает токен, enrolls TOTP.
#
# Требование: identity service должен быть запущен с IDENTITY_DEV_SKIP_MFA=1
#
# Использование:
#   IDENTITY_DEV_SKIP_MFA=1 make run SERVICE=identity   # в другом терминале
#   ./scripts/bootstrap-dev.sh
set -euo pipefail

BASE="${IDENTITY_URL:-http://localhost:8086}"
EMAIL="${DEV_EMAIL:-admin@dev.local}"
PASSWORD="${DEV_PASSWORD:-admin123456789}"
ORG_ID="${DEV_ORG_ID:-000000000000}"

echo "=== DEV Bootstrap Identity ==="
echo "URL:      $BASE"
echo "Email:    $EMAIL"
echo ""

# 1. Регистрация
echo "--- 1. Registering user ---"
REG=$(curl -sf -w "\n%{http_code}" -X POST "$BASE/api/v1/auth/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"email\": \"$EMAIL\",
    \"password\": \"$PASSWORD\",
    \"full_name\": \"DEV Admin\",
    \"org_id\": \"$ORG_ID\",
    \"roles\": [\"operator\", \"reviewer\", \"admin\"]
  }") || true

HTTP_CODE=$(echo "$REG" | tail -1)
BODY=$(echo "$REG" | head -1)
echo "HTTP $HTTP_CODE: $BODY"
if [[ "$HTTP_CODE" != "201" && "$HTTP_CODE" != "409" ]]; then
  echo "ERROR: unexpected status $HTTP_CODE"
  exit 1
fi
if [[ "$HTTP_CODE" == "409" ]]; then
  echo "(user already exists, continuing...)"
fi

# 2. Login без TOTP (требует DEV_SKIP_MFA=1 в identity service)
echo ""
echo "--- 2. Login (DEV_SKIP_MFA required) ---"
LOGIN=$(curl -sf -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\": \"$EMAIL\", \"password\": \"$PASSWORD\", \"totp_code\": \"\"}")

ACCESS_TOKEN=$(echo "$LOGIN" | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])" 2>/dev/null || \
               echo "$LOGIN" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)

if [[ -z "$ACCESS_TOKEN" ]]; then
  echo "ERROR: Could not get access token. Is IDENTITY_DEV_SKIP_MFA=1 set?"
  echo "Response: $LOGIN"
  exit 1
fi
echo "Got access token (truncated): ${ACCESS_TOKEN:0:50}..."

# 3. TOTP Enrollment
echo ""
echo "--- 3. TOTP Enrollment ---"
ENROLL=$(curl -sf -X POST "$BASE/api/v1/auth/enroll-totp" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json")

OTP_URL=$(echo "$ENROLL" | python3 -c "import json,sys; print(json.load(sys.stdin)['otpauth_url'])" 2>/dev/null || \
          echo "$ENROLL" | grep -o '"otpauth_url":"[^"]*"' | cut -d'"' -f4)
BASE32=$(echo "$ENROLL" | python3 -c "import json,sys; print(json.load(sys.stdin)['base32'])" 2>/dev/null || \
         echo "$ENROLL" | grep -o '"base32":"[^"]*"' | cut -d'"' -f4)

echo "TOTP Secret (Base32): $BASE32"
echo "OTPAuth URL: $OTP_URL"
echo ""
echo "Scan QR in Google Authenticator or paste Base32 manually."
echo ""

# 4. Confirm TOTP
echo "--- 4. Confirm TOTP ---"
read -rp "Enter the 6-digit TOTP code from your authenticator: " TOTP_CODE

CONFIRM=$(curl -sf -X POST "$BASE/api/v1/auth/confirm-totp" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"totp_code\": \"$TOTP_CODE\"}")
echo "Confirm result: $CONFIRM"

# 5. Final login check
echo ""
echo "--- 5. Full login test ---"
FULL_LOGIN=$(curl -sf -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\": \"$EMAIL\", \"password\": \"$PASSWORD\", \"totp_code\": \"$(read -rp 'Enter TOTP code again: ' c; echo $c)\"}" || echo "")

echo "Login result: $FULL_LOGIN"
echo ""
echo "=== Bootstrap complete ==="
echo "User: $EMAIL / $PASSWORD"
echo "Add IDENTITY_DEV_SKIP_MFA=1 to .env for DEV (remove for production)."

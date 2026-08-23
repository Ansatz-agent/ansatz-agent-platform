#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CASE_ROOT="$ROOT/tmp/tests/voice-trace-secrets-$$"
mkdir -p "$CASE_ROOT"

output="$(ANSATZ_PLATFORM_ROOT="$CASE_ROOT" bash "$ROOT/scripts/bootstrap-voice-trace-secrets.sh")"
SECRETS_DIR="$CASE_ROOT/.secrets/voice-trace"
SERVER_ENV="$SECRETS_DIR/server.env"
ADMIN_FILE="$SECRETS_DIR/langfuse-admin-20260823.txt"

mode() {
  stat -f '%Lp' "$1"
}

field() {
  sed -n "s/^$1=//p" "$2"
}

[[ "$(mode "$SECRETS_DIR")" == 700 ]]
[[ "$(mode "$SERVER_ENV")" == 600 ]]
[[ "$(mode "$ADMIN_FILE")" == 600 ]]
[[ "$output" != *"PASSWORD="* ]]
[[ "$output" != *"SECRET="* ]]
[[ "$(field DJANGO_SECRET_KEY "$SERVER_ENV")" != "$(field TRACE_GATEWAY_INTERNAL_SECRET "$SERVER_ENV")" ]]
[[ "$(field POSTGRES_PASSWORD "$SERVER_ENV")" != "$(field CLICKHOUSE_PASSWORD "$SERVER_ENV")" ]]
[[ "$(field LANGFUSE_INIT_USER_EMAIL "$SERVER_ENV")" == trace-admin-20260823-* ]]
[[ -n "$(field LANGFUSE_INIT_USER_PASSWORD "$SERVER_ENV")" ]]
[[ "$(field LANGFUSE_INIT_USER_PASSWORD "$SERVER_ENV")" == "$(field PASSWORD "$ADMIN_FILE")" ]]
[[ "$(field LANGFUSE_INIT_PROJECT_PUBLIC_KEY "$SERVER_ENV")" == pk-lf-* ]]
[[ "$(field LANGFUSE_INIT_PROJECT_SECRET_KEY "$SERVER_ENV")" == sk-lf-* ]]

rtk proxy cp "$SERVER_ENV" "$CASE_ROOT/server.before"
rtk proxy cp "$ADMIN_FILE" "$CASE_ROOT/admin.before"
ANSATZ_PLATFORM_ROOT="$CASE_ROOT" bash "$ROOT/scripts/bootstrap-voice-trace-secrets.sh" >/dev/null
cmp -s "$SERVER_ENV" "$CASE_ROOT/server.before"
cmp -s "$ADMIN_FILE" "$CASE_ROOT/admin.before"

PARTIAL_ROOT="$CASE_ROOT/partial"
mkdir -p "$PARTIAL_ROOT/.secrets/voice-trace"
touch "$PARTIAL_ROOT/.secrets/voice-trace/server.env"
if ANSATZ_PLATFORM_ROOT="$PARTIAL_ROOT" bash "$ROOT/scripts/bootstrap-voice-trace-secrets.sh" >/dev/null 2>&1; then
  printf 'partial secret set was accepted\n' >&2
  exit 1
fi

printf 'voice trace secret bootstrap contract passed\n'

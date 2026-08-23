#!/usr/bin/env bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${ANSATZ_PLATFORM_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"
SECRETS_DIR="$ROOT/.secrets/voice-trace"
SERVER_ENV="$SECRETS_DIR/server.env"
ADMIN_FILE="$SECRETS_DIR/langfuse-admin-20260823.txt"

for command_name in openssl rtk; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'required command is unavailable: %s\n' "$command_name" >&2
    exit 127
  fi
done

existing=0
[[ -e "$SERVER_ENV" ]] && existing=$((existing + 1))
[[ -e "$ADMIN_FILE" ]] && existing=$((existing + 1))
if [[ "$existing" -eq 2 ]]; then
  printf 'Owner-only Voice Trace credentials already exist at %s\n' "$SECRETS_DIR"
  exit 0
fi
if [[ "$existing" -ne 0 ]]; then
  printf 'refusing partial Voice Trace credential set in %s\n' "$SECRETS_DIR" >&2
  exit 1
fi

mkdir -p "$SECRETS_DIR"
rtk proxy chmod 700 "$SECRETS_DIR"

random_hex() {
  openssl rand -hex "$1"
}

django_secret="$(random_hex 48)"
gateway_internal_secret="$(random_hex 32)"
nextauth_secret="$(random_hex 32)"
salt="$(random_hex 16)"
encryption_key="$(random_hex 32)"
postgres_password="$(random_hex 24)"
clickhouse_password="$(random_hex 24)"
redis_password="$(random_hex 24)"
minio_password="$(random_hex 24)"
project_public_key="pk-lf-$(random_hex 20)"
project_secret_key="sk-lf-$(random_hex 24)"
admin_password="$(random_hex 32)"
admin_suffix="$(random_hex 4)"
admin_username="trace-admin-20260823-$admin_suffix"
admin_email="$admin_username@c2sml.cn"

{
  printf '%s\n' \
    'DEPLOY_ROOT=/root/ansatz-agent/voice-trace-20260823' \
    'NPM_NETWORK=nginx-proxy-manager_default' \
    'AUTH_SERVICE_IMAGE=localhost/ansatz-auth-service:77959189e16a' \
    'TRACE_GATEWAY_IMAGE=localhost/ansatz-trace-gateway:615a97a0f2dd' \
    'LANGFUSE_WEB_IMAGE=localhost/ansatz-langfuse-web@sha256:1c50837be0ad92bbfec54e0054af5da8e4b7027c502a10b8acd33eaea0320480' \
    'LANGFUSE_WORKER_IMAGE=localhost/ansatz-langfuse-worker@sha256:de5b3059cce72312b6a9552748f811381df941c1b57e6e0762410a9848021349' \
    'POSTGRES_IMAGE=docker.io/library/postgres@sha256:a65e6a841f6c4dbc4abda3d67fa3bc21824e9611064fcd82e87ea67aad60a0c3' \
    'CLICKHOUSE_IMAGE=docker.io/clickhouse/clickhouse-server@sha256:8a790dd3468db22b1d4e7b18a176f378ff5ff6053b9c48dd4ea1fa71a24c5ba6' \
    'REDIS_IMAGE=docker.io/library/redis@sha256:91d0f7e8c748ec7a4c2b4fb2c4f84edab794dd91d01e095e38dc906db9d684ab' \
    'MINIO_IMAGE=cgr.dev/chainguard/minio@sha256:c9680a1ad80b56c67b2b9e44cc480a8fd0fb4362dab01f68b8bfbccae9d77596' \
    "DJANGO_SECRET_KEY=$django_secret" \
    "TRACE_GATEWAY_INTERNAL_SECRET=$gateway_internal_secret" \
    "NEXTAUTH_SECRET=$nextauth_secret" \
    "SALT=$salt" \
    "ENCRYPTION_KEY=$encryption_key" \
    'POSTGRES_USER=postgres' \
    "POSTGRES_PASSWORD=$postgres_password" \
    'POSTGRES_DB=postgres' \
    "DATABASE_URL=postgresql://postgres:$postgres_password@postgres:5432/postgres" \
    'CLICKHOUSE_USER=clickhouse' \
    "CLICKHOUSE_PASSWORD=$clickhouse_password" \
    "REDIS_AUTH=$redis_password" \
    'MINIO_ROOT_USER=ansatz-minio' \
    "MINIO_ROOT_PASSWORD=$minio_password" \
    'LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID=ansatz-minio' \
    "LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY=$minio_password" \
    'LANGFUSE_S3_MEDIA_UPLOAD_ACCESS_KEY_ID=ansatz-minio' \
    "LANGFUSE_S3_MEDIA_UPLOAD_SECRET_ACCESS_KEY=$minio_password" \
    'LANGFUSE_S3_BATCH_EXPORT_ACCESS_KEY_ID=ansatz-minio' \
    "LANGFUSE_S3_BATCH_EXPORT_SECRET_ACCESS_KEY=$minio_password" \
    'LANGFUSE_INIT_ORG_ID=ansatz-voice-trace-org' \
    'LANGFUSE_INIT_ORG_NAME=Ansatz Voice Trace' \
    'LANGFUSE_INIT_PROJECT_ID=ansatz-voice-trace-project' \
    'LANGFUSE_INIT_PROJECT_NAME=Ansatz Voice Trace' \
    "LANGFUSE_INIT_PROJECT_PUBLIC_KEY=$project_public_key" \
    "LANGFUSE_INIT_PROJECT_SECRET_KEY=$project_secret_key" \
    "LANGFUSE_INIT_USER_EMAIL=$admin_email" \
    "LANGFUSE_INIT_USER_NAME=$admin_username" \
    "LANGFUSE_INIT_USER_PASSWORD=$admin_password"
} > "$SERVER_ENV"

{
  printf '%s\n' \
    'URL=https://trace.c2sml.cn' \
    "USERNAME=$admin_username" \
    "EMAIL=$admin_email" \
    "PASSWORD=$admin_password" \
    'ORG_ID=ansatz-voice-trace-org' \
    'PROJECT_ID=ansatz-voice-trace-project'
} > "$ADMIN_FILE"

rtk proxy chmod 600 "$SERVER_ENV" "$ADMIN_FILE"
printf 'Created owner-only Voice Trace credentials in %s\n' "$SECRETS_DIR"

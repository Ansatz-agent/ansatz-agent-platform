#!/usr/bin/env bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REMOTE_HOST=hermes
REMOTE_ROOT=/root/ansatz-agent/voice-trace-20260823
COMPOSE_PROJECT=ansatz-voice-trace-20260823
COMPOSE_FILE="$REMOTE_ROOT/deploy/docker-compose.yml"
SERVER_ENV="$PLATFORM_ROOT/.secrets/voice-trace/server.env"
ADMIN_FILE="$PLATFORM_ROOT/.secrets/voice-trace/langfuse-admin-20260823.txt"
IMAGE_ARCHIVE="$PLATFORM_ROOT/tmp/image-archives/voice-trace-images-20260823.tar"
IMAGE_HASH="$IMAGE_ARCHIVE.sha256"

for path in "$SERVER_ENV" "$ADMIN_FILE" "$IMAGE_ARCHIVE" "$IMAGE_HASH"; do
  if [[ ! -f "$path" ]]; then
    printf 'required deployment input is missing: %s\n' "$path" >&2
    exit 1
  fi
done

expected_hash="$(awk '{print $1}' "$IMAGE_HASH")"
actual_hash="$(rtk proxy shasum -a 256 "$IMAGE_ARCHIVE" | awk '{print $1}')"
if [[ "$expected_hash" != "$actual_hash" ]]; then
  printf 'local image archive hash mismatch\n' >&2
  exit 1
fi

rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 '$REMOTE_ROOT' '$REMOTE_ROOT/deploy' '$REMOTE_ROOT/secrets' '$REMOTE_ROOT/staging' '$REMOTE_ROOT/evidence' '$REMOTE_ROOT/data'"
rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 -o 999 -g 999 '$REMOTE_ROOT/data/postgres' '$REMOTE_ROOT/data/redis'"
rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 -o 101 -g 101 '$REMOTE_ROOT/data/clickhouse' '$REMOTE_ROOT/data/clickhouse-logs'"
rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 -o 65532 -g 65532 '$REMOTE_ROOT/data/minio'"

rtk proxy scp -r "$PLATFORM_ROOT/deploy/voice-trace/." "$REMOTE_HOST:$REMOTE_ROOT/deploy/"
rtk proxy scp "$SERVER_ENV" "$REMOTE_HOST:$REMOTE_ROOT/secrets/server.env"
rtk proxy scp "$ADMIN_FILE" "$REMOTE_HOST:$REMOTE_ROOT/secrets/langfuse-admin.txt"
remote_archive="$REMOTE_ROOT/staging/voice-trace-images-20260823.tar"
staged_hash="$(rtk proxy ssh "$REMOTE_HOST" "if test -f '$remote_archive'; then sha256sum '$remote_archive'; fi" | awk '{print $1}')"
if [[ "$staged_hash" != "$expected_hash" ]]; then
  rtk proxy scp "$IMAGE_ARCHIVE" "$REMOTE_HOST:$remote_archive"
fi
rtk proxy ssh "$REMOTE_HOST" "chmod 600 '$REMOTE_ROOT/secrets/server.env' '$REMOTE_ROOT/secrets/langfuse-admin.txt' '$REMOTE_ROOT/staging/voice-trace-images-20260823.tar'"

remote_hash="$(rtk proxy ssh "$REMOTE_HOST" "sha256sum '$REMOTE_ROOT/staging/voice-trace-images-20260823.tar'" | awk '{print $1}')"
if [[ "$remote_hash" != "$expected_hash" ]]; then
  printf 'remote image archive hash mismatch\n' >&2
  exit 1
fi

rtk proxy ssh "$REMOTE_HOST" 'bash /opt/agent-history-portal/scripts/backup.sh'
rtk proxy ssh "$REMOTE_HOST" "docker exec agent-history-web python manage.py shell -c 'import json; from django.contrib.auth import get_user_model; User=get_user_model(); print(json.dumps(list(User.objects.order_by(\"id\").values(\"id\",\"username\",\"email\",\"is_active\",\"is_staff\",\"is_superuser\"))))' > '$REMOTE_ROOT/evidence/auth-users-before.json'"
rtk proxy ssh "$REMOTE_HOST" "docker load -i '$REMOTE_ROOT/staging/voice-trace-images-20260823.tar'"
rtk proxy ssh "$REMOTE_HOST" "cd '$REMOTE_ROOT' && /usr/bin/podman-compose --env-file '$REMOTE_ROOT/secrets/server.env' -f '$COMPOSE_FILE' -p '$COMPOSE_PROJECT' config > /dev/null"

cutover_started=0
rollback_cutover() {
  if [[ "$cutover_started" -eq 1 ]]; then
    rtk proxy ssh "$REMOTE_HOST" "cd '$REMOTE_ROOT' && /usr/bin/podman-compose --env-file '$REMOTE_ROOT/secrets/server.env' -f '$COMPOSE_FILE' -p '$COMPOSE_PROJECT' down > /dev/null 2>&1" || true
    rtk proxy ssh "$REMOTE_HOST" 'systemctl enable agent-history-portal.service' || true
    rtk proxy ssh "$REMOTE_HOST" 'systemctl start agent-history-portal.service' || true
  fi
}
trap rollback_cutover ERR

cutover_started=1
rtk proxy ssh "$REMOTE_HOST" 'systemctl stop agent-history-portal.service'
rtk proxy ssh "$REMOTE_HOST" "cd '$REMOTE_ROOT' && /usr/bin/podman-compose --env-file '$REMOTE_ROOT/secrets/server.env' -f '$COMPOSE_FILE' -p '$COMPOSE_PROJECT' up -d > /dev/null 2>&1"
bash "$SCRIPT_DIR/check-voice-trace.sh"
bash "$SCRIPT_DIR/configure-voice-trace-npm.sh"
rtk proxy ssh "$REMOTE_HOST" 'systemctl disable agent-history-portal.service'
cutover_started=0
trap - ERR

printf 'Voice Trace services deployed on %s under %s\n' "$REMOTE_HOST" "$REMOTE_ROOT"

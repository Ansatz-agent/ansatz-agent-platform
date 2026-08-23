#!/usr/bin/env bash

set -euo pipefail

REMOTE_HOST=hermes
REMOTE_ROOT=/root/ansatz-agent/voice-trace-20260823
COMPOSE_PROJECT=ansatz-voice-trace-20260823
COMPOSE_FILE="$REMOTE_ROOT/deploy/docker-compose.yml"
ENV_FILE="$REMOTE_ROOT/secrets/server.env"
services=(auth-service trace-gateway langfuse-web langfuse-worker postgres clickhouse redis minio)

rtk proxy ssh "$REMOTE_HOST" "cd '$REMOTE_ROOT' && /usr/bin/podman-compose --env-file '$ENV_FILE' -f '$COMPOSE_FILE' -p '$COMPOSE_PROJECT' ps"

wait_remote() {
  local label="$1"
  local command="$2"
  local attempt
  for attempt in $(seq 1 36); do
    if rtk proxy ssh "$REMOTE_HOST" "$command" >/dev/null 2>&1; then
      return 0
    fi
    sleep 5
  done
  printf 'timed out waiting for %s\n' "$label" >&2
  return 1
}

for service in "${services[@]}"; do
  container="${COMPOSE_PROJECT}_${service}_1"
  running="$(rtk proxy ssh "$REMOTE_HOST" "docker inspect '$container' --format '{{.State.Running}}'")"
  if [[ "$running" != true ]]; then
    printf 'service is not running: %s\n' "$service" >&2
    exit 1
  fi
  published="$(rtk proxy ssh "$REMOTE_HOST" "docker port '$container'")"
  if [[ -n "$published" ]]; then
    printf 'published port detected for %s\n' "$service" >&2
    exit 1
  fi
done

wait_remote trace-gateway "docker exec '${COMPOSE_PROJECT}_trace-gateway_1' /trace-gateway healthcheck"
wait_remote auth-service "docker exec '${COMPOSE_PROJECT}_auth-service_1' python -c \"import urllib.request; request=urllib.request.Request('http://127.0.0.1:8000/healthz', headers={'X-Forwarded-Proto':'https'}); urllib.request.urlopen(request, timeout=2)\""
wait_remote langfuse-web "docker exec '${COMPOSE_PROJECT}_langfuse-web_1' /bin/sh -c 'wget --no-verbose --tries=1 --spider http://\$(hostname):3000/langfuse/api/public/health'"

printf 'Voice Trace private service health checks passed\n'

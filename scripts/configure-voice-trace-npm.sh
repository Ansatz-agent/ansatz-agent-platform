#!/usr/bin/env bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REMOTE_HOST=hermes
REMOTE_ROOT=/root/ansatz-agent/voice-trace-20260823
NPM_ROOT=/root/nginx-proxy-manager
NPM_COMPOSE="$NPM_ROOT/compose.yaml"
NPM_LEGACY_OVERRIDE="$NPM_ROOT/compose.voice-trace.yml"
NPM_PROXY_LOCAL="$PLATFORM_ROOT/deploy/voice-trace/nginx/server_proxy.conf"
NPM_PROXY_REMOTE="$NPM_ROOT/data/nginx/custom/server_proxy.conf"
NPM_UPDATER_LOCAL="$PLATFORM_ROOT/scripts/update_npm_compose.py"
NPM_UPDATER_REMOTE="$REMOTE_ROOT/staging/update_npm_compose.py"

for path in "$NPM_PROXY_LOCAL" "$NPM_UPDATER_LOCAL"; do
  if [[ ! -f "$path" ]]; then
    printf 'required NPM input is missing: %s\n' "$path" >&2
    exit 1
  fi
done

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$REMOTE_ROOT/backups/npm-$stamp"
rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 '$backup' '$NPM_ROOT/data/nginx/custom' '$REMOTE_ROOT/staging'"
rtk proxy ssh "$REMOTE_HOST" "install -m 0600 '$NPM_COMPOSE' '$backup/compose.yaml'"
rtk proxy ssh "$REMOTE_HOST" "if test -f '$NPM_LEGACY_OVERRIDE'; then install -m 0600 '$NPM_LEGACY_OVERRIDE' '$backup/compose.voice-trace.yml'; else touch '$backup/compose.voice-trace.yml.absent'; fi"
rtk proxy ssh "$REMOTE_HOST" "if test -f '$NPM_PROXY_REMOTE'; then install -m 0600 '$NPM_PROXY_REMOTE' '$backup/server_proxy.conf'; else touch '$backup/server_proxy.conf.absent'; fi"

rtk proxy scp "$NPM_PROXY_LOCAL" "$REMOTE_HOST:$NPM_PROXY_REMOTE"
rtk proxy scp "$NPM_UPDATER_LOCAL" "$REMOTE_HOST:$NPM_UPDATER_REMOTE"
rtk proxy ssh "$REMOTE_HOST" "chmod 0644 '$NPM_PROXY_REMOTE'; chmod 0600 '$NPM_UPDATER_REMOTE'"

rtk proxy ssh "$REMOTE_HOST" "python3 '$NPM_UPDATER_REMOTE' '$NPM_COMPOSE'"
rtk proxy ssh "$REMOTE_HOST" "cd '$NPM_ROOT' && /usr/bin/podman-compose -f '$NPM_COMPOSE' -p nginx-proxy-manager config > /dev/null"
rtk proxy ssh "$REMOTE_HOST" "umask 077; cd '$NPM_ROOT' && /usr/bin/podman-compose -f '$NPM_COMPOSE' -p nginx-proxy-manager up -d --force-recreate > '$REMOTE_ROOT/evidence/npm-up-$stamp.log' 2>&1"
rtk proxy ssh "$REMOTE_HOST" "podman exec npm nginx -t"
rtk proxy ssh "$REMOTE_HOST" "podman exec npm nginx -s reload"
rtk proxy ssh "$REMOTE_HOST" "podman exec npm nginx -t"

printf 'NPM Voice Trace routes configured on %s\n' "$REMOTE_HOST"

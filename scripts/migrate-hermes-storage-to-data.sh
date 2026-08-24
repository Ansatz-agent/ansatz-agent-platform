#!/usr/bin/env bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REMOTE_HOST=hermes
OLD_GRAPHROOT=/var/lib/containers/storage
NEW_GRAPHROOT=/data/containers/storage
OLD_CNI_STATE=/var/lib/cni
NEW_CNI_STATE=/data/containers/cni
OLD_RELEASE_ROOT=/root/ansatz-agent/voice-trace-20260823
NEW_RELEASE_ROOT=/data/ansatz-agent/voice-trace
OLD_AUTH_DATA=/var/lib/agent-history
FSTAB=/etc/fstab
REMOTE_SCRIPT_LOCAL="$PLATFORM_ROOT/deploy/voice-trace/scripts/migrate-storage-on-host.sh"
REMOTE_SCRIPT="$NEW_RELEASE_ROOT/staging/migrate-storage-on-host.sh"

usage() {
  printf 'usage: bash scripts/migrate-hermes-storage-to-data.sh {preflight|stage|cutover|verify|rollback|retire-legacy-binds|cleanup-old-graphroot|status}\n' >&2
  exit 2
}

action="${1:-}"
case "$action" in
  preflight|stage|cutover|verify|rollback|retire-legacy-binds|cleanup-old-graphroot|status) ;;
  *) usage ;;
esac

if [[ ! -f "$REMOTE_SCRIPT_LOCAL" ]]; then
  printf 'missing inspected remote migration helper: %s\n' "$REMOTE_SCRIPT_LOCAL" >&2
  exit 1
fi

rtk proxy ssh "$REMOTE_HOST" "install -d -m 0700 '$NEW_RELEASE_ROOT/staging'"
rtk proxy scp "$REMOTE_SCRIPT_LOCAL" "$REMOTE_HOST:$REMOTE_SCRIPT"
rtk proxy ssh "$REMOTE_HOST" "chmod 0600 '$REMOTE_SCRIPT'"

rtk proxy ssh "$REMOTE_HOST" "bash '$REMOTE_SCRIPT' '$action'"

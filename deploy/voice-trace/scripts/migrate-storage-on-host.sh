#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

REMOTE_HOST=hermes
DATA_MOUNT=/data
OLD_GRAPHROOT=/var/lib/containers/storage
NEW_GRAPHROOT=/data/containers/storage
OLD_CNI_STATE=/var/lib/cni
NEW_CNI_STATE=/data/containers/cni
OLD_RELEASE_ROOT=/root/ansatz-agent/voice-trace-20260823
NEW_RELEASE_ROOT=/data/ansatz-agent/voice-trace
OLD_AUTH_DATA=/var/lib/agent-history
FSTAB=/etc/fstab
STATE_DIR="$NEW_RELEASE_ROOT/evidence/storage-migration"
RUNNING_FILE="$STATE_DIR/running-containers.txt"
STAGED_MARKER="$STATE_DIR/migration-staged"
CUTOVER_MARKER="$STATE_DIR/migration-cutover"
VERIFIED_MARKER="$STATE_DIR/migration-verified"
LEGACY_RETIRED_MARKER="$STATE_DIR/legacy-binds-retired"
FSTAB_BACKUP="$STATE_DIR/fstab.before"
ROOTFS_VIEW="$NEW_RELEASE_ROOT/staging/rootfs-view"
FSTAB_BEGIN="# BEGIN ANSATZ DATA STORAGE"
FSTAB_END="# END ANSATZ DATA STORAGE"

log() { printf '[storage-migration] %s\n' "$*"; }
die() { printf '[storage-migration] error: %s\n' "$*" >&2; exit 1; }
require_root() { [[ "$(id -u)" -eq 0 ]] || die "run this helper as root"; }
active_graphroot_value() { podman info --format '{{.Store.GraphRoot}}'; }

assert_data_mount() {
  local data_source root_source data_target
  data_source="$(findmnt -n -o SOURCE -T "$DATA_MOUNT")"
  root_source="$(findmnt -n -o SOURCE -T /)"
  data_target="$(findmnt -n -o TARGET -T "$DATA_MOUNT")"
  [[ "$data_target" == "$DATA_MOUNT" ]] || die "$DATA_MOUNT is not an independent mount target"
  [[ "$data_source" != "$root_source" ]] || die "$DATA_MOUNT uses the root filesystem source"
}

source_bytes() {
  local total=0 path bytes
  for path in "$OLD_GRAPHROOT" "$OLD_CNI_STATE" "$OLD_RELEASE_ROOT" "$OLD_AUTH_DATA"; do
    if [[ -e "$path" ]]; then
      bytes="$(du -sb "$path" | awk '{print $1}')"
      total=$((total + bytes))
    fi
  done
  printf '%s\n' "$total"
}

preflight() {
  local active_graphroot required_bytes available_bytes source_total graphroot_target
  require_root
  assert_data_mount
  [[ -d "$OLD_GRAPHROOT" ]] || die "old graphroot is missing: $OLD_GRAPHROOT"
  graphroot_target="$(findmnt -n -o TARGET -T "$OLD_GRAPHROOT")"
  [[ "$graphroot_target" == "$OLD_GRAPHROOT" || "$graphroot_target" == / ]] || die "unexpected graphroot mount target: $graphroot_target"
  active_graphroot="$(active_graphroot_value)"
  [[ "$active_graphroot" == "$OLD_GRAPHROOT" ]] || die "unexpected active graphroot: $active_graphroot"
  source_total="$(source_bytes)"
  required_bytes=$((source_total * 120 / 100))
  available_bytes="$(df -B1 --output=avail "$DATA_MOUNT" | awk 'NR == 2 {print $1}')"
  [[ "$available_bytes" =~ ^[0-9]+$ ]] || die "could not read available bytes on $DATA_MOUNT"
  (( available_bytes >= required_bytes )) || die "insufficient capacity on $DATA_MOUNT"
  log "preflight passed: active_graphroot=$active_graphroot source_bytes=$source_total required_bytes=$required_bytes available_bytes=$available_bytes"
}

prune_destination_extras() {
  local source_root="$1" destination_root="$2" target relative source_path
  [[ -d "$source_root" ]] || die "prune source is missing: $source_root"
  [[ "$destination_root" == /data/* ]] || die "refusing to prune outside /data: $destination_root"
  install -d -m 0700 "$destination_root"
  while IFS= read -r -d '' target; do
    relative="${target#"$destination_root"/}"
    source_path="$source_root/$relative"
    if [[ ! -e "$source_path" && ! -L "$source_path" ]]; then
      rm -rf --one-file-system "$target"
    fi
  done < <(find "$destination_root" -depth -mindepth 1 -print0)
}

copy_tree() {
  local mode="$1" source_root="$2" destination_root="$3"
  [[ -d "$source_root" ]] || return 0
  install -d -m 0700 "$destination_root"
  if [[ "$mode" == "initial" ]]; then
    cp -a --preserve=all "$source_root/." "$destination_root/"
  else
    prune_destination_extras "$source_root" "$destination_root"
    cp -au --preserve=all "$source_root/." "$destination_root/"
  fi
}

copy_stage() {
  local mode="$1" name
  install -d -m 0700 "$NEW_GRAPHROOT" "$NEW_CNI_STATE" "$NEW_RELEASE_ROOT/data" "$NEW_RELEASE_ROOT/data/auth" "$NEW_RELEASE_ROOT/deploy" "$NEW_RELEASE_ROOT/secrets" "$NEW_RELEASE_ROOT/backups" "$NEW_RELEASE_ROOT/evidence" "$NEW_RELEASE_ROOT/staging"
  copy_tree "$mode" "$OLD_GRAPHROOT" "$NEW_GRAPHROOT"
  copy_tree "$mode" "$OLD_CNI_STATE" "$NEW_CNI_STATE"
  copy_tree "$mode" "$OLD_RELEASE_ROOT/data" "$NEW_RELEASE_ROOT/data"
  copy_tree "$mode" "$OLD_AUTH_DATA" "$NEW_RELEASE_ROOT/data/auth"
  for name in deploy secrets backups evidence; do
    copy_tree final "$OLD_RELEASE_ROOT/$name" "$NEW_RELEASE_ROOT/$name"
  done
}

stage() {
  preflight
  install -d -m 0700 "$STATE_DIR"
  podman ps --format '{{.Names}}' > "$RUNNING_FILE"
  copy_stage initial
  date -u +%Y-%m-%dT%H:%M:%SZ > "$STAGED_MARKER"
  log "initial copy complete"
}

write_managed_fstab() {
  local include_legacy="$1" replacement="$STATE_DIR/fstab.new"
  awk -v begin="$FSTAB_BEGIN" -v end="$FSTAB_END" '
    $0 == begin { skip = 1; next }
    $0 == end { skip = 0; next }
    !skip { print }
  ' "$FSTAB" > "$replacement"
  {
    printf '\n%s\n' "$FSTAB_BEGIN"
    printf '%s %s none bind,x-systemd.requires-mounts-for=/data 0 0\n' "$NEW_GRAPHROOT" "$OLD_GRAPHROOT"
    printf '%s %s none bind,private,x-systemd.requires-mounts-for=%s 0 0\n' "$OLD_GRAPHROOT/overlay" "$OLD_GRAPHROOT/overlay" "$OLD_GRAPHROOT"
    printf '%s %s none bind,x-systemd.requires-mounts-for=/data 0 0\n' "$NEW_CNI_STATE" "$OLD_CNI_STATE"
    if [[ "$include_legacy" == "yes" ]]; then
      printf '%s %s none bind,x-systemd.requires-mounts-for=/data 0 0\n' "$NEW_RELEASE_ROOT/data" "$OLD_RELEASE_ROOT/data"
      printf '%s %s none bind,x-systemd.requires-mounts-for=/data 0 0\n' "$NEW_RELEASE_ROOT/data/auth" "$OLD_AUTH_DATA"
    fi
    printf '%s\n' "$FSTAB_END"
  } >> "$replacement"
  findmnt --verify --tab-file "$replacement" >/dev/null
  install -m 0644 "$replacement" "$FSTAB"
  systemctl daemon-reload
}

is_exact_mountpoint() {
  [[ "$(findmnt -n -o TARGET -M "$1" 2>/dev/null | tail -n 1)" == "$1" ]]
}

unmount_exact() {
  local target="$1"
  if is_exact_mountpoint "$target"; then umount "$target"; fi
}

mount_data_layout() {
  install -d -m 0700 "$OLD_GRAPHROOT" "$OLD_GRAPHROOT/overlay" "$OLD_CNI_STATE" "$OLD_RELEASE_ROOT/data" "$OLD_AUTH_DATA"
  mount --bind "$NEW_GRAPHROOT" "$OLD_GRAPHROOT"
  mount --bind "$OLD_GRAPHROOT/overlay" "$OLD_GRAPHROOT/overlay"
  mount --make-private "$OLD_GRAPHROOT/overlay"
  mount --bind "$NEW_CNI_STATE" "$OLD_CNI_STATE"
  mount --bind "$NEW_RELEASE_ROOT/data" "$OLD_RELEASE_ROOT/data"
  mount --bind "$NEW_RELEASE_ROOT/data/auth" "$OLD_AUTH_DATA"
}

start_recorded_containers() {
  local name
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    podman start "$name" >/dev/null
  done < "$RUNNING_FILE"
}

stop_running_containers() {
  local -a current=()
  mapfile -t current < <(podman ps --format '{{.Names}}')
  if (( ${#current[@]} > 0 )); then podman stop --time 60 "${current[@]}" >/dev/null; fi
}

rollback() {
  trap - ERR
  require_root
  [[ -f "$FSTAB_BACKUP" ]] || die "fstab backup is missing"
  stop_running_containers || true
  unmount_exact "$OLD_AUTH_DATA" || true
  unmount_exact "$OLD_RELEASE_ROOT/data" || true
  unmount_exact "$OLD_CNI_STATE" || true
  unmount_exact "$OLD_GRAPHROOT/overlay" || true
  unmount_exact "$OLD_GRAPHROOT" || true
  install -m 0644 "$FSTAB_BACKUP" "$FSTAB"
  systemctl daemon-reload
  [[ "$(active_graphroot_value)" == "$OLD_GRAPHROOT" ]] || die "rollback did not restore the logical graphroot"
  start_recorded_containers
  log "rollback restored the original root-filesystem data"
}

cutover() {
  require_root
  assert_data_mount
  [[ -f "$STAGED_MARKER" ]] || die "stage must complete before cutover"
  [[ -s "$RUNNING_FILE" ]] || die "running container manifest is empty"
  [[ ! -e "$VERIFIED_MARKER" ]] || die "migration is already verified"
  [[ "$(active_graphroot_value)" == "$OLD_GRAPHROOT" ]] || die "cutover requires the logical graphroot $OLD_GRAPHROOT"
  install -m 0600 "$FSTAB" "$FSTAB_BACKUP"
  trap 'rollback' ERR
  stop_running_containers
  copy_stage final
  write_managed_fstab yes
  mount_data_layout
  [[ "$(active_graphroot_value)" == "$OLD_GRAPHROOT" ]] || die "logical graphroot changed unexpectedly"
  start_recorded_containers
  date -u +%Y-%m-%dT%H:%M:%SZ > "$CUTOVER_MARKER"
  trap - ERR
  log "cutover complete; application verification is required"
}

assert_bind_root() {
  local target="$1" expected_root="$2" actual_root
  actual_root="$(findmnt -n -o FSROOT -M "$target" | tail -n 1)"
  [[ "$actual_root" == "$expected_root" ]] || die "$target has unexpected filesystem root: $actual_root"
}

verify() {
  require_root
  assert_data_mount
  [[ -f "$CUTOVER_MARKER" ]] || die "cutover marker is missing"
  [[ "$(active_graphroot_value)" == "$OLD_GRAPHROOT" ]] || die "logical graphroot is not $OLD_GRAPHROOT"
  assert_bind_root "$OLD_GRAPHROOT" /containers/storage
  assert_bind_root "$OLD_GRAPHROOT/overlay" /containers/storage/overlay
  assert_bind_root "$OLD_CNI_STATE" /containers/cni
  [[ -f "$NEW_RELEASE_ROOT/data/auth/db.sqlite3" ]] || die "migrated auth database is missing"
  [[ -d "$NEW_RELEASE_ROOT/data/postgres" ]] || die "migrated PostgreSQL data is missing"
  podman ps --format '{{.Names}}' > "$STATE_DIR/running-containers-after.txt"
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    podman container exists "$name" || die "migrated container is missing: $name"
    [[ "$(podman inspect --format '{{.State.Running}}' "$name")" == "true" ]] || die "migrated container is not running: $name"
  done < "$RUNNING_FILE"
  findmnt --verify --tab-file "$FSTAB" >/dev/null
  date -u +%Y-%m-%dT%H:%M:%SZ > "$VERIFIED_MARKER"
  log "migration-verified"
}

container_uses_source() {
  local source="$1" container sources
  while IFS= read -r container; do
    [[ -n "$container" ]] || continue
    sources="$(podman inspect --format '{{range .Mounts}}{{println .Source}}{{end}}' "$container")"
    if grep -Fqx "$source" <<< "$sources"; then return 0; fi
  done < <(podman ps -aq)
  return 1
}

retire_legacy_binds() {
  require_root
  [[ -f "$VERIFIED_MARKER" ]] || die "migration-verified marker is missing"
  container_uses_source "$OLD_RELEASE_ROOT/data" && die "a container still uses legacy release data"
  container_uses_source "$OLD_AUTH_DATA" && die "a container still uses legacy auth data"
  unmount_exact "$OLD_AUTH_DATA"
  unmount_exact "$OLD_RELEASE_ROOT/data"
  write_managed_fstab no
  findmnt --verify --tab-file "$FSTAB" >/dev/null
  date -u +%Y-%m-%dT%H:%M:%SZ > "$LEGACY_RETIRED_MARKER"
  log "legacy service binds retired; only Podman graphroot, overlay propagation, and CNI binds remain"
}

clear_underlying_path() {
  local path="$1"
  case "$path" in
    "$ROOTFS_VIEW/var/lib/containers/storage"|"$ROOTFS_VIEW/var/lib/agent-history"|"$ROOTFS_VIEW/root/ansatz-agent/voice-trace-20260823/data") ;;
    *) die "refusing unexpected root-filesystem cleanup path: $path" ;;
  esac
  [[ -d "$path" ]] || return 0
  find "$path" -xdev -mindepth 1 -delete
}

cleanup_old_graphroot() {
  local path root_source data_source
  require_root
  [[ -f "$VERIFIED_MARKER" ]] || die "migration-verified marker is missing"
  [[ -f "$LEGACY_RETIRED_MARKER" ]] || die "legacy-binds-retired marker is missing"
  [[ "$OLD_GRAPHROOT" == "/var/lib/containers/storage" ]] || die "refusing unexpected cleanup path"
  [[ "$(active_graphroot_value)" == "$OLD_GRAPHROOT" ]] || die "logical graphroot changed unexpectedly"
  install -d -m 0700 "$ROOTFS_VIEW"
  mount --bind / "$ROOTFS_VIEW"
  mount --make-rprivate "$ROOTFS_VIEW"
  trap 'unmount_exact "$ROOTFS_VIEW"' EXIT
  root_source="$(findmnt -n -o SOURCE -T "$ROOTFS_VIEW")"
  data_source="$(findmnt -n -o SOURCE -T "$DATA_MOUNT")"
  [[ "$root_source" != "$data_source" ]] || die "rootfs cleanup view unexpectedly resolves to /data"
  for path in \
    "$ROOTFS_VIEW/var/lib/containers/storage" \
    "$ROOTFS_VIEW/var/lib/agent-history" \
    "$ROOTFS_VIEW/root/ansatz-agent/voice-trace-20260823/data"; do
    clear_underlying_path "$path"
  done
  unmount_exact "$ROOTFS_VIEW"
  trap - EXIT
  log "removed verified obsolete root-filesystem copies while preserving active /data mounts"
}

status() {
  require_root
  printf 'active_graphroot=%s\n' "$(active_graphroot_value)"
  printf 'graphroot_fsroot=%s\n' "$(findmnt -n -o FSROOT -M "$OLD_GRAPHROOT" | tail -n 1)"
  printf 'data_mount_source=%s\n' "$(findmnt -n -o SOURCE -T "$DATA_MOUNT")"
  printf 'staged=%s\n' "$([[ -f "$STAGED_MARKER" ]] && printf yes || printf no)"
  printf 'cutover=%s\n' "$([[ -f "$CUTOVER_MARKER" ]] && printf yes || printf no)"
  printf 'verified=%s\n' "$([[ -f "$VERIFIED_MARKER" ]] && printf yes || printf no)"
  printf 'legacy_binds_retired=%s\n' "$([[ -f "$LEGACY_RETIRED_MARKER" ]] && printf yes || printf no)"
  df -h / "$DATA_MOUNT"
}

case "${1:-}" in
  preflight) preflight ;;
  stage) stage ;;
  cutover) cutover ;;
  verify) verify ;;
  rollback) rollback ;;
  retire-legacy-binds) retire_legacy_binds ;;
  cleanup-old-graphroot) cleanup_old_graphroot ;;
  status) status ;;
  *) die "usage: bash migrate-storage-on-host.sh {preflight|stage|cutover|verify|rollback|retire-legacy-binds|cleanup-old-graphroot|status}" ;;
esac

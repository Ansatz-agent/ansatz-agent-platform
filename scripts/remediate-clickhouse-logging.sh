#!/usr/bin/env bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REMOTE_HOST=hermes
REMOTE_ROOT=/data/ansatz-agent/voice-trace
COMPOSE_PROJECT=ansatz-voice-trace-20260823
COMPOSE_FILE="$REMOTE_ROOT/deploy/docker-compose.yml"
ENV_FILE="$REMOTE_ROOT/secrets/server.env"
CLICKHOUSE_CONTAINER=ansatz-voice-trace-20260823_clickhouse_1
LOCAL_COMPOSE="$PLATFORM_ROOT/deploy/voice-trace/docker-compose.yml"
LOCAL_SERVER_CONFIG="$PLATFORM_ROOT/deploy/voice-trace/clickhouse/config.d/logging.xml"
LOCAL_USER_CONFIG="$PLATFORM_ROOT/deploy/voice-trace/clickhouse/users.d/profilers.xml"

for path in "$LOCAL_COMPOSE" "$LOCAL_SERVER_CONFIG" "$LOCAL_USER_CONFIG"; do
  if [[ ! -f "$path" ]]; then
    printf 'required remediation input is missing: %s\n' "$path" >&2
    exit 1
  fi
done

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$REMOTE_ROOT/backups/clickhouse-logging-$stamp"
evidence="$REMOTE_ROOT/evidence/clickhouse-logging-$stamp"
task_tmp="$PLATFORM_ROOT/tmp/clickhouse-remediation-$stamp"
mkdir -p "$task_tmp"

remote_sql() {
  local sql="$1" quoted_sql
  printf -v quoted_sql '%q' "$sql"
  rtk proxy ssh "$REMOTE_HOST" \
    "podman exec '$CLICKHOUSE_CONTAINER' sh -c 'exec clickhouse-client --user \"\$CLICKHOUSE_USER\" --password \"\$CLICKHOUSE_PASSWORD\" --query \"\$1\"' sh $quoted_sql"
}

wait_for_clickhouse() {
  local attempt
  for attempt in $(seq 1 60); do
    if remote_sql "SELECT 1" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  printf 'timed out waiting for ClickHouse\n' >&2
  return 1
}

capture_other_container_ids() {
  local destination="$1"
  rtk proxy ssh "$REMOTE_HOST" \
    "podman ps -a --format '{{.Names}}\t{{.ID}}' | sort > '$destination'"
}

business_rows_sql="SELECT count() FROM default.events_core FINAL"
business_tables_sql="SELECT database, name, coalesce(total_rows, 0), coalesce(total_bytes, 0) FROM system.tables WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema') ORDER BY database, name FORMAT TSV"
# Business data contract: default.events* tables are read-only throughout remediation.

rtk proxy ssh "$REMOTE_HOST" "test -f '$COMPOSE_FILE' && test -f '$ENV_FILE' && podman container exists '$CLICKHOUSE_CONTAINER'"
sed \
  -e '/clickhouse\/config.d\/logging.xml/d' \
  -e '/clickhouse\/users.d\/profilers.xml/d' \
  "$LOCAL_COMPOSE" > "$task_tmp/expected-compose-before.yml"
rtk proxy scp "$REMOTE_HOST:$COMPOSE_FILE" "$task_tmp/remote-compose-before.yml"
if ! cmp -s "$task_tmp/expected-compose-before.yml" "$task_tmp/remote-compose-before.yml"; then
  printf 'remote Compose differs from the expected pre-remediation baseline:\n' >&2
  diff -u "$task_tmp/expected-compose-before.yml" "$task_tmp/remote-compose-before.yml" >&2 || true
  exit 1
fi
rtk proxy ssh "$REMOTE_HOST" "set -e; install -d -m 0700 '$backup' '$evidence'; cp -a '$COMPOSE_FILE' '$backup/docker-compose.yml'; if test -d '$REMOTE_ROOT/deploy/clickhouse'; then cp -a '$REMOTE_ROOT/deploy/clickhouse' '$backup/clickhouse'; else touch '$backup/clickhouse-config-was-absent'; fi"
rtk proxy ssh "$REMOTE_HOST" "if podman cp '$CLICKHOUSE_CONTAINER:/etc/clickhouse-server/config.d/logging.xml' '$backup/container-logging.xml' 2>/dev/null; then :; else touch '$backup/container-logging-was-absent'; fi; if podman cp '$CLICKHOUSE_CONTAINER:/etc/clickhouse-server/users.d/profilers.xml' '$backup/container-profilers.xml' 2>/dev/null; then :; else touch '$backup/container-profilers-was-absent'; fi"
capture_other_container_ids "$evidence/before-container-ids.tsv"
remote_sql "$business_rows_sql" > "$task_tmp/business-rows-before.txt"
remote_sql "$business_tables_sql" > "$task_tmp/business-tables-before.tsv"
before_business_rows="$(tr -d '[:space:]' < "$task_tmp/business-rows-before.txt")"
if [[ ! "$before_business_rows" =~ ^[0-9]+$ ]]; then
  printf 'invalid pre-remediation business row count: %s\n' "$before_business_rows" >&2
  exit 1
fi
rtk proxy ssh "$REMOTE_HOST" "set -e; printf '%s\n' '$before_business_rows' > '$evidence/business-rows-before.txt'; du -sk '$REMOTE_ROOT/data/clickhouse-logs' > '$evidence/storage-before.tsv'"
rtk proxy scp "$task_tmp/business-tables-before.tsv" "$REMOTE_HOST:$evidence/business-tables-before.tsv"

rollback_enabled=1
container_mutated=0
cleanup_on_exit() {
  local status=$?
  if [[ "$status" -ne 0 && "$rollback_enabled" -eq 1 ]]; then
    printf 'remediation failed; restoring the previous ClickHouse configuration\n' >&2
    rollback_failed=0
    if ! rtk proxy ssh "$REMOTE_HOST" "set -e; cp -a '$backup/docker-compose.yml' '$COMPOSE_FILE'; rm -f '$REMOTE_ROOT/deploy/clickhouse/config.d/logging.xml' '$REMOTE_ROOT/deploy/clickhouse/users.d/profilers.xml'; if test -d '$backup/clickhouse'; then cp -a '$backup/clickhouse/.' '$REMOTE_ROOT/deploy/clickhouse/'; fi"; then
      rollback_failed=1
    fi
    if [[ "$container_mutated" -eq 1 ]]; then
      if ! rtk proxy ssh "$REMOTE_HOST" "set -e; podman stop '$CLICKHOUSE_CONTAINER' >/dev/null 2>&1 || true; rootfs=\$(podman mount '$CLICKHOUSE_CONTAINER'); test -n \"\$rootfs\"; if test -f '$backup/container-logging.xml'; then cp -a '$backup/container-logging.xml' \"\$rootfs/etc/clickhouse-server/config.d/logging.xml\"; else rm -f \"\$rootfs/etc/clickhouse-server/config.d/logging.xml\"; fi; if test -f '$backup/container-profilers.xml'; then cp -a '$backup/container-profilers.xml' \"\$rootfs/etc/clickhouse-server/users.d/profilers.xml\"; else rm -f \"\$rootfs/etc/clickhouse-server/users.d/profilers.xml\"; fi; podman unmount '$CLICKHOUSE_CONTAINER' >/dev/null; podman start '$CLICKHOUSE_CONTAINER' >/dev/null"; then
        rollback_failed=1
      elif ! wait_for_clickhouse; then
        rollback_failed=1
      fi
    fi
    if [[ "$rollback_failed" -eq 0 ]]; then
      printf 'ROLLBACK SUCCEEDED: previous ClickHouse configuration is active\n' >&2
      rtk proxy ssh "$REMOTE_HOST" "printf '%s\n' 'ROLLBACK SUCCEEDED' > '$evidence/rollback-status.txt'" || true
    else
      printf 'ROLLBACK FAILED: inspect %s and restore ClickHouse immediately\n' "$backup" >&2
      rtk proxy ssh "$REMOTE_HOST" "printf '%s\n' 'ROLLBACK FAILED' > '$evidence/rollback-status.txt'" || true
    fi
  fi
  rm -f \
    "$task_tmp/business-rows-before.txt" \
    "$task_tmp/business-rows-after.txt" \
    "$task_tmp/business-tables-before.tsv" \
    "$task_tmp/business-tables-after.tsv" \
    "$task_tmp/business-table-names-before.tsv" \
    "$task_tmp/business-table-names-after.tsv" \
    "$task_tmp/missing-business-tables.tsv" \
    "$task_tmp/ttl-table-definitions.tsv" \
    "$task_tmp/expected-compose-before.yml" \
    "$task_tmp/remote-compose-before.yml" \
    "$task_tmp/profiler-settings.tsv"
  rmdir "$task_tmp" 2>/dev/null || true
}
trap cleanup_on_exit EXIT

rtk proxy ssh "$REMOTE_HOST" "install -d -m 0755 '$REMOTE_ROOT/deploy/clickhouse/config.d' '$REMOTE_ROOT/deploy/clickhouse/users.d'"
rtk proxy scp "$LOCAL_COMPOSE" "$REMOTE_HOST:$COMPOSE_FILE"
rtk proxy scp "$LOCAL_SERVER_CONFIG" "$REMOTE_HOST:$REMOTE_ROOT/deploy/clickhouse/config.d/logging.xml"
rtk proxy scp "$LOCAL_USER_CONFIG" "$REMOTE_HOST:$REMOTE_ROOT/deploy/clickhouse/users.d/profilers.xml"
rtk proxy ssh "$REMOTE_HOST" "set -e; chmod 0644 '$COMPOSE_FILE' '$REMOTE_ROOT/deploy/clickhouse/config.d/logging.xml' '$REMOTE_ROOT/deploy/clickhouse/users.d/profilers.xml'; cd '$REMOTE_ROOT'; /usr/bin/podman-compose --env-file '$ENV_FILE' -f '$COMPOSE_FILE' -p '$COMPOSE_PROJECT' config > /dev/null"

container_mutated=1
rtk proxy ssh "$REMOTE_HOST" "set -e; podman stop '$CLICKHOUSE_CONTAINER' >/dev/null; podman cp '$REMOTE_ROOT/deploy/clickhouse/config.d/logging.xml' '$CLICKHOUSE_CONTAINER:/etc/clickhouse-server/config.d/logging.xml'; podman cp '$REMOTE_ROOT/deploy/clickhouse/users.d/profilers.xml' '$CLICKHOUSE_CONTAINER:/etc/clickhouse-server/users.d/profilers.xml'; find '$REMOTE_ROOT/data/clickhouse-logs' -maxdepth 1 -type f \( -name 'clickhouse-server.log*' -o -name 'clickhouse-server.err.log*' \) -delete; podman start '$CLICKHOUSE_CONTAINER' >/dev/null"
wait_for_clickhouse

cleanup_statements=(
  "TRUNCATE TABLE IF EXISTS system.metric_log"
  "TRUNCATE TABLE IF EXISTS system.trace_log"
  "TRUNCATE TABLE IF EXISTS system.text_log"
  "TRUNCATE TABLE IF EXISTS system.asynchronous_metric_log"
  "TRUNCATE TABLE IF EXISTS system.opentelemetry_span_log"
  "TRUNCATE TABLE IF EXISTS system.processors_profile_log"
  "TRUNCATE TABLE IF EXISTS system.query_metric_log"
  "TRUNCATE TABLE IF EXISTS system.background_schedule_pool_log"
)
for sql in "${cleanup_statements[@]}"; do
  remote_sql "$sql"
done

retained_log_count=0
for table in system.query_log system.part_log system.error_log; do
  table_exists="$(remote_sql "EXISTS TABLE $table" | tr -d '[:space:]')"
  if [[ ! "$table_exists" =~ ^[01]$ ]]; then
    printf 'invalid table-existence result for %s: %s\n' "$table" "$table_exists" >&2
    exit 1
  fi
  if [[ "$table_exists" == "1" ]]; then
    remote_sql "ALTER TABLE $table MODIFY TTL event_date + INTERVAL 7 DAY DELETE"
    retained_log_count=$((retained_log_count + 1))
  fi
done
remote_sql "SELECT name, create_table_query FROM system.tables WHERE database = 'system' AND name IN ('query_log', 'part_log', 'error_log') ORDER BY name FORMAT TSV" > "$task_tmp/ttl-table-definitions.tsv"
ttl_log_count="$(remote_sql "SELECT count() FROM system.tables WHERE database = 'system' AND name IN ('query_log', 'part_log', 'error_log') AND (position(create_table_query, 'TTL event_date + toIntervalDay(7)') > 0 OR position(create_table_query, 'TTL event_date + INTERVAL 7 DAY') > 0)" | tr -d '[:space:]')"
if [[ "$ttl_log_count" != "$retained_log_count" ]]; then
  printf 'retained ClickHouse log TTL verification failed: expected=%s actual=%s\n' "$retained_log_count" "$ttl_log_count" >&2
  exit 1
fi
rtk proxy scp "$task_tmp/ttl-table-definitions.tsv" "$REMOTE_HOST:$evidence/ttl-table-definitions.tsv"

remote_sql "$business_rows_sql" > "$task_tmp/business-rows-after.txt"
remote_sql "$business_tables_sql" > "$task_tmp/business-tables-after.tsv"
cut -f1,2 "$task_tmp/business-tables-before.tsv" | sort > "$task_tmp/business-table-names-before.tsv"
cut -f1,2 "$task_tmp/business-tables-after.tsv" | sort > "$task_tmp/business-table-names-after.tsv"
comm -23 "$task_tmp/business-table-names-before.tsv" "$task_tmp/business-table-names-after.tsv" > "$task_tmp/missing-business-tables.tsv"
if [[ -s "$task_tmp/missing-business-tables.tsv" ]]; then
  printf 'business tables disappeared during remediation:\n' >&2
  sed 's/^/  /' "$task_tmp/missing-business-tables.tsv" >&2
  exit 1
fi
after_business_rows="$(tr -d '[:space:]' < "$task_tmp/business-rows-after.txt")"
if [[ ! "$before_business_rows" =~ ^[0-9]+$ || ! "$after_business_rows" =~ ^[0-9]+$ || "$after_business_rows" -lt "$before_business_rows" ]]; then
  printf 'business row preservation check failed: before=%s after=%s\n' "$before_business_rows" "$after_business_rows" >&2
  exit 1
fi
rtk proxy ssh "$REMOTE_HOST" "printf '%s\n' '$after_business_rows' > '$evidence/business-rows-after.txt'"
rtk proxy scp "$task_tmp/business-tables-after.tsv" "$REMOTE_HOST:$evidence/business-tables-after.tsv"

capture_other_container_ids "$evidence/after-container-ids.tsv"
rtk proxy ssh "$REMOTE_HOST" "set -e; cmp -s '$evidence/before-container-ids.tsv' '$evidence/after-container-ids.tsv'; podman inspect '$CLICKHOUSE_CONTAINER' --format '{{range .Mounts}}{{println .Destination .Options}}{{end}}' > '$evidence/clickhouse-mounts.txt'; podman exec '$CLICKHOUSE_CONTAINER' test -r /etc/clickhouse-server/config.d/logging.xml; podman exec '$CLICKHOUSE_CONTAINER' test -r /etc/clickhouse-server/users.d/profilers.xml; podman exec '$CLICKHOUSE_CONTAINER' clickhouse extract-from-config --config-file /etc/clickhouse-server/config.xml --key logger.level > '$evidence/logger-level.txt'; grep -qx warning '$evidence/logger-level.txt'; podman exec '$CLICKHOUSE_CONTAINER' clickhouse extract-from-config --config-file /etc/clickhouse-server/config.xml --key total_memory_profiler_step > '$evidence/total-memory-profiler-step.txt'; grep -qx 0 '$evidence/total-memory-profiler-step.txt'; du -sk '$REMOTE_ROOT/data/clickhouse-logs' > '$evidence/storage-after.tsv'"
remote_sql "SELECT name, value FROM system.settings WHERE name IN ('query_profiler_real_time_period_ns', 'query_profiler_cpu_time_period_ns', 'memory_profiler_step') ORDER BY name FORMAT TSV" > "$task_tmp/profiler-settings.tsv"
awk 'NF != 2 || $2 != "0" { exit 1 } END { if (NR != 3) exit 1 }' "$task_tmp/profiler-settings.tsv"
rtk proxy scp "$task_tmp/profiler-settings.tsv" "$REMOTE_HOST:$evidence/profiler-settings.tsv"

log_bytes_before="$(rtk proxy ssh "$REMOTE_HOST" "find '$REMOTE_ROOT/data/clickhouse-logs' -maxdepth 1 -type f -exec stat -c %s {} + | awk '{sum += \$1} END {print sum + 0}'")"
sleep 30
log_bytes_after="$(rtk proxy ssh "$REMOTE_HOST" "find '$REMOTE_ROOT/data/clickhouse-logs' -maxdepth 1 -type f -exec stat -c %s {} + | awk '{sum += \$1} END {print sum + 0}'")"
if (( log_bytes_after - log_bytes_before > 10485760 )); then
  printf 'ClickHouse logs grew by more than 10 MiB in 30 seconds\n' >&2
  exit 1
fi
disabled_log_rows="$(remote_sql "SELECT coalesce(sum(coalesce(total_rows, 0)), 0) FROM system.tables WHERE database = 'system' AND name IN ('metric_log', 'trace_log', 'text_log', 'asynchronous_metric_log', 'opentelemetry_span_log', 'processors_profile_log', 'query_metric_log', 'background_schedule_pool_log')" | tr -d '[:space:]')"
if [[ "$disabled_log_rows" != "0" ]]; then
  printf 'disabled ClickHouse system logs resumed writing: rows=%s\n' "$disabled_log_rows" >&2
  exit 1
fi
rtk proxy ssh "$REMOTE_HOST" "set -e; log_file_count=\$(find '$REMOTE_ROOT/data/clickhouse-logs' -maxdepth 1 -type f -name 'clickhouse-server*.log' | wc -l); if test \"\$log_file_count\" -gt 0 && grep -E 'metric_log.*MEMORY_LIMIT_EXCEEDED|MEMORY_LIMIT_EXCEEDED.*metric_log' '$REMOTE_ROOT/data/clickhouse-logs'/clickhouse-server*.log >/dev/null 2>&1; then exit 1; fi; printf 'before_bytes\t%s\nafter_bytes\t%s\nlog_file_count\t%s\n' '$log_bytes_before' '$log_bytes_after' \"\$log_file_count\" > '$evidence/log-growth-30s.tsv'"

bash "$SCRIPT_DIR/check-voice-trace.sh"

rollback_enabled=0
printf 'ClickHouse logging remediated on %s; evidence: %s\n' "$REMOTE_HOST" "$evidence"

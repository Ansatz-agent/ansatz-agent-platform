from __future__ import annotations

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class VoiceTraceDeployScriptContractTest(unittest.TestCase):
    def read(self, name: str) -> str:
        path = ROOT / "scripts" / name
        self.assertTrue(path.is_file(), f"missing {path}")
        return path.read_text(encoding="utf-8")

    def test_deploy_is_exact_scoped_backup_first_and_non_destructive(self) -> None:
        source = self.read("deploy-voice-trace.sh")
        self.assertIn("REMOTE_HOST=hermes", source)
        self.assertIn("REMOTE_ROOT=/data/ansatz-agent/voice-trace", source)
        self.assertIn("COMPOSE_PROJECT=ansatz-voice-trace-20260823", source)
        self.assertNotIn("agent-history-portal.service", source)
        self.assertIn("ansatz-auth-traces-images-20260824.tar.gz", source)
        self.assertIn("podman load", source)
        self.assertIn("/usr/bin/podman-compose", source)
        self.assertIn("config > /dev/null", source)
        self.assertIn("deploy/clickhouse/config.d", source)
        self.assertIn("deploy/clickhouse/users.d", source)
        self.assertIn("logging.xml", source)
        self.assertIn("profilers.xml", source)
        self.assertIn(
            'bash "$SCRIPT_DIR/configure-voice-trace-npm.sh"',
            source,
        )
        self.assertLess(
            source.index('bash "$SCRIPT_DIR/configure-voice-trace-npm.sh"'),
            source.index('bash "$SCRIPT_DIR/check-voice-trace.sh"'),
        )
        self.assertNotIn("docker system prune", source)
        self.assertNotIn("docker volume prune", source)
        self.assertNotIn("ansatz-source-langfuse-20260822 down", source)
        self.assertNotIn("rm -rf", source)

    def test_check_requires_every_service_and_no_public_ports(self) -> None:
        source = self.read("check-voice-trace.sh")
        self.assertIn("podman ps -a --format", source)
        self.assertNotIn("podman-compose --env-file", source)
        for required in (
            "auth-service",
            "trace-gateway",
            "langfuse-web",
            "langfuse-worker",
            "postgres",
            "clickhouse",
            "redis",
            "minio",
            "/trace-gateway healthcheck",
            "published port detected",
            ":3000/langfuse/api/public/health",
            "https://c2sml.cn/agent",
            "https://c2sml.cn/auth/login/",
            "https://c2sml.cn/traces/",
            "https://c2sml.cn/langfuse/",
        ):
            self.assertIn(required, source)
        for code in ('"404"', '"200"', '"302"'):
            self.assertIn(code, source)
        self.assertNotIn("PASSWORD", source)
        self.assertNotIn("LANGFUSE_SECRET_KEY", source)

    def test_npm_configurator_is_backup_first_exact_and_secret_safe(self) -> None:
        source = self.read("configure-voice-trace-npm.sh")
        for required in (
            "REMOTE_HOST=hermes",
            "REMOTE_ROOT=/data/ansatz-agent/voice-trace",
            "NPM_ROOT=/root/nginx-proxy-manager",
            "server_proxy.conf",
            "update_npm_compose.py",
            "database.sqlite",
            "proxy_host/1.conf",
            "podman-compose",
            "config > /dev/null",
            "--force-recreate",
            "nginx -t",
            "nginx -s reload",
        ):
            self.assertIn(required, source)
        self.assertLess(
            source.index('backup="$REMOTE_ROOT/backups/npm-'),
            source.index("python3 '$NPM_UPDATER_REMOTE'"),
        )
        self.assertNotIn("PASSWORD", source)
        self.assertNotIn("SECRET", source)
        self.assertNotIn("NPM_OVERRIDE_LOCAL", source)
        self.assertNotIn("podman network connect", source)
        self.assertNotIn("sed -i", source)
        self.assertNotIn("system prune", source)
        self.assertNotIn("rm -rf", source)

    def test_check_uses_data_release_root(self) -> None:
        source = self.read("check-voice-trace.sh")
        self.assertIn("REMOTE_ROOT=/data/ansatz-agent/voice-trace", source)
        self.assertIn("COMPOSE_PROJECT=ansatz-voice-trace-20260823", source)

    def test_clickhouse_log_remediation_is_exact_backup_first_and_business_safe(self) -> None:
        source = self.read("remediate-clickhouse-logging.sh")
        for required in (
            "REMOTE_HOST=hermes",
            "REMOTE_ROOT=/data/ansatz-agent/voice-trace",
            "COMPOSE_PROJECT=ansatz-voice-trace-20260823",
            "ansatz-voice-trace-20260823_clickhouse_1",
            "clickhouse/config.d/logging.xml",
            "clickhouse/users.d/profilers.xml",
            "/usr/bin/podman-compose",
            "config > /dev/null",
            "system.metric_log",
            "system.trace_log",
            "system.text_log",
            "system.asynchronous_metric_log",
            "system.opentelemetry_span_log",
            "system.processors_profile_log",
            "system.query_metric_log",
            "system.background_schedule_pool_log",
            "clickhouse-server.log*",
            "clickhouse-server.err.log*",
            "default.events",
            "default.events_core FINAL",
            "comm -23",
            "ttl-table-definitions.tsv",
            "create_table_query",
            "log_file_count",
            "expected-compose-before.yml",
            "remote-compose-before.yml",
            "ROLLBACK SUCCEEDED",
            "ROLLBACK FAILED",
            "podman cp",
            "podman mount",
            "podman unmount",
            "before-container-ids.tsv",
            "after-container-ids.tsv",
            'bash "$SCRIPT_DIR/check-voice-trace.sh"',
        ):
            with self.subTest(required=required):
                self.assertIn(required, source)

        self.assertLess(source.index('backup="$REMOTE_ROOT/backups/clickhouse-logging-'), source.index("podman stop"))
        self.assertLess(source.index("before-container-ids.tsv"), source.index("podman stop"))
        self.assertLess(source.index("default.events"), source.index("TRUNCATE TABLE IF EXISTS system.metric_log"))
        self.assertIn("container_mutated=0", source)
        self.assertIn("container_mutated=1", source)
        self.assertIn('if [[ "$container_mutated" -eq 1 ]]', source)
        self.assertLess(source.rindex("container_mutated=1"), source.rindex("podman stop"))
        self.assertIn('"set -e; cmp -s', source)
        self.assertIn('"set -e; install -d -m 0700', source)
        self.assertIn('cmp -s "$task_tmp/expected-compose-before.yml" "$task_tmp/remote-compose-before.yml"', source)
        self.assertNotIn("/^${COMPOSE_PROJECT}_/", source)
        for forbidden in (
            "rm -rf",
            "podman system prune",
            "podman volume prune",
            "TRUNCATE TABLE default",
            "DROP TABLE default",
            "postgres",
            "auth-service",
            "ALTER TABLE IF EXISTS",
            "'$REMOTE_ROOT/data/clickhouse-logs' '$REMOTE_ROOT/data/clickhouse'",
            "podman rm '$CLICKHOUSE_CONTAINER'",
            '\\$1 != \"$CLICKHOUSE_CONTAINER\"',
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, source)


if __name__ == "__main__":
    unittest.main()

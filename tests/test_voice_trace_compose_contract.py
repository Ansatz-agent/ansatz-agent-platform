from __future__ import annotations

import re
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
COMPOSE_PATH = ROOT / "deploy" / "voice-trace" / "docker-compose.yml"
ENV_EXAMPLE_PATH = ROOT / "deploy" / "voice-trace" / ".env.example"
CLICKHOUSE_SERVER_CONFIG_PATH = (
    ROOT / "deploy" / "voice-trace" / "clickhouse" / "config.d" / "logging.xml"
)
CLICKHOUSE_USER_CONFIG_PATH = (
    ROOT / "deploy" / "voice-trace" / "clickhouse" / "users.d" / "profilers.xml"
)


class VoiceTraceComposeContractTest(unittest.TestCase):
    def load_compose(self) -> tuple[str, dict]:
        self.assertTrue(COMPOSE_PATH.is_file(), f"missing {COMPOSE_PATH}")
        source = COMPOSE_PATH.read_text(encoding="utf-8")
        digest = "sha256:" + "a" * 64
        variables = {
            "DEPLOY_ROOT": "/data/ansatz-agent/voice-trace",
            "NPM_NETWORK": "nginx-proxy-manager_default",
            "AUTH_SERVICE_IMAGE": "localhost/ansatz-auth-service:d728ff78fd8b",
            "TRACE_GATEWAY_IMAGE": "localhost/ansatz-trace-gateway:615a97a0f2dd",
            "LANGFUSE_WEB_IMAGE": f"localhost/ansatz-langfuse-web@{digest}",
            "LANGFUSE_WORKER_IMAGE": f"localhost/ansatz-langfuse-worker@{digest}",
            "POSTGRES_IMAGE": f"docker.io/library/postgres@{digest}",
            "CLICKHOUSE_IMAGE": f"docker.io/clickhouse/clickhouse-server@{digest}",
            "REDIS_IMAGE": f"docker.io/library/redis@{digest}",
            "MINIO_IMAGE": f"cgr.dev/chainguard/minio@{digest}",
            "DJANGO_SECRET_KEY": "redacted-django-secret",
            "TRACE_GATEWAY_INTERNAL_SECRET": "redacted-service-secret",
            "DATABASE_URL": "postgresql://postgres:redacted@postgres:5432/postgres",
            "POSTGRES_USER": "postgres",
            "POSTGRES_PASSWORD": "redacted-postgres",
            "POSTGRES_DB": "postgres",
            "CLICKHOUSE_USER": "clickhouse",
            "CLICKHOUSE_PASSWORD": "redacted-clickhouse",
            "REDIS_AUTH": "redacted-redis",
            "MINIO_ROOT_USER": "ansatz-minio",
            "MINIO_ROOT_PASSWORD": "redacted-minio",
            "NEXTAUTH_SECRET": "redacted-nextauth",
            "SALT": "redacted-salt",
            "ENCRYPTION_KEY": "b" * 64,
            "LANGFUSE_S3_EVENT_UPLOAD_ACCESS_KEY_ID": "ansatz-minio",
            "LANGFUSE_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY": "redacted-minio",
            "LANGFUSE_S3_MEDIA_UPLOAD_ACCESS_KEY_ID": "ansatz-minio",
            "LANGFUSE_S3_MEDIA_UPLOAD_SECRET_ACCESS_KEY": "redacted-minio",
            "LANGFUSE_S3_BATCH_EXPORT_ACCESS_KEY_ID": "ansatz-minio",
            "LANGFUSE_S3_BATCH_EXPORT_SECRET_ACCESS_KEY": "redacted-minio",
            "LANGFUSE_INIT_ORG_ID": "ansatz-voice-trace-org",
            "LANGFUSE_INIT_ORG_NAME": "Ansatz Voice Trace",
            "LANGFUSE_INIT_PROJECT_ID": "ansatz-voice-trace-project",
            "LANGFUSE_INIT_PROJECT_NAME": "Ansatz Voice Trace",
            "LANGFUSE_INIT_PROJECT_PUBLIC_KEY": "pk-lf-redacted",
            "LANGFUSE_INIT_PROJECT_SECRET_KEY": "sk-lf-redacted",
            "LANGFUSE_INIT_USER_EMAIL": "trace-admin-20260823@example.local",
            "LANGFUSE_INIT_USER_NAME": "Trace Administrator 20260823",
            "LANGFUSE_INIT_USER_PASSWORD": "redacted-admin",
        }

        def replace(match: re.Match[str]) -> str:
            name = match.group(1)
            self.assertIn(name, variables, f"fixture lacks {name}")
            return variables[name]

        rendered = re.sub(r"\$\{([A-Z0-9_]+)\}", replace, source)
        self.assertNotRegex(rendered, r"\$\{")
        return source, yaml.safe_load(rendered)

    def test_services_images_and_loopback_exposure_are_exact(self) -> None:
        source, compose = self.load_compose()
        services = compose["services"]
        self.assertEqual(
            set(services),
            {
                "auth-service",
                "trace-gateway",
                "langfuse-web",
                "langfuse-worker",
                "postgres",
                "clickhouse",
                "redis",
                "minio",
            },
        )
        for name in services:
            self.assertNotIn("ports", services[name], name)
        self.assertNotIn(":latest", source)
        for name in ("postgres", "clickhouse", "redis", "minio"):
            self.assertIn("@sha256:", services[name]["image"], name)

    def test_networks_enforce_only_required_service_paths(self) -> None:
        source, compose = self.load_compose()
        self.assertEqual(
            set(compose["networks"]),
            {"service"},
        )
        for name in ("service",):
            network = compose["networks"][name]
            self.assertIsNot(network.get("external"), True)
            self.assertIsNot(network.get("internal"), True)
        self.assertEqual(
            compose["networks"]["service"]["ipam"]["config"],
            [{"subnet": "10.89.2.0/24"}],
        )
        expected = {
            "auth-service": {"service"},
            "trace-gateway": {"service"},
            "langfuse-web": {"service"},
            "langfuse-worker": {"service"},
            "postgres": {"service"},
            "clickhouse": {"service"},
            "redis": {"service"},
            "minio": {"service"},
        }
        for name, networks in expected.items():
            self.assertEqual(set(compose["services"][name]["networks"]), networks, name)
        self.assertNotIn(
            "aliases",
            compose["services"]["auth-service"]["networks"]["service"],
        )
        self.assertEqual(
            compose["services"]["auth-service"]["networks"]["service"]["ipv4_address"],
            "10.89.2.32",
        )
        self.assertEqual(
            compose["services"]["langfuse-web"]["networks"]["service"]["ipv4_address"],
            "10.89.2.38",
        )
        self.assertEqual(
            compose["services"]["trace-gateway"]["networks"]["service"]["ipv4_address"],
            "10.89.2.39",
        )
        self.assertNotIn("nginx-proxy-manager_default", source)

    def test_auth_and_gateway_credentials_are_private_and_distinct(self) -> None:
        source, compose = self.load_compose()
        services = compose["services"]
        auth_env = services["auth-service"]["environment"]
        gateway_env = services["trace-gateway"]["environment"]
        self.assertEqual(auth_env["TRACE_GATEWAY_INTERNAL_SECRET"], "redacted-service-secret")
        self.assertEqual(gateway_env["TRACE_GATEWAY_INTERNAL_SECRET"], "redacted-service-secret")
        self.assertEqual(
            gateway_env["AUTH_INTROSPECTION_URL"],
            "http://auth-service:8000/internal/trace-token/introspect/",
        )
        self.assertEqual(
            gateway_env["LANGFUSE_OTLP_TRACES_URL"],
            "http://langfuse-web:3000/langfuse/api/public/otel/v1/traces",
        )
        self.assertEqual(
            auth_env["LANGFUSE_INTERNAL_BASE_URL"],
            "http://langfuse-web:3000/langfuse/api/public",
        )
        self.assertEqual(auth_env["LANGFUSE_PROJECT_PUBLIC_KEY"], "pk-lf-redacted")
        self.assertEqual(auth_env["LANGFUSE_PROJECT_SECRET_KEY"], "sk-lf-redacted")
        self.assertNotIn("LANGFUSE_INIT_USER_PASSWORD", gateway_env)
        self.assertNotIn("Basic ", source)

    def test_auth_healthcheck_is_a_single_shell_command(self) -> None:
        _, compose = self.load_compose()
        healthcheck = compose["services"]["auth-service"]["healthcheck"]

        self.assertEqual(healthcheck["test"][0], "CMD-SHELL")
        self.assertEqual(len(healthcheck["test"]), 2)
        self.assertIn("python -c", healthcheck["test"][1])
        self.assertIn("/healthz", healthcheck["test"][1])

    def test_trace_gateway_has_private_durable_inbox_and_exact_safety_limits(self) -> None:
        _, compose = self.load_compose()
        gateway = compose["services"]["trace-gateway"]
        self.assertEqual(
            gateway.get("volumes"),
            ["/data/ansatz-agent/voice-trace/data/trace-gateway:/data"],
        )
        self.assertEqual(gateway["environment"].get("TRACE_GATEWAY_INBOX_PATH"), "/data/inbox.db")
        self.assertEqual(gateway["environment"].get("TRACE_GATEWAY_RECEIPT_RETENTION"), "720h")
        self.assertEqual(gateway["environment"].get("TRACE_GATEWAY_MAX_DB_BYTES"), "68719476736")
        self.assertEqual(gateway["environment"].get("TRACE_GATEWAY_MIN_FREE_BYTES"), "10737418240")

    def test_langfuse_is_public_url_signup_disabled_and_persistent(self) -> None:
        _, compose = self.load_compose()
        web_env = compose["services"]["langfuse-web"]["environment"]
        self.assertEqual(web_env["NEXTAUTH_URL"], "https://c2sml.cn/langfuse")
        self.assertEqual(web_env["NEXT_PUBLIC_BASE_PATH"], "/langfuse")
        self.assertEqual(
            compose["services"]["langfuse-worker"]["environment"][
                "NEXT_PUBLIC_BASE_PATH"
            ],
            "/langfuse",
        )
        self.assertEqual(web_env["AUTH_DISABLE_SIGNUP"], "true")
        self.assertEqual(web_env["TELEMETRY_ENABLED"], "false")
        self.assertEqual(
            compose["services"]["auth-service"]["volumes"],
            ["/data/ansatz-agent/voice-trace/data/auth:/data"],
        )
        for name in ("auth-service", "postgres", "redis", "minio"):
            for mount in compose["services"][name].get("volumes", []):
                source = mount.split(":", 1)[0]
                self.assertTrue(source.startswith("/data/ansatz-agent/voice-trace/data/"), source)

    def test_clickhouse_logging_is_bounded_and_unused_system_logs_are_disabled(self) -> None:
        _, compose = self.load_compose()
        self.assertTrue(CLICKHOUSE_SERVER_CONFIG_PATH.is_file())
        self.assertTrue(CLICKHOUSE_USER_CONFIG_PATH.is_file())

        clickhouse = compose["services"]["clickhouse"]
        self.assertEqual(
            clickhouse["volumes"],
            [
                "/data/ansatz-agent/voice-trace/data/clickhouse:/var/lib/clickhouse",
                "/data/ansatz-agent/voice-trace/data/clickhouse-logs:/var/log/clickhouse-server",
                "/data/ansatz-agent/voice-trace/deploy/clickhouse/config.d/logging.xml:/etc/clickhouse-server/config.d/logging.xml:ro",
                "/data/ansatz-agent/voice-trace/deploy/clickhouse/users.d/profilers.xml:/etc/clickhouse-server/users.d/profilers.xml:ro",
            ],
        )

        server_root = ET.parse(CLICKHOUSE_SERVER_CONFIG_PATH).getroot()
        self.assertEqual(server_root.findtext("logger/level"), "warning")
        self.assertEqual(server_root.findtext("logger/size"), "100M")
        self.assertEqual(server_root.findtext("logger/count"), "3")
        self.assertEqual(server_root.findtext("total_memory_profiler_step"), "0")
        for name in (
            "metric_log",
            "trace_log",
            "text_log",
            "asynchronous_metric_log",
            "opentelemetry_span_log",
            "processors_profile_log",
            "query_metric_log",
            "background_schedule_pool_log",
        ):
            with self.subTest(system_log=name):
                self.assertEqual(server_root.find(name).get("remove"), "1")
        for name in ("query_log", "part_log", "error_log"):
            with self.subTest(retained_log=name):
                self.assertEqual(
                    server_root.findtext(f"{name}/ttl"),
                    "event_date + INTERVAL 7 DAY DELETE",
                )

        user_root = ET.parse(CLICKHOUSE_USER_CONFIG_PATH).getroot()
        for setting in (
            "query_profiler_real_time_period_ns",
            "query_profiler_cpu_time_period_ns",
            "memory_profiler_step",
        ):
            with self.subTest(profile_setting=setting):
                self.assertEqual(user_root.findtext(f"profiles/default/{setting}"), "0")

        query_guards = {
            "max_memory_usage": "1073741824",
            "max_result_bytes": "67108864",
            "max_execution_time": "35",
            "max_threads": "2",
        }
        for setting, expected in query_guards.items():
            with self.subTest(query_guard=setting):
                self.assertEqual(
                    user_root.findtext(f"profiles/default/{setting}"), expected
                )
                self.assertEqual(
                    user_root.findtext(
                        f"profiles/default/constraints/{setting}/max"
                    ),
                    expected,
                )
        self.assertEqual(
            user_root.findtext("profiles/default/result_overflow_mode"), "throw"
        )
        self.assertEqual(
            user_root.findtext("profiles/default/timeout_overflow_mode"), "throw"
        )

        combined = CLICKHOUSE_SERVER_CONFIG_PATH.read_text() + CLICKHOUSE_USER_CONFIG_PATH.read_text()
        for forbidden in ("password", "secret", "CLICKHOUSE_PASSWORD"):
            self.assertNotIn(forbidden, combined.lower())

    def test_minio_creates_the_langfuse_bucket_before_serving(self) -> None:
        _, compose = self.load_compose()
        minio = compose["services"]["minio"]
        self.assertEqual(minio["entrypoint"], "sh")
        self.assertEqual(
            minio["command"],
            "-c 'mkdir -p /data/langfuse && minio server --address \":9000\" "
            "--console-address \":9001\" /data'",
        )

    def test_example_has_names_and_instructions_but_no_usable_secrets(self) -> None:
        self.assertTrue(ENV_EXAMPLE_PATH.is_file(), f"missing {ENV_EXAMPLE_PATH}")
        text = ENV_EXAMPLE_PATH.read_text(encoding="utf-8")
        self.assertIn("bootstrap-voice-trace-secrets.sh", text)
        for forbidden in ("CHANGEME", "password=", "sk-lf-", "Basic ", ":latest"):
            self.assertNotIn(forbidden, text)
        self.assertIn("DEPLOY_ROOT=/data/ansatz-agent/voice-trace", text)
        for required in (
            "DJANGO_SECRET_KEY",
            "TRACE_GATEWAY_INTERNAL_SECRET",
            "LANGFUSE_INIT_PROJECT_SECRET_KEY",
            "LANGFUSE_INIT_USER_PASSWORD",
        ):
            self.assertRegex(text, rf"(?m)^{required}=<generated-owner-only>$")

    def test_trace_continuity_runbook_documents_exact_storage_and_recovery_contract(self) -> None:
        path = ROOT / "docs" / "runbooks" / "trace-upload-continuity.md"
        self.assertTrue(path.is_file(), f"missing {path}")
        text = path.read_text(encoding="utf-8")
        for required in (
            "2 GiB per account",
            "30 days",
            "64 MiB maximum encrypted record",
            "single `active.segment` can grow to the 2 GiB per-account cap",
            "Brotli then AES-256-GCM",
            "greater of 1 GiB or 5%",
            "64 GiB bbolt ceiling",
            "10 GiB minimum free space",
            "720h receipts",
            "accepted-undelivered data is never auto-evicted",
            "accepted",
            "duplicate",
            "503 storage_unavailable",
            "Retry-After",
            "Retry-After: 60",
            "application/x-protobuf",
            "key loss",
            "owner mismatch",
            "read-only diagnostics",
            "offline compaction",
            "authoritative active-work counter",
            "startup maintenance is asynchronous",
            "idle transition triggers maintenance",
            "one encrypted record at a time",
            "duplicate receipt also wakes",
            "Sign out preserves data",
            "structured revocation pauses upload",
            "rollback",
        ):
            with self.subTest(required=required):
                self.assertIn(required, text)
        self.assertNotIn("507 storage_unavailable", text)
        self.assertNotIn("64 MiB segment,", text)
        self.assertNotIn("The application performs the supported compaction", text)


if __name__ == "__main__":
    unittest.main()

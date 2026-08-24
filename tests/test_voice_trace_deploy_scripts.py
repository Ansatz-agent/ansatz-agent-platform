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


if __name__ == "__main__":
    unittest.main()

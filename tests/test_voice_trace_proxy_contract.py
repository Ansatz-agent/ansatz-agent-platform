from __future__ import annotations

import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
NGINX = ROOT / "deploy" / "voice-trace" / "nginx"


class VoiceTraceProxyContractTest(unittest.TestCase):
    def read(self, name: str) -> str:
        path = NGINX / name
        self.assertTrue(path.is_file(), f"missing {path}")
        return path.read_text(encoding="utf-8")

    def test_root_host_exposes_auth_personal_traces_ingest_and_admin_langfuse(self) -> None:
        source = self.read("server_proxy.conf")
        for required in (
            "location = /auth",
            "return 308 /auth/",
            "location ^~ /auth/",
            "location = /traces",
            "return 308 /traces/",
            "location ^~ /traces/",
            "proxy_pass http://auth-service:8000",
            "location = /trace-ingest/v1/traces",
            "limit_except POST",
            "proxy_pass http://trace-gateway:8080/v1/traces",
            "client_max_body_size 8m",
            "proxy_read_timeout 15s",
            "proxy_send_timeout 15s",
            "proxy_cache off",
            'add_header Cache-Control "no-store" always',
            "proxy_set_header Authorization $http_authorization",
            "location = /langfuse",
            "return 308 /langfuse/",
            "location ^~ /langfuse/",
            "proxy_pass http://langfuse-web:3000",
            'proxy_set_header Authorization ""',
            "location = /agent",
            "location ^~ /agent/",
            "location ^~ /internal/",
            "location = /healthz",
        ):
            self.assertIn(required, source)
        self.assertNotIn("LANGFUSE_SECRET", source)

        deny = source.index("location ^~ /internal/")
        ingest = source.index("location = /trace-ingest/v1/traces")
        self.assertLess(deny, ingest)
        self.assertIn("return 404", source[deny:ingest])

    def test_legacy_agent_proxy_fragment_is_removed(self) -> None:
        self.assertFalse((NGINX / "c2sml.cn-agent.conf").exists())

    def test_native_session_routes_forward_bearer_without_exposing_it_to_legacy_auth(self) -> None:
        source = self.read("server_proxy.conf")
        broad_auth_start = source.index("location ^~ /auth/ {")
        broad_auth_end = source.index("\n}\n", broad_auth_start)
        broad_auth = source[broad_auth_start:broad_auth_end]
        self.assertIn('proxy_set_header Authorization ""', broad_auth)

        for route in (
            "/auth/api/client-session/",
            "/auth/api/client-session/current/",
            "/auth/api/client-session/trace-token/",
        ):
            marker = f"location = {route}"
            self.assertIn(marker, source)
            start = source.index(marker)
            end = source.index("\n}\n", start)
            block = source[start:end]
            self.assertIn("proxy_set_header Authorization $http_authorization", block)
            self.assertIn(
                "proxy_set_header X-Ansatz-Installation-ID $http_x_ansatz_installation_id",
                block,
            )

if __name__ == "__main__":
    unittest.main()

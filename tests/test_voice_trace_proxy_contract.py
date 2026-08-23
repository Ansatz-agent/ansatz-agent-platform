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

    def test_root_host_exposes_bounded_ingest_and_prefixed_dashboard(self) -> None:
        source = self.read("server_proxy.conf")
        for required in (
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
        ):
            self.assertIn(required, source)
        self.assertNotIn("auth-service:8000", source)
        self.assertNotIn("LANGFUSE_SECRET", source)

        deny = source.index("location ^~ /agent/internal/")
        ingest = source.index("location = /trace-ingest/v1/traces")
        self.assertLess(deny, ingest)
        self.assertIn("return 404", source[deny:ingest])

    def test_root_agent_proxy_denies_private_introspection_before_general_agent_route(self) -> None:
        source = self.read("c2sml.cn-agent.conf")
        deny = source.index("location ^~ /agent/internal/")
        general = source.index("location ^~ /agent/ {")
        self.assertLess(deny, general)
        self.assertIn("return 404", source[deny:general])
        self.assertIn("proxy_pass http://auth-service:8000", source)

if __name__ == "__main__":
    unittest.main()

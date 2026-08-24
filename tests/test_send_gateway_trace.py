from __future__ import annotations

import importlib.util
import json
import unittest
from pathlib import Path

import httpx


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "scripts" / "send_gateway_trace.py"


def load_module():
    spec = importlib.util.spec_from_file_location("send_gateway_trace", MODULE_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {MODULE_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class SendGatewayTraceTest(unittest.TestCase):
    def test_builds_deterministic_protobuf_with_full_semantics(self) -> None:
        module = load_module()

        first = module.build_trace_payload(
            label="A",
            trace_id=bytes.fromhex("11" * 16),
            root_span_id=bytes.fromhex("22" * 8),
            start_ns=1_000_000_000,
        )
        second = module.build_trace_payload(
            label="A",
            trace_id=bytes.fromhex("11" * 16),
            root_span_id=bytes.fromhex("22" * 8),
            start_ns=1_000_000_000,
        )

        self.assertEqual(first, second)
        self.assertTrue(first.startswith(b"\x0a"))
        for semantic in (
            b"complete user prompt A",
            b"complete model response A",
            b"read_file",
            b"tool arguments A",
            b"tool result A",
            b"voice transcript A",
            b"forged-langfuse-user-A",
        ):
            self.assertIn(semantic, first)

    def test_real_http_flow_uses_session_token_and_retries_identical_bytes(self) -> None:
        module = load_module()
        password = "password-sentinel-that-must-not-be-returned"
        upload_token = "upload-token-sentinel-12345678901234567890"
        payloads: list[bytes] = []

        def handler(request: httpx.Request) -> httpx.Response:
            if request.method == "GET" and request.url.path == "/auth/login/":
                return httpx.Response(
                    200,
                    headers={
                        "set-cookie": "__Host-ansatz_csrftoken=csrf-value; Secure; Path=/"
                    },
                )
            if request.method == "POST" and request.url.path == "/auth/login/":
                self.assertEqual(request.headers["x-csrftoken"], "csrf-value")
                self.assertIn(password.encode(), request.content)
                return httpx.Response(
                    302,
                    headers=[
                        ("location", "/traces/"),
                        ("set-cookie", "__Host-ansatz_sessionid=session-value; Secure; Path=/"),
                        ("set-cookie", "__Host-ansatz_csrftoken=rotated-csrf; Secure; Path=/"),
                    ],
                )
            if request.method == "POST" and request.url.path == "/auth/api/trace-token/":
                self.assertEqual(request.headers["x-csrftoken"], "rotated-csrf")
                request_body = json.loads(request.content)
                self.assertEqual(
                    request_body["installation_id"],
                    "11111111-1111-4111-8111-111111111111",
                )
                return httpx.Response(
                    201,
                    json={
                        "access_token": upload_token,
                        "expires_in": 900,
                        "expires_at": "2026-08-23T10:00:00+00:00",
                        "installation_id": request_body["installation_id"],
                    },
                    headers={"cache-control": "no-store"},
                )
            if request.method == "POST" and request.url.path == "/trace-ingest/v1/traces":
                self.assertEqual(request.headers["authorization"], f"Bearer {upload_token}")
                self.assertEqual(request.headers["content-type"], "application/x-protobuf")
                payloads.append(request.content)
                return httpx.Response(
                    200,
                    content=b"\x0a\x00",
                    headers={"content-type": "application/x-protobuf"},
                )
            raise AssertionError(f"unexpected request: {request.method} {request.url}")

        credentials = {
            "AUTH_BASE_URL": "https://c2sml.cn/auth",
            "USER_A_ID": "42",
            "USER_A_USERNAME": "trace-user-a",
            "USER_A_PASSWORD": password,
            "USER_A_INSTALLATION_ID": "11111111-1111-4111-8111-111111111111",
        }
        result = module.upload_user_trace(
            credentials,
            "A",
            transport=httpx.MockTransport(handler),
            trace_id=bytes.fromhex("33" * 16),
            root_span_id=bytes.fromhex("44" * 8),
            start_ns=2_000_000_000,
        )

        self.assertEqual(result["upload_status"], 200)
        self.assertEqual(result["retry_status"], 200)
        self.assertEqual(len(payloads), 2)
        self.assertEqual(payloads[0], payloads[1])
        self.assertNotIn(password, json.dumps(result))
        self.assertNotIn(upload_token, json.dumps(result))


if __name__ == "__main__":
    unittest.main()

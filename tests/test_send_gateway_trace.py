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

    def test_retry_reuses_batch_and_digest_and_requires_durable_receipts(self) -> None:
        module = load_module()
        password = "password-sentinel-that-must-not-be-returned"
        upload_token = "upload-token-sentinel-12345678901234567890"
        payloads: list[bytes] = []
        uploads: list[httpx.Request] = []

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
                uploads.append(request)
                receipt = "accepted" if len(uploads) == 1 else "duplicate"
                return httpx.Response(
                    200,
                    content=b"\x0a\x00",
                    headers={
                        "content-type": "application/x-protobuf",
                        "x-trace-batch-id": request.headers["idempotency-key"],
                        "x-trace-receipt": receipt,
                    },
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
        self.assertEqual(len({request.headers["idempotency-key"] for request in uploads}), 1)
        batch_id = uploads[0].headers["idempotency-key"]
        self.assertRegex(
            batch_id,
            r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
        )
        self.assertEqual(
            {request.headers["x-trace-payload-sha256"] for request in uploads},
            {module.hashlib.sha256(payloads[0]).hexdigest()},
        )
        self.assertEqual(result["first_receipt"], "accepted")
        self.assertEqual(result["retry_receipt"], "duplicate")
        self.assertNotIn(password, json.dumps(result))
        self.assertNotIn(upload_token, json.dumps(result))

    def test_validates_exact_matching_structured_terminal_403_without_secrets(self) -> None:
        module = load_module()
        account_id = "11111111-1111-4111-8111-111111111111"
        session_id = "22222222-2222-4222-8222-222222222222"
        upload_token = "upload-token-sentinel-12345678901234567890"
        payload = "private-trace-payload-sentinel"
        response = httpx.Response(
            403,
            json={
                "state": "revoked",
                "code": "account_disabled",
                "account_id": account_id,
                "session_id": session_id,
                "revoked_at": "2099-08-23T14:00:00Z",
                "retryable": False,
            },
            request=httpx.Request(
                "POST",
                "https://c2sml.cn/trace-ingest/v1/traces",
                headers={"Authorization": f"Bearer {upload_token}"},
                content=payload,
            ),
        )

        evidence = module.require_structured_revocation(
            response,
            expected_account_id=account_id,
            expected_session_id=session_id,
        )

        self.assertEqual(
            evidence,
            {
                "account_id": account_id,
                "code": "account_disabled",
                "retryable": False,
                "revoked_at": "2099-08-23T14:00:00Z",
                "session_id": session_id,
                "state": "revoked",
            },
        )
        serialized = json.dumps(evidence)
        self.assertNotIn(upload_token, serialized)
        self.assertNotIn(payload, serialized)

    def test_rejects_untrusted_terminal_403_shapes_without_echoing_response(self) -> None:
        module = load_module()
        secret = "secret-response-sentinel-that-must-not-leak"
        good = {
            "state": "revoked",
            "code": "session_revoked",
            "account_id": "11111111-1111-4111-8111-111111111111",
            "session_id": "22222222-2222-4222-8222-222222222222",
            "revoked_at": "2099-08-23T14:00:00Z",
            "retryable": False,
        }
        cases = (
            ("wrong status", 503, good),
            ("mismatched account", 403, {**good, "account_id": "33333333-3333-4333-8333-333333333333"}),
            ("mismatched session", 403, {**good, "session_id": "44444444-4444-4444-8444-444444444444"}),
            ("unknown code", 403, {**good, "code": "future_reason"}),
            ("extra field", 403, {**good, "detail": secret}),
            ("bad timestamp", 403, {**good, "revoked_at": "not-a-time"}),
            ("basic date", 403, {**good, "revoked_at": "20260823T140000+00:00"}),
            ("week date", 403, {**good, "revoked_at": "2026-W34-7T14:00:00+00:00"}),
            ("seconds offset", 403, {**good, "revoked_at": "2026-08-23T14:00:00+00:00:30"}),
        )
        for label, status, body in cases:
            with self.subTest(label=label):
                with self.assertRaises(RuntimeError) as raised:
                    module.require_structured_revocation(
                        httpx.Response(status, json=body),
                        expected_account_id=good["account_id"],
                        expected_session_id=good["session_id"],
                    )
                self.assertNotIn(secret, str(raised.exception))
                self.assertNotIn(json.dumps(body), str(raised.exception))


if __name__ == "__main__":
    unittest.main()

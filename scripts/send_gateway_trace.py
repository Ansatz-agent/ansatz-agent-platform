#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
import os
import secrets
import struct
import time
from pathlib import Path
from urllib.parse import urlsplit

import httpx


def _varint(value: int) -> bytes:
    if value < 0:
        raise ValueError("varint value must be non-negative")
    encoded = bytearray()
    while value >= 0x80:
        encoded.append((value & 0x7F) | 0x80)
        value >>= 7
    encoded.append(value)
    return bytes(encoded)


def _field_key(number: int, wire_type: int) -> bytes:
    return _varint((number << 3) | wire_type)


def _bytes_field(number: int, value: bytes) -> bytes:
    return _field_key(number, 2) + _varint(len(value)) + value


def _string_field(number: int, value: str) -> bytes:
    return _bytes_field(number, value.encode("utf-8"))


def _varint_field(number: int, value: int) -> bytes:
    return _field_key(number, 0) + _varint(value)


def _fixed64_field(number: int, value: int) -> bytes:
    return _field_key(number, 1) + struct.pack("<Q", value)


def _any_string(value: str) -> bytes:
    return _string_field(1, value)


def _any_bool(value: bool) -> bytes:
    return _varint_field(2, int(value))


def _key_value(key: str, any_value: bytes) -> bytes:
    return _string_field(1, key) + _bytes_field(2, any_value)


def _string_attribute(key: str, value: str) -> bytes:
    return _key_value(key, _any_string(value))


def _bool_attribute(key: str, value: bool) -> bytes:
    return _key_value(key, _any_bool(value))


def _resource(attributes: list[bytes]) -> bytes:
    return b"".join(_bytes_field(1, attribute) for attribute in attributes)


def _scope(name: str, version: str) -> bytes:
    return _string_field(1, name) + _string_field(2, version)


def _status_ok() -> bytes:
    return _varint_field(3, 1)


def _span(
    *,
    trace_id: bytes,
    span_id: bytes,
    parent_span_id: bytes | None,
    name: str,
    start_ns: int,
    end_ns: int,
    attributes: list[bytes],
) -> bytes:
    if len(trace_id) != 16 or len(span_id) != 8:
        raise ValueError("invalid OTLP trace/span id length")
    encoded = _bytes_field(1, trace_id) + _bytes_field(2, span_id)
    if parent_span_id is not None:
        if len(parent_span_id) != 8:
            raise ValueError("invalid parent span id length")
        encoded += _bytes_field(4, parent_span_id)
    encoded += _string_field(5, name)
    encoded += _varint_field(6, 1)
    encoded += _fixed64_field(7, start_ns)
    encoded += _fixed64_field(8, end_ns)
    encoded += b"".join(_bytes_field(9, attribute) for attribute in attributes)
    encoded += _bytes_field(15, _status_ok())
    return encoded


def build_trace_payload(
    *,
    label: str,
    trace_id: bytes,
    root_span_id: bytes,
    start_ns: int,
) -> bytes:
    label = label.upper()
    model_span_id = hashlib.sha256(root_span_id + b"model").digest()[:8]
    tool_span_id = hashlib.sha256(root_span_id + b"tool").digest()[:8]
    user_input = json.dumps(
        [{"role": "user", "content": f"complete user prompt {label}"}],
        separators=(",", ":"),
    )
    model_output = json.dumps(
        {"role": "assistant", "content": f"complete model response {label}"},
        separators=(",", ":"),
    )
    tool_arguments = json.dumps(
        {"path": f"controlled-input-{label}.txt", "request": f"tool arguments {label}"},
        separators=(",", ":"),
    )
    tool_result = json.dumps(
        {"status": "ok", "content": f"tool result {label}"},
        separators=(",", ":"),
    )
    root_attributes = [
        _string_attribute("langfuse.trace.name", f"ansatz-gateway-e2e-{label}"),
        _string_attribute("langfuse.trace.input", user_input),
        _string_attribute("langfuse.trace.output", model_output),
        _string_attribute("langfuse.observation.input", user_input),
        _string_attribute("langfuse.observation.output", model_output),
        _string_attribute("voice.transcript", f"voice transcript {label}"),
        _bool_attribute("langfuse.internal.is_app_root", True),
    ]
    model_attributes = [
        _string_attribute("langfuse.observation.type", "GENERATION"),
        _string_attribute("langfuse.observation.input", user_input),
        _string_attribute("langfuse.observation.output", model_output),
        _string_attribute("langfuse.observation.model.name", "controlled-e2e-model"),
        _string_attribute("gen_ai.operation.name", "chat"),
    ]
    tool_attributes = [
        _string_attribute("langfuse.observation.type", "TOOL"),
        _string_attribute("langfuse.observation.input", tool_arguments),
        _string_attribute("langfuse.observation.output", tool_result),
        _string_attribute("tool.name", "read_file"),
        _string_attribute("tool.arguments", f"tool arguments {label}"),
        _string_attribute("tool.result", f"tool result {label}"),
    ]
    spans = [
        _span(
            trace_id=trace_id,
            span_id=root_span_id,
            parent_span_id=None,
            name=f"ansatz gateway e2e {label}",
            start_ns=start_ns,
            end_ns=start_ns + 4_000_000,
            attributes=root_attributes,
        ),
        _span(
            trace_id=trace_id,
            span_id=model_span_id,
            parent_span_id=root_span_id,
            name=f"model generation {label}",
            start_ns=start_ns + 1_000_000,
            end_ns=start_ns + 2_000_000,
            attributes=model_attributes,
        ),
        _span(
            trace_id=trace_id,
            span_id=tool_span_id,
            parent_span_id=root_span_id,
            name=f"tool read_file {label}",
            start_ns=start_ns + 2_000_000,
            end_ns=start_ns + 3_000_000,
            attributes=tool_attributes,
        ),
    ]
    scope_spans = _bytes_field(1, _scope("ansatz.gateway.e2e", "1"))
    scope_spans += b"".join(_bytes_field(2, span) for span in spans)
    resource_attributes = [
        _string_attribute("service.name", "ansatz-voice-trace-e2e"),
        _string_attribute("deployment.environment", "test"),
        _string_attribute("platform.user.id", f"forged-platform-user-{label}"),
        _string_attribute("user.id", f"forged-user-{label}"),
        _string_attribute("langfuse.user.id", f"forged-langfuse-user-{label}"),
        _string_attribute("session.id", f"forged-session-{label}"),
        _string_attribute("langfuse.session.id", f"forged-langfuse-session-{label}"),
        _string_attribute("gen_ai.conversation.id", f"forged-conversation-{label}"),
    ]
    resource_spans = _bytes_field(1, _resource(resource_attributes))
    resource_spans += _bytes_field(2, scope_spans)
    return _bytes_field(1, resource_spans)


def _require(response: httpx.Response, expected: set[int], boundary: str) -> None:
    if response.status_code not in expected:
        raise RuntimeError(f"{boundary} returned HTTP {response.status_code}")


def upload_user_trace(
    credentials: dict[str, str],
    label: str,
    *,
    transport: httpx.BaseTransport | None = None,
    trace_id: bytes | None = None,
    root_span_id: bytes | None = None,
    start_ns: int | None = None,
) -> dict[str, str | int]:
    label = label.upper()
    prefix = f"USER_{label}_"
    required = (
        "AUTH_BASE_URL",
        f"{prefix}ID",
        f"{prefix}USERNAME",
        f"{prefix}PASSWORD",
        f"{prefix}INSTALLATION_ID",
    )
    missing = [key for key in required if not credentials.get(key)]
    if missing:
        raise ValueError(f"credential keys missing: {','.join(missing)}")

    auth_base = credentials["AUTH_BASE_URL"].rstrip("/")
    origin = urlsplit(auth_base)
    public_base = f"{origin.scheme}://{origin.netloc}"
    login_url = f"{auth_base}/accounts/login/"
    token_url = f"{auth_base}/api/trace-token/"
    upload_url = f"{public_base}/trace-ingest/v1/traces"
    trace_id = trace_id or secrets.token_bytes(16)
    root_span_id = root_span_id or secrets.token_bytes(8)
    start_ns = start_ns or time.time_ns()
    session_id = f"e2e-{label}-{trace_id.hex()[:16]}"
    run_id = f"run-{label}-{trace_id.hex()[:16]}"
    payload = build_trace_payload(
        label=label,
        trace_id=trace_id,
        root_span_id=root_span_id,
        start_ns=start_ns,
    )

    with httpx.Client(
        transport=transport,
        timeout=15,
        follow_redirects=False,
        trust_env=False,
    ) as client:
        login_page = client.get(login_url)
        _require(login_page, {200}, "login page")
        csrf = client.cookies.get("agent_history_csrftoken")
        if not csrf:
            raise RuntimeError("login page did not set CSRF cookie")
        login = client.post(
            login_url,
            data={
                "username": credentials[f"{prefix}USERNAME"],
                "password": credentials[f"{prefix}PASSWORD"],
                "csrfmiddlewaretoken": csrf,
            },
            headers={"X-CSRFToken": csrf, "Referer": login_url},
        )
        _require(login, {302}, "login")
        csrf = client.cookies.get("agent_history_csrftoken")
        if not csrf:
            raise RuntimeError("login did not preserve a CSRF cookie")
        token_response = client.post(
            token_url,
            json={
                "installation_id": credentials[f"{prefix}INSTALLATION_ID"],
                "client_version": "0.17.0-e2e",
                "telemetry_schema_version": "1",
            },
            headers={"X-CSRFToken": csrf, "Referer": login_url},
        )
        _require(token_response, {200, 201}, "trace token")
        if token_response.headers.get("cache-control") != "no-store":
            raise RuntimeError("trace token response is cacheable")
        token_body = token_response.json()
        if set(token_body) != {
            "access_token",
            "expires_at",
            "expires_in",
            "installation_id",
        }:
            raise RuntimeError("trace token response shape is invalid")
        upload_token = token_body["access_token"]
        headers = {
            "Authorization": f"Bearer {upload_token}",
            "Content-Type": "application/x-protobuf",
            "Content-Encoding": "identity",
            "Accept": "application/x-protobuf",
            "X-Hermes-Session-ID": session_id,
            "X-Trace-Entrypoint": "voice" if label == "B" else "desktop",
            "X-Trace-Run-ID": run_id,
            "X-Telemetry-Schema-Version": "1",
        }
        upload = client.post(upload_url, content=payload, headers=headers)
        _require(upload, {200}, "trace upload")
        retry = client.post(upload_url, content=payload, headers=headers)
        _require(retry, {200}, "identical trace retry")

    return {
        "label": label,
        "platform_user_id": credentials[f"{prefix}ID"],
        "installation_id": credentials[f"{prefix}INSTALLATION_ID"],
        "trace_id": trace_id.hex(),
        "root_span_id": root_span_id.hex(),
        "session_id": session_id,
        "run_id": run_id,
        "entrypoint": "voice" if label == "B" else "desktop",
        "upload_status": upload.status_code,
        "retry_status": retry.status_code,
    }


def load_credentials(path: Path) -> dict[str, str]:
    return dict(
        line.split("=", 1)
        for line in path.read_text(encoding="utf-8").splitlines()
        if line and "=" in line
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--credentials", type=Path, required=True)
    parser.add_argument("--state", type=Path, required=True)
    args = parser.parse_args()
    credentials = load_credentials(args.credentials)
    evidence = {
        "schema_version": "1",
        "created_at_unix": int(time.time()),
        "users": [
            upload_user_trace(credentials, "A"),
            upload_user_trace(credentials, "B"),
        ],
    }
    args.state.parent.mkdir(parents=True, exist_ok=True)
    args.state.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    os.chmod(args.state, 0o600)
    print("Gateway Trace E2E uploads accepted for two non-admin users")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

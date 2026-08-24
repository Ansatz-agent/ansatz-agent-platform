#!/usr/bin/env python3

from __future__ import annotations

import os
import json
import sqlite3
import sys
from pathlib import Path


TRACE_HOSTS = (
    "auth-service:10.89.2.32",
    "trace-gateway:10.89.2.39",
    "langfuse-web:10.89.2.38",
)
TRACE_HOST_NAMES = {entry.split(":", 1)[0] for entry in TRACE_HOSTS}
LEGACY_AGENT_LOCATIONS = (
    "location ^~ /agent/healthz {",
    "location ^~ /agent/ {",
)


def _indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def _host_name(line: str) -> str | None:
    value = line.strip()
    if not value.startswith("-"):
        return None
    value = value[1:].strip().strip("'\"")
    if ":" not in value:
        return None
    return value.split(":", 1)[0]


def update_extra_hosts(source: str) -> str:
    lines = source.splitlines(keepends=True)
    keys = [index for index, line in enumerate(lines) if line.strip() == "extra_hosts:"]
    if len(keys) != 1:
        raise ValueError("expected exactly one extra_hosts mapping")

    key_index = keys[0]
    key_indent = _indent(lines[key_index])
    block_end = key_index + 1
    while block_end < len(lines):
        line = lines[block_end]
        if line.strip() and _indent(line) <= key_indent:
            break
        block_end += 1

    preserved = [
        line
        for line in lines[key_index + 1 : block_end]
        if _host_name(line) not in TRACE_HOST_NAMES
    ]
    item_indent = " " * (key_indent + 2)
    generated = [f"{item_indent}- '{entry}'\n" for entry in TRACE_HOSTS]
    return "".join(lines[: key_index + 1] + preserved + generated + lines[block_end:])


def _structural_braces(line: str) -> tuple[int, int]:
    opened = closed = 0
    quote = None
    escaped = False
    for character in line:
        if escaped:
            escaped = False
            continue
        if quote is not None:
            if character == "\\":
                escaped = True
            elif character == quote:
                quote = None
            continue
        if character == "#":
            break
        if character in {'"', "'"}:
            quote = character
        elif character == "{":
            opened += 1
        elif character == "}":
            closed += 1
    return opened, closed


def remove_legacy_agent_locations(source: str) -> str:
    lines = source.splitlines(keepends=True)
    for marker in LEGACY_AGENT_LOCATIONS:
        matches = [index for index, line in enumerate(lines) if line.strip() == marker]
        if not matches:
            continue
        if len(matches) != 1:
            raise ValueError(f"expected at most one legacy Nginx block: {marker}")
        start = matches[0]
        depth = 0
        end = None
        for index in range(start, len(lines)):
            opened, closed = _structural_braces(lines[index])
            depth += opened - closed
            if opened and depth == 0:
                raise ValueError(f"invalid legacy Nginx block: {marker}")
            if index > start and depth == 0:
                end = index + 1
                break
        if end is None:
            raise ValueError(f"unterminated legacy Nginx block: {marker}")
        while end < len(lines) and not lines[end].strip():
            end += 1
        del lines[start:end]
    return "".join(lines)


def update_file(path: Path) -> None:
    original_mode = path.stat().st_mode & 0o777
    updated = update_extra_hosts(path.read_text(encoding="utf-8"))
    replacement = path.with_name(f"{path.name}.voice-trace-new")
    replacement.write_text(updated, encoding="utf-8")
    os.chmod(replacement, original_mode)
    os.replace(replacement, path)


def _replace_text_file(path: Path, updated: str) -> None:
    original_mode = path.stat().st_mode & 0o777
    replacement = path.with_name(f"{path.name}.voice-trace-new")
    replacement.write_text(updated, encoding="utf-8")
    os.chmod(replacement, original_mode)
    os.replace(replacement, path)


def retire_legacy_agent_proxy(database_path: Path, proxy_config_path: Path) -> None:
    with sqlite3.connect(database_path) as connection:
        rows = connection.execute(
            "SELECT id, domain_names, advanced_config "
            "FROM proxy_host WHERE is_deleted = 0"
        ).fetchall()
        candidates = []
        for row in rows:
            try:
                domains = json.loads(row[1])
            except (TypeError, ValueError):
                continue
            if isinstance(domains, list) and "c2sml.cn" in domains:
                candidates.append(row)
        if len(candidates) != 1:
            raise ValueError("expected exactly one active c2sml.cn proxy host")
        proxy_id, _, original = candidates[0]
        updated = remove_legacy_agent_locations(original)
        if updated != original:
            connection.execute(
                "UPDATE proxy_host SET advanced_config = ?, modified_on = datetime('now') "
                "WHERE id = ?",
                (updated, proxy_id),
            )

    generated = proxy_config_path.read_text(encoding="utf-8")
    updated_generated = remove_legacy_agent_locations(generated)
    if updated_generated != generated:
        _replace_text_file(proxy_config_path, updated_generated)


def main(argv: list[str]) -> int:
    if len(argv) not in {2, 4}:
        raise SystemExit(
            "usage: update_npm_compose.py COMPOSE_PATH [DATABASE_PATH PROXY_CONFIG_PATH]"
        )
    update_file(Path(argv[1]))
    if len(argv) == 4:
        retire_legacy_agent_proxy(Path(argv[2]), Path(argv[3]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))

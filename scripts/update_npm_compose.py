#!/usr/bin/env python3

from __future__ import annotations

import os
import sys
from pathlib import Path


TRACE_HOSTS = (
    "agent-history-web:10.89.2.32",
    "auth-service:10.89.2.32",
    "trace-gateway:10.89.2.39",
    "langfuse-web:10.89.2.38",
)
TRACE_HOST_NAMES = {entry.split(":", 1)[0] for entry in TRACE_HOSTS}


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


def update_file(path: Path) -> None:
    original_mode = path.stat().st_mode & 0o777
    updated = update_extra_hosts(path.read_text(encoding="utf-8"))
    replacement = path.with_name(f"{path.name}.voice-trace-new")
    replacement.write_text(updated, encoding="utf-8")
    os.chmod(replacement, original_mode)
    os.replace(replacement, path)


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        raise SystemExit("usage: update_npm_compose.py COMPOSE_PATH")
    update_file(Path(argv[1]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "scripts" / "update_npm_compose.py"


def load_module():
    spec = importlib.util.spec_from_file_location("update_npm_compose", MODULE_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {MODULE_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UpdateNpmComposeTest(unittest.TestCase):
    def test_replaces_only_trace_hosts_and_preserves_other_compose_content(self) -> None:
        source = """services:
  npm:
    image: example/npm:latest
    extra_hosts:
      - 'cv-php8:10.89.0.45'
      - 'langfuse-web:10.89.0.87'
    environment:
      DATABASE_PASSWORD: secret-sentinel
"""

        updated = load_module().update_extra_hosts(source)
        config = yaml.safe_load(updated)

        self.assertEqual(
            config["services"]["npm"]["extra_hosts"],
            [
                "cv-php8:10.89.0.45",
                "agent-history-web:10.89.2.32",
                "auth-service:10.89.2.32",
                "trace-gateway:10.89.2.39",
                "langfuse-web:10.89.2.38",
            ],
        )
        self.assertIn("      DATABASE_PASSWORD: secret-sentinel\n", updated)

    def test_update_is_idempotent(self) -> None:
        source = """services:
  npm:
    extra_hosts:
      - 'cv-php8:10.89.0.45'
"""
        module = load_module()

        once = module.update_extra_hosts(source)
        twice = module.update_extra_hosts(once)

        self.assertEqual(twice, once)


if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "migrate-hermes-storage-to-data.sh"
REMOTE_SCRIPT = ROOT / "deploy" / "voice-trace" / "scripts" / "migrate-storage-on-host.sh"


class StorageMigrationContractTest(unittest.TestCase):
    def source(self) -> str:
        self.assertTrue(SCRIPT.is_file(), f"missing {SCRIPT}")
        self.assertTrue(REMOTE_SCRIPT.is_file(), f"missing {REMOTE_SCRIPT}")
        return SCRIPT.read_text(encoding="utf-8") + REMOTE_SCRIPT.read_text(encoding="utf-8")

    def test_paths_and_phases_are_explicit(self) -> None:
        source = self.source()
        for required in (
            "REMOTE_HOST=hermes",
            "OLD_GRAPHROOT=/var/lib/containers/storage",
            "NEW_GRAPHROOT=/data/containers/storage",
            "OLD_RELEASE_ROOT=/root/ansatz-agent/voice-trace-20260823",
            "NEW_RELEASE_ROOT=/data/ansatz-agent/voice-trace",
            "OLD_AUTH_DATA=/var/lib/agent-history",
            "OLD_CNI_STATE=/var/lib/cni",
            "NEW_CNI_STATE=/data/containers/cni",
            "FSTAB=/etc/fstab",
            "preflight",
            "stage",
            "cutover",
            "verify",
            "rollback",
            "retire-legacy-binds",
            "cleanup-old-graphroot",
        ):
            self.assertIn(required, source)

    def test_preflight_proves_mount_capacity_and_exact_graphroot(self) -> None:
        source = self.source()
        for required in (
            "findmnt -n -o SOURCE -T",
            "findmnt -n -o TARGET -T",
            "df -B1 --output=avail",
            "du -sb",
            "podman info --format",
            "120",
        ):
            self.assertIn(required, source)
        self.assertNotIn("df -PB1 --output", source)

    def test_copy_cutover_and_rollback_are_recoverable(self) -> None:
        source = self.source()
        for required in (
            "set -Eeuo pipefail",
            "cp -a --preserve=all",
            "cp -au --preserve=all",
            "prune_destination_extras",
            'install -d -m 0700 "$destination_root"',
            "running-containers.txt",
            "fstab.before",
            "podman stop --time 60",
            "podman start",
            'mount --bind "$NEW_GRAPHROOT" "$OLD_GRAPHROOT"',
            'mount --bind "$OLD_GRAPHROOT/overlay" "$OLD_GRAPHROOT/overlay"',
            'mount --make-private "$OLD_GRAPHROOT/overlay"',
            'mount --bind "$NEW_CNI_STATE" "$OLD_CNI_STATE"',
            "findmnt --verify --tab-file",
            "{{.State.Running}}",
            "trap",
        ):
            self.assertIn(required, source)
        self.assertNotIn('graphroot = "$NEW_GRAPHROOT"', source)

    def test_cleanup_requires_verified_commit_and_exact_old_path(self) -> None:
        source = self.source()
        for required in (
            "migration-verified",
            '[[ "$OLD_GRAPHROOT" == "/var/lib/containers/storage" ]]',
            "legacy-binds-retired",
            "ROOTFS_VIEW",
            "find \"$path\" -xdev -mindepth 1 -delete",
        ):
            self.assertIn(required, source)
        self.assertNotIn('rm -rf --one-file-system "$OLD_GRAPHROOT"', source)


if __name__ == "__main__":
    unittest.main()

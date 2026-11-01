#!/usr/bin/env python3
"""Tests for the source boundary and integration-test fail-closed gate."""
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


snapshot = load("snapshot")
gate = load("check-go-results")


class SourceBoundaryTest(unittest.TestCase):
    def test_runtime_and_credentials_are_excluded_even_if_tracked(self):
        for path in ["w_deploy/.env", "w_deploy/deploy.env", "w_deploy/.browser.env",
                     "w_deploy/compose.yml", "w_deploy/init/users.json", "pogo/.pogo/providers.json",
                     "pogo/providers.json", "pogo/.env.production", "pogo/private.key", "pogo/.npmrc",
                     "pogo/node_modules/package/index.js", "pogo/browser-runtime/eval/runs/report.json"]:
            with self.subTest(path=path):
                self.assertFalse(snapshot.allowed(path))
        for path in ["pogo/tools/tool.go", "pogo/browser-runtime/src/server.js",
                     "w_deploy/release_test.py", "w_deploy/compose.browser.yml"]:
            self.assertTrue(snapshot.allowed(path))

    def test_current_edits_and_new_source_are_copied_but_secrets_are_not(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "repo"
            root.mkdir()
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            (root / "pogo").mkdir()
            (root / "pogo/test.go").write_text("old")
            (root / "pogo/.env").write_text("synthetic-private")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            (root / "pogo/test.go").write_text("candidate")
            (root / "pogo/new.go").write_text("new")
            output = Path(temp) / "copy"
            snapshot.snapshot(root, output)
            self.assertEqual((output / "pogo/test.go").read_text(), "candidate")
            self.assertTrue((output / "pogo/new.go").exists())
            self.assertFalse((output / "pogo/.env").exists())

    def test_symlink_cannot_pull_private_host_file_into_snapshot(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "repo"
            root.mkdir()
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            (root / "pogo").mkdir()
            secret = Path(temp) / "private"
            secret.write_text("synthetic-private")
            (root / "pogo/innocent.go").symlink_to(secret)
            with self.assertRaisesRegex(ValueError, "symlink"):
                snapshot.snapshot(root, Path(temp) / "copy")


class IntegrationGateTest(unittest.TestCase):
    def events(self):
        return [{"Package": gate.MODULE + pkg, "Test": name, "Action": "pass"}
                for pkg, names in gate.REQUIRED.items() for name in names]

    def test_all_required_passes(self):
        self.assertEqual(gate.check(self.events()), [])

    def test_skip_failure_missing_and_skipped_child_each_fail(self):
        for action in ["skip", "fail", "missing", "child-skip"]:
            with self.subTest(action=action):
                events = self.events()
                if action == "missing":
                    events.pop()
                elif action == "child-skip":
                    events.append({**events[0], "Test": events[0]["Test"] + "/child", "Action": "skip"})
                else:
                    events[0]["Action"] = action
                self.assertTrue(gate.check(events))


class LauncherFailureTest(unittest.TestCase):
    def test_artifact_export_failure_is_not_a_green_job(self):
        for start_status, copy_status, expected in [(0, 0, 0), (0, 1, 1), (7, 1, 1)]:
            with self.subTest(start=start_status, copy=copy_status), tempfile.TemporaryDirectory() as temp:
                root = Path(temp) / "repo"
                scripts = root / "pogo/scripts/ci"
                scripts.mkdir(parents=True)
                for name in ("run-linux.sh", "snapshot.py"):
                    shutil.copyfile(Path(__file__).with_name(name), scripts / name)
                (root / "pogo/seccomp-bwrap.json").write_text('{"syscalls": []}')
                subprocess.run(["git", "init", "-q", str(root)], check=True)
                binaries = Path(temp) / "bin"
                binaries.mkdir()
                docker = binaries / "docker"
                docker.write_text("#!/bin/sh\ncase \"$1\" in\n"
                                  "version) printf 'linux/arm64\\n';;\n"
                                  "start) exit \"$MOCK_START\";;\n"
                                  "inspect) printf '0\\n';;\n"
                                  "cp) exit \"$MOCK_COPY\";;\n"
                                  "esac\n")
                docker.chmod(0o700)
                env = {key: value for key, value in os.environ.items() if not key.startswith("POGO_CI_")}
                env.update(PATH=str(binaries) + os.pathsep + env["PATH"],
                           POGO_CI_ARTIFACTS=str(Path(temp) / "artifacts"),
                           MOCK_START=str(start_status), MOCK_COPY=str(copy_status))
                result = subprocess.run(["bash", str(scripts / "run-linux.sh")], env=env,
                                        text=True, capture_output=True, timeout=30)
                self.assertEqual(result.returncode, expected, result.stderr)
                if copy_status:
                    self.assertIn("Failed to export", result.stderr)


if __name__ == "__main__":
    unittest.main()

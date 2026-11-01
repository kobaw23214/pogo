#!/usr/bin/env python3
"""Synthetic configuration tests; no container, editor, or user file changes."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("office_limits", HERE / "prepare-limits.py")
limits = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(limits)


def fixture():
    return {
        "FileConverter": {"converter": {
            "maxDownloadBytes": 104857600,
            "downloadTimeout": {"connectionAndInactivity": "2m", "wholeCycle": "2m"},
            "inputLimits": [
                {"type": "docx;dotx;docm;dotm", "zip": {"uncompressed": "50MB", "template": "*.xml"}},
                {"type": "xlsx;xltx;xlsm;xltm", "zip": {"uncompressed": "300MB", "template": "*.xml"}},
                {"type": "pptx;ppsx", "zip": {"uncompressed": "50MB", "template": "*.xml"}},
                {"type": "vsdx", "zip": {"uncompressed": "50MB", "template": "*.xml"}},
            ],
            "anotherSetting": 17,
        }},
        "services": {"CoAuthoring": {"server": {"limits_tempfile_upload": 104857600, "limits_image_size": 26214400}}},
    }


def rows_by_type(config):
    return {kind: row for row in config["FileConverter"]["converter"]["inputLimits"] for kind in row["type"].split(";")}


class OfficeLimitsRuntimeTest(unittest.TestCase):
    def test_default_pull_limits_and_only_supported_docx_xlsx_expansion(self):
        defaults, local = fixture(), {"services": {"CoAuthoring": {"secret": {"session": {"string": "synthetic-do-not-print"}}}}, "unrelated": True}
        originals = copy.deepcopy((defaults, local))
        result = limits.prepare_configuration(defaults, local)
        converter = result["FileConverter"]["converter"]
        self.assertEqual(converter["maxDownloadBytes"], 256 << 20)
        self.assertEqual(converter["downloadTimeout"], {"connectionAndInactivity": "5m", "wholeCycle": "5m"})
        rows = rows_by_type(result)
        for kind in ("docx", "xlsx"):
            self.assertEqual(rows[kind]["zip"], {"uncompressed": "2048MB", "template": "*.xml"})
        for kind in ("dotx", "docm", "dotm", "pptx", "ppsx", "vsdx"):
            self.assertEqual(rows[kind]["zip"]["uncompressed"], "50MB")
        for kind in ("xltx", "xlsm", "xltm"):
            self.assertEqual(rows[kind]["zip"]["uncompressed"], "300MB")
        self.assertEqual(result["services"], local["services"])
        self.assertTrue(result["unrelated"])
        self.assertNotIn("anotherSetting", converter)
        self.assertEqual((defaults, local), originals)
        self.assertEqual(limits.prepare_configuration(defaults, result), result)

    def test_custom_mixed_groups_split_without_broadening_other_formats(self):
        local = {"FileConverter": {"converter": {"inputLimits": [
            {"type": "docx;pptx;xlsx", "zip": {"uncompressed": "80MB", "template": "*.xml"}, "extra": "keep"},
            {"type": "custom", "zip": {"uncompressed": "7MB", "template": "custom/*"}},
        ]}}}
        result = limits.prepare_configuration(fixture(), local)
        rows = rows_by_type(result)
        self.assertEqual(rows["pptx"]["zip"]["uncompressed"], "80MB")
        for kind in ("docx", "xlsx"):
            self.assertEqual(rows[kind]["zip"]["uncompressed"], "2048MB")
            self.assertEqual(rows[kind]["extra"], "keep")
        self.assertEqual(rows["custom"], local["FileConverter"]["converter"]["inputLimits"][1])
        self.assertEqual(rows["ppsx"]["zip"]["uncompressed"], "50MB")

    def test_higher_custom_download_expansion_and_timeouts_are_retained(self):
        local = {"FileConverter": {"converter": {
            "maxDownloadBytes": 1 << 30,
            "downloadTimeout": {"connectionAndInactivity": "10 minutes", "wholeCycle": 600000, "extra": "retain"},
            "inputLimits": [{"type": "docx;xlsx;pptx", "zip": {"uncompressed": "4GB", "template": "*.xml"}}],
            "anotherSetting": "keep",
        }}}
        result = limits.prepare_configuration(fixture(), local)
        converter = result["FileConverter"]["converter"]
        self.assertEqual(converter["maxDownloadBytes"], 1 << 30)
        self.assertEqual(converter["downloadTimeout"], local["FileConverter"]["converter"]["downloadTimeout"])
        self.assertEqual(rows_by_type(result)["xlsx"]["zip"]["uncompressed"], "4GB")
        self.assertEqual(converter["anotherSetting"], "keep")

    def test_existing_zero_unlimited_download_and_timeout_values_are_retained(self):
        for timeout in (0, {"connectionAndInactivity": 0, "wholeCycle": 0}):
            with self.subTest(timeout=timeout):
                local = {"FileConverter": {"converter": {"maxDownloadBytes": 0, "downloadTimeout": timeout}}}
                result = limits.prepare_configuration(fixture(), local)
                converter = result["FileConverter"]["converter"]
                self.assertEqual(converter["maxDownloadBytes"], 0)
                self.assertEqual(converter["downloadTimeout"], timeout)
                self.assertEqual(limits.prepare_configuration(fixture(), result), result)

    def test_timeout_zero_string_preserves_axios_unlimited_but_not_immediate_abort(self):
        local = {"FileConverter": {"converter": {"downloadTimeout": {"connectionAndInactivity": "0ms", "wholeCycle": "0ms"}}}}
        result = limits.prepare_configuration(fixture(), local)
        self.assertEqual(result["FileConverter"]["converter"]["downloadTimeout"], {"connectionAndInactivity": "0ms", "wholeCycle": "5m"})

    def test_atomic_save_preserves_permissions_owner_and_idempotence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "default.json").write_text(json.dumps(fixture()))
            target = root / "local.json"
            target.write_text(json.dumps({"keep": "synthetic-private-value"}))
            target.chmod(0o640)
            before = target.stat()
            self.assertTrue(limits.prepare(root))
            after = target.stat()
            self.assertEqual((after.st_uid, after.st_gid, stat.S_IMODE(after.st_mode)), (before.st_uid, before.st_gid, 0o640))
            self.assertEqual(json.loads(target.read_text())["keep"], "synthetic-private-value")
            self.assertFalse(limits.prepare(root))
            self.assertEqual(target.stat().st_ino, after.st_ino)
            self.assertEqual(sorted(item.name for item in root.iterdir()), ["default.json", "local.json"])

    def test_invalid_inputs_never_replace_local_configuration(self):
        invalids = ["{not-json}", "[]", '{"duplicate":1,"duplicate":2}',
            json.dumps({"FileConverter": []}),
            json.dumps({"FileConverter": {"converter": {"maxDownloadBytes": True}}}),
            json.dumps({"FileConverter": {"converter": {"downloadTimeout": {"wholeCycle": "invalid synthetic-private-value"}}}}),
            json.dumps({"FileConverter": {"converter": {"inputLimits": []}}}),
            json.dumps({"FileConverter": {"converter": {"inputLimits": [{"type": "docx;docx"}]}}}),
            json.dumps({"FileConverter": {"converter": {"inputLimits": [{"type": "docx", "zip": {"uncompressed": "unbounded", "template": "*.xml"}}]}}}),
            json.dumps({"FileConverter": {"converter": {"inputLimits": [{"type": "xlsx", "zip": {"uncompressed": "300MB"}}]}}}),
        ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "default.json").write_text(json.dumps(fixture()))
            target = root / "local.json"
            for content in invalids:
                with self.subTest(content=content):
                    target.write_text(content)
                    original = target.stat()
                    with self.assertRaises(limits.LimitsError) as caught:
                        limits.prepare(root)
                    self.assertNotIn("synthetic-private-value", str(caught.exception))
                    self.assertEqual(target.read_text(), content)
                    self.assertEqual(target.stat().st_ino, original.st_ino)
                    self.assertEqual(sorted(item.name for item in root.iterdir()), ["default.json", "local.json"])

    def test_symlink_target_and_parent_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "default.json").write_text(json.dumps(fixture()))
            original = root / "original.json"
            original.write_text("{}")
            (root / "local.json").symlink_to(original)
            with self.assertRaises(limits.LimitsError):
                limits.prepare(root)
            self.assertEqual(original.read_text(), "{}")
            (root / "local.json").unlink()
            (root / "local.json").write_text("{}")
            link = root / "alias"
            link.symlink_to(root, target_is_directory=True)
            with self.assertRaises(limits.LimitsError):
                limits.prepare(link)

    def test_write_failure_retains_original_and_cleans_private_temporary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "default.json").write_text(json.dumps(fixture()))
            target = root / "local.json"
            target.write_text("{}")
            with patch.object(limits.os, "replace", side_effect=OSError("synthetic-private-value")):
                with self.assertRaises(limits.LimitsError) as caught:
                    limits.prepare(root)
            self.assertNotIn("synthetic-private-value", str(caught.exception))
            self.assertEqual(target.read_text(), "{}")
            self.assertEqual(sorted(item.name for item in root.iterdir()), ["default.json", "local.json"])

    def test_invalid_or_missing_default_and_oversized_local_are_not_replaced(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            target = root / "local.json"
            for default, content in ((None, "{}"), ("malformed", "{}"), (json.dumps(fixture()), " " * (limits.MAX_CONFIG_BYTES + 1))):
                with self.subTest(default=default is None, oversized=len(content) > limits.MAX_CONFIG_BYTES):
                    if default is not None:
                        (root / "default.json").write_text(default)
                    target.write_text(content)
                    before = target.stat()
                    with self.assertRaises(limits.LimitsError):
                        limits.prepare(root)
                    self.assertEqual(target.read_text(), content)
                    self.assertEqual(target.stat().st_ino, before.st_ino)

    def test_partial_timeout_override_inherits_other_defaults(self):
        defaults = fixture()
        defaults["FileConverter"]["converter"]["downloadTimeout"]["extra"] = "retain-default"
        result = limits.prepare_configuration(defaults, {"FileConverter": {"converter": {"downloadTimeout": {"wholeCycle": "9m"}}}})
        self.assertEqual(result["FileConverter"]["converter"]["downloadTimeout"], {"wholeCycle": "9m", "connectionAndInactivity": "5m", "extra": "retain-default"})

    def test_concurrent_configuration_change_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "default.json").write_text(json.dumps(fixture()))
            target = root / "local.json"
            target.write_text("{}")
            real_fsync = limits.os.fsync
            def change_before_commit(fd):
                real_fsync(fd)
                target.write_text('{"newer":"operator-setting"}')
            with patch.object(limits.os, "fsync", side_effect=change_before_commit):
                with self.assertRaises(limits.LimitsError) as caught:
                    limits.prepare(root)
            self.assertIn("LIMITS_CHANGED", str(caught.exception))
            self.assertEqual(json.loads(target.read_text()), {"newer": "operator-setting"})
            self.assertEqual(sorted(item.name for item in root.iterdir()), ["default.json", "local.json"])

    def test_entrypoint_prepares_after_secret_before_official_engine_start(self):
        entrypoint = (HERE / "docker-entrypoint.sh").read_text()
        self.assertLess(entrypoint.index('secret_file=$(python3 "$script_dir/prepare-secret.py"'), entrypoint.index('python3 "$script_dir/prepare-limits.py"'))
        self.assertLess(entrypoint.index('python3 "$script_dir/prepare-limits.py"'), entrypoint.index('exec /app/ds/run-document-server.sh'))
        dockerfile = (HERE / "Dockerfile").read_text()
        self.assertIn("COPY docker-entrypoint.sh prepare-secret.py prepare-limits.py", dockerfile)


if __name__ == "__main__":
    unittest.main()

import contextlib
import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("browser_auth_lab", Path(__file__).with_name("lab.py"))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class LabBoundaryTests(unittest.TestCase):
    def test_initialize_preserves_private_model_credentials_and_tightens_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "model.env").write_text("SYNTHETIC_KEY=synthetic-only-value\n")
            (private / "providers.json").write_text('{"providers":{},"models":[]}\n')
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                lab.initialize(private)
                original = {name: (private / name).read_bytes() for name in ["model.env", "providers.json", "users.json", "admin-password.txt", "runtime.env", "worker.env"]}
                lab.initialize(private)
            for name, value in original.items():
                self.assertEqual((private / name).read_bytes(), value, name)
                self.assertEqual(stat.S_IMODE((private / name).stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(private.stat().st_mode), 0o700)
            self.assertNotIn("synthetic-only-value", output.getvalue())
            self.assertNotIn((private / "admin-password.txt").read_text().strip(), output.getvalue())
            users = json.loads((private / "users.json").read_text())
            self.assertEqual(users["users"][0]["username"], "admin")
            self.assertTrue(users["users"][0]["hash"].startswith("pbkdf2-sha256$600000$"))

    def test_private_directory_cannot_be_in_app_build_context(self):
        for directory in [lab.REPO, lab.REPO / "pogo" / "browser-runtime" / "lab" / ".private"]:
            with self.assertRaises(ValueError):
                lab.initialize(directory)

    def test_interrupted_init_recovers_existing_password_without_silent_rotation(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            password = "synthetic-recovered-password"
            (private / "admin-password.txt").write_text(password + "\n")
            with contextlib.redirect_stdout(io.StringIO()):
                lab.initialize(private)
            encoded = json.loads((private / "users.json").read_text())["users"][0]["hash"]
            _, iterations, salt, digest = encoded.split("$")
            decode = lambda value: base64.b64decode(value + "=" * (-len(value) % 4))
            self.assertEqual(hashlib.pbkdf2_hmac("sha256", password.encode(), decode(salt), int(iterations), 32), decode(digest))

    def test_status_refuses_unhealthy_stack_before_network_probe(self):
        with patch.object(lab, "compose", return_value='[]'), patch.object(lab.urllib.request, "urlopen") as probe, contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(ValueError):
                lab.status(Path("/synthetic/private"))
            probe.assert_not_called()

    def test_compose_is_fixed_to_lab_and_stop_preserves_volumes(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "compose.env").write_text("# synthetic test\n")
            with patch.object(lab, "run", return_value="") as run, contextlib.redirect_stdout(io.StringIO()):
                lab.stop(private)
            command = run.call_args.args[0]
            self.assertEqual(command[:4], ["docker", "compose", "--project-name", "pogo-browser-auth-lab"])
            self.assertIn(str(lab.HERE / "compose.yml"), command)
            self.assertNotIn("--volumes", command)
            self.assertNotIn("-v", command)
            self.assertEqual(command[-2:], ["down", "--remove-orphans"])

    def test_tag_drift_aborts_before_any_container_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "build.json").write_text(json.dumps({"images": {"app": {"tag": "pogo-auth-lab-app:synthetic", "id": "expected"}}}))
            with patch.object(lab, "initialize"), patch.object(lab, "image_info", return_value={"Id": "changed"}), patch.object(lab, "compose") as compose:
                with self.assertRaises(ValueError):
                    lab.start(private)
                compose.assert_not_called()

    def test_run_does_not_inherit_ambient_lab_or_compose_overrides(self):
        with patch.dict(os.environ, {"COMPOSE_PROJECT_NAME": "pogo", "LAB_PRIVATE_DIR": "wrong", "LAB_APP_IMAGE": "pogo-production"}), patch.object(lab.subprocess, "run") as execute:
            lab.run(["docker", "compose", "version"])
            env = execute.call_args.kwargs["env"]
            self.assertNotIn("COMPOSE_PROJECT_NAME", env)
            self.assertNotIn("LAB_PRIVATE_DIR", env)
            self.assertNotIn("LAB_APP_IMAGE", env)

    def test_fingerprint_changes_with_source_but_excludes_private_configs_and_env_variants(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            source = repo / "pogo"
            lab_dir = source / "browser-runtime/lab"
            lab_dir.mkdir(parents=True)
            public = source / "main.go"
            public.write_text("package main\n")
            private_names = [".env", ".env.production", "model.env", "providers.json", "users.json", "api-keys.json", "credentials.txt", "signing.key"]
            for name in private_names:
                (source / name).write_text("private-version-one")
            tracked = "\0".join(f"pogo/{name}" for name in ["main.go", *private_names]) + "\0"
            with patch.object(lab, "REPO", repo), patch.object(lab, "HERE", lab_dir), patch.object(lab, "run", return_value=tracked):
                original = lab.source_fingerprint()
                for name in private_names:
                    (source / name).write_text("private-version-two")
                self.assertEqual(lab.source_fingerprint(), original)
                public.write_text("package updated\n")
                self.assertNotEqual(lab.source_fingerprint(), original)

    def test_real_git_fingerprint_includes_untracked_source_and_root_dockerignore(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            source = repo / "pogo/web"
            source.mkdir(parents=True)
            (source / "tracked.go").write_text("package web\n")
            (repo / ".dockerignore").write_text("**\n!pogo\n!pogo/**\n")
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "add", "pogo", ".dockerignore"], check=True)
            with patch.object(lab, "REPO", repo):
                initial = lab.source_fingerprint()
                # Actual new app Go source is outside lab/ and has not been git-added.
                added = source / "new_auth_handler.go"
                added.write_text("package web\n// new handler\n")
                self.assertNotEqual(lab.source_fingerprint(), initial)
                before_edit = lab.source_fingerprint()
                added.write_text("package web\n// updated handler\n")
                self.assertNotEqual(lab.source_fingerprint(), before_edit)
                before_ignore_edit = lab.source_fingerprint()
                (repo / ".dockerignore").write_text("**\n!pogo\n!pogo/**\n# reviewed context rule\n")
                self.assertNotEqual(lab.source_fingerprint(), before_ignore_edit)

    def test_fingerprint_records_modes_and_links_without_reading_external_private_target(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory) / "repo"
            source = repo / "pogo"
            source.mkdir(parents=True)
            script = source / "run.sh"
            script.write_text("#!/bin/sh\nexit 0\n")
            target = Path(directory) / "private.env"
            target.write_text("synthetic-private-one")
            link = source / "external-link"
            link.symlink_to(target)
            names = "pogo/run.sh\0pogo/external-link\0"
            with patch.object(lab, "REPO", repo), patch.object(lab, "run", return_value=names):
                before = lab.source_fingerprint()
                script.chmod(0o755)
                self.assertNotEqual(lab.source_fingerprint(), before)
                before_secret_edit = lab.source_fingerprint()
                target.write_text("synthetic-private-two")
                self.assertEqual(lab.source_fingerprint(), before_secret_edit)
                link.unlink()
                link.symlink_to(Path(directory) / "different-private.env")
                self.assertNotEqual(lab.source_fingerprint(), before_secret_edit)


if __name__ == "__main__":
    unittest.main()

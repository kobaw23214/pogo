import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("pogo_v2_lab", Path(__file__).with_name("browser-use-v2-lab.py"))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class BrowserUseV2LabTests(unittest.TestCase):
    def test_private_init_preserves_credentials_and_excludes_old_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            with contextlib.redirect_stdout(io.StringIO()):
                lab.initialize(private)
                original = (private / "recorder.env").read_bytes()
                lab.initialize(private)
            self.assertEqual((private / "recorder.env").read_bytes(), original)
            self.assertEqual((private / "recorder.env").stat().st_mode & 0o777, 0o600)
        with self.assertRaises(ValueError):
            lab.initialize((lab.REPO / ".localagent/browser-auth-lab-private").resolve())

    def test_stop_is_fixed_to_new_project_and_preserves_volumes(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "v2-compose.env").write_text("# synthetic\n")
            with patch.object(lab, "run", return_value="") as execute, contextlib.redirect_stdout(io.StringIO()):
                lab.stop(private)
            args = execute.call_args.args[0]
            self.assertEqual(args[:4], ["docker", "compose", "--project-name", "pogo-browser-v2-lab"])
            self.assertIn(str(lab.HERE / "compose.browser-use-v2.yml"), args)
            self.assertNotIn("--volumes", args)
            self.assertEqual(args[-2:], ["down", "--remove-orphans"])

    def test_ambient_compose_or_candidate_variables_cannot_redirect_commands(self):
        with patch.dict(os.environ, {"COMPOSE_PROJECT_NAME": "production", "V2_APP_IMAGE": "wrong", "LAB_PRIVATE_DIR": "wrong"}), patch.object(lab.subprocess, "run") as execute:
            lab.run(["docker", "compose", "version"])
        environment = execute.call_args.kwargs["env"]
        self.assertNotIn("COMPOSE_PROJECT_NAME", environment)
        self.assertNotIn("V2_APP_IMAGE", environment)
        self.assertNotIn("LAB_PRIVATE_DIR", environment)

    def test_image_or_source_drift_prevents_any_start(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "v2-build.json").write_text(json.dumps({"project": lab.PROJECT, "source_sha256": "expected", "images": {"app": {"tag": "v2-test", "id": "expected-image"}}}))
            for image, source in [("wrong-image", "expected"), ("expected-image", "wrong-source")]:
                with self.subTest(image=image, source=source), patch.object(lab, "initialize"), patch.object(lab.base, "image_info", return_value={"Id": image}), patch.object(lab.base, "source_fingerprint", return_value=source), patch.object(lab, "compose") as compose:
                    with self.assertRaises(ValueError):
                        lab.start(private)
                    compose.assert_not_called()

    def test_unhealthy_project_is_not_reported_ready(self):
        with patch.object(lab, "compose", return_value='[]'), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(ValueError):
                lab.status(Path("/synthetic/v2-private"))


if __name__ == '__main__':
    unittest.main()

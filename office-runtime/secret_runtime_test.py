#!/usr/bin/env python3
"""Run inside the pinned Office image as root with /tmp on Linux tmpfs.

No DocServer/application service is started. A private Supervisor fixture and
real UID transitions expose the Linux DAC failure
that Docker Desktop bind-mount behavior can conceal. Only synthetic keys exist.
"""
import errno
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("office_secret", HERE / "prepare-secret.py")
secret = importlib.util.module_from_spec(spec)
spec.loader.exec_module(secret)

READ_PROBE = r"""
import hashlib,json,os,pathlib,sys
try:
 raw=pathlib.Path(sys.argv[1]).read_bytes()
 print(json.dumps({'readable':True,'uid':os.geteuid(),'gid':os.getegid(),'raw_sha256':hashlib.sha256(raw).hexdigest(),'key_sha256':hashlib.sha256(raw.rstrip(b'\n')).hexdigest()}))
except OSError as exc:
 print(json.dumps({'readable':False,'uid':os.geteuid(),'gid':os.getegid(),'errno':exc.errno}))
"""


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def metadata(path):
    st = path.stat()
    return (st.st_dev, st.st_ino, st.st_uid, st.st_gid, stat.S_IMODE(st.st_mode), st.st_size, st.st_mtime_ns, st.st_ctime_ns, digest(path.read_bytes()))


def filesystems():
    if not sys.platform.startswith("linux"):
        return {}
    return {fields[1]: fields[2] for line in Path("/proc/mounts").read_text().splitlines() if (fields := line.split())[1] in ("/tmp", secret.RUNTIME_DIRECTORY)}


@unittest.skipUnless(sys.platform.startswith("linux") and os.geteuid() == 0, "requires actual Linux container root and service UID transitions")
class OfficeSecretRuntimeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if filesystems() != {"/tmp": "tmpfs", secret.RUNTIME_DIRECTORY: "tmpfs"}:
            raise AssertionError("cross-UID tests require container-internal tmpfs, not host bind mounts")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="pogo-office-secret-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.root.chmod(0o755)
        self.runtime = self.root / "runtime"
        self.source = self.root / "source-jwt"
        self.raw = b"synthetic_Office_Deployment_Key_1234567890-abcdefgh\n"
        self.source.write_bytes(self.raw)
        self.source.chmod(0o600)
        os.chown(self.source, 1000, 1000)
        self.ds = pwd.getpwnam("ds")

    def read_as(self, uid, gid, path):
        result = subprocess.run([sys.executable, "-c", READ_PROBE, str(path)], user=uid, group=gid, extra_groups=[], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, "read probe did not complete")
        return json.loads(result.stdout)

    def prepare(self):
        return Path(secret.prepare(self.source, runtime_directory=str(self.runtime)))

    def test_real_ds_denied_original_source_and_reads_only_private_copy(self):
        self.assertEqual((self.ds.pw_uid, self.ds.pw_gid), (101, 102), "pinned Office service identity changed; revalidate actual image")
        before = metadata(self.source)
        original = self.read_as(self.ds.pw_uid, self.ds.pw_gid, self.source)
        self.assertFalse(original["readable"])
        self.assertEqual(original["errno"], errno.EACCES)
        self.assertTrue(self.read_as(1000, 1000, self.source)["readable"])
        copied = self.prepare()
        ds_read = self.read_as(self.ds.pw_uid, self.ds.pw_gid, copied)
        self.assertTrue(ds_read["readable"])
        self.assertEqual(ds_read["raw_sha256"], digest(self.raw))
        self.assertEqual(ds_read["key_sha256"], digest(self.raw.rstrip(b"\n")))
        self.assertFalse(self.read_as(2000, 2000, copied)["readable"])
        self.assertFalse(self.read_as(1000, 1000, copied)["readable"])
        self.assertEqual(metadata(self.source), before)
        self.assertEqual((copied.stat().st_uid, copied.stat().st_gid, stat.S_IMODE(copied.stat().st_mode)), (0, self.ds.pw_gid, 0o640))
        self.assertEqual((self.runtime.stat().st_uid, self.runtime.stat().st_gid, stat.S_IMODE(self.runtime.stat().st_mode)), (0, self.ds.pw_gid, 0o710))

    def test_restart_and_rotation_publish_complete_new_copy_and_preserve_source(self):
        copied = self.prepare()
        first_fd = os.open(copied, os.O_RDONLY)
        self.addCleanup(os.close, first_fd)
        before = metadata(self.source)
        self.prepare()
        self.assertEqual(metadata(self.source), before)
        self.assertEqual(self.read_as(self.ds.pw_uid, self.ds.pw_gid, copied)["raw_sha256"], digest(self.raw))
        rotated = b"synthetic_Rotated_Office_Key_1234567890-abcdefghijkl\n"
        self.source.write_bytes(rotated)
        source_after_rotation = metadata(self.source)
        self.prepare()
        self.assertEqual(metadata(self.source), source_after_rotation)
        self.assertEqual(os.read(first_fd, 16384), self.raw)
        self.assertEqual(self.read_as(self.ds.pw_uid, self.ds.pw_gid, copied)["raw_sha256"], digest(rotated))
        self.assertEqual(sorted(p.name for p in self.runtime.iterdir()), ["jwt"])

    def test_source_symlink_and_symlink_parent_are_rejected(self):
        link = self.root / "source-link"
        link.symlink_to(self.source)
        with self.assertRaises(secret.SecretError):
            secret.prepare(link, runtime_directory=self.runtime)
        directory = self.root / "source-parent"
        directory.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(secret.SecretError):
            secret.prepare(directory / self.source.name, runtime_directory=self.runtime)
        self.assertFalse(self.runtime.exists())

    def test_runtime_symlink_and_existing_jwt_symlink_are_rejected(self):
        other = self.root / "other"
        other.mkdir(mode=0o700)
        self.runtime.symlink_to(other, target_is_directory=True)
        with self.assertRaises(secret.SecretError):
            self.prepare()
        self.assertEqual(list(other.iterdir()), [])
        self.runtime.unlink()
        self.runtime.mkdir(mode=0o700)
        target = self.root / "unrelated"
        target.write_bytes(b"untouched")
        (self.runtime / "jwt").symlink_to(target)
        with self.assertRaises(secret.SecretError):
            self.prepare()
        self.assertEqual(target.read_bytes(), b"untouched")

    def test_group_or_world_readable_source_is_rejected_without_changing_it(self):
        for mode in (0o640, 0o644):
            self.source.chmod(mode)
            before = metadata(self.source)
            with self.assertRaises(secret.SecretError) as caught:
                self.prepare()
            self.assertEqual(caught.exception.code, "POGO_OFFICE_SECRET_SOURCE_PERMISSIONS")
            self.assertEqual(metadata(self.source), before)

    def test_nonregular_source_is_rejected_without_blocking(self):
        self.source.unlink()
        os.mkfifo(self.source, 0o600)
        with self.assertRaises(secret.SecretError):
            self.prepare()

    def test_invalid_and_oversized_secret_does_not_replace_valid_copy(self):
        copied = self.prepare()
        original = metadata(copied)
        for raw in (b"short", b"a" * 32 + b"\r\n", b"a" * 32 + b"\nb", b"a" * 8193, b"a" * 16385):
            self.source.write_bytes(raw)
            with self.assertRaises(secret.SecretError) as caught:
                self.prepare()
            self.assertEqual(caught.exception.code, "POGO_OFFICE_SECRET_SOURCE_INVALID")
            self.assertEqual(metadata(copied), original)

    def test_interrupted_atomic_publish_preserves_old_copy_and_cleans_temporary(self):
        copied = self.prepare()
        original = metadata(copied)
        self.source.write_bytes(b"synthetic_Replacement_Key_1234567890-abcdefghijkl\n")
        with patch.object(secret.os, "replace", side_effect=PermissionError("synthetic publish failure")):
            with self.assertRaises(secret.SecretError):
                self.prepare()
        self.assertEqual(metadata(copied), original)
        self.assertEqual(sorted(p.name for p in self.runtime.iterdir()), ["jwt"])

    def test_runtime_owned_or_writable_by_other_accounts_is_rejected(self):
        self.runtime.mkdir(mode=0o700)
        os.chown(self.runtime, self.ds.pw_uid, self.ds.pw_gid)
        with self.assertRaises(secret.SecretError):
            self.prepare()
        os.chown(self.runtime, 0, 0)
        self.runtime.chmod(0o770)
        with self.assertRaises(secret.SecretError):
            self.prepare()
        self.assertFalse((self.runtime / "jwt").exists())

    def test_unprivileged_caller_cannot_prepare_runtime(self):
        result = subprocess.run([sys.executable, str(HERE / "prepare-secret.py"), str(self.source)], user=1000, group=1000, extra_groups=[], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 1)
        self.assertIn("POGO_OFFICE_SECRET_ROOT_REQUIRED", result.stderr)
        self.assertNotIn(self.raw.decode().strip(), result.stdout + result.stderr)

    def test_entrypoint_exports_same_normalized_key_and_private_path_to_children(self):
        # Execute the real entrypoint text, changing only its final engine exec
        # to a read-only child probe. No DocServer or other service is started.
        script = (HERE / "docker-entrypoint.sh").read_text()
        self.assertEqual(script.count('exec /app/ds/run-document-server.sh "$@"'), 1)
        probe = self.root / "child-probe.py"
        probe.write_text('''import hashlib,json,os,pathlib,pwd,subprocess,sys
p=pathlib.Path(os.environ['POGO_OFFICE_JWT_SECRET_FILE'])
raw=p.read_bytes();key=os.environ['JWT_SECRET'].encode();ds=pwd.getpwnam('ds')
child=subprocess.run([sys.executable,'-c',"import os,pathlib; pathlib.Path(os.environ['POGO_OFFICE_JWT_SECRET_FILE']).read_bytes()"],user=ds.pw_uid,group=ds.pw_gid,extra_groups=[],capture_output=True)
print(json.dumps({'source_path_hidden':str(p)!='/run/secrets/pogo-office-jwt','private_path':str(p),'raw_sha256':hashlib.sha256(raw).hexdigest(),'key_sha256':hashlib.sha256(key).hexdigest(),'normalized_match':key==raw.rstrip(b'\\n'),'ds_child_inherited_readable':child.returncode==0,'upstream_jwt_enabled':os.environ.get('JWT_ENABLED')=='true','upstream_header':os.environ.get('JWT_HEADER')}))
''')
        entry = self.root / "docker-entrypoint.sh"
        entry.write_text(script.replace('exec /app/ds/run-document-server.sh "$@"', 'exec python3 "' + str(probe) + '"'))
        shutil.copyfile(HERE / "prepare-secret.py", self.root / "prepare-secret.py")
        before = metadata(self.source)
        result = subprocess.run(["bash", str(entry)], env=dict(os.environ, POGO_OFFICE_JWT_SECRET_FILE=str(self.source)), capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(self.raw.decode().strip(), result.stdout + result.stderr)
        info = json.loads(result.stdout)
        self.assertEqual(info["private_path"], "/run/pogo-office-private/jwt")
        self.assertTrue(info["normalized_match"])
        self.assertTrue(info["ds_child_inherited_readable"])
        self.assertTrue(info["upstream_jwt_enabled"])
        self.assertEqual(info["raw_sha256"], digest(self.raw))
        self.assertEqual(info["key_sha256"], digest(self.raw.rstrip(b"\n")))
        self.assertEqual(info["upstream_header"], "Authorization")
        self.assertEqual(metadata(self.source), before)

    def test_actual_supervisor_ds_child_uses_private_copy_even_with_original_parent_env(self):
        secret.prepare(self.source)
        probe = self.root / "supervisor-probe.mjs"
        probe.write_text("import fs from 'node:fs'; import crypto from 'node:crypto'; const p=process.env.POGO_OFFICE_JWT_SECRET_FILE; const raw=fs.readFileSync(p); console.log(JSON.stringify({uid:process.getuid(),gid:process.getgid(),path:p,raw_sha256:crypto.createHash('sha256').update(raw).digest('hex')})); setInterval(()=>{},1000);\n")
        probe.chmod(0o644)
        program = (HERE / "docker-supervisor.conf").read_text()
        original = "command=/usr/local/bin/pogo-office-node /opt/pogo-office/docker-proxy.mjs"
        self.assertEqual(program.count(original), 1)
        program = program.replace(original, "command=/usr/local/bin/pogo-office-node " + str(probe))
        program = program.replace("/var/log/onlyoffice/documentserver/pogo-office-proxy/", str(self.root) + "/")
        config = self.root / "supervisord.conf"
        config.write_text("[supervisord]\nnodaemon=true\nuser=root\nlogfile=" + str(self.root / "supervisord.log") + "\npidfile=" + str(self.root / "supervisord.pid") + "\nchildlogdir=" + str(self.root) + "\n" + program)
        process = subprocess.Popen(["supervisord", "-c", str(config)], env=dict(os.environ, POGO_OFFICE_JWT_SECRET_FILE=str(self.source)), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            output = self.root / "out.log"
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                if output.exists() and output.stat().st_size:
                    break
                if process.poll() is not None:
                    self.fail("isolated Supervisor exited before the ds probe")
                time.sleep(0.025)
            self.assertTrue(output.exists() and output.stat().st_size, "isolated Supervisor did not receive a ds read receipt")
            info = json.loads(output.read_text().splitlines()[0])
            self.assertEqual((info["uid"], info["gid"]), (self.ds.pw_uid, self.ds.pw_gid))
            self.assertEqual(info["path"], "/run/pogo-office-private/jwt")
            self.assertEqual(info["raw_sha256"], digest(self.raw))
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


if __name__ == "__main__":
    identity = pwd.getpwnam("ds") if sys.platform.startswith("linux") else None
    print(json.dumps({"platform": sys.platform, "python": sys.version.split()[0], "uid": os.geteuid(), "ds_uid": identity.pw_uid if identity else None, "ds_gid": identity.pw_gid if identity else None, "filesystems": filesystems(), "application_services_started": False, "isolated_supervisor_fixture": True}))
    unittest.main(verbosity=2)

#!/usr/bin/env python3
"""Copy the candidate working tree, never deployment credentials or runtime data."""
import hashlib
import json
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys

DEPLOY_FILES = {
    "deploy.sh", "deploy_test.py", "compose.browser.yml", "compose_browser_test.py",
    "release.py", "release_test.py",
}
DENIED_PARTS = {".git", ".pogo", ".claude", ".ssh", ".aws", ".kube", ".config",
                "node_modules", "__pycache__", ".cache", "runs"}


def allowed(name):
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts:
        return False
    if path.parts[0] == "w_deploy":
        return len(path.parts) == 2 and path.name in DEPLOY_FILES
    if path.parts[0] != "pogo":
        return name == ".gitlab-ci.yml"
    if any(p in DENIED_PARTS or p.startswith(".tmp") for p in path.parts):
        return False
    if path.name.startswith((".env", ".browser.env", "providers.")) or path.name in {
        "deploy.env", "users.json", "credentials.json", "id_rsa", "id_ed25519",
        ".npmrc", ".netrc", ".pypirc", ".git-credentials",
    }:
        return False
    if path.suffix in {".key", ".pem", ".log", ".db", ".sqlite", ".sqlite3"}:
        return False
    return not name.startswith("pogo/web/ui/dist/")


def snapshot(root, destination):
    if destination.exists():
        raise ValueError("snapshot destination must not exist")
    names = subprocess.check_output([
        "git", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard",
    ]).decode().split("\0")
    destination.mkdir(parents=True)
    manifest = []
    for name in sorted(set(filter(None, names))):
        if not allowed(name):
            continue
        source = root / name
        if not source.exists():  # deleted in the candidate working tree
            continue
        # Never follow a tracked or untracked symlink into private host data.
        if any((root / Path(*PurePosixPath(name).parts[:i])).is_symlink()
               for i in range(1, len(PurePosixPath(name).parts) + 1)):
            raise ValueError(f"candidate symlink is unsupported: {name}")
        if not source.is_file():
            continue
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
        manifest.append({"path": name, "sha256": hashlib.sha256(target.read_bytes()).hexdigest()})
    (destination / "source-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return len(manifest)


if __name__ == "__main__":
    count = snapshot(Path(sys.argv[1]).resolve(), Path(sys.argv[2]))
    print(f"Prepared {count} candidate source files (credentials/runtime data excluded)")

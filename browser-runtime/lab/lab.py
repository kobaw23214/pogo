#!/usr/bin/env python3
"""Auditable candidate-only stack. Never read production credentials or compose files."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import urllib.request

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
PROJECT = "pogo-browser-auth-lab"
APP_URL = "http://pogo-auth-lab.localhost:19778"
APP_HEALTH_URL = "http://127.0.0.1:19778"
MOCK_URL = "http://127.0.0.1:5190"


def run(command, capture=False):
    # Do not allow an ambient COMPOSE_PROJECT_NAME or LAB_* value to redirect a lab operation.
    env = {key: value for key, value in os.environ.items() if not key.startswith(("COMPOSE_", "LAB_"))}
    return subprocess.run(command, cwd=REPO, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None).stdout


def write_new(path, content):
    try:
        with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as handle:
            handle.write(content)
    except FileExistsError:
        pass


def private_write(path, content):
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("w") as handle:
        os.chmod(temporary, 0o600)
        handle.write(content)
    temporary.replace(path)


def initialize(private):
    if private == REPO or private.is_relative_to(REPO / "pogo"):
        raise ValueError("Private files must be outside pogo/; repository-root private files are excluded by the root Docker ignore rules")
    private.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(private, 0o700)
    if not (private / "users.json").exists():
        password_file = private / "admin-password.txt"
        password = password_file.read_text().strip() if password_file.exists() else secrets.token_urlsafe(24)
        if not password:
            raise ValueError("Existing candidate admin password file is empty")
        salt = secrets.token_bytes(16)
        digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600000, 32)
        encoded = lambda value: base64.b64encode(value).decode().rstrip("=")
        password_hash = f"pbkdf2-sha256$600000${encoded(salt)}${encoded(digest)}"
        write_new(private / "admin-password.txt", password + "\n")
        write_new(private / "users.json", json.dumps({"users": [{"username": "admin", "hash": password_hash, "admin": True}]}) + "\n")
    if not (private / "runtime.env").exists():
        browser_token = secrets.token_urlsafe(36)
        write_new(private / "runtime.env", f"POGO_WEB_TOKEN={secrets.token_urlsafe(36)}\nPOGO_BROWSER_TOKEN={browser_token}\n")
        write_new(private / "worker.env", f"POGO_BROWSER_TOKEN={browser_token}\n")
    if not (private / "worker.env").exists():
        values = dict(line.split("=", 1) for line in (private / "runtime.env").read_text().splitlines() if "=" in line)
        write_new(private / "worker.env", "POGO_BROWSER_TOKEN=" + values["POGO_BROWSER_TOKEN"] + "\n")
    write_new(private / "providers.json", json.dumps({"providers": {"lab-unconfigured": {"base_url": "http://127.0.0.1:1/v1", "key_env": "LAB_MODEL_DISABLED"}}, "models": [{"id": "lab-unconfigured", "provider": "lab-unconfigured", "context_window": 8192, "max_tokens": 1024}]}) + "\n")
    write_new(private / "model.env", "# Supply the approved model key privately before model testing.\n")
    for name in ["providers.json", "model.env", "runtime.env", "worker.env", "users.json", "admin-password.txt"]:
        if (private / name).exists():
            os.chmod(private / name, 0o600)
    print(f"Private config ready: {private} (credentials are never printed)")


def image_info(tag):
    return json.loads(run(["docker", "image", "inspect", tag], capture=True))[0]


def source_fingerprint():
    """Audit tracked/nonignored source, not the full Docker context or dependency provenance."""
    # NUL separators preserve non-ASCII names and paths containing newlines.
    # Untracked Go/JS/UI source outside lab/ can affect the actual candidate.
    names = run(["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "pogo", ".dockerignore"], capture=True)
    files = [REPO / name for name in names.split("\0") if name]
    digest = hashlib.sha256()
    excluded_dirs = {"__pycache__", "node_modules", "dist", ".git", ".pogo", ".claude", ".localagent", ".private", "private", "secrets", "credentials"}
    private_config = re.compile(r"(?:^|[._-])(?:secrets?|credentials?|passwords?|tokens?|keys?|users|providers)(?:[._-]|$)", re.I)
    for path in sorted(set(files)):
        relative = path.relative_to(REPO)
        name = path.name.lower()
        if not (path.is_symlink() or path.is_file()) or excluded_dirs.intersection(relative.parts):
            continue
        if name == "env" or name.endswith(".env") or ".env." in name or name.startswith(".env"):
            continue
        if path.suffix.lower() in {".pem", ".key", ".p12", ".pfx"} or (path.suffix.lower() in {".json", ".yaml", ".yml", ".toml", ".ini", ".txt"} and private_config.search(name)):
            continue
        # Record a symlink itself, never read its possibly private external target.
        kind = "symlink" if path.is_symlink() else "file"
        content = os.readlink(path).encode() if kind == "symlink" else path.read_bytes()
        metadata = {"path": relative.as_posix(), "kind": kind, "mode": stat.S_IMODE(path.lstat().st_mode), "length": len(content)}
        digest.update(json.dumps(metadata, sort_keys=True, separators=(",", ":")).encode() + b"\0" + content)
    return digest.hexdigest()


def build(private):
    initialize(private)
    lock = json.loads((HERE / "base-images.json").read_text())
    platform = lock["platform"]
    for tag, expected in lock["images"].items():
        current = image_info(tag)
        if current["Id"] != expected or f"{current['Os']}/{current['Architecture']}" != platform:
            raise ValueError(f"Pinned dependency image mismatch: {tag}. Review base-images.json before changing the pin.")
    revision = run(["git", "rev-parse", "HEAD"], capture=True).strip()
    source_hash = source_fingerprint()
    suffix = f"{revision[:7]}-{source_hash[:12]}-{platform.split('/')[1]}"
    images = {"app": f"pogo-auth-lab-app:{suffix}", "worker": f"pogo-auth-lab-worker:{suffix}", "mock": f"pogo-auth-lab-mock:{suffix}"}
    common = ["docker", "build", "--pull=false", "--platform", platform,
              "--label", f"org.opencontainers.image.revision={revision}", "--label", "io.pogo.lab=browser-auth-candidate",
              "--label", f"io.pogo.lab.source-sha256={source_hash}"]
    run(common + ["-f", "pogo/Dockerfile", "-t", images["app"], "."])
    for service, dockerfile, base in [("worker", "Dockerfile.worker", "pogo-browser:settle-20261001-arm64"), ("mock", "Dockerfile.mock", "node:24-bookworm-slim")]:
        run(common + ["-f", str(HERE / dockerfile), "--build-arg", f"BASE_IMAGE={base}",
                      "--build-arg", f"BASE_IMAGE_ID={lock['images'][base]}", "--build-arg", f"SOURCE_REVISION={revision}",
                      "-t", images[service], "pogo/browser-runtime"])
    entries = {service: {"tag": tag, "id": image_info(tag)["Id"]} for service, tag in images.items()}
    if source_fingerprint() != source_hash:
        raise ValueError("Source changed during build; candidate manifest was not replaced. Rebuild after edits finish.")
    private_write(private / "build.json", json.dumps({"revision": revision, "source_sha256": source_hash, "platform": platform, "images": entries, "base_images": lock["images"]}, indent=2) + "\n")
    private_write(private / "compose.env", "\n".join([f"LAB_PRIVATE_DIR={private}", f"LAB_PLATFORM={platform}",
                  f"LAB_APP_IMAGE={images['app']}", f"LAB_WORKER_IMAGE={images['worker']}", f"LAB_MOCK_IMAGE={images['mock']}"]) + "\n")
    print(f"Built isolated candidate revision {revision[:12]} ({platform}); manifest: {private / 'build.json'}")


def compose(private, *arguments, capture=False):
    if not (private / "compose.env").is_file():
        raise ValueError("Run build before starting this isolated stack")
    return run(["docker", "compose", "--project-name", PROJECT, "--env-file", str(private / "compose.env"),
                "-f", str(HERE / "compose.yml"), *arguments], capture=capture)


def start(private):
    initialize(private)
    if not (private / "build.json").exists():
        build(private)
    manifest = json.loads((private / "build.json").read_text())
    for value in manifest["images"].values():
        if image_info(value["tag"])["Id"] != value["id"]:
            raise ValueError("Candidate tag changed since build; rebuild explicitly")
    compose(private, "config", "--quiet", capture=True)
    # This one-shot service refreshes provider config only; it preserves existing lab users/data.
    compose(private, "run", "--rm", "--no-deps", "bootstrap")
    compose(private, "up", "-d", "--wait", "--wait-timeout", "120", "app", "worker", "mock-platform")
    status(private)


def status(private):
    raw = compose(private, "ps", "--format", "json", capture=True).strip()
    rows = json.loads(raw) if raw.startswith("[") else [json.loads(line) for line in raw.splitlines() if line]
    summary = [{"service": row["Service"], "state": row["State"], "health": row.get("Health", "")} for row in rows]
    print(json.dumps({"project": PROJECT, "services": summary}, ensure_ascii=False))
    healthy = {row["service"] for row in summary if row["state"] == "running" and row["health"] == "healthy"}
    if not {"app", "worker", "mock-platform"}.issubset(healthy):
        raise ValueError("Candidate services are not all healthy")
    for url, label in [(APP_HEALTH_URL + "/api/auth/status", "app login"), (MOCK_URL + "/", "mock page")]:
        with urllib.request.urlopen(url, timeout=5) as response:
            if response.status != 200:
                raise ValueError(f"{label} is not ready")
    print(f"App: {APP_URL}/manage / mock: {MOCK_URL}")
    print("In the candidate browser, open http://mock-platform:5190/ (private Docker network).")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["init", "build", "start", "status", "stop"])
    parser.add_argument("--private-dir", default=os.environ.get("LAB_PRIVATE_DIR", str(REPO / ".localagent/browser-auth-lab-private")))
    args = parser.parse_args()
    private = Path(args.private_dir).expanduser().resolve()
    {"init": initialize, "build": build, "start": start, "status": status, "stop": stop}[args.command](private)


def stop(private):
    compose(private, "down", "--remove-orphans")
    print("Only the candidate project stopped. Its named volumes are preserved.")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        # Captured errors could include resolved Compose values; never echo them.
        print(f"Lab command failed (exit {error.returncode}); private values were not printed.", file=sys.stderr)
        sys.exit(1)
    except Exception as error:
        print(f"Lab error: {error}", file=sys.stderr)
        sys.exit(1)

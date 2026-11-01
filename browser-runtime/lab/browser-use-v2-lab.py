#!/usr/bin/env python3
"""Build and run only the separate Studio browser-use-v2 candidate project."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("pogo_auth_lab_helpers", HERE / "lab.py")
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
REPO = base.REPO
PROJECT = "pogo-browser-v2-lab"
DEFAULT_PRIVATE = REPO / ".localagent/browser-use-v2-lab-private"


def run(command, capture=False):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("COMPOSE_", "LAB_", "V2_"))}
    result = subprocess.run(command, cwd=REPO, env=env, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None)
    return result.stdout


def initialize(private):
    if private == (REPO / ".localagent/browser-auth-lab-private").resolve():
        raise ValueError("Use a separate v2 private directory; the existing auth candidate is preserved")
    base.initialize(private)
    base.write_new(private / "recorder.env", "V2_RECORDER_ADMIN_TOKEN=" + secrets.token_urlsafe(36) + "\n")
    os.chmod(private / "recorder.env", 0o600)


def build(private):
    initialize(private)
    lock = json.loads((HERE / "base-images.json").read_text())
    platform = lock["platform"]
    for tag, expected in lock["images"].items():
        image = base.image_info(tag)
        if image["Id"] != expected or f"{image['Os']}/{image['Architecture']}" != platform:
            raise ValueError("Pinned dependency image mismatch: " + tag)
    revision = run(["git", "rev-parse", "HEAD"], capture=True).strip()
    source = base.source_fingerprint()
    suffix = f"{revision[:7]}-{source[:12]}-{platform.split('/')[-1]}"
    images = {"app": f"pogo-browser-v2-app:{suffix}", "runtime": f"pogo-browser-v2-runtime:{suffix}"}
    common = ["docker", "build", "--pull=false", "--platform", platform,
              "--label", "io.pogo.lab=browser-use-v2-candidate",
              "--label", f"io.pogo.lab.source-sha256={source}",
              "--label", f"org.opencontainers.image.revision={revision}"]
    run(common + ["-f", "pogo/Dockerfile", "-t", images["app"], "."])
    dependency = "pogo-browser:settle-20261001-arm64"
    run(common + ["-f", str(HERE / "Dockerfile.browser-use-v2"),
                  "--build-arg", "BASE_IMAGE=" + dependency,
                  "--build-arg", "BASE_IMAGE_ID=" + lock["images"][dependency],
                  "--build-arg", "SOURCE_REVISION=" + revision,
                  "-t", images["runtime"], "pogo/browser-runtime"])
    if base.source_fingerprint() != source:
        raise ValueError("Source changed during build; freeze source and rebuild before starting")
    manifest = {"project": PROJECT, "revision": revision, "source_sha256": source, "platform": platform,
                "images": {k: {"tag": tag, "id": base.image_info(tag)["Id"]} for k, tag in images.items()},
                "base_images": lock["images"]}
    base.private_write(private / "v2-build.json", json.dumps(manifest, indent=2) + "\n")
    base.private_write(private / "v2-compose.env", "\n".join([
        f"V2_PRIVATE_DIR={private}", f"V2_PLATFORM={platform}",
        f"V2_APP_IMAGE={images['app']}", f"V2_RUNTIME_IMAGE={images['runtime']}"]) + "\n")
    print(json.dumps({"project": PROJECT, "source_sha256": source, "images": manifest["images"]}))


def compose(private, *args, capture=False):
    if not (private / "v2-compose.env").is_file():
        raise ValueError("Build the separate v2 candidate first")
    return run(["docker", "compose", "--project-name", PROJECT, "--env-file", str(private / "v2-compose.env"),
                "-f", str(HERE / "compose.browser-use-v2.yml"), *args], capture=capture)


def start(private):
    initialize(private)
    manifest = json.loads((private / "v2-build.json").read_text())
    if manifest["project"] != PROJECT:
        raise ValueError("Unexpected candidate build project")
    for entry in manifest["images"].values():
        if base.image_info(entry["tag"])["Id"] != entry["id"]:
            raise ValueError("Candidate image tag drift; rebuild explicitly")
    if base.source_fingerprint() != manifest["source_sha256"]:
        raise ValueError("Source no longer matches candidate build; rebuild explicitly")
    compose(private, "config", "--quiet", capture=True)
    compose(private, "run", "--rm", "--no-deps", "bootstrap")
    compose(private, "up", "-d", "--wait", "--wait-timeout", "120", "app", "worker", "recorder")
    status(private)


def status(private):
    raw = compose(private, "ps", "--format", "json", capture=True).strip()
    rows = json.loads(raw) if raw.startswith("[") else [json.loads(line) for line in raw.splitlines() if line]
    result = [{"service": row["Service"], "state": row["State"], "health": row.get("Health", "")} for row in rows]
    print(json.dumps({"project": PROJECT, "services": result}))
    if not {"app", "worker", "recorder"}.issubset({r['service'] for r in result if r['state'] == 'running' and r['health'] == 'healthy'}):
        raise ValueError("Candidate is not fully healthy")
    print("Candidate UI: http://pogo-browser-v2.localhost:19779/manage; private recorder admin: loopback port 19780")


def stop(private):
    compose(private, "down", "--remove-orphans")
    print("Only the separate v2 project stopped; named volumes retained")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["init", "build", "start", "status", "stop"])
    parser.add_argument("--private-dir", default=str(DEFAULT_PRIVATE))
    args = parser.parse_args()
    private = Path(args.private_dir).expanduser().resolve()
    {"init": initialize, "build": build, "start": start, "status": status, "stop": stop}[args.command](private)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        print(f"Candidate command failed (exit {error.returncode}); resolved private values omitted", file=sys.stderr)
        sys.exit(1)
    except Exception as error:
        print(f"Candidate error: {error}", file=sys.stderr)
        sys.exit(1)

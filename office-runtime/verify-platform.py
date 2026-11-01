#!/usr/bin/env python3
"""Fail the shared Office build if its platform, engine and asset lock disagree."""
import argparse
import json
from pathlib import Path
import re


def verify(manifest, platform, upstream_image, runtime_architecture):
    if platform not in {"linux/arm64", "linux/amd64"}:
        raise ValueError("unsupported Office build platform")
    if runtime_architecture != platform.split("/", 1)[1]:
        raise ValueError("Office image architecture differs from the target platform")
    if manifest.get("platform") != platform:
        raise ValueError("Office asset manifest belongs to another platform")
    if (not re.fullmatch(r"onlyoffice/documentserver@sha256:[a-f0-9]{64}", upstream_image)
            or manifest.get("upstreamImageDigest") != upstream_image):
        raise ValueError("Office engine image differs from its audited asset manifest")
    if (manifest.get("engineVersion") != "9.4.0 (build:129)"
            or manifest.get("upstreamSDKTag") != "v9.4.0.129"
            or not re.fullmatch(r"9\.4\.0-[a-f0-9]{32}", manifest.get("vendorVersion", ""))):
        raise ValueError("Office engine version or vendor identity is invalid")
    assets = manifest.get("assets", [])
    loaders = manifest.get("loaderAssets", [])
    if len(assets) != 4 or len(loaders) != 5:
        raise ValueError("Office manifest must lock all nine SDK and loader resources")
    paths = [entry.get("path") for entry in assets + loaders]
    if len(set(paths)) != 9 or any(not isinstance(p, str) or not p.startswith("/") for p in paths):
        raise ValueError("Office resource paths are invalid")
    if any(not re.fullmatch(r"[a-f0-9]{64}", entry.get("sha256", "")) for entry in assets + loaders):
        raise ValueError("Office resource hashes are invalid")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--upstream-image", required=True)
    parser.add_argument("--runtime-architecture", required=True)
    parser.add_argument("--manifest", type=Path, default=Path(__file__).with_name("engine-manifest.json"))
    args = parser.parse_args()
    verify(json.loads(args.manifest.read_text()), args.platform, args.upstream_image, args.runtime_architecture)

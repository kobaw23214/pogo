#!/usr/bin/env python3
"""Prepare bounded large-document downloads before the pinned engine starts.

Only the FileConverter pull-download and DOCX/XLSX XML expansion limits are
raised. This does not raise image, gateway, or temporary-upload limits. Existing
larger operator limits and every unrelated setting, including JWTs, are kept.
No configuration contents or secrets are printed.
"""
import copy
import json
import math
import os
from pathlib import Path
import re
import secrets
import stat
import sys

CONFIG_ROOT = Path("/etc/onlyoffice/documentserver")
MAX_CONFIG_BYTES = 4 << 20
DOCUMENT_BYTES = 256 << 20
EXPANDED_XML_BYTES = 2 << 30
DOWNLOAD_MILLISECONDS = 5 * 60 * 1000
TARGET_TYPES = frozenset(("docx", "xlsx"))


class LimitsError(Exception):
    pass


def invalid():
    raise LimitsError("POGO_OFFICE_LIMITS_INVALID: Office download-limit configuration is invalid; the original configuration was retained.")


def object_field(parent, key, fallback=None):
    value = parent.get(key, {} if fallback is None else fallback)
    if not isinstance(value, dict):
        invalid()
    return value


def size_bytes(value):
    if not isinstance(value, str) or len(value) > 64:
        invalid()
    match = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*(B|KB|MB|GB|TB)\s*", value, re.I)
    if not match:
        invalid()
    number = float(match[1]) * (1024 ** ("B", "KB", "MB", "GB", "TB").index(match[2].upper()))
    if not math.isfinite(number) or number < 0:
        invalid()
    return number


def duration_milliseconds(value):
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if not math.isfinite(value) or value < 0:
            invalid()
        return value
    if not isinstance(value, str) or len(value) > 100:
        invalid()
    match = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*([a-z]*)\s*", value, re.I)
    if not match:
        invalid()
    units = {
        "": 1, "ms": 1, "millisecond": 1, "milliseconds": 1,
        "s": 1000, "sec": 1000, "secs": 1000, "second": 1000, "seconds": 1000,
        "m": 60000, "min": 60000, "mins": 60000, "minute": 60000, "minutes": 60000,
        "h": 3600000, "hr": 3600000, "hrs": 3600000, "hour": 3600000, "hours": 3600000,
        "d": 86400000, "day": 86400000, "days": 86400000,
        "w": 604800000, "week": 604800000, "weeks": 604800000,
        "y": 31557600000, "yr": 31557600000, "yrs": 31557600000, "year": 31557600000, "years": 31557600000,
    }
    if match[2].lower() not in units:
        invalid()
    result = float(match[1]) * units[match[2].lower()]
    if not math.isfinite(result):
        invalid()
    return result


def typed_rows(value):
    if not isinstance(value, list) or not value or len(value) > 256:
        invalid()
    seen, result = set(), []
    for row in value:
        if not isinstance(row, dict) or not isinstance(row.get("type"), str):
            invalid()
        types = row["type"].split(";")
        if any(not re.fullmatch(r"[a-z0-9]{1,16}", item) or item in seen for item in types) or len(types) != len(set(types)):
            invalid()
        seen.update(types)
        result.append((copy.deepcopy(row), types))
    return result


def prepare_configuration(defaults, local):
    """Pure merge; validate the complete update before touching the local file."""
    if not isinstance(defaults, dict) or not isinstance(local, dict):
        invalid()
    default_converter = object_field(object_field(defaults, "FileConverter"), "converter")
    local_converter = object_field(object_field(local, "FileConverter"), "converter")
    result = copy.deepcopy(local)
    converter = result.setdefault("FileConverter", {}).setdefault("converter", {})
    maximum = local_converter.get("maxDownloadBytes", default_converter.get("maxDownloadBytes"))
    if not isinstance(maximum, int) or isinstance(maximum, bool) or maximum < 0:
        invalid()
    # In the pinned engine downloadUrlPromise/SizeLimitStream, numeric zero
    # disables the size check. It is an existing unlimited operator setting,
    # not a smaller cap to replace with our default.
    converter["maxDownloadBytes"] = 0 if maximum == 0 else max(maximum, DOCUMENT_BYTES)
    timeout_value = local_converter.get("downloadTimeout", default_converter.get("downloadTimeout"))
    if isinstance(timeout_value, (int, float)) and not isinstance(timeout_value, bool) and timeout_value == 0:
        # Optional timeout-property access on numeric zero omits both timers.
        converter["downloadTimeout"] = timeout_value
    else:
        default_timeouts = object_field(default_converter, "downloadTimeout")
        local_timeouts = object_field(local_converter, "downloadTimeout")
        timeouts = {**default_timeouts, **local_timeouts}
        for name in ("connectionAndInactivity", "wholeCycle"):
            if name not in timeouts:
                invalid()
            value = timeouts[name]
            milliseconds = duration_milliseconds(value)
            if isinstance(value, (int, float)) and value == 0:
                continue
            if name == "connectionAndInactivity" and milliseconds == 0:
                # axios timeout: 0 disables its timeout, including "0ms".
                continue
            # A truthy wholeCycle string such as "0ms" instead schedules an
            # immediate AbortSignal timeout, so it is not an unlimited value.
            if milliseconds < DOWNLOAD_MILLISECONDS:
                timeouts[name] = "5m"
        converter["downloadTimeout"] = timeouts

    default_rows = typed_rows(default_converter.get("inputLimits"))
    rows = typed_rows(local_converter["inputLimits"]) if "inputLimits" in local_converter else []
    # Array overrides must not accidentally drop a default limit for a format
    # absent from a custom local array. Local entries win by exact extension.
    covered = {kind for _row, kinds in rows for kind in kinds}
    for row, kinds in default_rows:
        missing = [kind for kind in kinds if kind not in covered]
        if missing:
            row["type"] = ";".join(missing)
            rows.append((row, missing))
            covered.update(missing)
    if not TARGET_TYPES.issubset(covered):
        invalid()
    limits = []
    for row, kinds in rows:
        selected = [kind for kind in kinds if kind in TARGET_TYPES]
        if not selected:
            limits.append(row)
            continue
        zip_limits = object_field(row, "zip")
        if not isinstance(zip_limits.get("template"), str) or not zip_limits["template"]:
            invalid()
        if size_bytes(zip_limits.get("uncompressed")) >= EXPANDED_XML_BYTES:
            limits.append(row)
            continue
        other = [kind for kind in kinds if kind not in TARGET_TYPES]
        if other:
            unchanged = copy.deepcopy(row)
            unchanged["type"] = ";".join(other)
            limits.append(unchanged)
        row["type"] = ";".join(selected)
        row["zip"]["uncompressed"] = "2048MB"
        limits.append(row)
    converter["inputLimits"] = limits
    if len(limits) > 256:
        invalid()
    return result


def unique_object(pairs):
    result = {}
    for name, value in pairs:
        if name in result:
            invalid()
        result[name] = value
    return result


def directory_fd(directory):
    pathname = os.fspath(directory)
    if not pathname.startswith("/") or any(part in (".", "..") for part in pathname.split("/")):
        invalid()
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        for part in filter(None, pathname.split("/")):
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        return fd
    except BaseException:
        os.close(fd)
        raise


def read_configuration(parent, name):
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC, dir_fd=parent)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_CONFIG_BYTES:
            invalid()
        with os.fdopen(fd, "rb", closefd=False) as stream:
            raw = stream.read(MAX_CONFIG_BYTES + 1)
        if len(raw) > MAX_CONFIG_BYTES:
            invalid()
        parsed = json.loads(raw, object_pairs_hook=unique_object)
        if not isinstance(parsed, dict):
            invalid()
        return parsed, info
    finally:
        os.close(fd)


def same_file(first, second):
    fields = ("st_dev", "st_ino", "st_mode", "st_uid", "st_gid", "st_size", "st_mtime_ns", "st_ctime_ns")
    return all(getattr(first, field) == getattr(second, field) for field in fields)


def prepare(config_root=CONFIG_ROOT):
    parent = fd = None
    temporary = None
    try:
        parent = directory_fd(config_root)
        defaults, _ = read_configuration(parent, "default.json")
        local, previous = read_configuration(parent, "local.json")
        result = prepare_configuration(defaults, local)
        if result == local:
            return False
        payload = (json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
        if len(payload) > MAX_CONFIG_BYTES:
            invalid()
        for _ in range(16):
            temporary = ".pogo-limits-" + secrets.token_hex(12)
            try:
                fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=parent)
                break
            except FileExistsError:
                temporary = None
        if fd is None:
            raise OSError("No private temporary configuration available")
        view = memoryview(payload)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("Incomplete private configuration write")
            view = view[written:]
        os.fchown(fd, previous.st_uid, previous.st_gid)
        os.fchmod(fd, stat.S_IMODE(previous.st_mode))
        os.fsync(fd)
        os.close(fd)
        fd = None
        if not same_file(previous, os.stat("local.json", dir_fd=parent, follow_symlinks=False)):
            raise LimitsError("POGO_OFFICE_LIMITS_CHANGED: Office configuration changed during preparation; the newer configuration was retained.")
        os.replace(temporary, "local.json", src_dir_fd=parent, dst_dir_fd=parent)
        temporary = None
        os.fsync(parent)
        return True
    except (OSError, ValueError, TypeError, OverflowError, RecursionError):
        raise LimitsError("POGO_OFFICE_LIMITS_UNAVAILABLE: Office download-limit preparation failed; check the original configuration and its directory permissions.") from None
    finally:
        if fd is not None:
            os.close(fd)
        if temporary is not None and parent is not None:
            try:
                os.unlink(temporary, dir_fd=parent)
            except FileNotFoundError:
                pass
        if parent is not None:
            os.close(parent)


if __name__ == "__main__":
    try:
        prepare()
    except LimitsError as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)

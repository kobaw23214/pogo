#!/usr/bin/env python3
"""Copy the read-only deployment JWT into a private Office runtime directory.

Only the final pathname is printed. The source, raw copied bytes and normalized
key are never logged or passed as command arguments. Run as container root.
"""
import argparse
import errno
import os
from pathlib import Path
import pwd
import re
import secrets
import stat
import sys

RUNTIME_DIRECTORY = "/run/pogo-office-private"
MAX_RAW_BYTES = 16384


class SecretError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def fail(code, message):
    raise SecretError(code, message)


def parts(path):
    value = os.fspath(path)
    if not value.startswith("/") or "\x00" in value or any(part in (".", "..") for part in value.split("/")):
        fail("POGO_OFFICE_SECRET_PATH_INVALID", "Office secret paths must be absolute without traversal.")
    return [part for part in value.split("/") if part]


def directory_fd(path):
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        for part in parts(path):
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        return fd
    except OSError:
        os.close(fd)
        raise


def read_source(path):
    path_parts = parts(path)
    if not path_parts:
        fail("POGO_OFFICE_SECRET_SOURCE_UNAVAILABLE", "Pogo Office JWT secret file is unavailable.")
    parent_fd = fd = None
    try:
        parent_fd = directory_fd("/" + "/".join(path_parts[:-1]))
        fd = os.open(path_parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC, dir_fd=parent_fd)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            fail("POGO_OFFICE_SECRET_SOURCE_PERMISSIONS", "Office JWT source must be an owner-only regular file.")
        raw = bytearray()
        while len(raw) <= MAX_RAW_BYTES:
            chunk = os.read(fd, min(4096, MAX_RAW_BYTES + 1 - len(raw)))
            if not chunk:
                break
            raw.extend(chunk)
        token = bytes(raw).rstrip(b"\n")
        if len(raw) > MAX_RAW_BYTES or not re.fullmatch(rb"[A-Za-z0-9_-]{32,8192}", token):
            fail("POGO_OFFICE_SECRET_SOURCE_INVALID", "Pogo Office JWT secret must contain 32 to 8192 safe ASCII characters.")
        return bytes(raw), token
    except OSError as exc:
        code = "POGO_OFFICE_SECRET_SOURCE_SYMLINK" if exc.errno in (errno.ELOOP, errno.ENOTDIR) else "POGO_OFFICE_SECRET_SOURCE_UNAVAILABLE"
        fail(code, "Pogo Office JWT secret file is unavailable; check the regular read-only mount and permissions.")
    finally:
        if fd is not None:
            os.close(fd)
        if parent_fd is not None:
            os.close(parent_fd)


def prepare(source, *, runtime_directory=RUNTIME_DIRECTORY):
    raw, token = read_source(source)
    if os.geteuid() != 0:
        fail("POGO_OFFICE_SECRET_ROOT_REQUIRED", "Office private secret preparation must run as container root.")
    try:
        identity = pwd.getpwnam("ds")
    except KeyError:
        fail("POGO_OFFICE_SECRET_USER_UNAVAILABLE", "Office service account ds is unavailable.")
    if identity.pw_uid == 0 or identity.pw_gid == 0:
        fail("POGO_OFFICE_SECRET_USER_INVALID", "Office service account ds must be unprivileged.")
    runtime_parts = parts(runtime_directory)
    if not runtime_parts:
        fail("POGO_OFFICE_SECRET_RUNTIME_UNSAFE", "Office private runtime directory is invalid.")
    parent_fd = fd = temporary_fd = None
    temporary = None
    try:
        parent_fd = directory_fd("/" + "/".join(runtime_parts[:-1]))
        try:
            os.mkdir(runtime_parts[-1], 0o700, dir_fd=parent_fd)
        except FileExistsError:
            pass
        fd = os.open(runtime_parts[-1], os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
        info = os.fstat(fd)
        if info.st_uid != 0 or info.st_mode & 0o022:
            fail("POGO_OFFICE_SECRET_RUNTIME_UNSAFE", "Office private runtime directory must be root-owned and not writable by other users.")
        os.fchown(fd, 0, identity.pw_gid)
        os.fchmod(fd, 0o710)
        try:
            previous = os.stat("jwt", dir_fd=fd, follow_symlinks=False)
        except FileNotFoundError:
            previous = None
        if previous is not None and (not stat.S_ISREG(previous.st_mode) or previous.st_uid != 0):
            fail("POGO_OFFICE_SECRET_RUNTIME_UNSAFE", "Office runtime JWT must be a root-owned regular file.")
        for _ in range(16):
            temporary = ".jwt-" + secrets.token_hex(12)
            try:
                temporary_fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=fd)
                break
            except FileExistsError:
                temporary = None
        if temporary_fd is None:
            fail("POGO_OFFICE_SECRET_RUNTIME_UNAVAILABLE", "Office private JWT could not be prepared.")
        # Use one bounded source read for the byte-exact copy. The caller reads
        # this validated copy for JWT_SECRET, so its normalized key is identical.
        view = memoryview(raw)
        while view:
            written = os.write(temporary_fd, view)
            view = view[written:]
        os.fchown(temporary_fd, 0, identity.pw_gid)
        os.fchmod(temporary_fd, 0o640)
        os.fsync(temporary_fd)
        os.close(temporary_fd)
        temporary_fd = None
        os.replace(temporary, "jwt", src_dir_fd=fd, dst_dir_fd=fd)
        temporary = None
        os.fsync(fd)
        return str(Path(runtime_directory) / "jwt")
    except OSError:
        fail("POGO_OFFICE_SECRET_RUNTIME_UNAVAILABLE", "Office private JWT could not be prepared; check the writable runtime tmpfs.")
    finally:
        if temporary_fd is not None:
            os.close(temporary_fd)
        if temporary is not None and fd is not None:
            try:
                os.unlink(temporary, dir_fd=fd)
            except FileNotFoundError:
                pass
        if fd is not None:
            os.close(fd)
        if parent_fd is not None:
            os.close(parent_fd)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    args = parser.parse_args()
    try:
        print(prepare(args.source))
    except SecretError as exc:
        print(f"{exc.code}: {exc}", file=sys.stderr)
        raise SystemExit(1)

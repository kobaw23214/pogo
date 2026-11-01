"""Pogo v1 sentinel protocol client (stdlib only, SDK version 1).

The script owns its source connection, authentication, observation IDs and
business predicate. This module owns bounded boot/ACK IO, immutable frame IDs
and checkpoints. It never polls, sleeps, reconnects or creates source adapters.

    from pogo_watch import Watcher, event
    with Watcher(mode="stream") as watcher:
        source = connect_to_source(watcher.cursor)  # script-defined
        watcher.checkpoint()  # after source readiness; no model wake
        for observation in source:
            events = [event(key, summary, observation)] if important else []
            watcher.emit(events, state=new_state, cursor=new_cursor)

State/cursor getters return deep copies of the last host-confirmed snapshots.
Pass changed values explicitly to emit/checkpoint. A stream snapshot advances
only after the matching ACK. A poll has no ACK: its getters retain boot values
until a later process is started with host-committed state. Emitting does not
mean an Agent has handled an event. Let IO/SDK errors exit the script so the
existing host can restart it with the committed cursor. Never log secrets.
"""

import hashlib
import io
import json
import math
import sys
import threading

SDK_VERSION = "1"
MAX_FRAME_BYTES = 64 << 10
MAX_STATE_BYTES = 16 << 10
MAX_EVENT_DATA_BYTES = 32 << 10
MAX_CONTROL_BYTES = 64 << 10
MAX_ACK_BYTES = 4096
MAX_EVENTS = 64
_UNSET = object()


class ProtocolError(RuntimeError):
    """The host protocol or a client's lifecycle is invalid; exit the script."""


class ValidationError(ValueError):
    """A proposed JSON value is invalid; nothing from it was written."""


def _validate_json(value, path="value", stack=None):
    if value is None or type(value) in (bool, int):
        return
    if type(value) is float:
        if not math.isfinite(value):
            raise ValidationError(path + " must not contain NaN or Infinity")
        return
    if type(value) is str:
        try:
            value.encode("utf-8")
        except UnicodeError as exc:
            raise ValidationError(path + " must contain valid UTF-8") from exc
        return
    if type(value) not in (dict, list):
        raise ValidationError(path + " must contain only JSON values")
    stack = set() if stack is None else stack
    identity = id(value)
    if identity in stack:
        raise ValidationError(path + " must not contain a cycle")
    stack.add(identity)
    try:
        if type(value) is dict:
            for key, child in value.items():
                if type(key) is not str:
                    raise ValidationError(path + " object keys must be strings")
                _validate_json(key, path + " key", stack)
                _validate_json(child, path, stack)
        else:
            for child in value:
                _validate_json(child, path, stack)
    finally:
        stack.remove(identity)


def _json_bytes(value):
    try:
        _validate_json(value)
        text = json.dumps(value, ensure_ascii=False, sort_keys=True,
                          separators=(",", ":"), allow_nan=False)
        # Match Go encoding/json's escaped byte limits, including nested raw
        # JSON. Otherwise HTML characters could pass our limit and fail theirs.
        text = (text.replace("&", "\\u0026").replace("<", "\\u003c")
                .replace(">", "\\u003e").replace("\u2028", "\\u2028")
                .replace("\u2029", "\\u2029"))
        return text.encode("utf-8")
    except ValidationError:
        raise
    except (TypeError, ValueError, UnicodeError, RecursionError) as exc:
        raise ValidationError("value cannot be encoded as bounded JSON") from exc


def _copy(value):
    return json.loads(_json_bytes(value))


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _invalid_constant(_):
    raise ValueError("non-finite JSON number")


def _read_json_line(stream, limit, label):
    try:
        line = stream.readline(limit + 2)
        if line in ("", b""):
            raise ProtocolError(label + " channel closed")
        raw = line.encode("utf-8") if isinstance(line, str) else line
        if not isinstance(raw, bytes) or len(raw) > limit or not raw.endswith(b"\n"):
            raise ProtocolError(label + " line is missing a newline or exceeds its limit")
        value = json.loads(raw.decode("utf-8"), parse_constant=_invalid_constant,
                           object_pairs_hook=_unique_object)
        _validate_json(value)
        return value
    except ProtocolError:
        raise
    except (OSError, ValueError, TypeError, UnicodeError, RecursionError) as exc:
        # Do not echo source values, control bytes, tokens or credentials.
        raise ProtocolError(label + " is not a valid bounded JSON line") from exc


def _event(value):
    if type(value) is not dict or set(value) - {"key", "summary", "data"}:
        raise ValidationError("event must contain only key, summary and optional data")
    key, summary = value.get("key"), value.get("summary")
    _validate_json(value)
    if type(key) is not str or not key.strip() or len(key.encode("utf-8")) > 256:
        raise ValidationError("event key must be nonblank UTF-8 text of at most 256 bytes")
    if type(summary) is not str or len(summary.encode("utf-8")) > 2048:
        raise ValidationError("event summary must be UTF-8 text of at most 2048 bytes")
    if "data" in value and len(_json_bytes(value["data"])) > MAX_EVENT_DATA_BYTES:
        raise ValidationError("event data exceeds 32 KiB")
    return _copy(value)


def event(key, summary, data=_UNSET):
    """Construct an event. The script chooses a stable source identity/version.

    The host persistently deduplicates event keys. Do not generate a fresh UUID
    on replay. Omitted data stays omitted; explicit None is JSON null.
    """
    value = {"key": key, "summary": summary}
    if data is not _UNSET:
        value["data"] = data
    return _event(value)


class Watcher:
    """One producer bound to the host's current script invocation.

    Nothing is emitted on construction. Connect/initialize the source before a
    stream's first checkpoint. In production leave stdin/stdout unspecified;
    the optional streams support deterministic local tests only.
    """

    def __init__(self, mode=None, *, stdin=None, stdout=None):
        self._input = sys.stdin if stdin is None else stdin
        self._output = sys.stdout if stdout is None else stdout
        self._lock = threading.RLock()
        self._poisoned = False
        self._closed = False
        self._emitted = 0
        boot = _read_json_line(self._input, MAX_CONTROL_BYTES, "boot")
        if (type(boot) is not dict or type(boot.get("v")) is not int
                or boot["v"] != 1 or boot.get("kind") != "boot"
                or boot.get("mode") not in ("poll", "stream")):
            raise ProtocolError("invalid Pogo v1 boot envelope")
        if mode is not None and mode != boot["mode"]:
            raise ProtocolError("requested mode does not match the host boot mode")
        workspace = boot.get("workspace")
        if type(workspace) is not str or not workspace:
            raise ProtocolError("boot workspace must be nonempty text")
        for name in ("state", "cursor"):
            if len(_json_bytes(boot.get(name))) > MAX_STATE_BYTES:
                raise ProtocolError("boot " + name + " exceeds 16 KiB")
        self._mode = boot["mode"]
        self._workspace = workspace
        self._state = _copy(boot.get("state"))
        self._cursor = _copy(boot.get("cursor"))

    @property
    def mode(self):
        return self._mode

    @property
    def workspace(self):
        return self._workspace

    @property
    def state(self):
        with self._lock:
            return _copy(self._state)

    @property
    def cursor(self):
        with self._lock:
            return _copy(self._cursor)

    def __enter__(self):
        with self._lock:
            if self._closed or self._poisoned:
                raise ProtocolError("watcher is closed or has an unconfirmed submission")
            return self

    def __exit__(self, exc_type, exc_value, traceback):
        with self._lock:
            self._closed = True
            if exc_type is None:
                if self._poisoned:
                    raise ProtocolError("watcher has an unconfirmed submission; exit rather than continue")
                if self._emitted == 0:
                    raise ProtocolError("watcher ended without a protocol frame")
            return False

    def checkpoint(self, *, state=_UNSET, cursor=_UNSET):
        """Submit quiet progress. This never asks the host to wake a model."""
        return self.emit([], state=state, cursor=cursor)

    def emit(self, events, *, state=_UNSET, cursor=_UNSET):
        """Submit one immutable proposal and return its content-derived ID.

        A stream waits for the matching durable-acceptance ACK before its local
        confirmed snapshots change. A poll may propose once and must exit zero;
        it cannot know whether the host committed, so getters retain boot state.
        """
        with self._lock:
            if self._closed or self._poisoned:
                raise ProtocolError("watcher is closed or has an unconfirmed submission")
            if self._mode == "poll" and self._emitted:
                self._poisoned = True
                raise ProtocolError("poll may emit exactly one frame")
            if type(events) is not list or len(events) > MAX_EVENTS:
                raise ValidationError("events must be a list of at most 64 events")
            proposed_events = [_event(value) for value in events]
            proposed_state = _copy(self._state if state is _UNSET else state)
            proposed_cursor = _copy(self._cursor if cursor is _UNSET else cursor)
            if len(_json_bytes(proposed_state)) > MAX_STATE_BYTES:
                raise ValidationError("state exceeds 16 KiB")
            if len(_json_bytes(proposed_cursor)) > MAX_STATE_BYTES:
                raise ValidationError("cursor exceeds 16 KiB")
            payload = {"events": proposed_events, "state": proposed_state,
                       "cursor": proposed_cursor}
            frame_id = "pogo-v1-" + hashlib.sha256(_json_bytes(payload)).hexdigest()
            frame = dict(payload, v=1, id=frame_id)
            raw = _json_bytes(frame)
            if len(raw) > MAX_FRAME_BYTES:
                raise ValidationError("frame exceeds 64 KiB")
            # After any write attempt an unknown prefix might have escaped. No
            # recovery or state advancement is safe without the matching ACK.
            self._poisoned = True
            try:
                output = getattr(self._output, "buffer", self._output)
                binary = output is not self._output or isinstance(output, (io.BufferedIOBase, io.RawIOBase))
                line = raw + b"\n" if binary else raw.decode("utf-8") + "\n"
                if output.write(line) != len(line):
                    raise ProtocolError("protocol output was incomplete; submission is unconfirmed")
                output.flush()
                if self._mode == "stream":
                    ack = _read_json_line(self._input, MAX_ACK_BYTES, "ACK")
                    if (type(ack) is not dict or type(ack.get("v")) is not int
                            or ack["v"] != 1 or ack.get("kind") != "accepted"
                            or ack.get("id") != frame_id):
                        raise ProtocolError("ACK does not match the submitted frame")
                    self._state, self._cursor = proposed_state, proposed_cursor
            except ProtocolError:
                raise
            except (OSError, ValueError, TypeError, UnicodeError) as exc:
                raise ProtocolError("protocol output failed; submission is unconfirmed") from exc
            self._poisoned = False
            self._emitted += 1
            return frame_id

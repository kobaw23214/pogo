// Package sentinelsdk embeds the source-agnostic Python protocol client used by
// sentinel scripts. It does not connect to, authenticate, or interpret a source.
package sentinelsdk

import (
	_ "embed"
	"errors"
	"fmt"
)

const (
	FileName = "pogo_watch.py"
	Version  = "2"
)

// ExitDiagnostic describes only fixed SDK failure codes. Script stderr remains
// untrusted and is never used to construct these public messages. The exit code
// is a script declaration, not proof that the SDK or source was used correctly.
func ExitDiagnostic(code int) (string, bool) {
	switch code {
	case 80:
		return "capacity_exceeded: observation or identity history cannot fit its declared limits; saved progress must not be cleared or silently evicted", true
	case 81:
		return "incomplete_observation: the source observation is incomplete or truncated; do not treat it as a successful empty observation", true
	case 82:
		return "source_unavailable: the source could not be read; an unreadable source is not evidence of no changes", true
	case 83:
		return "history_invalid: saved identity history is invalid or incompatible; do not replace it with an empty baseline", true
	default:
		return "", false
	}
}

//go:embed pogo_watch.py
var source []byte

// Source returns a copy so a caller cannot mutate the embedded SDK for later
// registrations. The host seals this exact source with the caller's script.
func Source() []byte { return append([]byte(nil), source...) }

// Starter returns an intentionally unfinished, source-agnostic entry point.
// A generated file cannot pass a protocol test until its source logic exists.
func Starter(mode string) ([]byte, error) {
	if mode != "poll" && mode != "stream" {
		return nil, errors.New("sentinel SDK mode must be poll or stream")
	}
	return []byte(fmt.Sprintf(`#!/usr/bin/env python3
"""Implement the source observation below; Pogo owns the protocol machinery.

Read Watcher.state/cursor to resume the last host-confirmed checkpoint. Choose
stable event keys from source identities/versions; the host durably deduplicates
them, so do not invent UUIDs or keep an unbounded local seen-id list.

A restored source cursor means continuation, not a new baseline. Do not drop
the first post-restart record just because it arrived first; use source IDs,
versions and cursor semantics to distinguish actual replay from new data.

For stream: connect/authenticate the source first, then call watcher.checkpoint()
to show protocol readiness. For each observation call watcher.emit(events,
state=..., cursor=...), or watcher.checkpoint(state=..., cursor=...) for quiet
progress. The SDK waits for Pogo's ACK before updating its state/cursor.
If the source ends or raises an IO error, raise/exit: the existing host retries
with backoff and supplies the last committed cursor. Do not hand-write an ACK
reader, protocol JSON, stdout diagnostics, or a reconnect loop.

For poll: read the source once and make exactly one final emit/checkpoint call.
An exception or nonzero process exit prevents the host accepting that proposal.
Readiness is protocol readiness, not proof that source facts were verified.

If required input is truncated, unsupported or unreadable, call
watcher.fail("incomplete_observation"), watcher.fail("capacity_exceeded") or
watcher.fail("source_unavailable") BEFORE advancing its cursor. These stop
with a fixed diagnostic; do not quiet-checkpoint partial work, turn missing
data into zero, or silently skip input to fit a self-imposed resource limit.
Input still being produced may remain explicitly pending for a later poll;
preserve its retry state without marking it consumed, complete or ignored.
SDK state/cursor/frame/event size ceilings also stop with CapacityError and
exit 80, including a too-large enclosing state around a valid history. These
are not recoverable JSON-format ValidationErrors. Do not catch them and then
commit a quiet fallback. Inside with Watcher, even caught event-construction
capacity failures poison the client; outside it they still exit 80 unhandled.

Optional finite deduplication: history = watcher.history(saved_history,
max_entries=128, max_bytes=12288); use history.seen(source_version) and
history.remember(source_version), then include history.snapshot() in the
proposed state. It never evicts identities or resets incompatible history:
capacity exhaustion fails visibly and leaves committed progress unchanged.
This is not permanent/unbounded storage; use a durable source cursor when the
required identity history cannot fit. Preserve the source's state schema.
"""
from pogo_watch import Watcher, event


def collect(watcher):
    # Replace this guard with the source connection/read and business predicate.
    # Example event: event(stable_source_key, "observed change", source_data).
    raise NotImplementedError("Implement this source and predicate before testing or registering")


if __name__ == "__main__":
    with Watcher(mode=%q) as watcher:
        collect(watcher)
`, mode)), nil
}

// Package core implements the isolated contract core of the Pogo declarative
// pkg prototype (docs/skill-runtime-v1/SPEC.md): strict structural and
// semantic validation of the v1 JSON documents, and immutable ZIP-based
// installation of pkg packages.
//
// Validation layers per document (ValidateDocument):
//
//  1. raw JSON resource checks: 1 MiB size cap, depth 32, duplicate-key
//     rejection, finite numbers only;
//  2. Draft 2020-12 JSON Schema validation against the embedded schemas
//     (core/schemas, byte-identical copies of the docs schemas) with an
//     offline loader — network schema fetching is impossible;
//  3. typed decoding with unknown-field rejection;
//  4. semantic checks: the v1 embedded-schema profile for contract
//     input/output schemas (closed keyword set, root object,
//     additionalProperties: false everywhere, local non-recursive $defs refs
//     only, bounded regexes), fileInputs coherence, manifest reference
//     closure and provider/effects/resource coherence.
//
// Installation (Store.InstallZIP) stages the outer archive, verifies the
// manifest and every referenced digest against actual bytes, extracts ZIP
// payloads with traversal/symlink/quota/bomb protections, persists the
// verified digests, and atomically publishes. Installed versions are
// immutable: identical bytes are a no-op, different bytes under the same
// id+version fail with ErrVersionConflict. tar.zst payloads fail explicitly
// with ErrUnsupportedArchiveFormat; no decompressor is ever shelled out and
// there are no install hooks.
//
// Security boundary notes:
//
//   - Installed content trees are marked read-only where the OS supports it
//     (on Windows this is merely the read-only attribute, which the owning
//     user can clear). Callers MUST enforce true OS-level isolation —
//     separate user, container or sandbox — against same-user writes; this
//     package does not and cannot provide that isolation.
//   - resourceRef, isolationProfileRef and connectionPolicyRef strings are
//     opaque identifiers; nothing here treats them as real authorization.
//     The host supplies an authoritative resource resolver.
//   - This package deliberately does NOT implement an executor, a network
//     client, live identity authorization, API handlers, or mutable data
//     access.
package core

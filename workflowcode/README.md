# Workflow code execution

`Execute(ctx, Request)` executes a reviewed Python `def main(inputs)` or
JavaScript `function main(inputs)` / `async function main(inputs)`. The sole
argument is an object, and the return value must be a JSON object. A code node
does not call a model, inherit Agent tools, load a Skill, or install dependencies.

`Capabilities()` probes fixed host-owned functions and caches per-language
availability for the lifetime of the service. It never executes user source.

Limits are 64 KiB source, 256 KiB input/output, 16 KiB per stdout/stderr log,
and a 1–60 second wall timeout (10 seconds by default). Logs retain their first
16 KiB and report truncation. Cancellation kills the process group. Runtime
identity, source hash, exit status, logs and duration are returned separately
from the JSON result. Output schema validation belongs to the workflow layer.

Python supports selected standard modules (JSON, math, regular expressions,
dates, decimal/statistics, collections, iterators, text/CSV, hashes/base64).
JavaScript supports language built-ins and logging. Neither interface exposes
user-configurable imports or runtime package installation.

The OS boundary is mandatory. Linux starts from an empty bubblewrap root and
mounts only system libraries, the selected interpreter and its standard
library read-only. Its private PID/network namespaces and seccomp policy deny
new processes, network sockets and process inspection; V8 threads remain
available. macOS uses a deny-default Seatbelt profile, read-only runtime files,
no file writes, no network/Mach services and no process forks. The only
permitted process execution is the initial interpreter. Environment variables
are constructed from a fixed list, with no inherited credentials or host paths.
Unsupported hosts or missing isolation refuse execution; there is no raw-command
fallback. Language import filters and Node's VM context are conveniences,
**not** the security boundary.

The 256 MiB RSS budget is sampled supervision. Node additionally limits its V8
heap; the deployment's container limit remains the hard aggregate memory/CPU
ceiling. It is not a per-node cgroup guarantee.

Tests execute actual native Python/JavaScript against temporary data when the
host isolation is available. They include deliberate language-level escapes to
verify the OS still blocks host reads/writes and new interpreter subprocesses.
The Linux syscall policy is also evaluated on both supported architectures.
Namespace enforcement tests on non-Linux hosts do not establish Linux runtime
acceptance.

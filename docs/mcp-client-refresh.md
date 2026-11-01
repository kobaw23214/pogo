# MCP client catalog refresh

Web sessions refresh external MCP tools before each new run, without recreating
the conversation. `Session.Start` reserves the run first; its goroutine checks
the owning account's pool immediately before `Agent.RunMessage`. The existing
`poolFor(owner)` and `mcp_tokens` rules still select the connection and credentials.
Steering a message into an already running loop does not switch its catalog.
Input arriving while the run is preparing its catalog/images is queued for that
same run, then handed to the agent before its first model call.

## Discovery and failure handling

- `notifications/tools/list_changed` invalidates the cached list. The existing
  500 ms debounce coalesces bursts; a new run can consume the invalidation before
  that timer fires.
- `Pool.Refresh` also polls before a run when the previous attempt is at least
  **5 seconds** old. This covers silent servers and HTTP transports without a
  persistent notification stream. Unsuccessful attempts consume the same TTL.
- The pre-run check has a **3 second** context budget across the entire pool.
  Servers are checked concurrently. Each connection serializes initial,
  notification and pre-run pulls, with cancellable waiting and a TTL recheck.
- All `tools/list` pages must succeed before the catalog is replaced. Duplicate
  names, repeated cursors and malformed list entries fail the pull. Individual
  tool definitions rejected by the existing wrapper validation are still skipped
  with a warning.
- A list failure retains the last valid catalog. `/api/mcp/status` exposes
  `last_error`, `catalog_stale` and `catalog_updated_at`. A live session with a
  list error can remain `connected` with a stale catalog. A disconnected server
  retains the last catalog for diagnostics, but its tools are withheld from new
  run projections. A successful **empty** list removes its tools and clears the
  stale/error flags.
- The existing keeper handles late startup and reconnects with its existing
  backoff/startup timeout. Every successful connection must fetch a complete
  catalog before becoming available.

## Run snapshots and approvals

The pool retains every accepted tool from the selected connected servers; there
is no pool-wide tool-count cutoff. A server exposing more than 20 tools emits an
observational warning, but its tail tools remain executable and retain exactly
that server's configured trust. Server selection, include/exclude filters,
definition validation, deterministic wire-name collision handling and
disconnected-server withholding still apply. Model-facing exposure is controlled
separately by metadata/discovery settings rather than deleting registry entries; see
[external MCP contracts](mcp-client-metadata.md).

`Registry.WithMCP` preserves the instances and ordering of built-ins and skill
tools, including their path guards, task state and workspace bindings. It keeps
the existing preference for skill tools on name collisions. MCP wrappers get
separate spill-directory configuration for each registry. `Agent.ReplaceMCP`
rebuilds both the model's input declarations and the execution registry together,
updates schema overhead, and refuses replacement during an active model loop.
History, attachments, citations, usage and UI state are retained.

Each wrapper hashes the complete incoming MCP definition, including the full
description, input/output schemas, annotations and other SDK-supported metadata.
Input schemas reach the model through the existing function-declaration format;
output schemas are retained on the MCP wrapper and included in version checks.
Annotations affect scheduling as before and never grant approval themselves.

An unchanged tool on the same connection retains its binding. A redefinition,
removal, or connection replacement invalidates old bindings. A call already sent
continues on its captured connection; a later attempt through an invalid binding
returns an explicit error and is not dispatched. No call is redirected to a new
connection or automatically replayed. The protocol has no atomic per-call
definition-version precondition: unannounced server changes are detected by the
next successful poll or by the server's call error.

Operator-configured `trust: true` is re-evaluated from the current projection.
Remembered tool/command approvals are revoked when their definition or connection
changes, and removed names lose trust. MCP command approvals are scoped to the
specific tool; shell command approvals do not authorize MCP calls. A new catalog
does not change the explicit session `auto` mode.

## Verification and deployment

The tests use local SDK MCP servers and controlled model stubs. They cover
addition, definition changes, deletion, running-loop snapshots, stale-call
rejection, late startup/reconnection, failure versus empty results, pagination,
TTL/concurrent refreshes/deadlines, account credentials, and approval invalidation
for output-schema/annotation-only changes.

```sh
go test ./... -count=1 -timeout 180s
go test -race ./mcpclient ./tools ./agent ./web -run 'MCP|Pool|DedupeMCP|Steer' -count=1 -timeout 120s
CGO_ENABLED=0 go build -trimpath -o /tmp/pogo .
```

Linux matches the deployment environment. When testing a Windows checkout in a
temporary Linux source copy, normalize `skills/builtin` line endings as the
existing Dockerfile does. Some unrelated full-suite tests assume Unix executable
bits/shell behavior and do not pass directly on Windows.

The fix changes the Go binary embedded in the pogo image, so container deployments
need a rebuilt image and a one-time container replacement to install it. No UI
source changes, MCP protocol upgrade or data migration is required. Subsequent
MCP catalog changes require neither a pogo restart nor a new conversation.

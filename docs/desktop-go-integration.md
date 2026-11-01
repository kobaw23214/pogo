# Isolated desktop browser integration

This harness starts the actual Go Manager, Server, Studio routes, agent loop,
browser tool, task gate, native HTTP bridge and embedded UI. Model responses are
deterministic local scripts. It does not contact a model provider, reuse user
sessions, start Chromium, or change Docker containers.

The desktop application is supplied separately. It must expose the existing
worker HTTP contract through a desktop-owned session factory with browser launch
disabled. A passing Go test alone does not establish that the displayed native
WebContents and the worker's target are the same page; the desktop/UI acceptance
must independently establish that identity.

The authenticated `PUT /sessions/{privateWorkerID}` carries optional trusted Go
metadata: `client_session_id` is the owning public Go session ID; `profile_key`
is a 64-character lowercase SHA-256 digest of a versioned encoding containing
the absolute, cleaned transcript path and full account scope (or null for a
single-user store). The path and account scope are not transmitted. Neither
metadata field is returned through public browser state. The worker execution
ID remains random on construction. Reopening the same store under the same
account retains the profile key; changing stores, sessions or accounts changes
it. Moving a store requires an explicit profile migration if retention is wanted.
Unbound internal test constructors still send the original empty object.

Desktop workers advertise `display_mode: "desktop"` in state; Go forwards only
that recognized value. Missing or unknown values preserve legacy compatibility.
On a desktop worker, retired human-view `/frame`, `/pick` and forwarded pointer/key/text/
scroll routes return HTTP 410. Go preserves that status without recreating the
context. A web client must direct the user to the desktop application instead of
falling back to JPEG rendering. Agent `execute` screenshots remain independent;
navigation and dialog controls retain their supported APIs.

## Preparation and startup

The integration test is skipped by default. Compile-check it without starting a
server or browser:

```sh
cd /Users/wangy/Documents/DiscoverAgent/pogo
go test ./web -run '^TestDesktopBrowserGoIntegration$' -count=1 -v
node --check browser-runtime/lab/desktop-integration.mjs
```

Build the final UI once all frontend changes have landed. The launcher compiles
the Go test executable again, embedding that `web/ui/dist`, and records the index
hash. Do not build the UI concurrently with the launcher.

Create a fresh private config containing paths, never print token contents:

```json
{
  "workerURL": "http://127.0.0.1:19783",
  "workerTokenFile": "/absolute/owned/private/worker-token",
  "listen": "127.0.0.1:19782",
  "hold": true,
  "workerWaitSeconds": 120
}
```

The worker token must be a fresh random value in a mode-0600 file, identical to
the desktop host's `POGO_DESKTOP_WORKER_TOKEN`. Use a new output directory for
each run:

```sh
node browser-runtime/lab/desktop-integration.mjs PRIVATE_CONFIG NEW_OUTPUT
```

The Go server starts first and writes `go-ready.json` with the Studio URL, owned
Agent/session IDs, synthetic fixture URL and a path to `private/web-token`.
Tokens are absent from stdout and public reports. Configure the desktop with:

- `POGO_DESKTOP_STUDIO_URL`: `studioURL` from Go readiness, with authentication
  supplied privately to the Studio surface. For the existing query-token login,
  append the private web token only inside the launcher/environment, never in a
  public report or command log.
- `POGO_DESKTOP_WORKER_PORT=19783` and the matching worker token.
- `POGO_DESKTOP_DATA_DIR`: a fresh directory owned by this test run.

The Go harness waits for authenticated `/healthz` before executing tools. The
desktop may therefore start after `go-ready.json` exists. It must not substitute
a newly launched headless browser for a desktop-owned page.

## Checks and evidence

The controlled fixture refuses iframe embedding and records mutations. Checks
exercise real public Go message/SSE/control endpoints and verify:

1. Embedded Studio and authenticated API access.
2. `navigate`, `snapshot`, `type`, `click`, `wait` and direct CDP evaluation through
   the real Go agent loop, including tool results and citation source stamps.
3. Exactly one synthetic form save with Unicode text.
4. Separate browser contexts for two owned Go sessions.
5. Cancellation after the fixture has observed a dispatched slow navigation,
   followed by a successful fresh navigation without replaying the prior save.
6. Public human takeover, agent-input refusal during the lease, and return to
   agent control.
7. Explicit desktop display capability, HTTP 410 for legacy human JPEG and text
   forwarding, unchanged context after rejected frames, and unchanged page value
   after rejected text. This is a retirement check, not native human input
   acceptance; the latter belongs to actual desktop mouse/keyboard testing.

`summary.json` and `go-acceptance-ready.json` record the result. With `hold:true`,
the real Studio remains available for native input, takeover, source-preview and
same-WebContents checks by the desktop harness. Subsequent chat responses remain
scripted; this is explicitly not real-model acceptance.

To finish normally, write `finish.json` in the output directory:

```json
{"action":"finish","run_nonce":"the exact nonce from go-ready.json"}
```

Send this while the desktop worker is still available so the Go Manager can
delete only its owned worker contexts. Close the desktop using its own lifecycle
after the Go launcher exits. The launcher records final status in `launcher.json`,
confirms temporary Go state removal, and compares the identities, image IDs,
start times and restart counts of `pogo-base` and `pogo-pogo-browser-1` before and
after. It never stops either container. Failure or missing Docker evidence is
reported rather than treated as proof of unchanged production.

## Isolation and migration boundaries

Go workspace, sessions, indexes, checkpoints, spill, worktrees, memory, schedules
and preference/MCP paths are explicitly isolated. The manager has no MCP client,
schedule timer or follow-up model generation. The source UI is reused; production
configurations and credentials are not loaded.

Before a later production migration, separately validate desktop ownership,
device/session binding, disconnect/reconnect, cancellation ACKs, stale
context/generation rejection, native keyboard/IME behavior, human lease rules,
DevTools coexistence, session history preservation and rollback. A planned
single desktop route must fail explicitly when its device is offline instead of
silently launching the retired hosted browser. Keep the accepted Docker images
and configuration backup available until migration is separately accepted; this
harness does not switch production or authorize stopping it.

# Managed Web service host integration

This is an opt-in, temporary application runtime. Static local HTML continues
to use HTML Preview. Installing a Skill or selecting a tool name does not
configure the runtime, gateway, network or access domain.

The executable profiles are `python-web` and, when the runner has a configured
local Node image, `node-web`. Both use their language's standard library without
dependency installation or a build step. Non-empty Python requirements, Node
package dependencies and build commands are rejected. Installing npm packages
or building a Vite application is not a supported deployment path. Application
processes accept inbound HTTP but cannot initiate TCP, UDP or Unix connections,
including loopback. Static projects should keep using HTML Preview.

The Pogo process never receives Docker control. A separate authenticated runner
owns runtime containers; a separate gateway owns the public entry and its
private management socket. Website containers only receive a project snapshot
and service data, not account, model, MCP or gateway credentials.

## Host configuration

The main workbench leaves the feature disabled unless
`POGO_WEB_SERVICE_ENABLED=1`. To enable it, configure all of:

| Variable | Meaning |
|---|---|
| `POGO_WEB_SERVICE_ROOT` | Absolute private service registry directory, outside workspace/read roots |
| `POGO_WEB_SERVICE_INSTALLATION` | Stable installation identity matching the runner |
| `POGO_WEB_SERVICE_RUNNER_URL` | Private runner control endpoint, never an application upstream |
| `POGO_WEB_SERVICE_RUNNER_TOKEN` | Runner authentication, read from host environment and not logged |
| `POGO_WEB_SERVICE_GATEWAY_SOCKET` | Absolute gateway Unix socket in a dedicated private directory |

The runner and gateway have their own administration settings. They must exist
and pass preflight before a conversation receives the callable tool. A runner
or gateway outage leaves the normal workbench/static preview available and
does not grant an unmanaged shell fallback.

The runner requires an installed, pinned Python runtime image. Node support is
optional and requires its own installed, pinned image; the advertised profiles
reflect the runner's available images. Its private state directory and service
data must survive runner replacement. The gateway likewise requires durable
access state: `pogo-web-gateway -state-path` selects a private file and defaults
to `gateway-state.json` beside its admin socket. Keep that file on a persistent
volume, with its directory owned by the gateway user and mode 0700 and the file
mode 0600. It contains the signing key and URL revocation versions, not project
data. Losing or replacing it changes existing website URLs; corrupt state is a
startup error. See the [gateway configuration](../cmd/pogo-web-gateway/README.md).

`POGO_WEB_SERVICE_RUNNER_TOKEN` and `POGO_WEB_RUNNER_TOKEN` are reserved host
control secrets. They are removed from child Agent and command environments,
including when explicitly named in sandbox environment extras or overrides.
Parent Pogo/runner RPC processes keep their credentials; ordinary provider-key
inheritance and administrator allowances otherwise retain their existing rules.

Control directories must not overlap workspaces, Skill read roots or other
file-tool read roots. They are hidden by actual sandbox mounts, including Unix
sockets; readonly mounts are insufficient. Parent-issued subagents inherit the
same hidden paths but do not receive a managed-service backend. If this OS
cannot apply the required masking, ordinary commands must fail closed rather
than run with access to the control store.

## Agent and owner interfaces

The optional native `web_service` tool provides start, list, status, logs, open,
stop, restart, renew and rename. Read-only actions are list/status/logs; malformed input
does not claim the read-only exemption. The host binds owner, durable session
ID and workspace. Tool input cannot supply those values, images, ports, mount
paths, environment credentials or proxy targets.

Agent selection updates use the existing native-tool refresh boundary. New and
existing conversations see the same effective grants; active calls retain the
already accepted execution snapshot until the normal safe boundary. Deleting a
Skill is not a stop operation. Revoking the Agent tool does not remove the
authenticated human owner's ability to inspect or stop their own service.

Owner APIs live under `/api/sessions/{sid}/web-services`:

- GET/POST the collection to list/start.
- GET `/{serviceID}` or `/{serviceID}/logs` for status/output.
- POST `/{serviceID}/previews`, `/stop`, `/restart`, `/renew`, `/rename` for those actions.
- DELETE `/{serviceID}/previews/{grantID}` to revoke the current lease URL.
- DELETE `/{serviceID}` for durable owner-requested runtime and data cleanup.

The service deletion API is a human management action, not an Agent tool action.
It persists the deletion intent and fences older operations before attempting
cleanup. Service data is removed only after gateway revocation and runtime stop
are confirmed. A failed attempt remains retryable; cancelling the HTTP request
does not undo accepted cleanup.

Administrator-only GET/PUT `/api/web-services/settings` reads or updates the
managed website policy. Settings include `default_lease_seconds`,
`max_per_session`, `max_per_user`, `max_total`, `internal_port`, `cpus` and
`memory_mib`. Defaults are a 7200-second lease, limits of 2 per session, 4 per
user and 16 total, internal port 8080, 1 CPU and 512 MiB. GET also reports the
available profiles. Port, CPU or memory changes enqueue durable restarts for
eligible active services; the update response reports their effects and restart
progress. Renaming a service changes its display metadata without a restart.

All APIs enforce existing authenticated session ownership. Internal workflow,
sentinel, background-task and discussion-only sessions do not gain this
detached service runtime. Native tools and HTTP APIs share one service manager;
closing a model run or evicting a conversation does not stop the service.
Deleting a persistent conversation invokes durable runtime/data cleanup first.
Deleting a local account first persists a runtime owner fence and revokes its
managed services, then removes the account credentials. If runtime cleanup is
pending, the API reports that credentials were retained for an explicit retry;
the old bound backend cannot start or reopen services. Existing transcript and
source-workspace retention remains unchanged.

Service-state SSE messages contain only public record data. Grant URLs are
returned by explicit open and are not broadcast in state events. The existing
generic tool output keeps its receipts. A Websites button beside the existing
conversation composer controls opens live service cards with state, generation,
deadline, bounded logs, open, renew, restart and stop actions. Todo and the
surrounding activity UI keep their existing behavior.

Each opened website gets its own dock preview. Its bearer URL belongs to the
service lease and stays stable across repeated opens, renewals, metadata renames
and runtime restarts. Closing a dock tab or unloading the workbench clears the
local view without revoking that shared address or stopping the service.
Reloads retain only service references, not bearer URLs, and require an explicit
open. Explicit URL revocation rotates the lease address for all viewers and
terminates its active requests; opening again returns the new address. It does
not stop the service. The gateway enforces the service lease deadline and stops
routing while the service is unavailable. Expired services can be started with
a new lease only after their old runtime is in a confirmed terminal state and
the original project specification is available; the configured default applies
when no lease duration is supplied.

Successful open receipts in Studio conversations include an owner-authenticated
reference: `/a/{agentId}?c={sessionId}&web_service={serviceId}`. It selects the
service card without issuing a grant and does not give another account access.
The built-in `web-development` Skill explains static versus managed delivery,
profile limits, readiness, logs, updates and lease/URL behavior; loading
it does not grant the `web_service` or browser tool.

Public answer sharing removes managed preview URLs from the question, answer,
title and tool-history snapshot while retaining ordinary owner-authenticated
Pogo links. Escaped URLs in tool JSON are also covered; large numeric IDs are
not parsed through floating-point conversion during redaction. Private owner
conversation history still retains its original open receipt. A later explicit
URL revocation or expired lease can make that receipt unusable.

## Preview entry modes

Domain mode remains available for a configured preview domain. For a private
network without DNS or certificates, the gateway can explicitly select
`preview-mode=path`. Pogo and its generated applications then share the entry
`http://LAN-IP:19770`. An explicit open returns a service-lease URL shaped as
`http://LAN-IP:19770/_pogo/web/<48hex>/`; the random grant is a bearer capability,
not a service ID or owner API credential. Only the exact prefix of an active
grant is routed to its registered application. Unknown or expired grants do
not fall through to the workbench.

The gateway strips `/_pogo/web/<48hex>` and forwards the remaining path and query
to the application. Application server routes therefore stay `/`,
`/api/counter`, `/assets/app.js` and `/ws`. Browser code must keep requests under
the returned grant root:

| Browser use | Compatible URL |
|---|---|
| Script, stylesheet, image | `./assets/app.js`, `./assets/style.css`, `./assets/logo.png` |
| Application API | `fetch('./api/counter', {method: 'POST'})` |
| WebSocket from the grant root | `new URL('./ws', window.location.href)`, then set its protocol to `ws:` for HTTP or `wss:` for HTTPS |

WebSocket endpoints must preserve the current host and pathname's grant prefix.
For application subroutes, derive `/_pogo/web/<48hex>/` from the current pathname
before appending `ws`. A root-absolute URL such as `/api/counter`, `/assets/app.js`
or `/ws` loses that prefix and is blocked by the path-scoped CSP. Path mode does
not transparently support arbitrary applications built for a site root, perform
response-body rewriting, or globally patch browser request APIs. Do not use
`<base href="/">` or add dependency/build profiles to bypass this contract.

Path previews keep the existing iframe sandbox `allow-scripts allow-forms`
without `allow-same-origin` and use `no-referrer`. A same-origin entry does not
grant access to the parent page, its storage or its authentication. The UI accepts
only an exact same-origin `/_pogo/web/<48hex>/` preview root; grant URLs stay in
the active pane rather than persisted dock state or public shares. Owner service
links continue to select the service card without automatically opening a grant.
Closing a preview clears its local view; explicit revocation rotates the shared
lease URL without stopping its service. Static
websites continue to use HTML Preview.

## Deployment boundary

The target deployment publishes only gateway TCP 19770. Managed services listen
on the policy's internal port (8080 by default). Select and verify either the explicit private HTTP path mode
or a configured domain mode; domain deployments additionally require their
DNS/certificates. Gateway routing and existing browser/Office compatibility must
be verified together before moving the live port. Source configuration and passing package checks are not proof
that the real deployment has been migrated or that a page was browser-tested.

Current integration evidence covers a standard-library Python/SQLite app on
Linux arm64 Docker Desktop and external Chrome through an isolated loopback
gateway. It does not cover a real model autonomously building the app, a public
DNS/TLS deployment, the native browser worker, or Office/media migration. The
feature remains disabled by default. The runner monitors soft runtime, service
data and owner storage budgets. Confirmed excess stops the affected service;
retryable measurement failures are reported as warnings without stopping an
otherwise healthy service, while incomplete scans block new admission. These
checks are not filesystem quotas. Node's standard-library profile is available
only with its configured image; dependency installation and build profiles
remain unsupported. Production deployment and browser acceptance require their
own checks.
Use only one Manager for each installation/registry; concurrent replicas are
not supported by this implementation.

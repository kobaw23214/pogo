# Pogo native browser worker

An authenticated Playwright 1.63.0 service for original-URL browsing. Each Pogo
session gets one private Wayland compositor and one real Chromium profile. Sheets
are separate native windows with private outputs in that profile. Sites keep their original
origin, cookies, CSP, redirects and service workers; no page reverse proxy or
cross-origin iframe embedding is involved.

## Local development

The production worker runs on Linux and requires the bundled Rust/Python Wayland
host and Go/Pion service. Build the Dockerfile for the target architecture; a
Node-only macOS launch is not the production runtime. Node.js 22 or newer is
required for source and contract tests. See `wayland/stream/README.md` for the
private media service and `lab/legacy-tabcapture/README.md` for retired sources.

The default listener is `127.0.0.1:7788`. Configure Pogo with
`POGO_BROWSER_URL=http://127.0.0.1:7788` and the same `POGO_BROWSER_TOKEN`.
The token is mandatory, checked on every endpoint, and never sent into the
Chromium process environment. The worker does not read `.env` files.

`POGO_BROWSER_MAX_SESSIONS` defaults to 32 (range 1–256).
`POGO_BROWSER_MAX_PAGES` defaults to 0 — no per-session tab cap; the
container's memory and pids budgets are the practical ceiling. Set a positive
integer (up to 1024) to reinstate a hard cap, after which explicit new-tab
requests fail with `browser tab capacity reached`.
`POGO_BROWSER_IDLE_MS` defaults to 1800000 (range 1000–86400000).
Page actions and navigation default to 60000 ms. Conditional `wait` also
defaults to 60 seconds and returns as soon as its condition holds; `seconds`
can select a shorter limit (0 uses the default, maximum 60). The Pogo operation
deadline is 75 seconds so the worker can return its result before transport
cancellation. Connection, cancellation and live-frame deadlines stay separate.
`POGO_BROWSER_SETTLE_MS` defaults to 2000 (range 0–5000, and never more than half
the action timeout); `0` disables the post-action settle described below.
Both Compose overlays forward this setting to the worker. Set it in the
environment used by Compose (the private `.browser.env` for `w_deploy`) and
recreate the worker to apply it; changing a file does not alter a running process.
Idle contexts are closed; UI activity keeps an open context alive.
If its owned Chromium process disconnects unexpectedly, the command-line worker
exits with status 1 so the container restart policy can replace it. That resets
all in-memory browsing sessions; normal worker shutdown does not report a crash.

### Optional LAN gateway connection route

When Docker Desktop cannot connect from the worker to the host's LAN address,
these two administrator-owned worker environment settings route only the exact
canonical gateway host and port through Docker Desktop's host connector:

```text
POGO_BROWSER_GATEWAY_ORIGIN=http://172.20.10.5:19770
POGO_BROWSER_GATEWAY_CONNECT_HOST=host.docker.internal
```

Both settings must be present together; leave both empty to retain normal
browser routing. This opt-in currently accepts only a canonical RFC1918 IPv4
`http://IP:19770` origin, with no trailing slash, path, credentials, query or
fragment. The connection host is restricted to `host.docker.internal`. Other
ports, HTTPS, DNS origins, loopback and IPv6 are not supported by this route.
The canonical URL, HTTP Host, port, resource paths and WebSocket URLs stay
unchanged; only Chromium's connection address is mapped. Grant checks and CSP
continue to apply at the gateway. The worker does not join application networks,
and no additional port is published.

The settings are read at worker startup and cannot be supplied by a browser
session or action. Recreate the worker to apply them; existing Chromium profiles
do not acquire a newly configured route. If any case-insensitive
`HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` variable is nonempty, gateway routing
is rejected at startup. It does not select global direct connections or replace
an existing proxy policy. `NO_PROXY` alone does not enable a proxy. Verify the
host connector reaches the gateway before opting in, then verify an actual
browser page, its relative HTTP API calls and WebSocket connection.

## Optional Docker deployment

The Dockerfile pins the official Playwright base image and compiles the native
Wayland host and Go/Pion WebRTC service in separate build stages. Linux ARM64 is
the locally verified platform; other architectures require their own acceptance. It runs as `pwuser`, with Chromium's
sandbox enabled. The supplied `seccomp.json` permits the namespace setup needed
by Chromium; the overlay drops capabilities, makes the root filesystem read-only,
provides private `/tmp` and shared memory. The worker HTTP listener stays private;
only the configured WebRTC UDP media range is published.

### WebRTC network configuration

Both Compose overlays use the same external/internal UDP range, defaulting to
`19780-19811`. `POGO_BROWSER_WEBRTC_UDP_PORT_RANGE` changes both mappings and the
Pion UDP listener; recreate the worker to apply changes. The optional
`POGO_BROWSER_WEBRTC_BIND` controls the host interface, default `0.0.0.0`.
The browser's internal HTTP API on port 7788 is never published.

Set `POGO_BROWSER_WEBRTC_ADVERTISE_IP` to the IPv4 address that viewers can
reach. The current native media service supports UDP4; IPv6 is rejected. Its default is empty; a Docker-private IP or mDNS name generally cannot
be reached by an external viewer. Use `127.0.0.1` only for a viewer on the Docker
host, the server's LAN address for local networks, or a public address with
matching UDP port forwarding for Internet access. An HTTP reverse proxy alone
does not forward WebRTC media. The advertised value is deployment-owned, never
accepted from a viewer request or inferred from a request Host header.
The default host mapping is IPv4. The packaged deploy helper detects the server
address; an explicit value can still select another reachable interface.

`POGO_BROWSER_WEBRTC_ICE_SERVERS` is an optional JSON array, default `[]`, using
the standard `urls`, `username` and `credential` fields. Configure a TURN service
for viewers behind restrictive firewalls/NAT; STUN discovers candidates but does
not relay media. No TURN container is installed by this overlay. Protect public
application/signalling routes with HTTPS/WSS. Receiving remote video does not
request a user's camera and is distinct from the secure-context requirement of
`getUserMedia`; LAN HTTP can be used for controlled testing.

The production path is headed Chromium on a private software Wayland output →
Pixman buffer → native VP8 encoder → Go/Pion WebRTC → client video. There is no
VNC, capture extension, loopback media page, page proxy or screenshot-stream
fallback. Input and signalling use the authenticated application WebSocket;
video flows directly from the worker's published UDP port. One UDP mux listener
is shared by the viewing peers. Resource limits remain 2 CPU / 3 GiB / 1024 pids;
these are limits, not a promise of 32 concurrent high-resolution streams. New
sessions are refused near resource exhaustion rather than breaking existing ones.

First rebuild the **Pogo application image from this source revision**, including
the new backend and Vue UI. From the repository root, for example:

```sh
docker build --platform linux/arm64 -t pogo:native-browser -f pogo/Dockerfile .
```

Use `linux/amd64` on an x86 host. For a remote/offline deployment, export and
import this application image and the matching worker image. Set the main
`pogo` service's `image` to that new application tag in the deployment configuration
before starting it. In particular, `w_deploy/compose.yml` has no application
`build` section: the command below builds only the browser worker and **does not
upgrade an existing `pogo:describe-args-*` application image**.

Then, from `w_deploy/` or `pogo/`, explicitly opt into the new worker:

```sh
export POGO_BROWSER_TOKEN="$(openssl rand -hex 32)"
docker compose -f compose.yml -f compose.browser.yml up -d --build
```

Keep that token available for subsequent Compose invocations. The overlay uses
`POGO_PLATFORM` for the worker, defaulting to `linux/amd64`; choose the native
architecture and an architecture-matched Pogo image. No existing deployment is
changed merely by adding the overlay file. `w_deploy/deploy.sh` persists this
choice when the private `w_deploy/.browser.env` file exists: it loads `.env`, then
`.browser.env`, and both Compose files for every lifecycle command. Store the
worker token and image tag there with mode 600; see
[deployment instructions](../../w_deploy/README.md). For an offline production bundle, build/export the worker image
and ship this directory (including its seccomp profile) or adjust the overlay's
paths to the delivered files.

The ARM64 overlay security settings were exercised in a temporary Docker
container: UID 1001, sandbox enabled, all capabilities dropped, read-only root,
private `/tmp`, and 1 GiB shared memory. Original-URL DOM and JPEG/version requests
passed. AMD64 is an available upstream image variant; this local check does not
substitute for startup acceptance on the target host.

Sandbox startup failure is a deployment error: fix host user-namespace/seccomp
support. Do not disable the sandbox to make a production readiness check pass.
The worker supports an explicit `POGO_BROWSER_CHROMIUM_SANDBOX=0` development
override, but the overlays do not set it.

## HTTP contract

All requests require `Authorization: Bearer <POGO_BROWSER_TOKEN>`.

| Endpoint | Behavior |
| --- | --- |
| `PUT /sessions/:id` | Idempotent creation; 1280×800 viewport, native scale factor 1.25. |
| `GET /sessions/:id/state` | Backend, viewed-tab control/control_epoch, context generation, version, URL/title, tabs with owner metadata, Agent active_tab, viewport, pick_pending, pending dialog, debugger_paused and devtools_blocked. Never returns lease tokens. |
| `GET /sessions/:id/frame` | JPEG UI frame with `X-Browser-Version`; transient page changes can return 409. |
| `GET /sessions/:id/live?tab_id=...&context_ref=...` | Authenticated WebSocket protocol 2; WebRTC signalling and bounded human input on that same Sheet. |
| `POST /sessions/:id/execute` | `{op_id, action, ...}` → BrowserReply. Repeated op_id never executes again. |
| `POST /sessions/:id/uploads` | Streams one authorized file as `application/octet-stream`; encoded basename in `X-Upload-Filename`, optional `X-Upload-Mime-Type`. Returns `{ok:true,file:{id,name,size,mime_type}}`; never a filesystem path. |
| `DELETE /sessions/:id/uploads/:uuid` | Removes unused staging. Receiving/in-use/selected files return 409; selected bytes remain until session cleanup for deferred website reads. |
| `POST /sessions/:id/cancel` | Fences queued work, interrupts active work by closing pages, ACKs only after interruption. Idle pages and human-owned dialogs are preserved. |
| `POST /sessions/:id/control` | Requires `context_ref` and `tab_id`. `{mode:'human',...}` returns that tab's random lease_token; an existing human lease returns 409. `force:true` explicitly replaces only that tab's lease. `{mode:'agent',lease_token,...}` returns only that tab. |
| `POST /sessions/:id/input` | Human-only input, requiring the current lease_token. |
| `POST /sessions/:id/pick/preview` | Read-only `{x,y,version,tab_id,context_ref}` lightweight geometry preview; returns `element:{tag,role,rect,fallback?}` and a short-lived `preview_token`, or `element:null`. No text, names, values or formal refs. |
| `POST /sessions/:id/pick` | Strict read-only `{x,y,version,tab_id,context_ref?,preview_token?}` observation; returns bounded visible element facts and a viewport rectangle, or `element:null`. A supplied token must match the exact previewed node and coordinates. No human lease or input events. |
| `POST /sessions/:id/auth` | Retired manual API grant route; returns 410. Login state remains in the browser context. |
| `DELETE /sessions/:id` | Idempotent context cleanup. |

### Web live view

The Web viewer upgrades through Pogo's authenticated session route; the worker
token remains on the Go server. Protocol 2 uses the native Wayland/Pion media
pipeline and an HTML video element. `ready.capabilities.runtime` is `wayland`;
there is no automatic fallback. Agent actions, screenshots, network inspection,
DevTools and live input operate on that same Chromium Page. Human ownership is
page-scoped. The default native density is fixed at 1.25 in this accepted pipeline;
the previous tabCapture density selector and standard/responsive mode chooser are
retired. Historical scale values in configuration do not select a second path.

Before each new media epoch, a random pixel challenge proves the actual native
output belongs to the current document, then proves its removal before transmission.
A new RTCPeerConnection and presentation acknowledgement fence stale media/input.
This is a causal source check, not an assumed delay or frame-count barrier.

Server messages are `ready {protocol:2,transport:'webrtc',state}`, `state`,
`media_reset {media_epoch}`, `media_offer {frame,sdp,ice_servers}`,
`media_candidate {media_epoch,candidate}`, input `ack`, and `error`.
The client sends one compatible `media_config {scale}` hint (1–2); native output
density stays at 1.25. It then sends an SDP `media_answer`
and trickled `media_candidate`, each bound to that epoch. `frame` is metadata:
`frame_id`, `media_epoch`, `pixel_width`, `pixel_height`, CSS `viewport`,
`frame_identity`, `generation`, `control_epoch`, `document_version`,
`context_ref` and `tab_id`. No base64 live image is sent.

The video compositor's first matching presented frame authorizes input through
`presentation_ack {frame_id,media_epoch,width,height}`. This acknowledgement
is per media epoch, never a per-frame flow-control gate. A fresh peer and video
are required after navigation, viewport or control-identity changes. Stale
tracks, offers and acknowledgements cannot authorize input. The blank new-tab
page stays local and starts no encoder; the viewer reports failure if a nonblank
page never presents a frame within 15 seconds. Video disconnection immediately
fences input, and reconnection never replays it.

Writes use `input {seq,generation,control_epoch,lease_token,frame_id,media_epoch,event}` or
`viewport {seq,generation,control_epoch,lease_token,frame_id,media_epoch,width,height}`.
Sequence numbers must strictly increase within this connection, including after
errors. No failed, disconnected, or uncertain input is replayed. Each input must
reference an acknowledged current frame and that tab's current human lease;
tabs and context are immutable for a connection. At most one live view per tab
owns input; additional viewers can observe. Events are `pointer {phase,x,y,button,buttons,
modifiers,click_count}`, `wheel {x,y,delta_x,delta_y,modifiers}`, `key {phase,key,
code,key_code,modifiers,repeat}`, `text {text}`, or `reset`. Pointer phases are
`down`, `move`, `up`, `cancel`; key phases are `down`, `up`. The modifier bitfield
is CDP's Alt=1, Ctrl=2, Meta=4, Shift=8. Coordinates use the reported CSS viewport.
Input is dispatched through a per-page queue without a DOM observation after
each event. A known Agent action on B does not block ordinary live input on
human-owned A. Agent operations and HTTP human navigation still use the main
session queue; this is not unrestricted concurrent navigation. Dragging preserves
down/move/up; IME and paste insert committed text once.

Authenticated viewers may resize an Agent-owned tab without an input lease when
the context is safely idle. This grants display resize only, never input. A
human-owned tab still requires its own lease and live input owner, and cannot
resize during held keys or a drag. Resize uses the main queue, invalidates element
refs/screenshot coordinates, and accepts 320–2560 by 200–1600 CSS pixels. Known
busy work returns `LIVE_BUSY` without closing the socket; the client may retry
resize after idle. Unknown outcomes require cancellation. State reports actual
dynamic viewport size.

Control is tab-scoped: `generation` changes for context cancellation/reset;
`control_epoch` changes only for that tab's handoff. `state.control_scope` is
`tab`, top-level control describes `viewed_tab`, and `tabs[]` has
`{id,url,title,control,control_epoch}` without leases. Agent `tabs` and explicit
`tab_action:new|switch` can leave a human-owned active tab; reading, navigating,
closing or debugging that human page is refused. Human navigation never changes
the Agent's `active_tab`. Popups inherit opener ownership, and human popups never
steal the Agent target. A popup may require explicit takeover in the Web UI when
that window has no in-memory lease for the new tab.

Control changes fence only the affected tab's queued input/observations and
release its pressed keys/buttons. Context cancellation, unknown execution,
unconfirmed key release and the existing modal-dialog handling retain their
context-wide protection; switching or creating a tab cannot bypass them.
A timeout preserves uncertain action
tracking and requires explicit cancellation instead of retrying a mutation.
Continuous frames are viewing/input coordinates, not a DOM citation capability:
element-reference mode temporarily uses one strict `/frame` observation and
the existing read-only `/pick` endpoint, then resumes the live compositor view.

Run `node --test test/live.test.js` for real Chromium coverage of Unicode text,
textarea Enter/Backspace, pointer dragging, resizing, duplicate sequence rejection,
lease handoff, disconnect cleanup, presentation fencing and independent viewers.

### Lightweight element preview and strict commit

Hover preview uses two fast revision checks around one combined native hit and
geometry evaluation per ordinary frame. It does not walk the selected subtree
for names/text or allocate `pN` references. Crossing an ordinary iframe adds a
node-identity check and actual child-frame resolution. Coordinates include the
frame's border and padding; transformed or uncertain frames stay on the frame
container. Geometry uses captured native getters rather than site overrides.

Each non-null preview returns a `pv_` plus 48-hex-character capability and
`expires_in_ms:3000`. The server retains at most 32 per Page and 512 per session.
A token binds the owning context, tab, generation, control epoch, displayed
version, exact x/y and private per-document node identity. Strict `/pick` consumes
it once, performs the existing complete revision validation and text observation,
and refuses replacement nodes or expired/stale positions. Token-free strict
`/pick` remains available for the existing desktop inspector and compatibility.

Preview has a 750 ms HTTP observation budget; strict commit has 1500 ms. They
share one actual pending observation per Page. On timeout, the response is 504
with `code:PICK_OBSERVATION_PENDING`; the underlying renderer read retains the
lock until it actually finishes. A conflicting request returns 409 with the same
code. `state.pick_pending` reports that selected Page's lock, so the UI can await
drain and then observe the current pointer without replaying an old commit.

Fast revision checks drain `MutationObserver.takeRecords`, discover only new
subtrees, observe newly attached shadow roots, and retain scroll, resize,
font/loading and animation boundaries. Common CSSOM mutators also dirty the
revision. Detached roots release observers/listeners and can be observed again
after reinsertion. Strict screenshots/commits still perform the full bounded
discovery scan; incomplete coverage refuses selection. This is bounded DOM
observation, not proof that every compositor-only visual change is tracked.

`node --test test/picker-preview.test.js` covers 10k-node lightweight observations,
token identity/expiry/capacity, actual pending-read drain, shadow lifecycle,
cross-origin padded iframe hit accuracy, hostile geometry getters, and version
invalidation. Physical hover latency in Docker/Web must be measured separately;
these correctness tests are not a user-device performance acceptance.

Destructive cancellation includes paused page/worker execution even when no
ordinary browser action remains in flight. It temporarily isolates this
context's network, atomically terminates paused execution, then destroys old
pages before releasing debugger connections. Network access is restored only
after successful page destruction; cookies survive. DELETE uses the same
termination boundary before destroying the context. Idle cancellation and
human-owned dialogs retain their existing behavior. Already-issued business
requests cannot be undone. See the [model evaluation and cancellation findings](../docs/browser-devtools.md).

Agent actions: `snapshot`, `find`, `click`, `type`, `navigate`, `wait`, `extract`,
`scroll`, `select`, `press`, `hover`, `upload`, `batch`, `screenshot`, `tabs`, `tab`,
`devtools`, and the optional observed-evidence `calculate` helper.
`devtools` accepts a scoped CDP `method`, optional JSON `params`, and optional
opaque `target` from `targets.list`. The inventory covers the current page and
its attached OOPIF/dedicated-worker descendants, including nested workers.
Start with `{"action":"devtools","method":"help"}` to discover supported
domains, stored Network/Console/event readers, and current limits. Use
`devtools` with `Runtime.evaluate` for page JavaScript; it may change the page
or issue requests and must run individually, never inside a batch. The old
`eval`, `auth_status` and `api_read` actions are retired and rejected before any
batch step executes. There is no credential-copying HTTP client or iframe
executor fallback. See the [native-only migration guide](../docs/browser-native-only.md).
The independent [Browser Use v2 acceptance suite](eval/browser-use-v2/README.md)
checks recorded worker evidence against controlled-site server state. Use
`npm run browser-use:eval:test` for regression tests and
`npm run browser-use:eval:validate -- --control reference` for the reference
control. Noop and fabricated-event controls must fail every task. These are
synthetic controls, not model or real-site success rates; historical scores stay
separate. Worker startup requests can race Playwright's own CDP attachment, so
this suite's worker sampling begins with an explicit action after attachment.
`tab` switches using `target: 'tN'`; `tab_action: 'new'|'switch'|'close'`,
`tab_id` and optional `url` support the human UI as well.

DOM refs (`eN`) belong to one page/document version. Navigation invalidates them.
Batch execution stops at navigation or a tab change; it never blindly follows
the remaining plan against the new document. Coordinate clicks need `x`, `y`
and the `version` from a fresh explicit screenshot. DOM mutation, scroll, resize,
navigation and control changes invalidate that version. UI clicks use the
`X-Browser-Version` associated with the displayed frame. Both use CSS pixels.

`type` and `select` require a string `text` (`""` clears a `type` field); a batch
with any invalid step executes none of its steps.

### Website file uploads

Pogo authorizes a conversation workspace file and streams its bytes to
`POST /sessions/:id/uploads`; the browser worker never opens an Agent-supplied
filesystem path or exposes a new client-facing port. The returned UUID belongs
only to that BrowserSession. An Agent uses it in a standalone action:

```json
{"op_id":"select-files-1","action":"upload","target":"#file","upload_mode":"input","files":["10203040-5060-7080-90ab-cdef01234567"]}
```

`input` sets a real file input, including hidden inputs and child-frame `eN`
refs. `chooser` intercepts the chooser before clicking the target button and
sets its selected files; it supports dynamically created inputs. `drop` sends
Playwright's external file `DataTransfer` events to a drop zone, which must
accept `dragover` with `preventDefault()`. It does not pretend to be a physical
mouse drag, and sites that require trusted OS drag events may reject it.
These are normal website events: FileReader parsing, previews and upload
requests still run in the original website. No site-specific HTTP request is
substituted. Raw `DOM.setFileInputFiles` remains denied in Agent DevTools.

Upload uses existing page ownership, busy/cancel boundaries and `op_id`
deduplication. It cannot run inside a batch. `data.upload.status:'selected'`
confirms file selection only; `site_parsing_verified:false` explicitly avoids
claiming successful website parsing or server submission. The Agent must inspect
the resulting page or wait for its expected preview/configuration before acting.
An unknown action outcome must never be automatically replayed.
Go attaches an internal `upload_binding` with the original context, tab,
generation, control epoch and document version. The worker checks it immediately
before upload execution; a handoff or navigation during file transfer cannot
silently retarget selection. This binding is not exposed in the model tool schema.

Limits are 256 MiB per file, 8 files per selection, 128 staged files per session,
512 MiB per session and 512 MiB shared across this worker. `state.upload_capabilities`
reports them. Empty files and unsafe/non-basename filenames are rejected. Uploads
stream to private UUID directories; failed/incomplete transfers release their
quota. Unused staging expires after 30 minutes. Once browser dispatch may have
selected a file, its bytes remain until session cleanup because the website can
call FileReader later. Main-side staging may reuse an unchanged file's UUID.

`node --test test/uploads.test.js` covers stream/storage/session boundaries and
real Chromium input, chooser, iframe and drop uploads with delayed frontend
parsing, operation deduplication and human-page ownership.
Set `POGO_UPLOAD_TEST_NATIVE=1` inside the browser worker image to run the same
upload assertions against the real headed Wayland session instead of an injected
headless contract browser.

After an agent `click`, `type`, `select`, `press`, `hover`, `scroll`, `navigate` or `upload`,
the worker briefly waits for document and fetch/XHR requests observed since the
action began, bounded by `POGO_BROWSER_SETTLE_MS`. The scope includes the starting
page and its frames, plus new descendant popups confirmed through Playwright's
opener events. Existing background tabs and their popups do not delay or replace
the action's observation. Same-page timer traffic can still count; this is a
page boundary, not proof that the action caused a request.

This is observation only: nothing is retried. Fetch/XHR tracking ends at response
headers because Chromium may never report request completion if the page does
not consume its body. A slow body or delayed DOM update can therefore outlive
settle with no `pending_requests`; use an explicit condition-based `wait` to
verify the expected result. Navigation-abandoned requests stop counting.
The reply adds `settle_ms` (including any popup DOM-loading wait), optional
`pending_requests`, and `opened_tabs`/`from_tab` for related new pages. `from_tab`
is historical: it does not assert that the original page is unchanged or still
open. Reads, explicit `wait`, tab switches and human input do not settle.
If an opener closes before Playwright reports its popup, attribution may be
unavailable. The worker then reports an unconfirmed observation with available
tabs and stops the remaining batch, rather than presenting a fallback homepage
as the action result. `tabs` and explicit `tab` switching work without an active
page, so the model can recover by observation without repeating the action.

Human input types: `click`, `key` (Playwright key syntax), `text` (Unicode insertion),
`scroll` (`deltaY`/`deltaX` in CSS pixels), `navigate`, `dialog` (`accept`, optional
`text`), and `tab`. Lease failures return 403; stale frames or control transitions
return 409; bearer failures return 401.

Element selection uses a separate single-flight reader with a 1500 ms deadline.
It checks document revision, tab, generation and action sequence before and after
reading. Selection never focuses, hovers, scrolls or clicks the page, and never
reads input values or calls the site's custom data hooks. The returned `pN`
observation identifier is stable for that node/document and is explicitly
rejected as an action target; it does not replace or invalidate existing `eN`
references. Pogo forwards only allowlisted facts to the composer, where the user
can review or remove the reference before sending.

Ordinary cross-origin frames and open shadow roots are supported. Transformed
frames fall back to the iframe container when coordinate conversion is uncertain;
closed shadow roots and canvas internals are not decoded. Versions track observable
document/layout changes, rather than every compositor pixel. Animating targets
are refused; continuously changing canvas or other unobserved visual changes are
outside the guarantee. Resource and traversal budgets may refuse a selection while
leaving ordinary browser viewing available.

Viewer state includes `activity.last_action` and up to eight `recent_actions`.
Each batch child has its own sequence, actor, static label and
`started`/`succeeded`/`failed` phase. Typed values and page text are not telemetry.
An optional `X-Browser-Overlay` response header contains unpadded base64url JSON
bound to the JPEG's `X-Browser-Version`, tab and viewport. It carries sampled
target/focus rectangles and a recent real pointer event. Locator rectangles are
target highlights, never guessed click coordinates; uncertain iframe pointers
are omitted. Input focus and mouse position are separate observations.

Telemetry never inserts visible page elements or dispatches extra mouse/focus
actions. Geometry is checked around screenshot capture; if pointer/focus changes
without a DOM revision, the worker omits decorations while retaining the valid
JPEG. Sampling has a time budget and shares unresolved reads across viewer polls.
Only click/hover may claim a new pointer event, bound to the action's starting
tab, document and generation. Chromium's trusted initialization/focus events
are not evidence that navigation, typing or keyboard input moved the mouse.
Those actions preserve an existing valid pointer; navigation never transfers
an old click to the new document.

Taking control fences the agent immediately, waits for its current atomic action,
and preserves the page. Confirm/prompt dialogs remain pending for the human;
they are never automatically accepted. Hard cancellation preserves context
cookies but destroys active documents and refs. If interruption cannot be
confirmed, the worker withholds ACK and closes/blocks the session. Clients must
not replay an uncertain operation. Ordinary completed results are cached for 64 operations;
older operation IDs remain tombstones until session deletion, not replay permits.
Authentication and debugging responses are not cached for operation-ID replay.
Debugger pauses are explicit: inspect events/call frames, resume the original
action and verify its outcome instead of repeating it. Unknown protocol outcomes
block subsequent actions until explicit cancellation; the viewer displays this
state. Debugging records are invalidated on control changes and cancellation.

## Current boundaries

- Login state survives page cancellation within a context, not worker restart,
  session deletion or idle expiry. There is no profile export/persistence UI.
- Native Wayland downloads are captured from the authenticated Chromium profile
  into a private GUID-named directory. `GET /sessions/:id/downloads` lists bounded
  metadata; `GET /sessions/:id/downloads/:guid/content` streams only completed
  files; `DELETE /sessions/:id/downloads/:guid` acknowledges import or cancels.
  All routes use the existing worker bearer authorization and session boundary.
  The Pogo server imports completed files into the conversation workspace before
  acknowledging them. No arbitrary URL fetch, filesystem path or CDP download
  control is exposed to pages/agents. Limits: 512 MiB/file, 2 GiB/session, 16
  unacknowledged entries, five-minute transfer timeout and 15-minute retention;
  session close/restart removes temporary payloads. Failed/expired downloads never
  report completed. HTTP and blob downloads retain the initiating Sheet identity
  and ownership; source metadata omits credentials, query strings and fragments.
  Human takeover and agent actions share this policy. The download budget is
  independent of attachment upload size preferences. Pogo imports in the existing
  background loop with a bounded stream and atomic workspace publication; status
  queries return cached receipts instead of waiting for a large file. Concrete
  quota, cancellation, expiry and validation errors survive the host boundary.
  The per-session temporary quota is a ceiling, subject to available worker
  storage. Only `saved` plus a path confirms a conversation file, which the user
  can then save locally from the file interface.
- Uploads and OS/native dialogs remain outside the integrated browser workflow.
- Page-scoped JavaScript and CDP are available within the authorized browser task.
  They are not read-only. Browser-global routing, host files and credential APIs
  remain unavailable; inspect `devtools help` for the method boundary.
- This is the agent protocol path, not an embedded full DevTools frontend.
  OOPIF and dedicated-worker targets are attached recursively; shared/service
  workers, pre-attachment history and complete heap/trace export are not covered.
  Coverage reports initializing/rejected targets and capacity limits. Standard
  CDP defaults to the root page; logs aggregate the tab family unless selected.
- Chromium contexts isolate site storage; the worker is a trusted control service,
  not a hostile-tenant VM boundary. Only Pogo should reach its authenticated endpoint.

## Tests

```sh
npm test
```

Tests launch a dedicated temporary Chromium and local fixture HTTP server. They
exercise original URLs with X-Frame-Options/CSP, real DOM operations, isolated
cookies, JPEG/version handling, at-most-once dispatch, navigation fences, hard
cancellation, human leases, modal handoff, and stale coordinates after site-driven
DOM changes. They do not connect to the user's existing browser or deployed Pogo.


## Browser-owned login and synthetic test platform

Run `npm run mock:start` to start the synthetic Chinese business platform at
`http://127.0.0.1:5190`. It supports real server-validated Cookie and Bearer login,
a simulated human-assisted second factor, orders, scoped personal tokens,
expiry/revocation and per-run audit counters. It never uses real model keys.
See [mock platform](mock-platform/README.md) and the
[login-state testing guide](../docs/browser-auth-testing.md).

Website authentication belongs to the Chromium context and the website's own
scripts. Read existing requests with `devtools` Network readers. A task-authorized
new page request is a new operation, never an implicit retry or evidence read.
The retired credential-helper endpoint returns 410 without contacting the site.
Run `npm run auth:test` for native-only rejection, browser-owned HttpOnly login,
and no-replay evidence checks, and `npm run mock:test` for synthetic platform
assertions. Historical auth evaluation artifacts describe older protocols and
are not current-product acceptance.

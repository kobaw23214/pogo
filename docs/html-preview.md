# Workspace HTML previews

## Choosing a delivery path

Static websites and interactive pages using project-local HTML, CSS,
JavaScript, modules, JSON and media use the built-in HTML Preview. Save a real
workspace HTML file and return an `html-preview` fence containing its path and
optional title. A Python or Node HTTP server is not needed to display these
files. Referenced assets must stay inside the preview project root; the
existing completed-edit refresh behavior applies.

Ordinary `html` fences show source code. Only `html-preview` metadata requests
the interactive Artifact; its body is JSON, not executable HTML.

The preview is temporary and restricted to the supported static assets. It
does not host a backend, SSR, server-side API or WebSocket server, and it does
not constitute permanent website deployment. Those projects need a managed
Web service capability that is actually available in the Agent's tools. If
that capability is absent or a project is unsupported, the Agent should
explain the limitation rather than start an unmanaged background server,
guess a `localhost` URL or change container port mappings. This document does
not announce a new hosting tool or expand the existing preview permissions.

## Existing preview behavior

Ordinary `.html` and `.htm` artifact links open source code in the file tab,
with line numbers, source editing, citation positioning and the original download.
The two toolbar actions, in order, are
“Open in current browser” and “Open in built-in browser”. Both open a separate
rendered tab in the right dock only when explicitly selected. The first renders
in the client's browser; the second opens a native remote-browser Page.
There is no HTML source/preview switch in the file toolbar.

A top-level `html-preview` fence instead embeds an HTML Artifact in the answer.
Its body is JSON with a workspace `path` and optional `title`, never executable
HTML. Streaming answers show this metadata as code and mount the iframe only
after the run settles. The inline card offers source, refresh, download and a
separate rendered dock tab. It mounts lazily near the viewport and uses the same
capability and sandbox path as the dock. Passive document previews keep the
metadata as code, and ordinary HTML links retain the source-file behavior above.

HTML runs in the client's browser inside an `allow-scripts` sandbox, without
`allow-same-origin`, forms, top navigation or popups. The response has its own
CSP sandbox as well, so opening the URL in a top-level native browser Page does
not restore access to Pogo's origin. The existing authenticated raw-file API
continues to refuse inline HTML.

`POST /api/sessions/{sid}/html-previews` accepts a file-panel-shaped `path` and
an optional `project_root`. Creation requires ownership of the conversation and
limits the project to that conversation's workspace. The default project root
is the HTML entry's parent directory. It returns `id`, relative `url`, native
`browser_url`, `expires_at`, `read_only`, `path` and `project_root`.

The URL grants read-only access to that project's allowed static assets for
30 minutes. It does not contain an account token. Local CSS, JavaScript,
modules, JSON, images, fonts and media resolve beneath the same capability.
Private/config/source files and symlinks are refused. External dependencies,
main application APIs, forms, subframes and workers are blocked. HTML entries
are limited to 8 MiB and other assets to 32 MiB. Storage is pinned with directory
handles; previewing does not modify the workspace.

Asset GET/HEAD/OPTIONS requests use the capability alone. Opaque-origin module
and JSON requests receive credential-free CORS support. Response CSP limits
network resource access to the exact capability path, including through an
HTTPS reverse proxy. The native browser URL uses the actual listener address
and port rather than a forwarded header or a browser-local `localhost` guess.

Each browser tab owns a separate capability, which survives closing the source
file tab and is revoked when its browser tab closes. Client-rendered tabs store
only the owning session and file path across reloads, never their capability
URLs. They obtain a fresh capability when restored. All capabilities have a fixed expiry;
deleting the owning conversation or account invalidates access. Limits are
16 live capabilities per owner and 128 per server. Restarting the server clears
them; the client-rendered browser tab can request a fresh preview.

The HTML response includes a capability-scoped live-reload client. It watches
only assets actually loaded through that capability and waits for the agent's
run (including background edits) to finish before reloading changed content.
Both browser types reload the same Page, without taking control, creating tabs
or changing focus. External file edits have a periodic fallback. The original
workspace file, source view and download are unchanged by script injection.
Source tabs also refresh after completed workspace writes, while preserving
unsaved editing drafts.

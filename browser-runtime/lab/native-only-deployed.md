# Native-only deployed smoke

`native-only-deployed.mjs` checks a running app/worker image pair. It does not
deploy, read Compose or environment files, modify panel registrations, or invoke
a model. Run it only after the operator has selected the target deployment and
authorized temporary test objects.

```sh
cd pogo/browser-runtime
node --test lab/native-only-deployed.test.mjs
node lab/native-only-deployed.mjs /absolute/path/new-output-directory
```

The caller supplies these environment variables without printing credentials:

| Variable | Meaning |
| --- | --- |
| `POGO_ACCEPT_APP_URL` | Plain loopback app origin, for example `http://127.0.0.1:19777` |
| `POGO_ACCEPT_APP_TOKEN` or `POGO_ACCEPT_APP_COOKIE` | Existing authorized administrator credential |
| `POGO_ACCEPT_MODEL` | Configured model ID for the temporary Agent; no prompt is sent |
| `POGO_ACCEPT_APP_CONTAINER` | Exact target app container name |
| `POGO_ACCEPT_WORKER_CONTAINER` | Exact target worker container name |
| `POGO_ACCEPT_APP_IMAGE` | Expected immutable app `sha256:...` image ID |
| `POGO_ACCEPT_WORKER_IMAGE` | Expected immutable worker `sha256:...` image ID |
| `RELEASE_TOKEN` | Optional scoped operator maintenance token; sent to the app origin only |
| `POGO_ACCEPT_OTHER_TOKEN` | Optional different-account token to additionally verify ownership denial |
| `POGO_ACCEPT_FIXTURE_HOST` | Host reachable from the worker; defaults to Docker Desktop's `host.docker.internal` |
| `POGO_ACCEPT_WORKER_URL` and `POGO_ACCEPT_WORKER_TOKEN` | Optional HTTP worker transport instead of `docker exec` |

The default worker transport reads the bearer token only within the worker
process. It invokes `docker exec <worker> node ...` and accepts exclusively fresh
`native-only-accept-*` session IDs. It never exposes a new port. Image attestation
uses a narrow Docker inspect projection (image, running, health), without
retrieving the container environment or mounts.

The temporary fixture listens on a random host port and a random URL path. It
serves only synthetic pages and data, refuses embedding, and counts website
requests itself. The worker must reach this host; configure the advertised host
for non-Docker-Desktop deployments. The fixture closes in cleanup.

## What is checked

- Both target containers run the supplied immutable images. Worker Docker
  health must be healthy. App Docker health, when configured, must be healthy;
  when absent, the subsequent authenticated app API checks establish readiness.
- The app returns worker-backed native state and can take/return human control
  without any UI/SSE client. A lease cannot control another test session.
- Anonymous access is denied. Cross-account ownership is checked only when a
  second account token was supplied; absence is reported explicitly.
- App credential helpers and old `browser_result` are retired; live navigation
  rejects `/panels/...` addresses.
- Native worker rejects `api_read`, `auth_status`, and `eval`, including their
  presence later in a batch before an earlier click can execute.
- Real CDP reads the page and a response captured from exactly one
  authenticated website request. Reading evidence issues no replay.
- The migration endpoint resolves an existing registered shortcut, when one
  exists, and preserves unresolved entries. The registry is compared before
  and after. If none exists, this specific coverage is reported absent.
- A fresh, isolated Playwright context loads actual deployed UI assets and APIs
  with synthetic old bookmarks/tabs. First load backs them up and migrates them;
  reload is idempotent; no saved website is automatically opened. This does not
  read or migrate the user's existing browser profile/localStorage.

One uniquely named temporary ready Agent and two test sessions are created.
Sessions are deleted and checked for 404 in `finally`; the Agent is retired
through the normal soft-delete API. Cleanup failure makes the report fail and
retains the exact test IDs for operator reconciliation. No existing Agent or
session is edited. Browser UI requests that could submit prompts, register
panels, navigate, or access another conversation are aborted and fail the check.

## Release gate contract

The private `smoke.json` contains evidence scope, check names, image IDs, cleanup
results, and fixture request counts. User registry names/URLs and credentials
are not archived. The output directory must not already exist.

This is **one input** to `w_deploy/release.py` acceptance, not a complete hook.
Its `release_hook` object reports actual `worker` and `human_takeover` results,
but leaves `model` and `workflow` false. A release hook must combine this report
with independently executed real-model and human-node workflow acceptance on
the same image pair; it must not replace those two fields with constants.
Full Agent → Go bridge → worker execution is covered by that model run. The
direct worker CDP check is labeled separately from the app human-control check.

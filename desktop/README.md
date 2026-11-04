# Pogo Desktop browser host

Pogo Desktop places the existing Studio in an Electron window and displays websites in real Chromium `WebContentsView`s. Mouse input, dragging, text selection and IME are handled locally by Chromium. The Go Agent still uses the existing browser-use tool and native worker protocol; Playwright/CDP attaches to these same website pages. There is no extra headless Chromium and no JPEG or remote input path in desktop mode. Rust is not required by this implementation.

## Run and package

Install the pinned dependencies with `npm ci` in this directory and `../browser-runtime`, then create a private configuration outside the repository:

```json
{
  "studioURL": "http://127.0.0.1:19777/",
  "workerPort": 19783,
  "workerTokenFile": "/absolute/private/path/browser-token",
  "studioTokenFile": "/absolute/private/path/account-token"
}
```

By default this is `~/Library/Application Support/Pogo Desktop/desktop.json`. `studioTokenFile` is optional; omit it for the normal Studio login. Both token files require mode `0600`; the worker token must match the Go service's browser token. Environment overrides and a separate test data directory are supported by `main.cjs`. Run `npm start`, or build the macOS development app using [PACKAGING.md](PACKAGING.md). Packaging excludes private configuration and profiles.

The worker listens only on loopback. Docker Desktop on the validated Mac reaches it through `host.docker.internal:19783`; deployments on other platforms must validate their own routing. Configure the Go service's `POGO_BROWSER_DESKTOP_OWNER` to the account bound to this device. Other accounts do not receive a browser executor and do not fall back to the hosted worker. A backend change is an explicit deployment operation; use the desktop migration procedure in [release documentation](../../w_deploy/RELEASE.md), not an edited running-container environment.

## Ownership and behavior

- Each account/session uses an isolated persistent Electron partition keyed by the full Go owner scope and canonical session storage path. Persistent cookies survive a graceful desktop restart. This does not import Chrome cookies or promise persistence for session-only cookies.
- Website pages have no Electron preload or Node access. Only the configured Studio origin's main frame receives the narrow attachment bridge. Page identity, revision, runtime generation, control epoch and human lease are checked in the main process.
- Agent ownership places a native input shield above the page. Human takeover enables direct input; giving control back closes DevTools and ends element picking before Agent execution resumes.
- Human DevTools is the full Chromium frontend. Agent tools access CDP directly; they do not click the DevTools interface.
- Element selection keeps Chromium Overlay's native hover highlighting, blocks selection clicks from reaching the website, and hit-tests the clicked node with CDP before verifying it against the page probe's document identity. Mouse-up remains blocked even when the quote finishes first; Escape exits selection while the native page has keyboard focus. The current human point-selection path supports the main document only: both same-process and cross-process iframe selections are explicitly refused until their frame/coordinate identity is verified. Agent iframe and multitarget browser-use capabilities are unchanged. For an initial popup main document without the page probe, the native fallback quotes only verified DOM geometry and accessibility text, explicitly marked as limited evidence.
- Popups retain their real WebContents, opener and POST semantics. Closing a sheet destroys its owned view; hiding a sheet only hides presentation. Cancelling awaits destruction before replacing a page.
- Restart reconstructs a new context with new leases and references. Only read-only state discovery reconnects automatically; interrupted mutations are not replayed. Closing the desktop makes browser actions unavailable until it is opened again.

In desktop mode `/frame`, coordinate picking over HTTP, and legacy keyboard/mouse/text forwarding return `410`. Agent screenshots remain supported for visual reasoning. File preview, PDF preview and the shared citation components remain independent features.

## Validation boundary

`npm test` covers scoped sessions, lifecycle races and native inspection. [Go integration](../docs/desktop-go-integration.md) drives the real server, Agent loop and browser tool using a deterministic model and synthetic pages. Real-model/site acceptance, packaged application acceptance and deployment acceptance must be recorded separately.

From the repository root, `node pogo/desktop/lab/picker-test.mjs /absolute/fresh/output-directory` runs a separate Electron fixture. It cancels an active navigation, selects a submit button with no preceding mouse movement, delays mouse-up until after citation creation, and verifies that no form submission occurs. It does not operate an existing desktop session. `POGO_DESKTOP_DIAGNOSTICS=1` enables native mouse coordinates, picker lifecycle and debugger-event diagnostics; it does not log page text, URLs, typed input or credentials.

On the development Mac, the user confirmed smooth physical dragging and Chinese IME input on the integrated desktop fixture. This is human usability acceptance, not a measured p95 input-latency benchmark or certification of external site verification widgets. The local app is ad-hoc signed, not notarized for distribution; Windows/Linux have not been accepted.

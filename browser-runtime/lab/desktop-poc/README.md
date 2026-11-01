# Desktop browser experiment

Isolated verification of native Electron WebContentsViews shared by a person and
Playwright. This directory does not change the deployed Pogo browser executor.

## Run

Install Electron into an independent experiment dependency directory and set
`POGO_POC_ELECTRON` to its executable. The harness uses the repository's existing
Playwright dependency. Example from the repository root:

```sh
POGO_POC_ELECTRON=/absolute/path/to/Electron.app/Contents/MacOS/Electron \
  node pogo/browser-runtime/lab/desktop-poc/run.mjs /absolute/path/to/fresh-output
```

Use `POGO_POC_KEEP_OPEN=1` to keep the owned experiment window open for manual
observation. For on-demand synthetic fixture snapshots, write
`{"id":"a-unique-observation-id","action":"snapshot"}` to `control.json` in
the output directory and wait for the matching ID in `control-result.json`.
Copy that result to a descriptive filename before the next request. Use
`action:"finish"` to capture final state before orderly shutdown. This is a
file-only lab interface, not an HTTP endpoint or a product control protocol.
SIGINT remains emergency cleanup; Playwright may close the page before a final
read. Every run requires a new output directory; failed results are preserved.

The application has its own profile, visits only the harness's local fixture
origin, and contains no production credentials. WebContentsViews are created by
Electron. The harness does not launch a second Chromium browser. The privileged
test interface exists in the main process; fixture pages receive no preload or
Electron API.

## What is verified

- Explicit mapping of both visible page identities to Playwright pages.
- Continuous synthetic printable input, Unicode insertion, ordinary pointer
  dragging, iframe input, and exactly one counted HTTP save.
- Stable page identity and retained state when switching Sheets.
- Chromium Overlay selection and the selected backend DOM node.
- Playwright and Electron CDP commands while native DevTools is open.
- Browser process metrics and separately reported controller RSS.

## Measurement boundaries

Command round trips are not physical input-to-photon latency. Unicode insertion
is not real OS input-method composition. The local, synthetic experiment is not
production acceptance, a real-site compatibility result, a Windows/Linux result,
or a fair old-versus-new architecture A/B benchmark. Resource totals are process
working-set sums and may double-count shared pages. First CPU samples must be
discarded. Keep manual observations separate from automation results.

Rust has not been introduced. The immediate target is removing image polling
and server round trips from local human interaction. A Rust sidecar or native
module remains a candidate for measured CPU-heavy parsing/indexing/evidence
work; any comparison must count IPC, conversion, and additional memory costs.

## Not yet integrated

Production device authentication, a restricted CDP proxy, remote Go routing,
ownership/cancellation, shared profile policy, OOPIF/worker coverage, recovery,
file transfer, packaging/signing and upgrades require later validation.

## 2026-10-08 result

Evidence is under `.localagent/checks/desktop-browser-poc-20261008/` at the
repository root. It is deliberately outside product data and deployment.

- `run-1/summary.json`: preserved failure. Chromium required a highlight
  configuration even when disabling Overlay inspection. Both manual and
  automatic exit now include that configuration and expose protocol errors.
- `run-2/summary.json`: all 14 synthetic checks passed with Electron 44.7.0
  and Playwright 1.63.0 on this macOS arm64 host.
- `run-2/desktop-observations.json`: native desktop automation entered
  `desktop-input-123` and pasted Chinese correctly. Two desktop automation
  drag attempts did not change the controls' values. Their cause is unresolved;
  only the Playwright pointer sequence passed. Physical mouse/IME acceptance
  remains open.
- `run-2/native-desktop-observation.png`: actual native application screenshot.
- `post-verification.json`: production image IDs and start times match the
  pre-experiment snapshot; the experiment's Electron and fixture server exited.
- `architecture-audit.json`: read-only whole-product source audit, with code
  pointers, existing optimizations, candidate changes and required measurements.

Run 2 command timing: page-switch round trip p50 1.34 ms / p95 13.19 ms
(20 samples); Playwright DOM-read round trip p50 0.28 ms / p95 0.41 ms
(60 samples). These values do not establish typing, dragging or rendering
latency and must not be compared with a remote screenshot product baseline.
Startup was 687 ms in this run after Electron had already run once.

After discarding the first sample in each phase, the median sum of browser
process working sets was about 665 MiB with two idle views and 778 MiB after
interaction/DevTools. Shared memory may be counted repeatedly; the harness
controller is separately recorded. These instrumented short samples are not
product memory limits or a Rust comparison.

Stopping the keep-open harness with SIGINT invoked Playwright's own signal
handler as well as cleanup (exit code 130). The final live fixture read therefore
returned null; native observations rely on the separately captured accessibility
state and screenshot. This does not change the already completed automatic
checks and is explicitly recorded instead of treating null as evidence.

## Architecture recommendation

Treat the browser prototype and the following product changes as separate
validation stages. The audit identifies possible work and data flow costs; it
does not establish measured production bottlenecks.

1. Validate physical input on local Chromium WebContentsViews. Keep human
   input out of the screenshot/HTTP relay. Bind the Agent to the same tab through
   explicit page identity, ownership and cancellation contracts.
2. Preserve stable completed-turn objects and source versions while updating
   only the active response. Window long histories/lists and preserve scroll,
   citation anchors, search and copy semantics. Pogo already coalesces live
   updates at 80 ms; adding another throttle does not replace this work.
3. Keep transcript durability synchronous where required, but batch and coalesce
   reconstructible search-index updates outside the response publication path.
   Pogo already has SQLite FTS5 and durable JSONL; retain those capabilities.
4. Reuse a typed session/turn/item event contract across desktop and web, with
   sequence-aware recovery, bounded queues and prompt cancellation. Pogo's Go
   runtime can provide this without a language rewrite. Profile current full
   history snapshots and per-event SSE flushes before changing them.
5. Measure global scheduling fairness under simultaneous jobs. Existing
   per-session and background-task limits are useful but do not establish a
   whole-system interactive priority policy.
6. Introduce Rust only for a demonstrated CPU-heavy kernel or isolated worker
   where end-to-end savings outweigh IPC, serialization, copies, memory and
   maintenance. Candidates include large evidence processing and document/file
   indexing; these are candidates, not currently proven hot spots. A worker
   process can isolate crashes; an asynchronous native module can reduce IPC
   but expands the host's failure boundary. A synchronous native call can still
   block the UI thread.

This borrows documented architecture and platform practice, not unverified
claims about proprietary application internals. OpenAI documents the Codex Rust
app-server and a reusable Agent harness; that does not establish that the entire
ChatGPT desktop UI is written in Rust. Tauri 2's system WebViews differ across
platforms and do not provide one common Chromium/CDP engine. Tauri 3's CEF path
is currently alpha and still embeds Chromium, so it is not the first validation
baseline for this project.

Primary references reviewed on 2026-10-08:

- [Codex App Server](https://learn.chatgpt.com/docs/app-server)
- [Codex as a platform](https://developers.openai.com/blog/codex-as-a-platform)
- [Electron performance guidance](https://www.electronjs.org/docs/latest/tutorial/performance)
- [Vue performance guidance](https://vuejs.org/guide/best-practices/performance.html)
- [napi-rs asynchronous tasks](https://napi.rs/docs/concepts/async-task)
- [Tauri system WebViews](https://v2.tauri.app/reference/webview-versions/)
- [Tauri CEF status](https://v3.tauri.app/develop/cef/)

## Follow-up baseline and direction

The follow-up evidence is at
`.localagent/checks/pogo-performance-baseline-20261008/summary.json`.
The user chose to continue desktop-browser integration and defer Rust unless a
concrete future need justifies it. No Rust toolchain or product rewrite was added.

The new native-automation trace shows the drag starting on the correct handle
with `buttons=1`, but subsequent moves have `buttons=0`; release reaches the
track rather than the captured handle. This explains the failed injected
sequence and does not establish a failure of physical mouse input. The clean
page B received no human test input before cleanup. Physical pointer and OS IME
acceptance remain pending. The file-based finish command successfully captured
final state and shut down the owned browser/server with exit code 0.

The existing UI component chain was measured with 10/100/500 synthetic
exchanges. At 500, each live update reparses all 500 historical answers; the
median update-to-Vue-flush cost is about 31.2 ms and the rendered fixture has
about 44,557 elements. This is a component benchmark, not full AgentView,
production network, OS keyboard or photon latency. Fixing repeated parsing with
the existing stack is a concrete follow-up; citation metadata changes must still
invalidate the displayed references.

Storage uses the actual Go paths, disposable data, and real transcript fsync.
At 500 exchanges, single-message Append with index has a 16.37 ms median;
touchIndex measured separately has an 11.19 ms median. Those component samples
are not additive timing spans. The index-off control still computes the summary
before skipping SQLite and must not be labeled pure fsync. These are warm-cache
macOS filesystem samples, not Docker-volume production measurements.

The next product milestone is a single real Pogo session controlling a
desktop-owned page with stable tab identity, human/Agent handoff, native
DevTools, element evidence and disconnect behavior. Keep the current tool and
evidence contracts. Retire screenshot polling and human input forwarding only
after that path passes integration acceptance; do not silently fall back to a
second hidden browser when the desktop disconnects. Production containers were
unchanged during both validation stages.

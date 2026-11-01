# Browser Use v2 acceptance

This independent suite evaluates the current shared browser-use Skill and DevTools/target tools against five controlled synthetic browser tasks. It does not rewrite general-web, browser, authentication, or historical model scores. Real Chromium runs with `--site-per-process`; service workers are blocked. Supported fixture targets include the top page, an out-of-process iframe and dedicated workers.

The model receives only the task URL and natural-language business goal. Session transport and explicit budgets are adapter metadata. Oracle values, expected answers, selectors, fixture controllers and reference plans remain in the runner process. Initial navigation and any controller preparation happen before the scored proxy ledger starts. Authentication preparation uses a **controller-simulated human lease** with fictional local accounts; it does not validate human login, real credentials or real websites.

| Task | Required evidence and business result |
| --- | --- |
| `network-diagnosis` | One actual HTTP 503 response, corresponding Console error, requested status/reason/trace facts; no repeated request. |
| `iframe-data` | Complete recorded inventory body, or a complete native iframe table row with exact source/frame/column identity and matching server-served data. |
| `worker-data` | Model/reference starts sampling once through the business page after worker attachment; one actual worker request and recorded sample body. |
| `debugger-recovery` | Original prepared pause, real call frame/local read, successful same-worker resume, subsequently received save request and exactly one saved settlement. |
| `authenticated-write` | Reuse prepared login state, submit once and verify all saved fields. Accept actual POST/GET bodies, or real POST 201 metadata plus a complete post-submit native DOM saved record. |

The controller only waits for the sampling worker to attach and become ready; it does not start sampling. **This case does not guarantee capture of worker startup requests.** Playwright's separate CDP session can release a worker before Pogo finishes enabling Network, so the first startup request may be absent. The explicit business action bounds this acceptance case; it does not resolve that runtime limitation. See the [runtime capture boundary](../../../docs/browser-devtools.md#网络记录和正文).

From `pogo/browser-runtime`:

```sh
node eval/browser-use-v2/run.js --list
node --test eval/browser-use-v2/*.test.js
node eval/browser-use-v2/run.js --mode validate --control reference --seed 20261006 --out /tmp/browser-use-v2-reference-new
node eval/browser-use-v2/run.js --mode validate --control noop --seed 20261006 --out /tmp/browser-use-v2-noop-new
node eval/browser-use-v2/run.js --mode validate --control fabricated --seed 20261006 --out /tmp/browser-use-v2-fabricated-new
```

`reference` runs a deterministic controller-owned browser plan. `noop` supplies the exact expected answer without any scored browser operation. `fabricated` additionally supplies plausible model tool events containing a false successful result. Both negative controls must be rejected normally for every planned task; a crash, timeout, cleanup failure or skipped task is not a successful negative control. These runs make no model calls and their rates are never model success rates.

The recording proxy supplies a private acknowledgement ledger directly to the judge. Every entry contains a session-specific call ID, actual requested arguments, actual worker reply, HTTP status, duration and acknowledgement status. Adapter stdout is stored separately as untrusted `adapter_events` and cannot populate this ledger. The judge checks tool evidence and source identities against fixture business state, with the natural answer as a separate model claim. Trace files contain a redacted copy of the ledger and untrusted events for review; the judge uses the original in-memory acknowledgements before public truncation/redaction. No oracle is written to model input or public report.

Debugger recovery also compares proxy dispatch and fixture request-arrival timestamps from the same Node monotonic clock. Reading an old saved response after an unrelated resume is insufficient. Local inspection must read an identifier or an object chain rooted in the actual paused local scope; returning an expected object literal does not count.

Preparation privately retains the original paused target, generation, event sequence, call-frame/script identities and local-scope object IDs. The judge can bind the model's actual paused observation to this anchor without demanding an extra script-source read solely because Chromium leaves `CallFrame.url` empty. The anchor never enters task input, the HTTP fixture, public preparation summaries or server-state reports. Without an anchor, the existing script-identity proof remains required; a wrong anchor cannot fall back to a looser proof.

DOM acceptance uses only top-level `dom-evidence-v1` returned by acknowledged native actions, including a batch's automatic result observation. It checks content hashes, observation/version/frame identities, complete cells and record relationships against actual server state. Runtime-returned imitation objects, plain answer text, split rows, wrong frames, partial captures and replayed old snapshots cannot establish this proof. Authenticated writes still require real browser POST response metadata to distinguish browser activity from an unrelated HTTP writer; body reads are optional when the saved DOM is complete. These are finite fixture-specific proofs, not a general guarantee that arbitrary JavaScript was read-only or the DOM was never modified.

`answer_accuracy` is a **bounded required-fact match**, not whole-answer semantic accuracy. It checks only fields explicitly requested in the goal, with limited negation/contradiction detection. Equivalent paraphrases can still be rejected, and unrelated extra claims are not comprehensively audited. Inspect the answer and evidence manually before interpreting model results; do not optimize the Skill to an expected JSON answer format.

The automatic evidence rules also have finite coverage. Network helpers and equivalent recorded CDP body/events paths may establish the same evidence; an extra `targets.list` is not required merely to read the default root page. Arbitrary Runtime expressions, other DOM structures, and alternative debugger stepping sequences are not a general semantic proof system. Preserve the original automatic verdict and review these paths against the user's actual goal separately. An automatic evidence miss by itself must not be reported as a model business failure.

Model runs require a freshly built evaluation adapter with profile `pogo-agent-browser-use-v2`, an explicit model and all four budgets. The runner does not read provider files; it passes an explicitly supplied path to the adapter. An actual model invocation may incur provider charges. Prepare the command with the intended provider/model before running it:

```sh
# From pogo/browser-runtime/, build the adapter from the Go module one level up.
go -C .. build -o /tmp/pogo-browser-eval-agent-v2 ./cmd/browser-eval-agent

node eval/browser-use-v2/run.js --mode model \
  --agent /tmp/pogo-browser-eval-agent-v2 --providers /absolute/path/providers.json --model MODEL \
  --max-turns 50 --token-budget 120000 --timeout-ms 180000 --max-steps 80 \
  --seed 20261006 --repeat 1 --out /absolute/path/NEW_RUN
```

`--tasks id,id` selects unique known tasks. Seed is a uint32; repeat `n` uses `(seed+n) uint32`. Limits: repeat 1–10, turns 1–100, tokens 1–200000, deadline 1000–600000 ms and requested browser steps 1–200. Each requested batch child consumes a step. Deadlines include controller preparation; model input receives only the remaining time. Tasks run serially in isolated browser contexts with network requests restricted to declared fixture origins and case paths.

Each run requires a new directory. `summary.json`, `report.html` and `traces/*.json` record the suite, runtime, fixture, judge, adapter, prompt, tool schema and shared Skill hashes; Chromium version and arguments; final answers; acknowledged evidence; server state; usage; and context cleanup. Skill fingerprints must match the current source, so stale adapters fail. Interrupted, failed and unstarted tasks remain in the denominator. Cancellation/cleanup failures invalidate the run. Historical reports are never revised in place.

The HTML report separates reference-control pass rate, noop/fabrication false positives and model rule pass rate. It exposes per-task judgement and evidence IDs, service state, actual tool returns, time and measured tokens. Natural-language rules and controlled fixtures remain bounded acceptance evidence, requiring manual review before any claim about unseen real sites or deployment readiness.

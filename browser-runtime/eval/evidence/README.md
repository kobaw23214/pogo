# Native evidence / calculate interface loop

This independent four-task suite exercises real DOM Evidence and native `calculate`. It is not a replacement score for general-web, browser, auth, or auth-auto. Existing reports remain immutable. Model inputs contain only a URL and business goal; no selector, API path, reference script, oracle result, or mandatory final JSON is supplied.

Tasks use new controlled synthetic data with deterministic seed/name/layout variants:

1. Two detail pages: compare g/kg masses and obtain the difference in g.
2. One table: preserve record ID/name/price correspondence and obtain an exact CNY difference.
3. A document: compare 12/24 days; preserve the archive's years without a fixed-day conversion.
4. A dynamically completed table collection: preserve all six observed records and two tied minima; the page has no explicit page number.

The fixtures expose ordinary business HTML only. Reset, inspection and reference/oracle functions are Node-only. Per-task browser contexts allow only the start origin and that case's path subtree; other cases, external origins and controller/eval paths are blocked. Recorded public server requests retain actual browser UA/fetch markers. No screenshots or private provider configuration are read or saved.

```sh
cd pogo/browser-runtime
node --test eval/evidence/evidence.test.js
node eval/evidence/run.js --mode validate --control reference --seed 811 --repeat 1 --out eval/runs/evidence-reference-811-v1
node eval/evidence/run.js --mode validate --control noop --seed 811 --repeat 1 --out eval/runs/evidence-noop-811-v1
```

The reference chooses worker-issued quantity refs from actual native observations and calls native calculate; it never passes raw quantities to that tool. The noop supplies a correct control answer with no recorded actions and must fail the interface checks. These controls validate the interface and evaluator, not model performance.

An explicitly authorized model run uses the existing public Agent adapter:

```sh
node eval/evidence/run.js --mode model --agent /absolute/public/browser-eval-agent --model MODEL --seed 913 --repeat 1 --out eval/runs/evidence-model-913-v1
```

Optional `--providers` is forwarded as a path without reading its contents. CLI budgets are recorded (`--max-turns`, `--token-budget`, `--timeout-ms`, `--max-steps`). Outputs must use a new directory. Cancellation, adapter group reaping, strict step/token/time accounting and confirmed context cleanup remain in force. An error or skipped task stays in the denominator.

## Stable contract

Native observations return `result.evidence = {schema:'dom-evidence-v1', status:'observed'|'partial'|'unavailable', frames:[...]}`. Each frame contains genuine rendered tables/definition lists/headings/paragraphs plus immutable `quantities` and `provenance`. The reference sends `{action:'calculate', operation:'compare'|'sort'|'difference'|'sum', quantity_refs:[...]}`. Its reply stores the calculation in **the same** `result.evidence` field, with `status:'calculated'|'unsupported'|'error'`, `sources`, `result`, and scope flags. No fallback silently fabricates evidence. Missing contract fields produce `interface_pending` errors.

`interface_verified` means observed quantity sources, actual calculate acknowledgements and required business page requests agree. It does **not** certify complete candidate universes, site truth, entity semantics, or the complete final answer. Answers stay `pending_review`; there is no regex answer grader, mandatory answer JSON, model business score or automatic complete-answer accuracy. Root or another independent reviewer must examine the unchanged natural answer, actual evidence and controller request/state artifacts. That review must preserve unsupported units and real incidental errors.

`summary.json`, `report.html`, and `traces/*.json` retain source/fixture/contract/adapter hashes, current shared Skill full/body hashes, reported prompt/tool/body fingerprints, seed/layout, usage, native Evidence/calculate returns, business requests, blocked routes and context cleanup. The JSON/HTML are redacted, bounded and HTML-escaped; HTML has a no-script CSP. Fresh seeds change data on these same four templates, not unseen-site coverage. Four model tasks are an interface smoke test, not a statistical improvement claim.

## Host-only delivery overview

Run `node eval/evidence/overview.js --help` for the immutable input flags. The generator verifies original summary/task/trace SHA references against independent reviews, plus final runtime and Linux test provenance, before writing `overview.html` and a public whitelist `manifest.json` to a **new** output directory. Existing output directories are rejected.

The overview keeps original interface scores, independently reviewed natural answers, pre-probe/final actual-app candidates, and reference/noop controls separate. It preserves unknown cleanup and original null business scores; it never pools denominators or copies private raw events, login state, account/session identity or configuration. This host report generator was added after the candidate image and is outside its deployed build digest; it does not establish equality between the current whole repository and that image.

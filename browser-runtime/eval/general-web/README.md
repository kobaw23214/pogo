# General Web task evaluation v1

This separate suite evaluates URL-and-business-goal tasks on three independently served, controlled synthetic websites. It does not edit or reinterpret the original 20-task browser suite, authentication suites, their prompts, or historical scores. Passing these fixtures is not real-internet or unseen-site acceptance.

The model receives each task's start URL and natural business goal. It receives no controller answer, private fixture data, API path, selector, reference plan, prescribed steps, or required final JSON format. The `general-web` adapter profile uses the actual embedded shared browser-use Skill and reports its body hash. Answers may use ordinary prose, links, Markdown or code fences; layout alone cannot decide the result.

The nine tasks cover catalog comparison, pagination and suitability; documentation policy, runbook and retention; and workbench creation, recovery and update. Reports separate `read_only`, `write`, and `recovery`. Read tasks require actual browser observations supported by the relevant server-served business pages and correct sourced answers. Writes require the requested stored result, exactly one successful submission and no extra mutation. Recovery additionally requires evidence of the intended failure and recovery. An unsupported success claim cannot pass.

From `pogo/browser-runtime`:

```sh
node eval/general-web/run.js --list
node eval/general-web/run.js --mode validate --control reference --seed 20261003 --repeat 2
node eval/general-web/run.js --mode validate --control noop --seed 20261003 --repeat 2
node --test eval/general-web/*.test.js eval/general-web/fixtures/*.test.js
node eval/general-web/run.js --mode model --agent /absolute/path/browser-eval-agent --providers /absolute/path/providers.json --model glm-5.3 --seed 20261003 --repeat 1 --out /absolute/path/NEW_RUN
```

Reference/noop runs do not invoke a model. The reference script uses controller-owned fixture data and the same recorded public browser worker as a model. Noop supplies the exact expected natural answer without taking any recorded browser action; all nine tasks must fail. These are judge controls, never model scores. The CLI exits successfully for a noop run only when every planned task is rejected normally, with no infrastructure error or skipped task.

`--seed` accepts a uint32; repeat `n` uses `(seed+n) uint32`, changing business values and layouts. Other bounds are `--repeat 1..10`, `--max-turns 1..100` (default 40), `--token-budget 1..200000` (default 60000), `--timeout-ms 1000..600000` (default 180000) and `--max-steps 1..100` (default 50). `--tasks id,id` chooses a unique nonempty subset. Model runs need explicit `--agent` and `--model`; the runner passes the provider path to the adapter without reading it. Measured token usage, task deadline and every requested action, including batch children, enforce the budgets.

Each task gets a separate browser context and its own fixture case. Playwright routes only its declared origin(s) and case path(s), blocks service workers and rejects eval/controller/oracle paths. Controller setup, inspection and judging exist only as Node functions, with no HTTP oracle endpoint. The worker transport token is generated per run and supplied through the owned adapter environment. Public artifacts redact secret values and object keys, strip headers and screenshot bytes, and capture no images. SIGINT/SIGTERM cancel the active adapter process group, cancel the worker action, close the context and save remaining scheduled tasks as skipped. Cleanup failure stops later tasks.

Every output directory must be new. `summary.json`, `report.html`, and per-task `traces/*.json` include seed/layout, task-goal fingerprint, source/fixture/judge hashes, adapter hash, Skill and profile hashes, natural answer, recorded tool actions, controller business state, usage and confirmed context cleanup. The runner hashes the current production Skill file and extracts its body with Go-compatible frontmatter boundaries; model runs must supply valid prompt/schema/body fingerprints and match that exact body hash. An older binary with the same profile ID cannot pass. The denominator contains every planned task, including errors and skipped tasks. Model rate, reference-control rate and noop false-positive rate are separate fields.

## Acceptance and review boundary

The natural-answer checker is a bounded ruleset, not a validated general semantic judge. Real GLM-5.3 runs exposed false rejections of correct answers and genuine extra factual errors after correct business execution. Do not use `model_judged_pass_rate` alone as application acceptance, a model ranking, or a release gate. Keep the original score and artifacts immutable; independently review the answer, native action acknowledgements and server state. Core business completion, whole-answer accuracy and driver validity are separate results. Unknown or ambiguous claims require review and cannot silently pass.

`audit.js` writes a new, versioned offline review while retaining the original summary hash and protected driver failures. It makes no model calls and does not recompute the official model score. `overview.js` combines source-verified reports and independent reviews for inspection without pooling different Skill, seed or grader versions. Its `--help` lists the inputs; `--self-test` checks escaping, source integrity and refusing an existing output directory. Later report-label changes do not rewrite earlier HTML artifacts.

For manual business-page preview, run `npm run general:preview` from this directory's package root. The catalog, documentation and workbench use local ports 5191, 5192 and 5193; a Docker-hosted browser uses `host.docker.internal` in place of `127.0.0.1`. The preview has no HTTP oracle. It is separate from the temporary origins and case state used by evaluation runs.

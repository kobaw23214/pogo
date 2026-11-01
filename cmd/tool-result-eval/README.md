# Tool-result recovery acceptance

The recovery scenario exercises the same saved large log through control,
compaction, persistence, compaction plus persistence, client reconnect, and
explicit model-switch boundaries. Task prompts, fixture, native reader schemas,
source lineage, factual answers, and actual delivered evidence have independent
checks.

For routine spill changes, start with the [short completion profile](#short-spill-completion-profile):
focused local regressions, three real-model tasks, and independent scope review.
Use the larger calibration/matrix when changing the semantic grader itself or
when a concrete failure requires broader recovery coverage.

Use `--coverage-check semantic --coverage-judge-model glm-5.3` to judge the final
answer's `coverage` field by meaning. The legacy exact wording result remains
available as `coverage_exact_match`; `exact` is still the CLI default for
backwards-compatible consumers. The semantic check changes only the coverage
acceptance conjunct, not the tested agent's context or behavior.

The independent grader receives the original coverage text and a host-verified
ledger of successful, actually delivered native reader outputs. Actual citation
headers and scan byte counts are separate receipts: they can establish source
reference availability or scan extent, never omitted log-body properties. It receives no
tools, full fixture, expected root cause, source checkpoint, or task-model
history. Its fixed configuration and prompt hash are recorded. Grader calls,
usage and wall time are reported separately from task-model measurements.

Equivalent saved-only scope language can pass. A completed search or a known
total count does not mean that every matching row reached the model. An
affirmative claim about every member of a selection must also pass a
deterministic delivery guard, including complete lines, domain totals, result
identity and actual ranges. For example, delivery of 20 out of 21 matches cannot
support “all recovered.” Model uncertainty, missing evidence, invalid grader
output and transport failure never count as passes.

Before using the fixed semantic grader as an acceptance gate, calibrate it on immutable historical
answers with `--coverage-replay /absolute/path/to/summary.json`. A replay performs
only separately accounted grader calls; it does not rerun the agent or modify
the original report. Keep known equivalent statements, true incomplete-evidence
negatives, and ambiguous statements as separate classifications. Ambiguous
examples must not be silently relabeled to improve pass rates.

Example (run from the Go module, using an existing trusted provider config):

```sh
go run ./cmd/tool-result-eval \
  --providers /absolute/path/to/providers.json \
  --model glm-5.3 --scenario recovery --recovery-phase compact-persist \
  --budget-mode both --repeats 1 \
  --coverage-check semantic --coverage-judge-model glm-5.3 \
  --output /absolute/path/to/new-report-directory
```

Use a new output directory per attempt. Freeze the binary, prompt, configuration
hashes, and calibration policy before the new group. Do not tune the evaluator
after seeing new task results. Keep provider-blocked tasks separate from product
failures and grader errors separate from unsupported business conclusions.

Production evidence checks are reported separately from acceptance. The current
run's protected native reader results are counted only after a successful model
request actually contains their bounded bodies. `evidence_coverage` can emit one
optional checkpoint for a partially delivered selection; it is not a semantic
verdict or a requirement to read every page. A model may continue a necessary
page or limit its conclusion. Acknowledgements and unrelated strict JSON schemas
do not opt into this checkpoint. `checkpoint_sent` records queued host guidance;
request-level checkpoint hashes and `coverage_checkpoint_delivered` distinguish
successful delivery from an unconsumed notice.

Only a standalone JSON answer declaring `sources: string[]` and
`evidence[].quote: string` opts into the explicit verbatim contract. `quote_check`
uses actually delivered native body fragments, never the full saved store or a
fixture answer. One optional text-only `quote_repair` may change these quotations
while protecting every other value and the JSON shape. An accepted repair proves
literal reproduction only, not relevance or correct attribution. Missing or
capped delivery evidence remains unverified. These diagnostics never replace
the independent factual, citation or coverage acceptance tests.

Compaction keeps the same model, summary prompt, output limit, timeout and retry
policy. An exact GLM-5.3 client with no explicit reasoning effort receives a
compaction-only `low` default; explicit effort or non-enabled thinking settings
are retained, and other models and ordinary task requests are untouched. Requests
record safe timing, profile and received byte counts, including failures. Byte
counts are not token usage; unreported usage remains unknown, and a partial
summary never replaces the conversation.

## Short spill completion profile

### Saved JSON grouping and sorting

`--scenario grouped-records` is a separate, bounded acceptance task over the
existing 10,000-record observation. It filters `status=active`, returns the top
three region sums and the top five records by amount, using the native saved
result's `group_by`, `aggregate`, `sort` and `select`. It does not change the
records, logs or recovery task prompts or their historical verdicts.

```sh
tool-result-eval --providers /absolute/path/to/providers.json \
  --model glm-5.3 --scenario grouped-records --budget-mode auto \
  --repeats 1 --coverage-check exact --output /absolute/path/to/new-directory
```

Run the same frozen binary once with `--model k3` in a second new directory for
Kimi. The structured `scope=saved_observation` declaration avoids a fixed
natural-language coverage phrase; no separate model grader is called.

`grouped_evidence` accepts only bounded JSON bodies from successful, non-aborted
actual model requests. It binds body hashes, native reader receipts, source
headers and original result identity; checks the complete saved-array scan and
each delivered ranked group/row; and requires the initial MCP model view to
externalize raw rows without samples. Host recovery is independently checked
after the task and cannot supply missing model evidence. Conflicting duplicate
JSON fields, hidden pages and failed requests do not pass. The fixture's ranked
values have no ties, so tie behavior remains covered by deterministic native
tests rather than this two-model task. This profile is not a performance
benchmark or a passing verdict for the earlier broader calibration suite.

Use `quick_acceptance.py` with an existing frozen binary for routine spill
completion checks. This profile runs only three
new, fixed, single-attempt tasks: GLM compact-plus-persist, Kimi
compact-plus-persist, and Kimi-to-GLM model switching. At most two run in parallel.
It adds no grader requests, retries, new timeouts, compilation or deployment.

```sh
python3 cmd/tool-result-eval/quick_acceptance.py \
  --binary /absolute/path/to/tool-result-eval \
  --providers /absolute/path/to/providers.json \
  --output /absolute/path/to/new-acceptance-directory
```

`--inspect` prints inputs and commands without calls or writes; `--self-check`
runs a few offline runner checks. The output root may contain unrelated planning
or regression records, but the three job directories and runner artifacts must
not already exist. The runner hashes the binary, provider configuration, itself
and all Go sources before the tasks, then checks that they remain unchanged.
Each original `summary.json` and `process.log` is retained.

This is a separately named, user-authorized profile, not a passing result for
the earlier full calibration or eight-task matrix. The existing v9 calibration
result stays 29/30. The binary is invoked with `--coverage-check exact` solely to
avoid further grader calls; its exact-wording result and raw `passed` value are
preserved as legacy diagnostics. Neither is silently rewritten or treated as a
semantic scope decision.

`core_pass` requires completed tasks, strict JSON, all factual fields and four
verbatim evidence lines, four required evidence bodies actually delivered in
successful late model requests, one original MCP call, no file-tool bypass,
saved-byte recovery, original source stamp and lineage, valid citations, and
the requested lifecycle boundary. Exact coverage wording is excluded from this
new core metric. Missing or unfinished reports, failed task calls, absent bodies
and changed source/configuration never pass.

Scope remains `pending_scope_review` even when all three core checks pass. An
independent reviewer must read each final coverage statement against its actual
successful-delivery ledger and record a separate review bound to the original
summary hash. Equivalent wording is allowed. Reading 20 out of 21 matching rows
cannot establish properties of every matching row; a row read through another
selector must be counted as delivered without inventing other unread rows.
Unknown source completeness stays unknown. Only core checks plus this review can
complete the short profile, and that completion is not a full application
release or Docker deployment.

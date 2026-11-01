# Linux validation

From the repository root:

```sh
bash pogo/scripts/ci/run-linux.sh
```

This is also the only GitLab CI entry point. It builds a disposable candidate
image and runs it as `pwuser` on the Docker server's native Linux architecture
(`amd64` or `arm64`). `POGO_CI_PLATFORM` can require one of these architectures;
a mismatch fails instead of silently using emulation. Passing on one architecture
does not validate the other. `POGO_CI_ARTIFACTS` changes the local output directory
(default `.ci-artifacts/`). No real model is called.

## Runner requirements

The root `.gitlab-ci.yml` selects a dedicated **shell** runner tagged
`pogo-linux-docker`. It needs Bash, Python 3, Git and a Linux Docker daemon it may
use to build and run disposable containers. Docker access is privileged host
access: use a dedicated CI host without production services or extra mounted
production credentials, and only allow trusted project contributors to run code
there. Treat its repository checkout as confidential: `w_deploy/deploy.env` is
currently tracked by repository policy, so Git checkout/history still contains
it on the runner. The source filter keeps it out of the validation container;
it does not remove it from Git or the runner checkout. No runner is installed
or registered by this repository. For a pending job, check matching runner tags,
protected-runner rules, availability and capacity. Configuration alone is not a
successful CI run; a local check cannot establish whether GitLab has a usable
remote runner.

The image uses Go 1.26, Node 24, Playwright 1.63.0, Docker Compose, a C toolchain,
Python/openpyxl and bubblewrap. The Go module's toolchain requirement still
applies. Initial builds need access to the image registries, Ubuntu packages,
Go proxy and npm. Version families are pinned here; the generated logs
record the installed tool versions. Reproducible release image digests are a
separate release concern.

The runner kernel must permit unprivileged user namespaces. The validation
container drops all capabilities, keeps `no-new-privileges`, and uses the existing
`pogo/seccomp-bwrap.json` plus the browser profile's `setns`/`chroot` allowances
inside user namespaces. This temporary CI-only combined profile retains the
base default-deny policy; it adds no host capability. `systempaths=unconfined`
lets nested namespaces mount their private `/proc`. Chromium's sandbox remains enabled. If the host's
AppArmor/user-namespace policy blocks this, provision a suitable isolated runner;
do not disable Chromium/bubblewrap or switch to privileged mode to get a green job.
The container has no Docker socket, host ports or production volumes. Docker CLI
inside it is only used for synthetic Compose parsing without a reachable daemon.

## Coverage and fail-closed integrations

The candidate source is copied from current tracked **and untracked** files,
including uncommitted edits. Dependency folders, existing UI build outputs,
credentials and runtime state are excluded. Deployment input is an explicit
allowlist: tests, scripts and the browser overlay only. It does not read the
production `.env`, `.browser.env`, `deploy.env`, users, or base Compose file.
`source-manifest.json` records included file hashes. Symlinks fail closed.

The container runs:

- UI dependency install, type check, tests and build; Go embeds this new UI build.
- Go dependency install, build, vet and the full race-enabled suite.
- Every worker and evaluation test file, real Chromium included.
- All 20 no-model reference tasks and all 20 noop negative controls.
- Deployment/release mock tests and both browser-overlay Compose parsing tests.
- CI source-filter and required-integration-gate tests.

The entry point explicitly enables `SCOPE_REQUIRE_SANDBOX=1`,
`POGO_SKILL_RUNTIME_TEST_PLATFORM=1`, `POGO_SANDBOX=enforce`, and network isolation.
It builds `pogo-skill-runner` and starts a private Chromium worker with a synthetic
in-memory token. Both Go-to-browser integration tests receive its URL/token.
`check-go-results.py` requires explicit PASS for 13 browser, command-scope,
bubblewrap, Skill runtime, and non-root installation tests and rejects skipped
subtests. Every other skipped test is listed explicitly in `validation.log` and
`go-test.jsonl`; skips do not count as coverage. In particular, real YARA package
acceptance needs a separately supplied architecture-matching publisher archive
or payload (`POGO_SKILL_RUNTIME_TEST_ARCHIVE`, `POGO_SKILL_RUNTIME_TEST_PAYLOAD`,
`POGO_SKILL_RUNTIME_TEST_LOCAL`), and the manual panel-compat browser fixture
needs `POGO_PANEL_BROWSER_FIXTURE`. This source-only profile does not run those
external/manual acceptance tests. It does require its own real native Skill
runtime, Chromium and bubblewrap integration tests on every run.

The container and candidate image are removed on completion or interruption.
The dependency image is retained for Docker layer caching. Artifacts include
build/validation logs, Go JSON results, synthetic reference/noop reports, source
hashes and the native platform. GitLab retains them for 14 days even on failure.
There is no publish, registry login, deployment, production acceptance, or paid
model-evaluation job in this pipeline. The old nested GitHub workflow is not the
GitLab entry point.

#!/usr/bin/env bash
set -euo pipefail
[[ $(uname -s) == Linux && $(id -u) != 0 ]] || { printf 'Linux non-root execution required\n' >&2; exit 1; }
case "$(uname -m)" in x86_64|aarch64) ;; *) printf 'Unsupported native architecture\n' >&2; exit 1 ;; esac
cd /work/source
artifacts=/work/artifacts
mkdir -p "$artifacts" "$HOME/bin"
printf 'Linux architecture=%s uid=%s\n' "$(uname -m)" "$(id -u)"
go version
node --version
python3 --version
python3 -c 'import openpyxl; print("openpyxl " + openpyxl.__version__)'
docker compose version
bwrap --version
# Fail before compilation if this host cannot supply the required isolation.
bwrap --unshare-user --unshare-pid --unshare-net --ro-bind / / --proc /proc --dev /dev -- /bin/true
chromium=(/ms-playwright/chromium_headless_shell-*/chrome-*/chrome-headless-shell)
timeout 15 "${chromium[0]}" --headless --dump-dom about:blank \
  > "$artifacts/chromium-preflight.html" 2> "$artifacts/chromium-preflight.log"
python3 pogo/scripts/ci/ci_test.py

# Required tests must really exercise isolation, including the non-root Skill
# lifecycle. The JSON gate below rejects absent or skipped integration tests.
export POGO_SANDBOX=enforce POGO_SANDBOX_NET=0 SCOPE_REQUIRE_SANDBOX=1
export POGO_SKILL_RUNTIME_TEST_PLATFORM=1
export POGO_BROWSER_CHROMIUM_SANDBOX=1 GOMAXPROCS=4
cd pogo/web/ui
npm ci --no-audit --no-fund
npm run check
npm test
npm run build
cd ../..
go mod download
go build ./...
go vet ./...
go build -o "$HOME/bin/pogo-skill-runner" ./cmd/pogo-skill-runner

cd browser-runtime
npm ci --no-audit --no-fund
node -e 'console.log("Playwright " + require("playwright/package.json").version)'
# Serial files bound browser/process pressure without omitting any test file.
node --test --test-concurrency=1 test/*.test.js
node --test mock-platform/server.test.js
python3 -m unittest discover -s lab -p 'test_lab.py'
node --test --test-concurrency=1 eval/test/*.test.js
node --test eval/auth-auto/*.test.js eval/auth/diagnose.test.js
node eval/auth-auto/run.js --mode validate --out "$artifacts/auth-auto-reference"
node eval/auth-auto/run.js --mode validate --control noop --out "$artifacts/auth-auto-noop"
node --test --test-concurrency=1 eval/general-web/*.test.js eval/general-web/fixtures/*.test.js
node eval/general-web/run.js --mode validate --repeat 2 --out "$artifacts/general-web-reference"
node eval/general-web/run.js --mode validate --control noop --repeat 2 --out "$artifacts/general-web-noop"
node --test eval/evidence/evidence.test.js
node eval/evidence/run.js --mode validate --control reference --out "$artifacts/evidence-reference"
node eval/evidence/run.js --mode validate --control noop --out "$artifacts/evidence-noop"
node eval/run.js --mode validate --out "$artifacts/reference" --no-frames
node eval/run.js --mode validate --control noop --out "$artifacts/noop" --no-frames

# The worker and fixtures stay inside this container; no host port is published.
export POGO_BROWSER_TOKEN
POGO_BROWSER_TOKEN=$(node -e 'process.stdout.write(require("crypto").randomBytes(32).toString("hex"))')
export POGO_BROWSER_HOST=127.0.0.1 POGO_BROWSER_PORT=7788
node src/server.js > "$artifacts/worker.log" 2>&1 &
worker_pid=$!
trap 'kill "$worker_pid" 2>/dev/null || true; wait "$worker_pid" 2>/dev/null || true' EXIT
node --input-type=module - <<'JS'
let ready = false;
for (let i = 0; i < 100; i++) {
  try {
    const result = await fetch('http://127.0.0.1:7788/healthz', {
      headers: {authorization: `Bearer ${process.env.POGO_BROWSER_TOKEN}`},
      signal: AbortSignal.timeout(500),
    });
    if (result.ok) { ready = true; break; }
  } catch {}
  await new Promise(resolve => setTimeout(resolve, 100));
}
if (!ready) throw new Error('Required sandboxed Chromium worker failed readiness');
JS
export POGO_NATIVE_BROWSER_TEST_URL=http://127.0.0.1:7788
export POGO_NATIVE_BROWSER_TEST_TOKEN="$POGO_BROWSER_TOKEN"
cd ..
set +e
go test -json -race -count=1 ./... > "$artifacts/go-test.jsonl" 2> "$artifacts/go-test.stderr"
go_status=$?
python3 scripts/ci/check-go-results.py "$artifacts/go-test.jsonl"
gate_status=$?
set -e
if [[ $go_status != 0 || $gate_status != 0 ]]; then
  printf 'Go suite failed: tests=%s required-integrations=%s; see go-test artifacts\n' "$go_status" "$gate_status" >&2
  exit 1
fi
cd ..
python3 w_deploy/deploy_test.py
python3 w_deploy/compose_browser_test.py
python3 w_deploy/release_test.py
printf 'All native Linux validation gates passed\n'

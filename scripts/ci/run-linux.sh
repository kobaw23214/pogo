#!/usr/bin/env bash
# Local and GitLab use exactly the same native Linux validation container.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
artifacts=${POGO_CI_ARTIFACTS:-"$repo/.ci-artifacts"}
for command in docker python3 git; do command -v "$command" >/dev/null || { printf '%s required\n' "$command" >&2; exit 1; }; done
server=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')
case "$server" in
  linux/arm64|linux/aarch64) platform=linux/arm64 ;;
  linux/amd64|linux/x86_64) platform=linux/amd64 ;;
  *) printf 'Native Linux amd64/arm64 Docker server required (got %s)\n' "$server" >&2; exit 1 ;;
esac
# Do not let an ambient cross-platform default turn this into an emulated pass.
if [[ -n ${POGO_CI_PLATFORM:-} && $POGO_CI_PLATFORM != "$platform" ]]; then
  printf 'POGO_CI_PLATFORM must match Docker server %s; emulation is not native validation\n' "$platform" >&2
  exit 1
fi
mkdir -p "$artifacts"
artifacts=$(cd "$artifacts" && pwd)
temp=$(mktemp -d "${TMPDIR:-/tmp}/pogo-linux-ci.XXXXXX")
run_id="$(date +%s)-$$"
container="pogo-ci-$run_id"
candidate="pogo-ci-candidate:$run_id"
dependencies="pogo-ci-dependencies:pw163-node24-go126-${platform#linux/}"
dependency_run="$dependencies-$run_id"
created=0
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ $created == 1 ]]; then
    if ! docker cp "$container:/work/artifacts/." "$artifacts/" >/dev/null 2>&1; then
      printf 'Failed to export Linux validation artifacts\n' >&2
      if [[ $status == 0 ]]; then status=1; fi
    fi
  fi
  # Also remove our unique name if cancellation raced with `docker create`.
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm "$candidate" >/dev/null 2>&1 || true
  docker image rm "$dependency_run" >/dev/null 2>&1 || true
  rm -rf "$temp"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# This test container combines the two production isolation environments.
# bubblewrap needs mounts/pivot_root; Chromium additionally needs setns/chroot
# inside its user namespace (as in browser-runtime/seccomp.json). No host
# capability is granted; the default-deny base policy is retained.
python3 - "$repo/pogo/seccomp-bwrap.json" "$temp/seccomp-ci.json" <<'PY'
import json, sys
with open(sys.argv[1]) as source:
    policy = json.load(source)
policy["syscalls"].append({"names": ["setns", "chroot"], "action": "SCMP_ACT_ALLOW"})
with open(sys.argv[2], "w") as target:
    json.dump(policy, target)
PY
python3 "$repo/pogo/scripts/ci/snapshot.py" "$repo" "$temp/source"
cp "$temp/source/source-manifest.json" "$artifacts/source-manifest.json"
printf 'Docker server: %s; validation platform: %s\n' "$server" "$platform" | tee "$artifacts/platform.txt"
printf 'Building Linux test dependencies (log: %s/dependency-build.log)\n' "$artifacts"
if ! docker build --platform "$platform" -t "$dependencies" -t "$dependency_run" "$repo/pogo/scripts/ci" > "$artifacts/dependency-build.log" 2>&1; then
  tail -n 80 "$artifacts/dependency-build.log"
  exit 1
fi
cat > "$temp/Dockerfile" <<EOF
FROM $dependency_run
COPY --chown=pwuser:pwuser source /work/source
ENTRYPOINT ["bash", "/work/source/pogo/scripts/ci/linux-verify.sh"]
EOF
if ! docker build --platform "$platform" -t "$candidate" "$temp" > "$artifacts/candidate-build.log" 2>&1; then
  tail -n 80 "$artifacts/candidate-build.log"
  exit 1
fi
docker create --name "$container" --platform "$platform" --init \
  --cap-drop ALL --security-opt no-new-privileges=true \
  --security-opt "seccomp=$temp/seccomp-ci.json" \
  --security-opt systempaths=unconfined --shm-size 1g --pids-limit 2048 \
  --tmpfs /tmp:rw,exec,nosuid,size=3g,mode=1777 \
  "$candidate" >/dev/null
created=1
printf 'Running all validation gates (log: %s/validation.log)\n' "$artifacts"
if ! docker start -a "$container" > "$artifacts/validation.log" 2>&1; then
  tail -n 100 "$artifacts/validation.log"
  exit 1
fi
status=$(docker inspect --format '{{.State.ExitCode}}' "$container")
tail -n 30 "$artifacts/validation.log"
exit "$status"

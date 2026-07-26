#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
script="$repo_root/scripts/e2e-phase4.sh"
supervisor="$repo_root/cmd/e2e-processing-supervisor/main.go"
supervisor_dockerfile="$repo_root/Dockerfile.e2e-worker"
fake_provider="$repo_root/cmd/fake-ai-provider/main.go"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"

test -f "$script"
test -f "$supervisor"
test -f "$supervisor_dockerfile"
bash -n "$script"

grep -Fq '"$license_file" != /*' "$script"
grep -Fq '! -r "$license_file"' "$script"
grep -Fq 'prefix="happylearn_phase4_' "$script"
grep -Fq 'docker_bounded 60 network create --internal "$network"' "$script"
for resource in postgres redis minio worker app fake_ai processing_supervisor e2e_runner; do
  grep -Fq "\"\$$resource\"" "$script"
done
grep -Fq 'fake_ai_image=' "$script"
grep -Fq 'supervisor_image=' "$script"
grep -Fq 'Dockerfile.fake-ai' "$script"
grep -Fq 'Dockerfile.e2e-worker' "$script"
grep -Fq -- '--network-alias fake-ai' "$script"
grep -Fq 'http://fake-ai:8090/v1' "$script"
grep -Fq 'http://processing-supervisor:8092' "$script"
grep -Fq 'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=true' "$script"
grep -Fq 'HAPPYLEARN_AI_MASTER_KEY=' "$script"
grep -Fq 'E2E_AI_PROVIDER_KEY=' "$script"
grep -Fq 'openssl rand -base64 32' "$script"
grep -Fq 'chmod 0600 "$master_key_file" "$provider_key_file" "$control_key_file"' "$script"
! grep -Eq '(^|[[:space:]])(-p|--publish)([[:space:]=]|$)|--network host|--privileged|/var/run/docker.sock' "$script"
! grep -Eq '(^|[[:space:]])(set -x|env|printenv)([[:space:]]|$)' "$script"
grep -Fq -- '--read-only --user 10001:10001' "$script"
grep -Fq -- '--read-only --user 10002:10002' "$script"
grep -Fq -- '--cap-drop ALL --security-opt no-new-privileges' "$script"
grep -Fq -- '--memory 256m --cpus .2' "$script"
grep -Fq -- '--memory 1792m --cpus 1' "$script"
grep -Fq 'trap cleanup EXIT INT TERM' "$script"
grep -Fq 'docker_bounded 30 rm -f "${temporary_containers[@]}"' "$script"
grep -Fq 'docker_bounded 30 rm -f "${service_containers[@]}"' "$script"
grep -Fq 'docker_bounded 30 network rm "$network"' "$script"
grep -Fq 'docker_bounded 30 volume rm "$runner_volume" "$fixture_volume" "$data_volume"' "$script"
grep -Fq 'docker_bounded 60 image rm "$supervisor_image" "$fake_ai_image" "$worker_image" "$app_image"' "$script"
grep -Fq 'for container in "$postgres" "$redis" "$minio" "$worker" "$app" "$fake_ai" "$processing_supervisor"' "$script"
grep -Fq 'case "$e2e_group" in' "$script"
grep -Fq 'all|phase4)' "$script"
grep -Fq 'tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts' "$script"
grep -Fq -- '--grep @phase4-mobile' "$script"
grep -Fq 'test_status=0' "$script"
for phase in phase1 phase2 phase3 phase4; do
  grep -Fq "/artifacts/results/$phase" "$script"
done
grep -Fq 'sanitize-e2e-artifacts.sh' "$script"
grep -Fq 'artifact_dir="${E2E_ARTIFACT_DIR:-$PWD/test-results/phase4}"' "$script"

grep -Fq 'exec.Command("/app/happylearn-worker")' "$supervisor"
grep -Fq 'syscall.SIGSTOP' "$supervisor"
grep -Fq 'syscall.SIGCONT' "$supervisor"
grep -Fq '"/hold"' "$supervisor"
grep -Fq '"/release"' "$supervisor"
grep -Fq 'defer releaseWorker()' "$supervisor"
grep -Fq 'signal.NotifyContext' "$supervisor"
grep -Fq 'E2E_AI_PROCESSING_CONTROL_TOKEN' "$supervisor"
! grep -Fq '/var/run/docker.sock' "$supervisor"
grep -Fq 'E2E_AI_PROVIDER_KEY' "$fake_provider"

grep -Fq 'e2e-phase4:' "$makefile"
grep -Fq 'bash scripts/e2e-phase4.sh' "$makefile"
grep -Fq 'scripts/e2e-phase4_contract_test.sh' "$makefile"
grep -Fq '"e2e-phase4": "make e2e-phase4"' "$package_json"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
touch "${E2E_UNEXPECTED_DOCKER_CALL:?}"
exit 99
FAKE_DOCKER
chmod +x "$tmpdir/bin/docker"

assert_fail_fast() {
  local license="$1" group="$2" expected="$3" status=0
  rm -f "$tmpdir/docker-called"
  PATH="$tmpdir/bin:$PATH" E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$license" HAPPYLEARN_E2E_GROUP="$group" \
    "$script" >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  test "$status" -eq 2
  grep -Fq "$expected" "$tmpdir/stderr"
  test ! -s "$tmpdir/stdout"
  test ! -e "$tmpdir/docker-called"
}

relative_license="relative-license"
absolute_missing="$tmpdir/missing-license"
readable_license="$tmpdir/license"
: > "$readable_license"
assert_fail_fast "$relative_license" phase4 'absolute readable AIStor Free license file'
assert_fail_fast "$absolute_missing" phase4 'absolute readable AIStor Free license file'
assert_fail_fast "$readable_license" invalid 'HAPPYLEARN_E2E_GROUP must be all or phase4'

echo 'phase 4 E2E harness contract: PASS'

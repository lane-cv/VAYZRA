#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
script="$repo_root/scripts/e2e-phase4.sh"
supervisor="$repo_root/cmd/e2e-processing-supervisor/main.go"
supervisor_dockerfile="$repo_root/Dockerfile.e2e-worker"
fake_provider="$repo_root/cmd/fake-ai-provider/main.go"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"
playwright_config="$repo_root/playwright.config.ts"
source "$repo_root/scripts/e2e-harness-lib.sh"

reject_fixed_pattern() {
  local pattern="$1" file="$2"
  if grep -Fq -- "$pattern" "$file"; then
    echo "forbidden pattern in $file: $pattern" >&2
    return 1
  fi
}

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
grep -Fq -- '--network "container:$app"' "$script"
! grep -Fq -- '--network-alias fake-ai' "$script"
grep -Fq 'http://localhost:8090/v1' "$script"
grep -Fq 'http://app:8090/test/counts' "$script"
grep -Fq 'service_containers=("$fake_ai" "$app" "$worker" "$minio" "$redis" "$postgres")' "$script"
grep -Fq 'http://processing-supervisor:8092' "$script"
grep -Fq '"$install_runner" --network bridge --read-only --user 1000:1000' "$script"
grep -Fq 'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=true' "$script"
grep -Fq 'HAPPYLEARN_AI_MASTER_KEY=' "$script"
grep -Fq 'E2E_AI_PROVIDER_KEY=' "$script"
! grep -Eq -- '-e "?HAPPYLEARN_AI_MASTER_KEY=|-e "?E2E_AI_PROVIDER_KEY=|-e "?E2E_AI_PROCESSING_CONTROL_TOKEN=' "$script"
grep -Fq 'openssl rand -base64 32' "$script"
grep -Fq 'chmod 0600 "$master_key_file" "$provider_key_file" "$control_key_file"' "$script"
grep -Fq 'trap early_cleanup EXIT INT TERM' "$script"
! grep -Eq '(^|[[:space:]])(-p|--publish)([[:space:]=]|$)|--network host|--privileged|/var/run/docker.sock' "$script"
! grep -Eq '(^|[[:space:]])(set -x|env|printenv)([[:space:]]|$)' "$script"
grep -Fq -- '--read-only --user 10001:10001' "$script"
grep -Fq -- '--read-only --user 10002:10002' "$script"
grep -Fq -- '--cap-drop ALL --security-opt no-new-privileges' "$script"
grep -Fq -- '--memory 256m --cpus .2' "$script"
grep -Fq -- '--memory 1792m --cpus 1' "$script"
grep -Fq -- '--tmpfs /var/lib/postgresql:rw,noexec,nosuid,size=320m,uid=999,gid=999' "$script"
! grep -Fq -- '--tmpfs /var/lib/postgresql/data:' "$script"
grep -Fq -- "-c 'chmod 0750 /data && chown 1000:0 /data'" "$script"
grep -Fq -- "-c 'chmod 0700 /workspace /fixtures && chown 1000:1000 /workspace /fixtures'" "$script"
grep -Fq 'trap cleanup EXIT INT TERM' "$script"
grep -Fq 'docker_bounded 30 rm -f "${temporary_containers[@]}"' "$script"
grep -Fq 'docker_bounded 30 rm -f "${service_containers[@]}"' "$script"
grep -Fq 'docker_bounded 30 network rm "$network"' "$script"
grep -Fq 'docker_bounded 30 volume rm "$runner_volume" "$fixture_volume" "$secret_volume" "$data_volume"' "$script"
grep -Fq 'docker_bounded 60 image rm "$supervisor_image" "$fake_ai_image" "$worker_image" "$app_image"' "$script"
grep -Fq 'for container in "$postgres" "$redis" "$minio" "$worker" "$app" "$fake_ai" "$processing_supervisor"' "$script"
grep -Fq 'case "$e2e_group" in' "$script"
grep -Fq 'all|phase4)' "$script"
grep -Fq 'tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts' "$script"
grep -Fq -- '--grep @phase4-mobile' "$script"
grep -Fq -- '--project=mobile --grep @phase4-mobile' "$script"
reject_fixed_pattern '--project=phase4-mobile' "$script"
grep -Fq "name: 'mobile'" "$playwright_config"
grep -Fq 'grepInvert: /@phase4-mobile|@phase5-mobile/' "$playwright_config"
grep -Fq 'grep: /@phase4-mobile|@phase5-mobile/' "$playwright_config"
test "$(grep -Fc '@phase4-mobile|@phase5-mobile' "$playwright_config")" -eq 2
reject_fixed_pattern "name: 'phase4-mobile'" "$playwright_config"
grep -Fq 'test_status=0' "$script"
grep -Fq 'preserve_first_failure' "$script"
for phase in phase1 phase2 phase3 phase4; do
  grep -Fq "/artifacts/results/$phase" "$script"
done
grep -Fq 'sanitize-e2e-artifacts.sh' "$script"
grep -Fq 'allowed_artifact_root="$repo_root/test-results"' "$script"
grep -Fq 'artifact_input="${E2E_ARTIFACT_DIR:-$allowed_artifact_root/phase4}"' "$script"

service_memory_mib=$((384 + 96 + 384 + 64 + 256 + 1792))
test "$((service_memory_mib + 1024))" -le 4096
test "$((service_memory_mib + 1024))" -le 4096
service_cpu_hundredths=$((10 + 5 + 10 + 5 + 20 + 100))
test "$((service_cpu_hundredths + 50))" -le 200
test "$((service_cpu_hundredths + 50))" -le 200
test "$(preserve_first_failure 0 7)" -eq 7
test "$(preserve_first_failure 7 9)" -eq 7

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

isolated_repo="$tmpdir/isolated-repo"
mkdir -p "$isolated_repo/scripts" "$isolated_repo/test-results" "$tmpdir/outside" "$tmpdir/path-bin"
cp "$script" "$repo_root/scripts/e2e-harness-lib.sh" "$repo_root/scripts/init-e2e-artifacts.sh" \
  "$repo_root/scripts/sanitize-e2e-artifacts.sh" "$repo_root/scripts/publish-e2e-diagnostics.sh" "$isolated_repo/scripts/"
isolated_script="$isolated_repo/scripts/e2e-phase4.sh"
printf 'repo sentinel\n' > "$isolated_repo/sentinel"
printf 'outside sentinel\n' > "$tmpdir/outside/sentinel"
ln -s "$tmpdir/outside" "$isolated_repo/test-results/escape"
cp "$tmpdir/bin/docker" "$tmpdir/path-bin/docker"

assert_unsafe_artifact_rejected() {
  local candidate="$1" status=0
  rm -f "$tmpdir/docker-called"
  (
    cd "$isolated_repo"
    PATH="$tmpdir/path-bin:$PATH" E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
      HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" HAPPYLEARN_E2E_GROUP=phase4 \
      E2E_ARTIFACT_DIR="$candidate" "$isolated_script" >"$tmpdir/path-stdout" 2>"$tmpdir/path-stderr"
  ) || status=$?
  test "$status" -eq 2
  grep -Fq 'absolute safe directory below repository test-results' "$tmpdir/path-stderr"
  test ! -e "$tmpdir/docker-called"
  test "$(cat "$isolated_repo/sentinel")" = 'repo sentinel'
  test "$(cat "$tmpdir/outside/sentinel")" = 'outside sentinel'
}

assert_unsafe_artifact_rejected .
assert_unsafe_artifact_rejected "$isolated_repo/."
assert_unsafe_artifact_rejected "$isolated_repo"
assert_unsafe_artifact_rejected "$isolated_repo/test-results"
assert_unsafe_artifact_rejected "$isolated_repo/test-results/escape"

mkdir -p "$tmpdir/openssl-fail-bin" "$tmpdir/openssl-fail-tmp"
cat > "$tmpdir/openssl-fail-bin/openssl" <<'FAIL_OPENSSL'
#!/usr/bin/env bash
exit 42
FAIL_OPENSSL
chmod +x "$tmpdir/openssl-fail-bin/openssl"
key_failure_status=0
PATH="$tmpdir/openssl-fail-bin:$tmpdir/path-bin:$PATH" TMPDIR="$tmpdir/openssl-fail-tmp" \
  E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
  HAPPYLEARN_E2E_GROUP=phase4 E2E_ARTIFACT_DIR="$isolated_repo/test-results/key-failure" \
  "$isolated_script" >/dev/null 2>"$tmpdir/key-failure-stderr" || key_failure_status=$?
test "$key_failure_status" -eq 42
test -z "$(find "$tmpdir/openssl-fail-tmp" -mindepth 1 -print -quit)"
test ! -e "$tmpdir/docker-called"

mkdir -p "$tmpdir/openssl-signal-bin" "$tmpdir/openssl-signal-tmp"
cat > "$tmpdir/openssl-signal-bin/openssl" <<'SIGNAL_OPENSSL'
#!/usr/bin/env bash
touch "${E2E_OPENSSL_READY:?}"
trap 'exit 143' TERM INT
sleep 2
exit 42
SIGNAL_OPENSSL
chmod +x "$tmpdir/openssl-signal-bin/openssl"
PATH="$tmpdir/openssl-signal-bin:$tmpdir/path-bin:$PATH" TMPDIR="$tmpdir/openssl-signal-tmp" \
  E2E_OPENSSL_READY="$tmpdir/openssl-ready" E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" HAPPYLEARN_E2E_GROUP=phase4 \
  E2E_ARTIFACT_DIR="$isolated_repo/test-results/key-signal" \
  "$isolated_script" >/dev/null 2>"$tmpdir/key-signal-stderr" &
signal_pid=$!
for _ in $(seq 1 100); do [[ -e "$tmpdir/openssl-ready" ]] && break; sleep .05; done
test -e "$tmpdir/openssl-ready"
kill -TERM "$signal_pid"
signal_status=0
wait "$signal_pid" || signal_status=$?
test "$signal_status" -ne 0
test -z "$(find "$tmpdir/openssl-signal-tmp" -mindepth 1 -print -quit)"
test ! -e "$tmpdir/docker-called"

mkdir -p "$tmpdir/argv-bin" "$tmpdir/argv-tmp"
cat > "$tmpdir/argv-bin/openssl" <<'ARGV_OPENSSL'
#!/usr/bin/env bash
count_file="${E2E_OPENSSL_COUNT:?}"
count=0
[[ -f "$count_file" ]] && count="$(cat "$count_file")"
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
case "$count" in
  1) printf '%s\n' 'TUFTVEVSLVNlY3JldC0wMDAwMDAwMDAwMDAwMDAwMDA=' ;;
  2) printf '%s\n' 'UFJPVklERVItU2VjcmV0LTAwMDAwMDAwMDAwMDAwMDA=' ;;
  3) printf '%s\n' 'Q09OVFJPTC1TZWNyZXQtMDAwMDAwMDAwMDAwMDAwMDA=' ;;
  *) exit 3 ;;
esac
ARGV_OPENSSL
cat > "$tmpdir/argv-bin/docker" <<'ARGV_DOCKER'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${E2E_DOCKER_ARGV_LOG:?}"
if [[ "${1:-}" == run ]]; then
  name=''
  args=("$@")
  for ((index=0; index<${#args[@]}; index+=1)); do
    if [[ "${args[$index]}" == --name ]]; then name="${args[$((index+1))]}"; break; fi
  done
  : "$name"
fi
exit 0
ARGV_DOCKER
chmod +x "$tmpdir/argv-bin/openssl" "$tmpdir/argv-bin/docker"
argv_status=0
PATH="$tmpdir/argv-bin:$PATH" TMPDIR="$tmpdir/argv-tmp" E2E_OPENSSL_COUNT="$tmpdir/openssl-count" \
  E2E_DOCKER_ARGV_LOG="$tmpdir/docker-argv.log" HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
  HAPPYLEARN_E2E_GROUP=phase4 E2E_ARTIFACT_DIR="$isolated_repo/test-results/argv" \
  "$isolated_script" >/dev/null 2>"$tmpdir/argv-stderr" || argv_status=$?
test "$argv_status" -eq 0
for secret in \
  TUFTVEVSLVNlY3JldC0wMDAwMDAwMDAwMDAwMDAwMDA \
  UFJPVklERVItU2VjcmV0LTAwMDAwMDAwMDAwMDAwMDA \
  Q09OVFJPTC1TZWNyZXQtMDAwMDAwMDAwMDAwMDAwMDA; do
  ! grep -Fq "$secret" "$tmpdir/docker-argv.log"
done
test -z "$(find "$tmpdir/argv-tmp" -mindepth 1 -print -quit)"

echo 'phase 4 E2E harness contract: PASS'

#!/usr/bin/env bash
# shellcheck disable=SC2016
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
compose=$root/deploy/compose.prod.yml
runner=$root/scripts/e2e-phase6_resources.sh
fail() { printf 'phase6 resources contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -x $runner && ! -L $runner ]] || fail 'resource runner missing or unsafe'
for literal in \
  '1800' 'docker stats --no-stream' 'com.docker.compose.service' \
  'cpuPercent' 'memoryWorkingSetBytes' 'restartCount' 'health' 'timestamp' \
  'sampleIndex' 'expected_samples' 'elapsedSeconds' \
  'sample_index == "$expected_samples"' 'elapsed -ge "$duration"' \
  'requestLatencyBuckets' 'le25' 'le50' 'le100' 'le250' 'le500' 'le1000' \
  'aggregate_cpu' 'aggregate_memory' '4294967296' '200.000001' \
  'worker_drained_backup' 'backup_exclusive_seen' 'steady_seen'; do
  grep -Fq -- "$literal" "$runner" || fail "runner invariant missing: $literal"
done
! grep -Fq 'wc -l' "$runner" || fail 'resource proof counts service rows as samples'
value() { awk -v service="$1" -v key="$2" '$0 == "  " service ":" {inside=1; next} inside && /^  [a-zA-Z0-9_-]+:$/ {exit} inside && $1 == key ":" {print $2; exit}' "$compose"; }
mem_mib() { local raw; raw=$(value "$1" mem_limit); printf '%s\n' "${raw%m}"; }
cpu_milli() { awk -v v="$(value "$1" cpus)" 'BEGIN {printf "%d\n", v*1000+0.5}'; }
steady=(caddy app worker postgres redis minio)
backup=(caddy app postgres redis minio backup)
sum_mem=0; sum_cpu=0
for service in "${steady[@]}"; do sum_mem=$((sum_mem + $(mem_mib "$service"))); sum_cpu=$((sum_cpu + $(cpu_milli "$service"))); done
[[ $sum_mem == 3072 && $sum_cpu == 1850 && $((4096-sum_mem)) -ge 1024 ]] || fail 'steady ceiling mismatch'
sum_mem=0; sum_cpu=0
for service in "${backup[@]}"; do sum_mem=$((sum_mem + $(mem_mib "$service"))); sum_cpu=$((sum_cpu + $(cpu_milli "$service"))); done
[[ $sum_mem == 1792 && $sum_cpu == 1100 ]] || fail 'backup ceiling mismatch'
grep -Fq 'hl_compose "$project_dir" "$env_file" stop --timeout 60 worker app' "$root/scripts/prod-release.sh" || fail 'release drain missing'
grep -Fq 'stop_and_verify_worker' "$root/scripts/phase5-backup.sh" || fail 'backup/worker exclusion missing'
for literal in \
  'run_resource_acceptance' 'run_regression' 'resource_pid=$!' \
  'ingest_resource_host_sample' './cmd/host-sampler' 'internal/host-samples' \
  'resource sampler ended before the live backup window' \
  'run_manual_backup_acceptance' '--trigger manual' \
  'workerDrainedBackupObserved == true'; do
  grep -Fq -- "$literal" "$root/scripts/e2e-phase6.sh" || fail "resource orchestration missing: $literal"
done
bash -n "$runner"
printf '%s\n' 'phase6 resources contract: PASS'

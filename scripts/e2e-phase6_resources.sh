#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

project=${HAPPYLEARN_PHASE6_PROJECT:?}
output=${HAPPYLEARN_PHASE6_RESOURCE_OUTPUT:?}
https_base=${HAPPYLEARN_PHASE6_RESOURCE_HTTPS_BASE_URL:?}
hostname=${HAPPYLEARN_PHASE6_RESOURCE_HOSTNAME:?}
ca_file=${HAPPYLEARN_PHASE6_RESOURCE_CA_FILE:?}
duration=${HAPPYLEARN_PHASE6_RESOURCE_DURATION_SECONDS:-1800}
interval=${HAPPYLEARN_PHASE6_RESOURCE_INTERVAL_SECONDS:-10}
[[ $project =~ ^happylearn_phase6_[a-f0-9]{12}_prod$ ]] || exit 2
[[ $hostname =~ ^phase6-[a-f0-9]{12}[.]test$ ]] || exit 2
[[ $https_base =~ ^https://${hostname}:([0-9]{5})$ ]] || exit 2
https_port=${BASH_REMATCH[1]}
[[ -f $ca_file && ! -L $ca_file && -r $ca_file ]] || exit 2
[[ $duration == 1800 && $interval =~ ^[1-9][0-9]*$ && $interval -le 60 && $((duration % interval)) -eq 0 ]] || exit 2
case $output in /*/test-results/phase6/*/evidence/resources.ndjson) ;; *) exit 2 ;; esac
mkdir -p "$(dirname -- "$output")"
chmod 0700 "$(dirname -- "$output")"
: >"$output"; chmod 0600 "$output"

bytes() {
  local raw=$1 number unit multiplier
  raw=${raw//[[:space:]]/}
  [[ $raw =~ ^([0-9]+([.][0-9]+)?)(B|kB|KB|KiB|MB|MiB|GB|GiB)$ ]] || return 1
  number=${BASH_REMATCH[1]}; unit=${BASH_REMATCH[3]}
  case $unit in
    B) multiplier=1 ;;
    kB|KB) multiplier=1000 ;;
    KiB) multiplier=1024 ;;
    MB) multiplier=1000000 ;;
    MiB) multiplier=1048576 ;;
    GB) multiplier=1000000000 ;;
    GiB) multiplier=1073741824 ;;
  esac
  awk -v number="$number" -v multiplier="$multiplier" 'BEGIN {printf "%.0f\n", number * multiplier}'
}

record_latency_buckets() {
  local timestamp=$1 sample_index=$2 elapsed total milliseconds
  local le25=0 le50=0 le100=0 le250=0 le500=0 le1000=0 total_count=0
  for _ in 1 2 3 4; do
    elapsed=$(curl --noproxy '*' --fail --silent --show-error --output /dev/null \
      --connect-timeout 5 --max-time 10 --cacert "$ca_file" \
      --resolve "$hostname:$https_port:127.0.0.1" \
      --write-out '%{time_total}' "$https_base/api/v1/health/live") || return 1
    [[ $elapsed =~ ^[0-9]+[.][0-9]+$ ]] || return 1
    milliseconds=$(awk -v seconds="$elapsed" 'BEGIN {printf "%d\n", seconds * 1000 + 0.5}')
    ((total_count += 1))
    ((milliseconds <= 25)) && ((le25 += 1))
    ((milliseconds <= 50)) && ((le50 += 1))
    ((milliseconds <= 100)) && ((le100 += 1))
    ((milliseconds <= 250)) && ((le250 += 1))
    ((milliseconds <= 500)) && ((le500 += 1))
    ((milliseconds <= 1000)) && ((le1000 += 1))
  done
  total=$total_count
  jq -cn --arg timestamp "$timestamp" --argjson sampleIndex "$sample_index" \
    --argjson le25 "$le25" --argjson le50 "$le50" --argjson le100 "$le100" \
    --argjson le250 "$le250" --argjson le500 "$le500" --argjson le1000 "$le1000" \
    --argjson total "$total" \
    '{kind:"requestLatencyBuckets",timestamp:$timestamp,sampleIndex:$sampleIndex,unit:"milliseconds",cumulative:{le25:$le25,le50:$le50,le100:$le100,le250:$le250,le500:$le500,le1000:$le1000,inf:$total}}' \
    >>"$output"
}

started=$SECONDS
deadline=$((started + duration))
expected_samples=$((duration / interval))
sample_index=0
steady_seen=false
backup_exclusive_seen=false
while ((SECONDS < deadline)); do
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mapfile -t live_ids < <(docker ps --quiet --filter "label=com.docker.compose.project=$project")
  ((${#live_ids[@]} > 0)) || exit 1

  aggregate_cpu=0
  aggregate_memory=0
  worker_seen=false
  backup_seen=false
  for container in "${live_ids[@]}"; do
    metadata=$(docker inspect --format \
      '{"service":{{json (index .Config.Labels "com.docker.compose.service")}},"restartCount":{{.RestartCount}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}{{json .State.Status}}{{end}}}' \
      "$container") || exit 1
    service=$(jq -er '.service' <<<"$metadata") || exit 1
    [[ $service =~ ^(app|worker|caddy|postgres|redis|minio|backup)$ ]] || exit 1
    stats=$(docker stats --no-stream --format '{{json .}}' "$container") || exit 1
    cpu=$(jq -er '.CPUPerc | rtrimstr("%") | tonumber' <<<"$stats") || exit 1
    memory_raw=$(jq -er '.MemUsage | split("/")[0]' <<<"$stats") || exit 1
    memory=$(bytes "$memory_raw") || exit 1
    restart=$(jq -er '.restartCount' <<<"$metadata") || exit 1
    health=$(jq -er '.health' <<<"$metadata") || exit 1
    [[ $restart =~ ^[0-9]+$ && $health =~ ^(healthy|running)$ ]] || exit 1
    aggregate_cpu=$(awk -v total="$aggregate_cpu" -v value="$cpu" 'BEGIN {printf "%.6f\n", total + value}')
    aggregate_memory=$((aggregate_memory + memory))
    [[ $service == worker ]] && worker_seen=true
    [[ $service == backup ]] && backup_seen=true
    jq -cn --arg timestamp "$timestamp" --argjson sampleIndex "$sample_index" \
      --arg service "$service" --arg health "$health" --argjson cpuPercent "$cpu" \
      --argjson memoryWorkingSetBytes "$memory" --argjson restartCount "$restart" \
      '{kind:"service",timestamp:$timestamp,sampleIndex:$sampleIndex,service:$service,cpuPercent:$cpuPercent,memoryWorkingSetBytes:$memoryWorkingSetBytes,restartCount:$restartCount,health:$health}' \
      >>"$output"
  done

  phase=transition
  if [[ $backup_seen == true && $worker_seen == false ]]; then
    phase=worker_drained_backup
    backup_exclusive_seen=true
  elif [[ $worker_seen == true && $backup_seen == false ]]; then
    phase=steady
    steady_seen=true
  fi
  awk -v value="$aggregate_cpu" 'BEGIN {exit !(value <= 200.000001)}' || exit 1
  ((aggregate_memory <= 4294967296)) || exit 1
  jq -cn --arg timestamp "$timestamp" --argjson sampleIndex "$sample_index" \
    --arg phase "$phase" --argjson cpuPercent "$aggregate_cpu" \
    --argjson memoryWorkingSetBytes "$aggregate_memory" --argjson serviceCount "${#live_ids[@]}" \
    '{kind:"aggregate",timestamp:$timestamp,sampleIndex:$sampleIndex,phase:$phase,cpuPercent:$cpuPercent,memoryWorkingSetBytes:$memoryWorkingSetBytes,serviceCount:$serviceCount}' \
    >>"$output"
  record_latency_buckets "$timestamp" "$sample_index"

  sample_index=$((sample_index + 1))
  next_sample=$((started + sample_index * interval))
  if ((sample_index < expected_samples && SECONDS < next_sample)); then
    sleep "$((next_sample - SECONDS))"
  elif ((sample_index >= expected_samples && SECONDS < deadline)); then
    sleep "$((deadline - SECONDS))"
  fi
done
elapsed=$((SECONDS - started))
[[ $sample_index == "$expected_samples" && $elapsed -ge "$duration" && $elapsed -le $((duration + interval)) ]] || exit 1
[[ $steady_seen == true && $backup_exclusive_seen == true ]] || exit 1
printf '{"status":"pass","category":"resource_capture_30m","samples":%d,"elapsedSeconds":%d,"steadyObserved":true,"workerDrainedBackupObserved":true}\n' "$sample_index" "$elapsed"

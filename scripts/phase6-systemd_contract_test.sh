#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
template_dir=$root/deploy/systemd
renderer=$root/scripts/render-systemd.sh
dispatcher=$root/scripts/systemd-maintenance.sh
units=(
  happylearn-compose.service
  happylearn-host-sample.service happylearn-host-sample.timer
  happylearn-backup-dispatch.service happylearn-backup-dispatch.timer
  happylearn-backup-retry.service happylearn-backup-retry.timer
  happylearn-backup-retention.service happylearn-backup-retention.timer
  happylearn-restore-verify.service happylearn-restore-verify.timer
)

fail() { printf 'phase6 systemd contract: FAIL: %s\n' "$1" >&2; exit 1; }
for unit in "${units[@]}"; do
  [[ -f $template_dir/$unit && ! -L $template_dir/$unit ]] || fail "missing $unit"
done
[[ -x $renderer && ! -L $renderer ]] || fail 'renderer missing or not executable'
[[ -x $dispatcher && ! -L $dispatcher ]] || fail 'maintenance dispatcher missing or not executable'

tmp=$(mktemp -d "${TMPDIR:-/tmp}/phase6-systemd.XXXXXX")
trap 'rm -rf -- "$tmp"' EXIT HUP INT TERM
out=$tmp/rendered
mkdir -m 0700 "$out"
"$renderer" --project-dir "$root" --deployment-user "$(id -un)" --output-dir "$out" >"$tmp/manifest"

for unit in "${units[@]}"; do
  file=$out/$unit
  [[ -f $file && ! -L $file ]] || fail "renderer omitted $unit"
  grep -Eq "^${unit}[[:space:]]+[a-f0-9]{64}$" "$tmp/manifest" || fail "hash missing for $unit"
  grep -Fq "User=$(id -un)" "$file" || [[ $unit == *.timer ]] || fail "User missing in $unit"
  grep -Fq "Group=$(id -gn)" "$file" || [[ $unit == *.timer ]] || fail "Group missing in $unit"
done

services=("$out"/*.service)
for file in "${services[@]}"; do
  for setting in NoNewPrivileges=yes PrivateTmp=yes ProtectSystem=strict ProtectHome=yes RestrictSUIDSGID=yes ReadWritePaths=; do
    grep -Fq "$setting" "$file" || fail "$setting missing in $(basename "$file")"
  done
  grep -Eq '^ExecStart=/[^$`;&|]*$' "$file" || fail "ExecStart is not an absolute, shell-free command in $(basename "$file")"
  grep -Fq "$root" "$file" || fail "absolute project path missing in $(basename "$file")"
done

grep -Fq -- '--project-name happylearn-prod' "$out/happylearn-compose.service" || fail 'exact Compose project missing'
grep -Fq 'After=docker.service network-online.target' "$out/happylearn-compose.service" || fail 'Compose ordering missing'
grep -Fq 'Wants=docker.service network-online.target' "$out/happylearn-compose.service" || fail 'Compose availability dependencies missing'
grep -Fq 'OnCalendar=*-*-* *:*:00' "$out/happylearn-host-sample.timer" || fail 'host sample is not minutely'
grep -Fq 'OnCalendar=*-*-* *:*:00' "$out/happylearn-backup-dispatch.timer" || fail 'backup dispatch is not minutely'
grep -Fq 'OnCalendar=hourly' "$out/happylearn-backup-retry.timer" || fail 'backup retry is not hourly'
grep -Fq 'OnCalendar=*-*-* 03:30:00' "$out/happylearn-backup-retention.timer" || fail 'retention is not daily'
grep -Fq 'OnCalendar=quarterly' "$out/happylearn-restore-verify.timer" || fail 'restore verification is not quarterly'
grep -Fq -- '--task scheduled' "$out/happylearn-backup-dispatch.service" || fail 'scheduled database dispatcher missing'
grep -Fq -- '--task retry-degraded' "$out/happylearn-backup-retry.service" || fail 'degraded retry dispatcher missing'
grep -Fq -- '--task retention' "$out/happylearn-backup-retention.service" || fail 'retention dispatcher missing'
grep -Fq -- '--task restore-verify' "$out/happylearn-restore-verify.service" || fail 'restore verifier dispatcher missing'
for literal in "Asia/Shanghai" "time '03:00'" "--project happylearn-prod --trigger scheduled" \
  "--project happylearn-prod --trigger manual" "state='degraded'" "error_category='remote_unavailable'" \
  'local-date idempotent' 'run_scheduled'; do
  grep -Fq -- "$literal" "$dispatcher" || fail "dispatcher invariant missing: $literal"
done

if rg -n '(BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|password[[:space:]]*=|token[[:space:]]*=|secret[[:space:]]*=|/var/run/docker\.sock|/run/docker\.sock|\$\{|\$\(|`)' "$out" >/dev/null; then
  fail 'credential value, Docker socket, or shell interpolation found'
fi
if rg -n '/etc/systemd/system' "$renderer" "$template_dir" >/dev/null; then
  fail 'renderer or template references the host unit directory'
fi

bad=$tmp/relative
if "$renderer" --project-dir . --deployment-user "$(id -un)" --output-dir "$bad" >/dev/null 2>&1; then
  fail 'relative project directory accepted'
fi
if "$renderer" --project-dir "$root" --deployment-user 'bad user' --output-dir "$bad" >/dev/null 2>&1; then
  fail 'invalid deployment user accepted'
fi

if command -v systemd-analyze >/dev/null 2>&1; then
  if ! systemd_output=$(systemd-analyze verify "${services[@]}" "$out"/*.timer 2>&1); then
    stripped=$(sed -e '/^Failed to turn off SO_PASSRIGHTS on user lookup socket, ignoring: Operation not permitted$/d' \
      -e '/^Failed to enable SO_PASSCRED on handoff timestamp socket: Operation not permitted$/d' <<<"$systemd_output")
    if [[ -n $stripped || ${CI:-} == true ]]; then
      printf '%s\n' "$systemd_output" >&2
      fail 'systemd-analyze verify failed'
    fi
    printf '%s\n' 'phase6 systemd contract: systemd-analyze unavailable in this sandbox' >&2
  fi
elif [[ ${CI:-} == true && $(uname -s) == Linux ]]; then
  fail 'systemd-analyze is required on Linux CI'
fi

printf '%s\n' 'phase6 systemd contract: PASS'

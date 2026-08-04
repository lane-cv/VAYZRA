#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

exec env HOST_SAMPLER_BIN="$ROOT/.tools/bin/host-sampler" \
  "$ROOT/scripts/phase4-ai-operations.sh" run-with-env \
  "$ROOT/.env" "$ROOT/secrets/ai.env" \
  bash "$ROOT/scripts/collect-host-metrics.sh" --environment development

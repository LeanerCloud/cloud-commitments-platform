#!/usr/bin/env bash
# Runs the order-independent test families under fixed --randomize seeds so a
# new shared-state leak fails CI deterministically (#1970).
set -euo pipefail

FAMILIES=(history inventory plans-prefill plans-service-ids settings-save settings-purchasing-grace)
SEEDS=(1 7 15)

cd "$(dirname "$0")/../frontend"

files=()
for family in "${FAMILIES[@]}"; do
  matched=(src/__tests__/"$family"*.test.ts)
  [[ -e "${matched[0]}" ]] || { echo "no test files match $family*" >&2; exit 1; }
  files+=("${matched[@]}")
done

for seed in "${SEEDS[@]}"; do
  npx jest --runInBand --no-watchman --no-coverage --silent --randomize --seed="$seed" "${files[@]}"
done

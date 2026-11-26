#!/usr/bin/env bash
# Writes scenarios/NAME/report.md for every recorded scenario. Same data in, same report out.
set -euo pipefail
cd "$(dirname "$0")/.."
[ -x bin/ebg ] || go build -o bin/ebg ./cmd/ebg
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for d in scenarios/*/; do
  d=${d%/}; n=$(basename "$d")
  div=$(sed -n 's/^forecast_divisor: *//p' "$d/expected.yaml"); div=${div:-12}
  ./bin/ebg parse --slo configs/demo/slos.yaml --format json --interval 1s --stats-out "$tmp/q.json" "$d/access.log" > "$tmp/events.jsonl" 2> /dev/null
  ./bin/ebg report --slo configs/demo/slos.yaml --alerts configs/demo/alerts.yaml --policy configs/policy.yaml \
    --events "$tmp/events.jsonl" --quality "$tmp/q.json" --forecast-divisor "$div" \
    --title "Error budget report: $n" --out "$d/report.md"
  echo "wrote $d/report.md"
done

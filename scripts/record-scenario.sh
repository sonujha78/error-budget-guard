#!/usr/bin/env bash
# Records one scenario: starts the demo, scrapes /metrics, runs the load generator,
# and leaves access.log, metrics.txt and truth.json in scenarios/NAME/. Then stops the demo.
set -euo pipefail
name="${1:?usage: record-scenario.sh NAME}"
cd "$(dirname "$0")/.."
dir="scenarios/$name"
[ -f "$dir/scenario.yaml" ] || { echo "no $dir/scenario.yaml" >&2; exit 1; }
for p in 19080 19081; do
  if ss -ltn | grep -qE ":$p\b"; then echo "port $p is already in use" >&2; exit 1; fi
done
export EBG_OUT="$PWD/$dir"
dc=(docker compose -f demo/docker-compose.yml)
scraper=""
cleanup() {
  [ -n "$scraper" ] && kill "$scraper" 2>/dev/null || true
  "${dc[@]}" down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

rm -f "$dir/access.log" "$dir/metrics.txt" "$dir/truth.json"
"${dc[@]}" up -d --build ebg-app ebg-nginx > /tmp/ebg-up.log 2>&1 || { cat /tmp/ebg-up.log; exit 1; }
for _ in $(seq 1 30); do
  curl -sf http://127.0.0.1:19080/healthz >/dev/null && curl -sf http://127.0.0.1:19081/healthz >/dev/null && break
  sleep 1
done
curl -sf http://127.0.0.1:19081/healthz >/dev/null || { echo "demo did not start" >&2; exit 1; }

(
  while true; do
    ts=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
    if body=$(curl -sf --max-time 1 http://127.0.0.1:19081/metrics); then
      printf '# ebg-scrape ts=%s\n%s\n' "$ts" "$body"
    fi
    sleep 2
  done
) > "$dir/metrics.txt" &
scraper=$!
sleep 3   # a few baseline scrapes before the load starts

"${dc[@]}" run --rm -T ebg-loadgen -scenario "/scenarios/$name/scenario.yaml" -target http://ebg-nginx:80 -out /out/truth.json
sleep 4   # at least one scrape after the last request
echo "recorded $dir"

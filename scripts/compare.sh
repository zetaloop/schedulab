#!/usr/bin/env bash
set -euo pipefail

NS="${NS:-schedulab-bench}"
OUT="${OUT:-results}"
COUNT="${COUNT:-120}"
PARALLELISM="${PARALLELISM:-32}"

mkdir -p "$OUT"

kubectl apply -f workloads/namespace.yaml

python3 scripts/run-scheduling-benchmark.py default --namespace "$NS" --count "$COUNT" --parallelism "$PARALLELISM" --out-prefix "$OUT/default"
python3 scripts/run-scheduling-benchmark.py schedulab --namespace "$NS" --scheduler schedulab-scheduler --count "$COUNT" --parallelism "$PARALLELISM" --out-prefix "$OUT/schedulab"

python3 scripts/summarize-latency.py \
  default "$OUT/default-latency.csv" \
  schedulab "$OUT/schedulab-latency.csv" > "$OUT/summary.txt"

cat "$OUT/summary.txt"

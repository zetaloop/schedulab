#!/usr/bin/env bash
set -euo pipefail

NS="${NS:-schedulab-bench}"
OUT="${OUT:-results}"

mkdir -p "$OUT"

kubectl apply -f workloads/namespace.yaml

run_case() {
  local name="$1"
  local manifest="$2"
  local deployment="benchmark-$name"

  kubectl delete -f "$manifest" --ignore-not-found
  kubectl apply -f "$manifest"
  kubectl -n "$NS" rollout status "deployment/$deployment" --timeout=180s
  kubectl -n "$NS" wait --for=condition=Ready pods -l "app=$deployment" --timeout=60s
  sleep 3
  kubectl -n "$NS" get pods -l "app=$deployment" -o json > "$OUT/$name-pods.json"
  scripts/collect-scheduling-latency.py "$OUT/$name-pods.json" | sort -t, -k2 > "$OUT/$name-latency.csv"
  kubectl delete -f "$manifest" --ignore-not-found
}

run_case default workloads/benchmark-default.yaml
run_case schedulab workloads/benchmark-schedulab.yaml

scripts/summarize-latency.py \
  default "$OUT/default-latency.csv" \
  schedulab "$OUT/schedulab-latency.csv" > "$OUT/summary.txt"

cat "$OUT/summary.txt"


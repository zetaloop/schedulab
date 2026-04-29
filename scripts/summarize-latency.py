#!/usr/bin/env python3
import csv
import statistics
import sys
from collections import Counter


def read_latencies(path):
    with open(path, newline="", encoding="utf-8") as file:
        rows = list(csv.DictReader(file))
    if not rows:
        return [], [], Counter(), "scheduled_after_ack_ms"
    latency_field = "scheduled_after_ack_ms" if "scheduled_after_ack_ms" in rows[0] else "latency_seconds"
    values = [float(row[latency_field]) for row in rows if row[latency_field]]
    nodes = Counter(row["node"] for row in rows if row["node"])
    return rows, values, nodes, latency_field


def percentile(values, percent):
    if not values:
        return 0.0
    ordered = sorted(values)
    index = round((len(ordered) - 1) * percent / 100)
    return ordered[index]


def summarize(name, path):
    rows, values, nodes, latency_field = read_latencies(path)
    scheduled = len(values)
    total = len(rows)
    print(f"[{name}]")
    print(f"pods: {scheduled}/{total}")
    print(f"latency_source: {latency_field}")
    if values:
        unit = "milliseconds" if latency_field.endswith("_ms") else "seconds"
        print(f"avg_{unit}: {statistics.fmean(values):.3f}")
        print(f"p50_{unit}: {percentile(values, 50):.3f}")
        print(f"p95_{unit}: {percentile(values, 95):.3f}")
        print(f"max_{unit}: {max(values):.3f}")
    print("nodes:")
    for node, count in sorted(nodes.items()):
        print(f"  {node}: {count}")


if len(sys.argv) < 3 or len(sys.argv[1:]) % 2:
    raise SystemExit("usage: summarize-latency.py name csv [name csv ...]")

args = sys.argv[1:]
for i in range(0, len(args), 2):
    summarize(args[i], args[i + 1])

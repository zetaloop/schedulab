#!/usr/bin/env python3
import csv
import json
import sys
from datetime import datetime


def parse_time(value):
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def scheduled_at(pod):
    for condition in pod.get("status", {}).get("conditions", []):
        if condition.get("type") == "PodScheduled" and condition.get("status") == "True":
            return parse_time(condition.get("lastTransitionTime"))
    return None


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: collect-scheduling-latency.py pods.json")

    with open(sys.argv[1], encoding="utf-8") as file:
        data = json.load(file)

    writer = csv.writer(sys.stdout, lineterminator="\n")
    writer.writerow(["namespace", "pod", "node", "created_at", "scheduled_at", "latency_seconds"])

    for pod in data.get("items", []):
        metadata = pod.get("metadata", {})
        status = pod.get("status", {})
        created = parse_time(metadata.get("creationTimestamp"))
        scheduled = scheduled_at(pod)
        latency = ""
        if created and scheduled:
            latency = f"{(scheduled - created).total_seconds():.3f}"
        writer.writerow([
            metadata.get("namespace", ""),
            metadata.get("name", ""),
            status.get("nodeName", ""),
            metadata.get("creationTimestamp", ""),
            scheduled.isoformat() if scheduled else "",
            latency,
        ])


main()


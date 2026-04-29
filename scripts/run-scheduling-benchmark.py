#!/usr/bin/env python3
import argparse
import base64
import csv
import json
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import UTC, datetime


def run_kubectl(*args, input_data=None):
    result = subprocess.run(
        ["kubectl", *args],
        check=True,
        input=input_data,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    return result.stdout


def utc_now():
    return datetime.now(UTC)


def isoformat(value):
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def parse_time(value):
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def write_data_file(directory, name, value):
    path = f"{directory}/{name}"
    with open(path, "wb") as file:
        file.write(base64.b64decode(value))
    return path


def selected_config(items, name):
    for item in items:
        if item.get("name") == name:
            return item
    raise SystemExit(f"kubeconfig entry not found: {name}")


class KubernetesClient:
    def __init__(self):
        self.tempdir = tempfile.TemporaryDirectory()
        config = json.loads(run_kubectl("config", "view", "--raw", "-o", "json"))
        context = selected_config(config["contexts"], config["current-context"])["context"]
        cluster = selected_config(config["clusters"], context["cluster"])["cluster"]
        user = selected_config(config["users"], context["user"])["user"]

        self.server = cluster["server"].rstrip("/")
        self.headers = {"Accept": "application/json"}
        if token := user.get("token"):
            self.headers["Authorization"] = f"Bearer {token}"
        if token_file := user.get("tokenFile"):
            with open(token_file, encoding="utf-8") as file:
                self.headers["Authorization"] = f"Bearer {file.read().strip()}"

        if cluster.get("insecure-skip-tls-verify"):
            self.context = ssl._create_unverified_context()
        else:
            cafile = None
            if value := cluster.get("certificate-authority-data"):
                cafile = write_data_file(self.tempdir.name, "ca.crt", value)
            elif value := cluster.get("certificate-authority"):
                cafile = value
            self.context = ssl.create_default_context(cafile=cafile)

        cert = user.get("client-certificate-data")
        key = user.get("client-key-data")
        if cert and key:
            certfile = write_data_file(self.tempdir.name, "client.crt", cert)
            keyfile = write_data_file(self.tempdir.name, "client.key", key)
            self.context.load_cert_chain(certfile, keyfile)

    def request(self, method, path, body=None):
        data = None
        headers = dict(self.headers)
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(f"{self.server}{path}", data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, context=self.context, timeout=30) as response:
                content = response.read()
        except urllib.error.HTTPError as error:
            detail = error.read().decode(errors="replace")
            raise RuntimeError(f"Kubernetes API {method} {path} failed: {error.code} {detail}") from error
        if not content:
            return {}
        return json.loads(content)

    def create_pod(self, namespace, pod):
        return self.request("POST", f"/api/v1/namespaces/{urllib.parse.quote(namespace)}/pods", pod)

    def list_pods(self, namespace, selector):
        query = urllib.parse.urlencode({"labelSelector": selector})
        return self.request("GET", f"/api/v1/namespaces/{urllib.parse.quote(namespace)}/pods?{query}")

    def watch_pods(self, namespace, selector, resource_version, timeout_seconds):
        query = urllib.parse.urlencode({
            "allowWatchBookmarks": "true",
            "labelSelector": selector,
            "resourceVersion": resource_version,
            "timeoutSeconds": str(timeout_seconds),
            "watch": "true",
        })
        request = urllib.request.Request(
            f"{self.server}/api/v1/namespaces/{urllib.parse.quote(namespace)}/pods?{query}",
            headers=self.headers,
            method="GET",
        )
        with urllib.request.urlopen(request, context=self.context, timeout=timeout_seconds + 10) as response:
            for line in response:
                if line := line.strip():
                    yield json.loads(line)

    def delete_pods(self, namespace, selector):
        query = urllib.parse.urlencode({"labelSelector": selector})
        self.request("DELETE", f"/api/v1/namespaces/{urllib.parse.quote(namespace)}/pods?{query}")

    def list_events(self, namespace):
        try:
            return self.request("GET", f"/apis/events.k8s.io/v1/namespaces/{urllib.parse.quote(namespace)}/events")
        except RuntimeError:
            return self.request("GET", f"/api/v1/namespaces/{urllib.parse.quote(namespace)}/events")


def condition_time(pod):
    for condition in pod.get("status", {}).get("conditions", []):
        if condition.get("type") == "PodScheduled" and condition.get("status") == "True":
            return parse_time(condition.get("lastTransitionTime"))
    return None


def pod_manifest(namespace, name, scheduler_name, run_id):
    spec = {
        "terminationGracePeriodSeconds": 0,
        "containers": [
            {
                "name": "pause",
                "image": "registry.k8s.io/pause:3.10",
                "resources": {
                    "requests": {
                        "cpu": "20m",
                        "memory": "32Mi",
                    },
                },
            },
        ],
    }
    if scheduler_name:
        spec["schedulerName"] = scheduler_name

    label_value = scheduler_name or "default"
    return {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {
            "name": name,
            "namespace": namespace,
            "labels": {
                "app": f"benchmark-{label_value}",
                "schedulab.dev/benchmark": label_value,
                "schedulab.dev/run": run_id,
            },
        },
        "spec": spec,
    }


def scheduled_event_time(event, pod_names, started_at):
    reason = event.get("reason")
    regarding = event.get("regarding") or event.get("involvedObject") or {}
    pod_name = regarding.get("name")
    if reason != "Scheduled" or pod_name not in pod_names:
        return None, None
    event_time = parse_time(
        event.get("eventTime")
        or event.get("deprecatedFirstTimestamp")
        or event.get("firstTimestamp")
        or event.get("metadata", {}).get("creationTimestamp")
    )
    if event_time and event_time < started_at:
        return None, None
    return pod_name, event_time


def wait_for_deleted(client, namespace, selector, timeout_seconds):
    deadline = time.perf_counter() + timeout_seconds
    while time.perf_counter() < deadline:
        if not client.list_pods(namespace, selector).get("items"):
            return
        time.sleep(0.1)
    raise SystemExit(f"timed out waiting for old benchmark pods to be deleted: {selector}")


def create_one(client, namespace, pod):
    name = pod["metadata"]["name"]
    started_at = utc_now()
    started = time.perf_counter()
    response = client.create_pod(namespace, pod)
    finished = time.perf_counter()
    finished_at = utc_now()
    return name, {
        "create_started_at": started_at,
        "create_finished_at": finished_at,
        "create_started": started,
        "create_finished": finished,
        "request_latency_ms": (finished - started) * 1000,
        "response": response,
    }


def create_pods(client, namespace, pods, parallelism):
    created = {}
    with ThreadPoolExecutor(max_workers=parallelism) as executor:
        futures = [executor.submit(create_one, client, namespace, pod) for pod in pods]
        for future in as_completed(futures):
            name, data = future.result()
            created[name] = data
    return created


def wait_for_scheduled(client, namespace, selector, pod_names, timeout_seconds):
    deadline = time.perf_counter() + timeout_seconds
    observed = {}
    pods = {name: {} for name in pod_names}
    while time.perf_counter() < deadline:
        data = client.list_pods(namespace, selector)
        now = time.perf_counter()
        now_at = utc_now()
        for pod in data.get("items", []):
            name = pod.get("metadata", {}).get("name")
            if name in pods:
                pods[name] = pod
                if pod.get("spec", {}).get("nodeName") and name not in observed:
                    observed[name] = {"time": now, "at": now_at}
        if len(observed) == len(pod_names):
            return data, observed
        time.sleep(0.05)
    missing = sorted(set(pod_names) - set(observed))
    raise SystemExit(f"timed out waiting for pods to be scheduled: {', '.join(missing)}")


class PodWatcher:
    def __init__(self, client, namespace, selector, resource_version, pod_names, timeout_seconds):
        self.client = client
        self.namespace = namespace
        self.selector = selector
        self.resource_version = resource_version
        self.pod_names = set(pod_names)
        self.timeout_seconds = timeout_seconds
        self.ready = threading.Event()
        self.done = threading.Event()
        self.lock = threading.Lock()
        self.observed = {}
        self.error = None
        self.thread = threading.Thread(target=self.run, daemon=True)

    def start(self):
        self.thread.start()
        if not self.ready.wait(timeout=10):
            raise SystemExit("timed out starting pod watch")

    def run(self):
        try:
            events = self.client.watch_pods(self.namespace, self.selector, self.resource_version, self.timeout_seconds)
            self.ready.set()
            for event in events:
                pod = event.get("object") or {}
                name = pod.get("metadata", {}).get("name")
                if name not in self.pod_names or not pod.get("spec", {}).get("nodeName"):
                    continue
                with self.lock:
                    if name not in self.observed:
                        self.observed[name] = {"time": time.perf_counter(), "at": utc_now()}
                    if len(self.observed) == len(self.pod_names):
                        self.done.set()
                        break
        except Exception as err:  # noqa: BLE001
            self.error = err
        finally:
            self.ready.set()
            self.done.set()

    def wait(self):
        self.done.wait(timeout=self.timeout_seconds)
        if self.error:
            raise SystemExit(f"pod watch failed: {self.error}")
        with self.lock:
            observed = dict(self.observed)
        missing = sorted(self.pod_names - set(observed))
        if missing:
            raise SystemExit(f"timed out waiting for pod watch events: {', '.join(missing)}")
        return observed


def collect_scheduled_events(client, namespace, pod_names, started_at, timeout_seconds):
    deadline = time.perf_counter() + timeout_seconds
    pod_names = set(pod_names)
    events_data = {"items": []}
    event_times = {}
    while time.perf_counter() < deadline:
        events_data = client.list_events(namespace)
        event_times = {}
        for event in events_data.get("items", []):
            name, event_time = scheduled_event_time(event, pod_names, started_at)
            if name and event_time:
                current = event_times.get(name)
                if current is None or event_time < current:
                    event_times[name] = event_time
        if len(event_times) == len(pod_names):
            break
        time.sleep(0.05)
    return events_data, event_times


def write_json(path, data):
    with open(path, "w", encoding="utf-8") as file:
        json.dump(data, file, indent=2)


def write_csv(path, rows):
    with open(path, "w", newline="", encoding="utf-8") as file:
        writer = csv.DictWriter(file, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("name")
    parser.add_argument("--namespace", default="schedulab-bench")
    parser.add_argument("--scheduler", default="")
    parser.add_argument("--count", type=int, default=120)
    parser.add_argument("--out-prefix", required=True)
    parser.add_argument("--parallelism", type=int, default=32)
    parser.add_argument("--timeout", type=int, default=180)
    args = parser.parse_args()

    client = KubernetesClient()
    label_value = args.scheduler or "default"
    run_id = utc_now().strftime("%Y%m%d%H%M%S%f")
    selector = f"schedulab.dev/benchmark={label_value}"
    run_selector = f"schedulab.dev/run={run_id}"
    client.delete_pods(args.namespace, selector)
    wait_for_deleted(client, args.namespace, selector, args.timeout)

    pod_names = [f"benchmark-{args.name}-{run_id}-{index:03d}" for index in range(args.count)]
    pods = [pod_manifest(args.namespace, name, args.scheduler, run_id) for name in pod_names]
    batch_started_at = utc_now()
    initial_pods = client.list_pods(args.namespace, run_selector)
    resource_version = initial_pods.get("metadata", {}).get("resourceVersion", "")
    watcher = PodWatcher(client, args.namespace, run_selector, resource_version, pod_names, args.timeout)
    watcher.start()
    created = create_pods(client, args.namespace, pods, args.parallelism)
    observed = watcher.wait()
    pods_data = client.list_pods(args.namespace, run_selector)
    events_data, event_times = collect_scheduled_events(client, args.namespace, pod_names, batch_started_at, 10)

    pods_by_name = {pod.get("metadata", {}).get("name"): pod for pod in pods_data.get("items", [])}
    rows = []
    for name in pod_names:
        pod = pods_by_name[name]
        created_at = created[name]
        created_timestamp = parse_time(pod.get("metadata", {}).get("creationTimestamp"))
        scheduled_condition = condition_time(pod)
        scheduled_event = event_times.get(name)
        event_latency_ms = ""
        if scheduled_event and created_timestamp:
            event_latency_ms = f"{(scheduled_event - created_timestamp).total_seconds() * 1000:.3f}"
        condition_latency_ms = ""
        if scheduled_condition and created_timestamp:
            condition_latency_ms = f"{(scheduled_condition - created_timestamp).total_seconds() * 1000:.3f}"
        rows.append({
            "namespace": args.namespace,
            "pod": name,
            "node": pod.get("spec", {}).get("nodeName", ""),
            "create_started_at": isoformat(created_at["create_started_at"]),
            "create_finished_at": isoformat(created_at["create_finished_at"]),
            "scheduled_observed_at": isoformat(observed[name]["at"]),
            "created_at": pod.get("metadata", {}).get("creationTimestamp", ""),
            "scheduled_event_at": isoformat(scheduled_event) if scheduled_event else "",
            "scheduled_condition_at": isoformat(scheduled_condition) if scheduled_condition else "",
            "request_latency_ms": f"{created_at['request_latency_ms']:.3f}",
            "scheduled_after_ack_ms": f"{(observed[name]['time'] - created_at['create_finished']) * 1000:.3f}",
            "scheduled_after_request_ms": f"{(observed[name]['time'] - created_at['create_started']) * 1000:.3f}",
            "event_latency_ms": event_latency_ms,
            "condition_latency_ms": condition_latency_ms,
        })

    write_json(f"{args.out_prefix}-pods.json", pods_data)
    write_json(f"{args.out_prefix}-events.json", events_data)
    write_csv(f"{args.out_prefix}-latency.csv", rows)

    client.delete_pods(args.namespace, run_selector)
    wait_for_deleted(client, args.namespace, run_selector, args.timeout)


main()

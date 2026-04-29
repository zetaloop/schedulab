# schedulab

Kubernetes scheduler framework lab for experiment 9.

## What is included

- `cmd/scheduler`: scheduler binary with the lab plugin registered.
- `pkg/plugins/schedulab`: one plugin implementing `QueueSort`, `Filter`, `Score`, `Reserve`, `Permit`, `Bind`, and `EnqueueExtensions`.
- `config/scheduler-config.yaml`: `KubeSchedulerConfiguration` profile for `schedulab-scheduler`.
- `deploy/scheduler.yaml`: RBAC, ConfigMap, and Deployment for running the scheduler in `kube-system`.
- `workloads`: comparable workloads for the default scheduler and the lab scheduler.
- `scripts`: latency collection and comparison helpers.

## Build

```bash
go build -o bin/schedulab-scheduler ./cmd/scheduler
```

## Container image

```bash
docker build -t localhost:5001/schedulab-scheduler:latest .
docker push localhost:5001/schedulab-scheduler:latest
```

Update `deploy/scheduler.yaml` if the image name is different.

## Deploy

```bash
kubectl apply -f deploy/scheduler.yaml
kubectl -n kube-system rollout status deployment/schedulab-scheduler
```

Pods that should use this scheduler need:

```yaml
spec:
  schedulerName: schedulab-scheduler
```

## Compare with the default scheduler

```bash
scripts/compare.sh
```

The script writes JSON, CSV, and summary files under `results/`.


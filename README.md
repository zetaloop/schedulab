# schedulab

课程实验，一个基于 Kubernetes Scheduler Framework 的自定义调度器。

## 目录

- `cmd/scheduler` 调度器入口，注册 Schedulab 插件并启动 kube-scheduler。
- `pkg/plugins/schedulab` 调度插件，实现了 `QueueSort`、`Filter`、`Score`、`Reserve`、`Permit`、`Bind`、`EnqueueExtensions`、`SignPlugin`。
- `config/scheduler-config.yaml` 独立的 `KubeSchedulerConfiguration`，定义 `schedulab-scheduler` profile 及其插件启用配置。
- `deploy/scheduler.yaml` RBAC、ConfigMap、Deployment 清单，用于在 `kube-system` 部署自定义调度器。
- `workloads` 用于对比的测试负载，分别对应默认调度器和 Schedulab 调度器。
- `scripts` 延迟采集与汇总脚本。

## 构建

```bash
go build -o bin/schedulab-scheduler ./cmd/scheduler
```

或通过 Makefile：

```bash
make build
```

## 镜像

```bash
docker build -t localhost:5001/schedulab-scheduler:latest .
docker push localhost:5001/schedulab-scheduler:latest
```

如果镜像仓库或 tag 不同，请同步修改 `deploy/scheduler.yaml` 中的 image 字段。

## 部署

```bash
kubectl apply -f deploy/scheduler.yaml
kubectl -n kube-system rollout status deployment/schedulab-scheduler
```

使用此调度器的 Pod 需要指定：

```yaml
spec:
  schedulerName: schedulab-scheduler
```

## 性能对比

```bash
scripts/compare.sh
```

脚本会依次部署默认调度器和 Schedulab 调度器的同规模负载，采集 `PodScheduled` 延迟和节点分布，输出到 `results/` 目录：

- `default-pods.json`、`schedulab-pods.json` 全部 Pod 状态
- `default-latency.csv`、`schedulab-latency.csv` 每个 Pod 的调度延迟
- `summary.txt` 平均、p50、p95、最大值及节点分布汇总


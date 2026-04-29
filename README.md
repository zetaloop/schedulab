# schedulab

课程实验，一个基于 Kubernetes Scheduler Framework 的自定义调度器。

## 目录结构

- `cmd/scheduler` 调度器入口，注册 Schedulab 插件并启动 kube-scheduler。
- `pkg/plugins/schedulab` 调度插件，实现了 `QueueSort`、`Filter`、`Score`、`Reserve`、`Permit`、`Bind`、`EnqueueExtensions`、`SignPlugin`。
- `config/scheduler-config.yaml` 独立的 `KubeSchedulerConfiguration`，定义 `schedulab-scheduler` profile 及其各调度阶段的插件启用配置。
- `deploy/scheduler.yaml` RBAC、ConfigMap、Deployment 清单。
- `k3d-config.yaml` k3d 集群定义，1 个 server、3 个 agent、本地 registry 映射到 `127.0.0.1:5001`。
- `workloads` 默认调度器和 Schedulab 调度器的同规模对比负载。
- `scripts` 调度延迟采集、汇总与对比脚本。
- `Dockerfile` / `Makefile` 构建与镜像打包。

## 环境

需要本地安装：

- Docker Desktop
- k3d（`v5.8.3` 或更新）
- kubectl
- Go 1.26

## 完整流程

以下从零搭建集群、部署调度器、运行对比，最后清理。

### 1. 构建

```bash
go build -o bin/schedulab-scheduler ./cmd/scheduler
go test ./...
```

或通过 Makefile：

```bash
make build
```

### 2. 创建 k3d 集群

```bash
k3d cluster create --config k3d-config.yaml
```

这会创建一个名为 `scheduler-lab` 的集群，内含一个本地 registry `scheduler-lab-registry`，监听 `127.0.0.1:5001`。

确认集群就绪：

```bash
k3d cluster list
kubectl get nodes
```

### 3. 构建镜像并推送到本地 registry

```bash
docker build -t localhost:5001/schedulab-scheduler:latest .
docker push localhost:5001/schedulab-scheduler:latest
```

k3d 集群内部也能通过 `localhost:5001` 拉取这个 registry 中的镜像。

如果改了镜像名或 tag，同步更新 `deploy/scheduler.yaml` 中容器的 image 字段。

### 4. 部署调度器

```bash
kubectl apply -f deploy/scheduler.yaml
kubectl -n kube-system rollout status deployment/schedulab-scheduler
```

这会在 `kube-system` 里创建 ServiceAccount、ClusterRole、ClusterRoleBinding、ConfigMap（内含 `scheduler-config.yaml`），以及一个单副本的 Deployment。

验证调度器正常运行：

```bash
kubectl -n kube-system get pods -l app=schedulab-scheduler
kubectl -n kube-system logs deployment/schedulab-scheduler
```

日志中会列出加载的 `schedulab-scheduler` profile 和启动后的调度循环。

此后任何需要走实验调度器的 Pod 只需在 spec 中加入：

```yaml
spec:
  schedulerName: schedulab-scheduler
```

### 5. 运行性能对比

```bash
scripts/compare.sh
```

脚本依次完成：

1. 创建 `schedulab-bench` namespace
2. 部署 30 个 pause Pod 使用默认调度器，等待全部就绪
3. 采集 Pod 状态写入 `results/default-pods.json`，提取 `PodScheduled` 延迟到 `results/default-latency.csv`
4. 删除默认调度器的负载
5. 部署 30 个 pause Pod 使用 `schedulerName: schedulab-scheduler`，等待全部就绪
6. 采集到了 `results/schedulab-pods.json` 和 `results/schedulab-latency.csv`
7. 汇总两个 CSV，输出 `results/summary.txt`

`summary.txt` 包含每种调度器的已调度 Pod 数、平均延迟、p50、p95、最大值，以及 Pod 在各节点上的分布计数。

### 6. 清理

用完后删除集群：

```bash
k3d cluster delete scheduler-lab
```

### 产出

- 调度器代码入口 `pkg/plugins/schedulab/plugin.go`，包含 `Less`、`Filter`、`Score`、`Reserve`、`Permit`、`Bind` 实现，以及对应的 `plugin_test.go` 测试用例。
- 调度器配置 `config/scheduler-config.yaml`，在各阶段启用了 Schedulab 插件。
- `results/default-latency.csv` 和 `results/schedulab-latency.csv`：每个 Pod 的 `PodScheduled` 延迟。
- `results/summary.txt`：平均、p50、p95、最大延迟和节点分布汇总。
- 两种 workload 分别对应默认调度器和 `schedulerName: schedulab-scheduler`。


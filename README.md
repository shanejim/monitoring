# Kubernetes Monitoring

本目录维护本地 Docker Desktop Kubernetes 集群的监控系统，核心组件使用
`kube-prometheus-stack` 安装，并额外部署 Pushgateway、Blackbox Exporter 和
Lark/飞书告警适配器。

## 环境与版本

- Kubernetes：Docker Desktop Kubernetes `v1.36.1`，ARM64
- Namespace：`monitoring`
- Helm Release：`monitoring-stack`
- kube-prometheus-stack：`88.6.1`
- Pushgateway chart：`3.8.0`，应用版本 `v1.11.3`
- Blackbox Exporter chart：`11.17.2`，应用版本 `v0.28.0`
- Lark Alertmanager Adapter：`0.5.0`

核心栈包含 Prometheus、Alertmanager、Grafana、Prometheus Operator、
kube-state-metrics 和 node-exporter。Docker Desktop 的 controller-manager、
scheduler、etcd 和 kube-proxy 指标端点不可从 Pod 正常访问，因此对应监控默认关闭。

## 目录结构

```text
monitoring/
├── kube-prometheus-stack-values.yaml   # 核心监控栈 Helm values
├── upgrade.sh                          # 核心监控栈升级命令
├── rules/
│   └── ops-system-pod-resource-alerts.yaml
├── exporters/
│   ├── prometheus-pushgateway-values.yaml
│   ├── prometheus-blackbox-exporter-values.yaml
│   └── README.md
└── lark-adapter/
    ├── main.go
    ├── Dockerfile
    ├── deploy/
    └── README.md
```

## 核心配置

Prometheus 使用单副本和 `15Gi` PVC，数据最多保留 7 天或 12 GiB，任一限制先达到
即开始清理旧数据。Prometheus 会跨命名空间发现所有 `PrometheusRule`、
`ServiceMonitor`、`PodMonitor`、`Probe` 和 `ScrapeConfig`。

Alertmanager 使用单副本和 `2Gi` PVC。当前路由逻辑为：

1. `Watchdog` 发送到 `null`，不产生外部通知。
2. 标签 `ops_platform_managed="true"` 的告警发送到 `ops-platform-webhook`。
3. 其他告警使用根路由的默认接收器 `lark-webhook`。

路由中的 receiver 名称必须与 `receivers[].name` 完全一致，否则 Operator 会拒绝
新配置，Alertmanager 将继续使用上一次有效配置。

## 安装和升级核心栈

首次安装或升级均可使用：

```bash
helm upgrade --install monitoring-stack \
  oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack \
  --version 88.6.1 \
  --namespace monitoring \
  --create-namespace \
  --values /Users/shanejim/Code/infrastructure/monitoring/kube-prometheus-stack-values.yaml \
  --wait \
  --timeout 10m
```

已安装后也可以执行现有脚本：

```bash
sh /Users/shanejim/Code/infrastructure/monitoring/upgrade.sh
```

## 安装可选 Exporter

Pushgateway 仅用于无法被 Prometheus 及时抓取的短生命周期批处理任务。配置为单副本、
`1Gi` PVC，并将数据写入 `/data/pushgateway.db`。

```bash
helm upgrade --install prometheus-pushgateway \
  oci://ghcr.io/prometheus-community/charts/prometheus-pushgateway \
  --version 3.8.0 \
  --namespace monitoring \
  --values /Users/shanejim/Code/infrastructure/monitoring/exporters/prometheus-pushgateway-values.yaml \
  --wait \
  --timeout 10m
```

Blackbox Exporter 当前每 30 秒探测 Prometheus、Alertmanager 和 Grafana 的健康端点，
连续失败 1 分钟会触发 `BlackboxProbeFailed`。

```bash
helm upgrade --install prometheus-blackbox-exporter \
  oci://ghcr.io/prometheus-community/charts/prometheus-blackbox-exporter \
  --version 11.17.2 \
  --namespace monitoring \
  --values /Users/shanejim/Code/infrastructure/monitoring/exporters/prometheus-blackbox-exporter-values.yaml \
  --wait \
  --timeout 10m
```

更多说明见 [exporters/README.md](exporters/README.md)。

## 告警规则

应用自定义规则：

```bash
kubectl apply \
  --filename /Users/shanejim/Code/infrastructure/monitoring/rules/ops-system-pod-resource-alerts.yaml
```

`PrometheusRule` 所在的 namespace 只是资源存放位置，实际监控范围由 PromQL 中的标签
选择器决定。推荐为每条告警提供：

```yaml
labels:
  severity: warning
  team: ops
  ops_platform_managed: "true"
annotations:
  summary: "简短告警名称"
  description: "包含对象和当前值的说明"
  runbook_url: "https://docs.example.com/runbooks/example"
  dashboard_url: "http://localhost:3000/d/example"
```

Prometheus 不限制 `severity` 的取值，本项目约定使用 `critical`、`warning` 和 `info`。

## Lark 告警适配器

适配器把 Alertmanager webhook 转换为 Lark 交互式卡片，可展示 Runbook、Grafana、
Prometheus 和 Alertmanager 链接。构建并部署：

```bash
docker build \
  --tag lark-alertmanager-adapter:0.5.0 \
  /Users/shanejim/Code/infrastructure/monitoring/lark-adapter

kubectl apply \
  --kustomize /Users/shanejim/Code/infrastructure/monitoring/lark-adapter/deploy
```

详细配置见 [lark-adapter/README.md](lark-adapter/README.md)。

## Pod 访问 Mac 服务

Alertmanager 运行在 Kubernetes Pod 中。Pod 内的 `localhost` 指向 Pod 自身，不是 Mac。
访问直接运行在 Mac 上的 webhook 服务必须使用：

```text
http://host.docker.internal:<port>/<path>
```

Mac 服务需要监听 `0.0.0.0`，不能只监听 `127.0.0.1`。

## 本地访问

以下命令需要分别占用一个终端并持续运行：

```bash
kubectl port-forward -n monitoring service/monitoring-stack-grafana 3000:80
kubectl port-forward -n monitoring service/monitoring-stack-kube-prom-prometheus 9090:9090
kubectl port-forward -n monitoring service/monitoring-stack-kube-prom-alertmanager 9093:9093
kubectl port-forward -n monitoring service/prometheus-pushgateway 9091:9091
kubectl port-forward -n monitoring service/prometheus-blackbox-exporter 9115:9115
```

对应地址：

- Grafana：<http://localhost:3000>
- Prometheus：<http://localhost:9090/query>
- Alertmanager：<http://localhost:9093/#/alerts>
- Pushgateway：<http://localhost:9091>
- Blackbox Exporter：<http://localhost:9115>

`externalUrl` 只决定通知中生成的链接，不会自动建立端口转发。

## 验证与排查

查看 Release 和 Pod：

```bash
helm list --namespace monitoring
kubectl get pods,pvc --namespace monitoring
kubectl get prometheusrule,servicemonitor,podmonitor,probe --all-namespaces
```

查看 Alertmanager 实际加载的路由，不输出 webhook 凭据：

```bash
kubectl get --raw \
  '/api/v1/namespaces/monitoring/services/http:monitoring-stack-kube-prom-alertmanager:9093/proxy/api/v2/status' \
  | jq -r '.config.original' \
  | yq '{"route": .route, "receiver_names": [.receivers[].name]}'
```

查看当前告警实际匹配的 receiver：

```bash
kubectl get --raw \
  '/api/v1/namespaces/monitoring/services/http:monitoring-stack-kube-prom-alertmanager:9093/proxy/api/v2/alerts' \
  | jq '[.[] | {labels, receivers, status}]'
```

查看 Operator 是否拒绝了配置：

```bash
kubectl logs \
  --namespace monitoring \
  deployment/monitoring-stack-kube-prom-operator \
  --since=30m \
  | rg 'failed|error'
```

查看 Prometheus 中 Pushgateway 和 Blackbox targets：

```bash
kubectl get --raw \
  '/api/v1/namespaces/monitoring/services/http:monitoring-stack-kube-prom-prometheus:9090/proxy/api/v1/targets' \
  | jq -r '.data.activeTargets[] | [.labels.job, .labels.instance, .health, .lastError] | @tsv' \
  | rg 'pushgateway|blackbox'
```

Mac 进入系统睡眠后 Docker Desktop VM 和整个 Kubernetes 集群都会暂停，睡眠期间
Prometheus 不会采集，也不会在唤醒后补数据。仅关闭显示器但系统保持唤醒不会造成缺口。

## 凭据安全

不要把 Lark webhook、Bearer token、Grafana 管理员密码等凭据提交到 Git。生产环境应
通过 Kubernetes Secret、External Secrets、Sealed Secrets 或其他密钥管理系统注入。
执行 `helm template`、`helm get values` 或排障命令时，也应避免把完整 receiver URL 和
authorization 内容写入日志或聊天记录。

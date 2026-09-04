# Optional Prometheus exporters

These files prepare two optional components for the existing
`monitoring-stack` installation. Nothing in this directory is installed merely
by creating these files.

## Prepared releases

| Release | Chart | Application | Purpose |
|---|---:|---:|---|
| `prometheus-pushgateway` | `prometheus-pushgateway` 3.8.0 | Pushgateway v1.11.3 | Metrics from short-lived batch jobs that cannot be scraped |
| `prometheus-blackbox-exporter` | `prometheus-blackbox-exporter` 11.17.2 | Blackbox Exporter v0.28.0 | HTTP/TCP/DNS-style endpoint probing |

Both releases are intended for the `monitoring` namespace. The existing
Prometheus configuration already selects ServiceMonitor and PrometheusRule
objects across all namespaces and labels.

## Pushgateway behavior

The internal push endpoint will be:

```text
http://prometheus-pushgateway.monitoring.svc.cluster.local:9091
```

Pushgateway is configured as a single replica with a 1 GiB persistent volume.
It should only be used for short-lived, service-level batch jobs. Long-running
services should expose `/metrics` and be scraped directly. A job must delete
its pushed metric group when that group is no longer valid; Pushgateway does
not expire arbitrary pushed series automatically.

## Blackbox behavior

The initial target list probes the health endpoints of the current Prometheus,
Alertmanager, and Grafana services every 30 seconds. Add business endpoints to
`serviceMonitor.targets` using in-cluster service DNS or an externally reachable
URL. Do not use `localhost` for probe targets because `localhost` inside the
exporter pod refers to the exporter pod itself.

HTTP and TCP modules are prepared. ICMP is intentionally disabled because it
requires the `NET_RAW` capability. A `BlackboxProbeFailed` critical alert fires
after one minute of failed probes.

## Planned commands (not executed)

```bash
helm upgrade --install prometheus-pushgateway \
  oci://ghcr.io/prometheus-community/charts/prometheus-pushgateway \
  --version 3.8.0 \
  --namespace monitoring \
  --values /Users/shanejim/Code/infrastructure/monitoring/exporters/prometheus-pushgateway-values.yaml \
  --wait \
  --timeout 10m
```

```bash
helm upgrade --install prometheus-blackbox-exporter \
  oci://ghcr.io/prometheus-community/charts/prometheus-blackbox-exporter \
  --version 11.17.2 \
  --namespace monitoring \
  --values /Users/shanejim/Code/infrastructure/monitoring/exporters/prometheus-blackbox-exporter-values.yaml \
  --wait \
  --timeout 10m
```

Before installation, render both charts locally and review the generated
resources. After installation, verify the new ServiceMonitor targets and send
one disposable Pushgateway metric group through a Kubernetes Job.

## Other related components

- Prometheus Adapter: only needed when Kubernetes HPA should consume custom or
  external Prometheus metrics. It does not replace `metrics-server` for normal
  CPU/memory resource metrics.
- SNMP Exporter and database exporters: install only when the corresponding
  network devices or databases exist.
- Thanos: useful for multi-cluster, high availability, and object-storage-based
  long retention; unnecessary for this single local cluster at present.
- OpenTelemetry Collector: useful when applications emit OTLP metrics, traces,
  or logs; it is complementary rather than a required Prometheus component.


# Lark Alertmanager Adapter

This service accepts Alertmanager generic webhook notifications and sends them
to a Lark/Feishu custom bot as an interactive alert card. The card uses a
severity/status color, concise alert details, and Runbook, Grafana, Prometheus,
and Alertmanager action buttons when the corresponding URLs are available.

Set `annotations.dashboard_url` on a Prometheus alert rule to add a Grafana
button. Alert label templates can be used inside the URL, for example:

```yaml
annotations:
  dashboard_url: "https://grafana.example/d/pod-resources?var-namespace={{ $labels.namespace }}&var-pod={{ $labels.pod }}"
```

## Endpoints

- `POST /webhook?webhook_url=<URL-encoded URL>`: Alertmanager webhook receiver
- `GET /healthz`: liveness endpoint
- `GET /readyz`: readiness endpoint

## Configuration

| Environment variable | Required | Default | Description |
|---|---:|---:|---|
| `LISTEN_ADDRESS` | no | `:8080` | HTTP listen address |
| `HTTP_TIMEOUT` | no | `10s` | Timeout for a Lark request |
| `MAX_BODY_BYTES` | no | `1048576` | Maximum Alertmanager request size |
| `MAX_TEXT_RUNES` | no | `20000` | Maximum outgoing text length |
| `ALLOWED_WEBHOOK_HOSTS` | no | `open.feishu.cn,open.larksuite.com` | Allowed target hostnames |

The webhook URL is supplied by each Alertmanager request and is never logged.
Only HTTPS custom-bot URLs on the configured host allowlist are accepted. A
non-zero Lark business response code is treated as an HTTP 502 response so
Alertmanager can retry the notification.

## Test

```bash
go test ./...
```

## Build

```bash
docker build \
  --tag lark-alertmanager-adapter:0.5.0 \
  /Users/shanejim/Code/infrastructure/monitoring/lark-adapter
```

The Kubernetes cluster must be able to pull or otherwise access the built
image. Change `deploy/deployment.yaml` to a registry image when necessary.

## Deploy

Deploy the adapter:

```bash
kubectl apply \
  --kustomize /Users/shanejim/Code/infrastructure/monitoring/lark-adapter/deploy
```

Point Alertmanager at the in-cluster adapter instead of directly at Lark:

```yaml
alertmanager:
  config:
    receivers:
      - name: "null"
      - name: lark-webhook
        webhook_configs:
          - url: "http://lark-alertmanager-adapter.monitoring.svc.cluster.local:8080/webhook?webhook_url=URL_ENCODED_LARK_WEBHOOK"
            send_resolved: true
```

Add one receiver per business line, each with its own URL-encoded
`webhook_url` query parameter. The adapter also accepts the target in the
`X-Lark-Webhook-URL` header when a query parameter is unsuitable.

For example, route two namespaces to two different business-line robots:

```yaml
alertmanager:
  config:
    route:
      receiver: lark-platform
      routes:
        - receiver: lark-business-a
          matchers:
            - namespace = "business-a"
        - receiver: lark-business-b
          matchers:
            - namespace = "business-b"
    receivers:
      - name: lark-platform
        webhook_configs:
          - url: "http://lark-alertmanager-adapter.monitoring.svc.cluster.local:8080/webhook?webhook_url=URL_ENCODED_PLATFORM_WEBHOOK"
      - name: lark-business-a
        webhook_configs:
          - url: "http://lark-alertmanager-adapter.monitoring.svc.cluster.local:8080/webhook?webhook_url=URL_ENCODED_BUSINESS_A_WEBHOOK"
      - name: lark-business-b
        webhook_configs:
          - url: "http://lark-alertmanager-adapter.monitoring.svc.cluster.local:8080/webhook?webhook_url=URL_ENCODED_BUSINESS_B_WEBHOOK"
```

Then run the existing Helm upgrade script.

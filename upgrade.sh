helm upgrade monitoring-stack \
  oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack \
  --version 88.6.1 \
  --namespace monitoring \
  --values /Users/shanejim/Code/infrastructure/monitoring/kube-prometheus-stack-values.yaml \
  --wait \
  --timeout 10m

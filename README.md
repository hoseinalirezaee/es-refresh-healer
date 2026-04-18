# es-refresh-healer

`es-refresh-healer` is a small Kubernetes controller that watches `ExternalSecret` resources and detects when External Secrets Operator has stopped refreshing one longer than expected. When a resource is stale, it safely patches metadata annotations so ESO gets a fresh update event and reconciles the object again.

The controller does not create CRDs and does not modify generated Kubernetes `Secret` data.

## Why This Tool Is Needed

External Secrets Operator normally reconciles `ExternalSecret` resources on its own refresh cadence. In real clusters, that periodic chain can stall in edge cases: controller restarts, missed watches, provider/API blips, status updates that stop moving, or unusual timing around `refreshInterval`. When this happens, the rendered Kubernetes `Secret` can stay stale even though the `ExternalSecret` still exists and looks mostly healthy.

Stale secrets are operationally risky because credentials, tokens, certificates, and provider-backed values may rotate outside Kubernetes while workloads continue using old data.

`es-refresh-healer` adds a narrow safety net. It compares `status.refreshTime` against `spec.refreshInterval`, applies configurable lag thresholds and cooldowns, then patches only metadata:

```text
healer.external-secrets.io/last-kick=<unix-ts>
healer.external-secrets.io/last-reason=stale-refresh
```

That patch is enough to enqueue ESO reconciliation without taking ownership of the secret lifecycle.

## How It Works

The controller watches `ExternalSecret` resources and also performs a full scan on a ticker, defaulting to every `60s`. A resource is considered stale when:

```text
now - status.refreshTime > spec.refreshInterval * staleMultiplier + graceSeconds
```

If `spec.refreshInterval` is missing, the controller uses `defaultRefreshInterval`. If `status.refreshTime` is missing, the resource gets the same bootstrap grace window before being considered stale. Non-positive refresh intervals are skipped by default.

Safety controls:

- Per-object cooldown using both in-memory state and the `last-kick` annotation.
- Global token bucket rate limit for patches.
- Dry-run mode for observation without writes.
- Optional namespace allowlist and denylist.
- Leader election by default.
- Optional Kubernetes Events.
- Prometheus metrics and JSON structured logs.

## Metrics

The manager exposes metrics on `:8080/metrics` by default:

- `healer_scan_total`
- `healer_es_seen_total`
- `healer_es_stale_total`
- `healer_es_patched_total`
- `healer_es_patch_errors_total`
- `healer_es_refresh_lag_seconds`
- `healer_es_rate_limited_total`
- `healer_es_cooldown_skipped_total`

Decision logs include namespace, name, lag, interval, threshold, stale state, and action.

## Install With Helm

Add the published Helm repository:

```bash
helm repo add es-refresh-healer https://hoseinalirezaee.github.io/es-refresh-healer
helm repo update
```

Install a short-SHA chart version from the repository:

```bash
helm install es-refresh-healer es-refresh-healer/es-refresh-healer \
  --namespace es-refresh-healer \
  --create-namespace \
  --version 0.0.0-<short-sha> \
  --set controller.dryRun=true
```

Install in dry-run mode first:

```bash
helm install es-refresh-healer ./charts/es-refresh-healer \
  --namespace es-refresh-healer \
  --create-namespace \
  --set image.repository=ghcr.io/hoseinalirezaee/es-refresh-healer \
  --set image.tag=<short-sha> \
  --set controller.dryRun=true
```

Enable patching after observing detections:

```bash
helm upgrade es-refresh-healer ./charts/es-refresh-healer \
  --namespace es-refresh-healer \
  --set image.repository=ghcr.io/hoseinalirezaee/es-refresh-healer \
  --set image.tag=<short-sha> \
  --set controller.dryRun=false
```

Limit to selected namespaces:

```bash
helm upgrade es-refresh-healer ./charts/es-refresh-healer \
  --namespace es-refresh-healer \
  --set-json 'controller.watchNamespaces=["apps","platform"]'
```

## Configuration

| Helm value | Flag | Default |
| --- | --- | --- |
| `controller.scanInterval` | `--scan-interval` | `60s` |
| `controller.defaultRefreshInterval` | `--default-refresh-interval` | `1h` |
| `controller.staleMultiplier` | `--stale-multiplier` | `3` |
| `controller.graceSeconds` | `--grace-seconds` | `30` |
| `controller.cooldownSeconds` | `--cooldown-seconds` | `600` |
| `controller.maxPatchesPerMinute` | `--max-patches-per-minute` | `20` |
| `controller.dryRun` | `--dry-run` | `false` |
| `controller.watchNamespaces` | `--watch-namespaces` | all namespaces |
| `controller.denyNamespaces` | `--deny-namespaces` | none |
| `controller.logLevel` | `--log-level` | `info` |
| `controller.metricsAddr` | `--metrics-addr` | `:8080` |
| `controller.leaderElect` | `--leader-elect` | `true` |
| `controller.externalSecretVersion` | `--externalsecret-version` | `v1` |
| `controller.emitEvents` | `--emit-events` | `false` |
| `controller.allowZeroRefreshInterval` | `--allow-zero-refresh-interval` | `false` |
| `controller.maxAllowedLagSeconds` | `--max-allowed-lag-seconds` | `0` |

Most flags can also be configured with uppercase environment variables, for example `SCAN_INTERVAL`, `DRY_RUN`, or `WATCH_NAMESPACES`.

## Delivery Policy

Images are pushed only with short SHA tags:

```text
ghcr.io/hoseinalirezaee/es-refresh-healer:<short-sha>
```

The chart workflow packages a chart artifact named with the same short SHA and sets chart `appVersion` to that short SHA. The chart template defaults `image.tag` to `.Chart.AppVersion`, so installs from the packaged chart pin the short SHA unless explicitly overridden.

No semver release image tags are produced.

## Development

```bash
go test ./...
helm lint charts/es-refresh-healer
helm template es-refresh-healer charts/es-refresh-healer --set image.tag=$(git rev-parse --short=7 HEAD)
docker build -t ghcr.io/hoseinalirezaee/es-refresh-healer:$(git rev-parse --short=7 HEAD) .
```

Or use:

```bash
make verify
```

Run the kind-based E2E test against the current Kubernetes context:

```bash
make e2e-kind
```

GitHub Actions runs the E2E workflow against the latest four kind-backed Kubernetes cluster versions: `v1.35.1`, `v1.34.3`, `v1.33.4`, and `v1.32.8`. The test applies the real External Secrets Operator CRD bundle from `external-secrets/external-secrets` and creates `external-secrets.io/v1` `ExternalSecret` resources.

## Operational Runbook

1. Deploy with `controller.dryRun=true` for 24-72 hours.
2. Review `healer_es_stale_total`, logs, and any known stale-secret incidents.
3. Enable patching for one namespace with `controller.watchNamespaces`.
4. Watch `healer_es_patched_total`, `healer_es_patch_errors_total`, ESO logs, and API server write volume.
5. Expand namespace coverage or run cluster-wide.
6. Alert on repeated patch errors or unexpectedly high stale detections.

## Troubleshooting

If no resources are seen, confirm the ESO API version. The default is `external-secrets.io/v1`; set `controller.externalSecretVersion` if your cluster uses another served version such as legacy `v1beta1`.

If stale resources are detected but not patched, check dry-run mode, cooldown, and `maxPatchesPerMinute`.

If leader election fails, confirm the service account can manage `leases.coordination.k8s.io` in the release namespace.

If ServiceMonitor is enabled but metrics are not scraped, confirm the Prometheus Operator is installed and its selector matches `metrics.serviceMonitor.labels`.

## Limitations

This controller is intentionally narrow. It does not validate provider credentials, inspect rendered `Secret` data, coordinate across clusters, or attempt remediation beyond a metadata nudge. ESO remains the source of truth for reconciliation.

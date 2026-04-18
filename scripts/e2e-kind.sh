#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-es-refresh-healer}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-es-refresh-healer}"
IMAGE_TAG="${IMAGE_TAG:-e2e}"
TEST_NAMESPACE="${TEST_NAMESPACE:-es-refresh-healer-e2e}"
EXTERNALSECRET_API_VERSION="${EXTERNALSECRET_API_VERSION:-v1}"
ESO_CRD_BUNDLE_URL="${ESO_CRD_BUNDLE_URL:-https://raw.githubusercontent.com/external-secrets/external-secrets/helm-chart-2.3.0/deploy/crds/bundle.yaml}"

LAST_KICK='healer.external-secrets.io/last-kick'
LAST_REASON='healer.external-secrets.io/last-reason'

annotation() {
  local name="$1"
  local key="$2"
  kubectl get externalsecret "$name" \
    --namespace "$TEST_NAMESPACE" \
    --output "go-template={{ with index .metadata.annotations \"$key\" }}{{ . }}{{ end }}"
}

wait_for_annotation() {
  local name="$1"
  local key="$2"
  local timeout_seconds="${3:-90}"

  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if [[ -n "$(annotation "$name" "$key" 2>/dev/null || true)" ]]; then
      return 0
    fi
    sleep 2
  done

  kubectl get externalsecret "$name" --namespace "$TEST_NAMESPACE" --output yaml
  kubectl logs --namespace es-refresh-healer deploy/es-refresh-healer --tail=200
  return 1
}

echo "Installing real External Secrets Operator CRDs"
kubectl apply --server-side --force-conflicts --filename "$ESO_CRD_BUNDLE_URL"
kubectl wait --for=condition=Established crd/externalsecrets.external-secrets.io --timeout=60s
kubectl get crd externalsecrets.external-secrets.io \
  --output "jsonpath={.spec.versions[?(@.name=='${EXTERNALSECRET_API_VERSION}')].served}" \
  | grep -qx true

echo "Creating E2E fixtures"
kubectl create namespace "$TEST_NAMESPACE" --dry-run=client --output yaml | kubectl apply --filename -

now_rfc3339="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
now_unix="$(date -u +%s)"

cat <<EOF | kubectl apply --filename -
apiVersion: external-secrets.io/${EXTERNALSECRET_API_VERSION}
kind: ExternalSecret
metadata:
  name: stale-secret
  namespace: ${TEST_NAMESPACE}
spec:
  refreshInterval: 1s
---
apiVersion: external-secrets.io/${EXTERNALSECRET_API_VERSION}
kind: ExternalSecret
metadata:
  name: fresh-secret
  namespace: ${TEST_NAMESPACE}
spec:
  refreshInterval: 1h
---
apiVersion: external-secrets.io/${EXTERNALSECRET_API_VERSION}
kind: ExternalSecret
metadata:
  name: cooldown-secret
  namespace: ${TEST_NAMESPACE}
  annotations:
    ${LAST_KICK}: "${now_unix}"
spec:
  refreshInterval: 1s
EOF

kubectl patch externalsecret stale-secret \
  --namespace "$TEST_NAMESPACE" \
  --subresource=status \
  --type=merge \
  --patch '{"status":{"refreshTime":"2000-01-01T00:00:00Z"}}'

kubectl patch externalsecret fresh-secret \
  --namespace "$TEST_NAMESPACE" \
  --subresource=status \
  --type=merge \
  --patch "{\"status\":{\"refreshTime\":\"${now_rfc3339}\"}}"

kubectl patch externalsecret cooldown-secret \
  --namespace "$TEST_NAMESPACE" \
  --subresource=status \
  --type=merge \
  --patch '{"status":{"refreshTime":"2000-01-01T00:00:00Z"}}'

echo "Building and loading controller image"
docker build --tag "${IMAGE_REPOSITORY}:${IMAGE_TAG}" .
kind load docker-image "${IMAGE_REPOSITORY}:${IMAGE_TAG}" --name "$CLUSTER_NAME"

echo "Installing chart"
helm upgrade --install es-refresh-healer ./charts/es-refresh-healer \
  --namespace es-refresh-healer \
  --create-namespace \
  --wait \
  --timeout 2m \
  --set image.repository="$IMAGE_REPOSITORY" \
  --set image.tag="$IMAGE_TAG" \
  --set image.pullPolicy=Never \
  --set controller.leaderElect=false \
  --set controller.externalSecretVersion="$EXTERNALSECRET_API_VERSION" \
  --set controller.scanInterval=2s \
  --set controller.defaultRefreshInterval=10s \
  --set controller.staleMultiplier=1 \
  --set controller.graceSeconds=0 \
  --set controller.cooldownSeconds=120 \
  --set controller.maxPatchesPerMinute=100 \
  --set controller.dryRun=false

kubectl rollout status deploy/es-refresh-healer --namespace es-refresh-healer --timeout=90s

echo "Verifying stale ExternalSecret is patched"
wait_for_annotation stale-secret "$LAST_KICK" 90

stale_reason="$(annotation stale-secret "$LAST_REASON")"
if [[ "$stale_reason" != "stale-refresh" ]]; then
  echo "expected stale-secret ${LAST_REASON}=stale-refresh, got ${stale_reason}"
  exit 1
fi

echo "Verifying fresh ExternalSecret is untouched"
sleep 8
fresh_kick="$(annotation fresh-secret "$LAST_KICK" 2>/dev/null || true)"
if [[ -n "$fresh_kick" ]]; then
  echo "fresh-secret was patched unexpectedly: ${LAST_KICK}=${fresh_kick}"
  kubectl get externalsecret fresh-secret --namespace "$TEST_NAMESPACE" --output yaml
  exit 1
fi

echo "Verifying cooldown blocks repeated patching"
cooldown_reason="$(annotation cooldown-secret "$LAST_REASON" 2>/dev/null || true)"
if [[ -n "$cooldown_reason" ]]; then
  echo "cooldown-secret was patched despite cooldown: ${LAST_REASON}=${cooldown_reason}"
  kubectl get externalsecret cooldown-secret --namespace "$TEST_NAMESPACE" --output yaml
  exit 1
fi

echo "E2E test passed"

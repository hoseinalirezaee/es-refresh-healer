#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-es-refresh-healer}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-es-refresh-healer}"
IMAGE_TAG="${IMAGE_TAG:-e2e}"
IMAGE_PULL_POLICY="${IMAGE_PULL_POLICY:-Never}"
BUILD_CONTROLLER_IMAGE="${BUILD_CONTROLLER_IMAGE:-true}"
TEST_NAMESPACE="${TEST_NAMESPACE:-es-refresh-healer-e2e}"
EXTERNALSECRET_API_VERSION="${EXTERNALSECRET_API_VERSION:-v1}"
ESO_CRD_BUNDLE_URL="${ESO_CRD_BUNDLE_URL:-https://raw.githubusercontent.com/external-secrets/external-secrets/helm-chart-2.3.0/deploy/crds/bundle.yaml}"
HELM_RELEASE_NAME="${HELM_RELEASE_NAME:-es-refresh-healer}"
HELM_NAMESPACE="${HELM_NAMESPACE:-es-refresh-healer}"
HELM_CHART_REF="${HELM_CHART_REF:-./charts/es-refresh-healer}"
HELM_CHART_VERSION="${HELM_CHART_VERSION:-}"
HELM_REPOSITORY_NAME="${HELM_REPOSITORY_NAME:-}"
HELM_REPOSITORY_URL="${HELM_REPOSITORY_URL:-}"

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
  kubectl logs --namespace "$HELM_NAMESPACE" "deploy/${HELM_RELEASE_NAME}" --tail=200
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

if [[ "$BUILD_CONTROLLER_IMAGE" == "true" ]]; then
  echo "Building and loading controller image"
  docker build --tag "${IMAGE_REPOSITORY}:${IMAGE_TAG}" .
  kind load docker-image "${IMAGE_REPOSITORY}:${IMAGE_TAG}" --name "$CLUSTER_NAME"
else
  echo "Using published controller image ${IMAGE_REPOSITORY}:${IMAGE_TAG}"
fi

echo "Installing chart"
if [[ -n "$HELM_REPOSITORY_URL" ]]; then
  helm repo add "${HELM_REPOSITORY_NAME:-es-refresh-healer}" "$HELM_REPOSITORY_URL" --force-update
  for attempt in $(seq 1 40); do
    helm repo update "${HELM_REPOSITORY_NAME:-es-refresh-healer}"
    chart_args=("$HELM_CHART_REF")
    if [[ -n "$HELM_CHART_VERSION" ]]; then
      chart_args+=(--version "$HELM_CHART_VERSION")
    fi
    if helm show chart "${chart_args[@]}" >/dev/null 2>&1; then
      break
    fi
    if [[ "$attempt" == "40" ]]; then
      echo "timed out waiting for Helm chart ${HELM_CHART_REF} ${HELM_CHART_VERSION}"
      exit 1
    fi
    sleep 15
  done
fi

helm_args=(
  upgrade
  --install
  "$HELM_RELEASE_NAME"
  "$HELM_CHART_REF"
  --namespace "$HELM_NAMESPACE"
  --create-namespace
  --wait
  --timeout 2m
  --set image.repository="$IMAGE_REPOSITORY"
  --set image.tag="$IMAGE_TAG"
  --set image.pullPolicy="$IMAGE_PULL_POLICY"
  --set controller.externalSecretVersion="$EXTERNALSECRET_API_VERSION"
  --set controller.scanInterval=2s
  --set controller.defaultRefreshInterval=10s
  --set controller.staleMultiplier=1
  --set controller.graceSeconds=0
  --set controller.cooldownSeconds=120
  --set controller.maxPatchesPerMinute=100
  --set controller.dryRun=false
)

if [[ -n "$HELM_CHART_VERSION" ]]; then
  helm_args+=(--version "$HELM_CHART_VERSION")
fi

helm "${helm_args[@]}"

kubectl rollout status "deploy/${HELM_RELEASE_NAME}" --namespace "$HELM_NAMESPACE" --timeout=90s

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

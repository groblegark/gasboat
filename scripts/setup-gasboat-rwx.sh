#!/usr/bin/env bash
# setup-gasboat-rwx.sh — Create the gasboat-rwx namespace, deploy the Helm chart,
# and provision the kd-beads-token secret.
#
# Mirrors the gasboat-e2e namespace setup but targets the RWX cluster infra.
# Requires kubectl + helm with cluster-level namespace-create permissions.
#
# Usage:
#   ./scripts/setup-gasboat-rwx.sh [--token TOKEN] [--chart-ref oci://ghcr.io/groblegark/charts/gasboat]
#
#   --token TOKEN   Beads daemon auth token. If omitted a random token is generated.
#   --chart-ref REF Helm chart reference (default: local helm/gasboat/).
#   --dry-run       Print commands without executing.
set -euo pipefail

NAMESPACE="gasboat-rwx"
RELEASE="gasboat-rwx"
CHART_REF="${CHART_REF:-helm/gasboat/}"
VALUES_FILE="helm/values-rwx.yaml"
TOKEN=""
DRY_RUN=false

usage() {
  grep '^#' "$0" | sed 's/^# \{0,2\}//' | head -20
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --chart-ref) CHART_REF="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    -h|--help) usage ;;
    *) echo "Unknown argument: $1" >&2; usage ;;
  esac
done

run() {
  if $DRY_RUN; then
    echo "[dry-run] $*"
  else
    "$@"
  fi
}

# ── 1. Namespace ─────────────────────────────────────────────────────────────
echo "==> Creating namespace ${NAMESPACE}"
if kubectl get namespace "${NAMESPACE}" &>/dev/null; then
  echo "    Namespace ${NAMESPACE} already exists — skipping"
else
  run kubectl create namespace "${NAMESPACE}"
fi

# ── 2. kd-beads-token secret ─────────────────────────────────────────────────
echo "==> Provisioning kd-beads-token secret in ${NAMESPACE}"
if [ -z "${TOKEN}" ]; then
  TOKEN="$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 32)"
  echo "    Generated random token: ${TOKEN}"
fi

if kubectl -n "${NAMESPACE}" get secret kd-beads-token &>/dev/null; then
  echo "    Secret kd-beads-token already exists — patching"
  run kubectl -n "${NAMESPACE}" patch secret kd-beads-token \
    --type=merge \
    -p "{\"stringData\":{\"token\":\"${TOKEN}\"}}"
else
  run kubectl -n "${NAMESPACE}" create secret generic kd-beads-token \
    --from-literal="token=${TOKEN}"
fi

# ── 3. Helm deploy ───────────────────────────────────────────────────────────
echo "==> Deploying ${RELEASE} chart to namespace ${NAMESPACE}"
run helm upgrade --install "${RELEASE}" "${CHART_REF}" \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  -f "${VALUES_FILE}" \
  --wait \
  --timeout 5m

# ── 4. Smoke check ───────────────────────────────────────────────────────────
echo "==> Smoke check: waiting for beads service"
BEADS_SVC="${RELEASE}-beads"
if ! $DRY_RUN; then
  kubectl -n "${NAMESPACE}" wait --for=condition=available deployment/"${BEADS_SVC}" \
    --timeout=120s 2>/dev/null \
    || kubectl -n "${NAMESPACE}" get svc "${BEADS_SVC}"
fi

echo ""
echo "======================================================================"
echo " gasboat-rwx namespace ready"
echo " Namespace : ${NAMESPACE}"
echo " Release   : ${RELEASE}"
echo " Beads svc : ${BEADS_SVC}"
echo ""
echo " Next step: rwx dispatch gasboat-e2e --params namespace=${NAMESPACE}"
echo "======================================================================"

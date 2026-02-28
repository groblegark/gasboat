#!/usr/bin/env bash
set -euo pipefail

# release.sh — Compute next calver tag and bump Chart.yaml
#
# Usage:
#   scripts/release.sh              # bump, lint, commit, tag
#   scripts/release.sh --dry-run    # show what would happen

CHART="helm/gasboat/Chart.yaml"
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    *) echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

# --- Compute next calver tag: YYYY.DDD.N ---

YEAR=$(date +%Y)
DOY=$(date +%-j)  # day-of-year without leading zeros
PREFIX="${YEAR}.${DOY}"

# Find highest build number for today's prefix among existing git tags.
# Check both bare (2026.58.N) and v-prefixed (v2026.58.N) tags.
LAST_N=$({ git tag -l "${PREFIX}.*"; git tag -l "v${PREFIX}.*"; } 2>/dev/null \
  | sed -E "s/^v?${PREFIX}\.//" \
  | sort -n \
  | tail -1)

if [ -z "$LAST_N" ]; then
  NEXT_N=1
else
  NEXT_N=$((LAST_N + 1))
fi

TAG="${PREFIX}.${NEXT_N}"

echo "Next release: ${TAG}"

if $DRY_RUN; then
  echo "(dry-run) Would update ${CHART}:"
  echo "  version: ${TAG}"
  echo "  appVersion: \"${TAG}\""
  echo "(dry-run) Would commit: chore: release ${TAG}"
  echo "(dry-run) Would create git tag: ${TAG}"
  exit 0
fi

# --- Bump Chart.yaml ---

sed -i.bak -E "s/^version: .+/version: ${TAG}/" "$CHART"
sed -i.bak -E "s/^appVersion: .+/appVersion: \"${TAG}\"/" "$CHART"
rm -f "${CHART}.bak"

echo "Updated ${CHART}"

# --- Lint ---

helm lint helm/gasboat/
echo "Helm lint passed"

# --- Commit & tag ---

git add "$CHART"
git commit -m "chore: release ${TAG}"
git tag "$TAG"

echo ""
echo "Created commit and tag: ${TAG}"
echo "To publish: git push origin main ${TAG}"

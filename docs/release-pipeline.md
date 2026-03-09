# Gasboat Release Pipeline

## Overview

The Golden Release Path automates the entire release lifecycle from tag creation
through deployment verification. After running `make release && git push origin main <tag>`,
the pipeline handles: image builds, helm chart publishing, GitHub Release creation,
fics-helm-chart deploy trigger, E2E tests, and post-deploy verification.

## Release Steps

### 1. Create Release Tag

```bash
make release              # compute next calver tag, bump Chart.yaml, commit + tag
# or preview first:
make release-dry-run      # show what would happen
```

This runs `scripts/release.sh` which:
- Computes the next calver tag (`YYYY.DDD.N` format)
- Updates `helm/gasboat/Chart.yaml` version and appVersion
- Runs `helm lint`
- Creates git commit and tag

### 2. Push Tag

```bash
git push origin main <tag>    # e.g. git push origin main 2026.61.5
```

This triggers the automated pipeline.

### 3. Automated Pipeline (triggered by tag push)

| Step | System | Pipeline | What it does |
|------|--------|----------|--------------|
| Build + push images | RWX | `.rwx/docker.yml` | Builds all 6 images, pushes to `ghcr.io/groblegark/gasboats/*` |
| Push helm chart | RWX | `.rwx/helm.yml` | Packages and pushes chart to `oci://ghcr.io/groblegark/charts` |
| GitHub Release | GitHub Actions | `.github/workflows/release.yml` | Creates release with auto-generated notes |
| Trigger deploy | GitHub Actions | `.github/workflows/release.yml` | Triggers fics-helm-chart GitLab pipeline |
| E2E tests | GitHub Actions + RWX | `.github/workflows/e2e-rwx.yml` | Dispatches E2E tests, files bug beads on failure |

### 4. Deployment (triggered by release.yml)

The `trigger-deploy` job in `release.yml` calls the GitLab API to trigger the
fics-helm-chart `deploy-gasboat` pipeline with `GASBOAT_DEPLOY=1`. That pipeline:

1. Runs `helm dependency update` in `charts/gasboat/` to resolve the new upstream chart
2. Deploys with `helm upgrade --install gasboat ./ -n gasboat --values values/gasboat.yaml`

### 5. Post-Deploy Verification

Run manually or integrate into CI:

```bash
make verify VERSION=2026.61.5
# or directly:
scripts/verify-deploy.sh --namespace=gasboat --version=2026.61.5
```

Checks:
- All pods Running with correct image tags
- No crashloops or excessive restarts
- Beads daemon responds on HTTP
- Slack bridge pod ready
- Controller pod ready

When `BEADS_AUTH_TOKEN` and `BEADS_HTTP_URL` are set, failures auto-create
bug beads labeled `release:<version>`.

## Calver Convention

Format: `YYYY.DDD.N`
- `YYYY` = year (2026)
- `DDD` = day-of-year without leading zeros (61 = March 2)
- `N` = build number for that day (increments for multiple releases)

Example: `2026.61.5` = 5th release on March 2, 2026.

## Images

All pushed to `ghcr.io/groblegark/gasboats/`:

| Image | Description |
|-------|-------------|
| `controller` | Main agent lifecycle controller |
| `agent` | Agent runtime (ubuntu + all dev tools) |
| `slack-bridge` | Slack bot + bridge |
| `jira-bridge` | JIRA integration bridge |
| `gitlab-bridge` | GitLab webhook bridge |
| `advice-viewer` | Advice web viewer |

Each image is tagged with: `<calver>`, `latest`, and `<appVersion>` (if different).

## Helm Chart

Published to: `oci://ghcr.io/groblegark/charts/gasboat:<calver>`

## fics-helm-chart Relationship

`fics-helm-chart/charts/gasboat/` is a wrapper chart that depends on the upstream
gasboat chart:

```yaml
# charts/gasboat/Chart.yaml
dependencies:
  - name: gasboat
    version: ">=2026.0.0"
    repository: "oci://ghcr.io/groblegark/charts"
```

Key points:
- **Fuzzy constraint** `>=2026.0.0`: `helm dependency update` resolves the latest matching chart
- **values/gasboat.yaml**: environment-specific production config, all values nested under `gasboat:` prefix (subchart convention)
- **values/gasboat-e2e.yaml**: minimal E2E config (no agents/bridges)
- **beads3d templates**: inlined in the wrapper because upstream lacks daemon token auth
- **Deploy pipeline**: `deploy-gasboat` GitLab CI job runs `helm dep update` + `helm upgrade --install`
- **Trigger methods**: `GASBOAT_DEPLOY=1` via scheduled/manual/API pipeline, or chart changes on main

### Manual Deploy (Emergency)

```bash
cd fics-helm-chart/charts/gasboat
echo "$GHCR_TOKEN" | helm registry login ghcr.io -u _token --password-stdin
helm dependency update
helm upgrade --install gasboat ./ -n gasboat --values values/gasboat.yaml --wait --timeout 5m
```

## Required Secrets

### GitHub Actions (gasboat repo)
- `GITLAB_TRIGGER_TOKEN` — GitLab pipeline trigger token for fics-helm-chart
- `RWX_ACCESS_TOKEN` — RWX CLI authentication
- `BEADS_AUTH_TOKEN` — Beads daemon auth (for E2E bug filing)
- `BEADS_HTTP_URL` — Beads daemon URL (for E2E bug filing)

### RWX Pipelines
- `GHCR_TOKEN` — GitHub PAT with `packages:write` for GHCR push
- `GITHUB_USER` — GitHub username for GHCR auth
- `E2E_AWS_ACCESS_KEY_ID` / `E2E_AWS_SECRET_ACCESS_KEY` — AWS creds for E2E cluster access
- `E2E_EKS_CLUSTER` — EKS cluster name for E2E

### GitLab CI (fics-helm-chart)
- `EKS_CLUSTER_NAME` — EKS cluster name
- `AWS_DEFAULT_REGION` — AWS region
- `GHCR_TOKEN` — GitHub PAT with `read:packages` for OCI chart pulls

## Troubleshooting

### Images not pushed
Check RWX dashboard for `.rwx/docker.yml` pipeline status. The pipeline triggers
on `main` branch pushes and tags starting with `20`.

### Helm chart not published
Check RWX dashboard for `.rwx/helm.yml` pipeline status. Only triggers on tags.

### Deploy not triggered
Verify `GITLAB_TRIGGER_TOKEN` is set in GitHub repo secrets. Check the
`release.yml` workflow logs for trigger errors.

### E2E failures
Check the GitHub Actions `e2e-rwx.yml` workflow. For release tags, it automatically
files bug beads. Check `kd list --type bug` for filed issues.

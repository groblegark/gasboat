# gasboat E2E Tests

Integration tests for the gasboat/kbeads stack.

## Prerequisites

- `kubectl` context pointing at the target cluster
- `gb` binary from `~/gasboat/controller` (orchestration: bus emit, decision, gate)
- `kd` binary from `~/kbeads` (CRUD: create, close, list, show)
- `jq` and `python3` installed
- A gasboat namespace deployed (`gasboat-rwx` for RWX, or `gasboat-e2e` for legacy EKS)

## Gate System Tests (`test-gate-system.sh`)

Tests `gb bus emit --hook=Stop` gate enforcement from the `bd-pe028` epic.

**Requires** `gb` binary (from `~/gasboat/controller`) and a kbeads server with gate system support.
The target namespace must be running kbeads at commit `8c92e4e` or later.

### Quick run (port-forward auto-setup):

```bash
GB_BIN=/tmp/gb KD_BIN=/tmp/kd \
  ./tests/e2e/scripts/test-gate-system.sh
```

### With explicit daemon URL:

```bash
GB_BIN=/tmp/gb KD_BIN=/tmp/kd \
BEADS_HTTP_URL=http://localhost:19090 \
  ./tests/e2e/scripts/test-gate-system.sh
```

### Scenarios covered:

1. **Decision gate blocks Stop** — no decision offered → exit 2
2. **Decision created, not responded** → Stop still blocks → exit 2
3. **Decision closed (responded)** → gate satisfied → Stop allowed → exit 0
4. **No agent identity** → fails open → exit 0
5. **Dirty git tree** → commit-push soft warning → exit 0 with `<system-reminder>`
6. **Gate status transitions** — pending → satisfied → pending via `gb gate status/mark/clear`

### Claudeless scenarios (`claudeless/`)

TOML scenarios for claudeless-based full session lifecycle tests.
These simulate a complete Claude Code session and require claudeless in PATH.

```bash
# Run a claudeless scenario
claudeless run tests/e2e/claudeless/gate-decision-flow.toml \
  --settings .claude/settings.json
```

Claudeless is installed in the `ghcr.io/groblegark/gasboat/agent:latest` image.

## Decisions/Yield Tests (`test-decisions-yield.sh`)

Tests `gb decision` CRUD and `gb yield` blocking behavior.

**Requires** `gb` binary and a kbeads server with decision/yield support.

### Quick run (port-forward auto-setup):

```bash
GB_BIN=/tmp/gb KD_BIN=/tmp/kd \
  ./tests/e2e/scripts/test-decisions-yield.sh
```

### With explicit daemon URL:

```bash
GB_BIN=/tmp/gb KD_BIN=/tmp/kd \
BEADS_HTTP_URL=http://localhost:19091 \
  ./tests/e2e/scripts/test-decisions-yield.sh
```

### Scenarios covered:

1. **Decision create** — `gb decision create --no-wait --json` returns ID, exit 0
2. **Decision list** — created decision appears in `gb decision list --json`
3. **Decision show** — shows prompt, options, status=open
4. **Decision respond** — `gb decision respond <id> --select=a` closes it; chosen=a
5. **Yield unblocks on decision close** — background `gb yield --timeout=15s`, respond to decision, yield exits 0 with "resolved"
6. **Yield unblocks on mail** — background yield, `gb mail send`, yield exits 0 with "Mail received"
7. **Yield timeout** — `gb yield --timeout=3s` with no events, exits 0 with "Yield timed out"

## CI/CD

E2E tests run via `.rwx/e2e.yml` (RWX-native):

- **Trigger**: `rwx dispatch gasboat-e2e` or `rwx run .rwx/e2e.yml` (CLI)
- **Cluster**: `gasboat-rwx` namespace (default), configurable via `namespace` param
- **Secrets**: `E2E_AWS_ACCESS_KEY_ID`, `E2E_AWS_SECRET_ACCESS_KEY`, `E2E_EKS_CLUSTER`

The legacy `.github/workflows/e2e.yml` (EKS `america-e2e-eks` / `gasboat-e2e`) is retired.

Run locally with `make e2e` (requires port-forward or `BEADS_HTTP_URL` set).

## Deploying gasboat-rwx namespace

```bash
helm upgrade --install gasboat-rwx helm/gasboat/ -n gasboat-rwx --create-namespace \
  --values helm/gasboat/values.yaml
```

Port-forward for local testing:
```bash
kubectl -n gasboat-rwx port-forward svc/gasboat-rwx-beads 19090:8080
# Then: BEADS_HTTP_URL=http://localhost:19090 kd list
```

# Gasboat Complexity & Drift Audit

**Date**: 2026-03-09 (Day 068)
**Scope**: Go controller, Helm chart, build systems, test coverage, package identity

---

## Executive Summary

Gasboat is a **67K-line Go codebase** (37K source, 30K test) across 12 internal packages and 7 command binaries. The architecture is **well-layered** with zero circular dependencies and clean separation of concerns. However, three areas demand attention:

1. **The bridge package is a monolith** — 49 source files, 15K lines, doing Slack bot + GitHub + GitLab + JIRA + Wasteland + SSE multiplexing + IP gating in a single Go package.
2. **Build system drift** — Dockerfile pins specific versions for coop/kbeads but RWX CI overrides them with `latest`, producing different images locally vs CI.
3. **Test gaps in the most-churned code** — the files that change most often (bot.go, bot_commands.go, bot_decisions.go, bot_agents.go) have zero test coverage.

---

## 1. Package Identity Map

The codebase has a clean 5-layer architecture with no circular dependencies:

```diagram
Layer 5: Entry Points
  cmd/controller/     cmd/slack-bridge/     cmd/gb/
  cmd/gitlab-bridge/  cmd/jira-bridge/      cmd/wl-bridge/
  cmd/advice-viewer/

Layer 4: Orchestration
  reconciler ──→ beadsapi, config, podmanager
  statusreporter ──→ podmanager, reconciler (IsPodReady only)
  bridge ──→ beadsapi (K8s optional via gate.go only)

Layer 3: Domain Logic
  poolmanager ──→ beadsapi, config
  scheduler ──→ beadsapi
  subscriber ──→ beadsapi
  advice ──→ beadsapi
  secretreconciler ──→ config
  tui/decision ──→ beadsapi, advice

Layer 2: Configuration
  config ──→ beadsapi (types only)

Layer 1: Foundation (zero internal deps)
  beadsapi     podmanager
```

**Key finding**: Bridge is genuinely K8s-independent as claimed — only `gate.go` (Bouncer for Traefik IP whitelisting) imports k8s.io, and it's optional with graceful degradation.

### Package Size Distribution

| Package | Source Lines | Test Lines | Ratio | Files |
|---------|-------------|------------|-------|-------|
| bridge | 15,436 | 14,307 | 0.93 | 49 src |
| beadsapi | 1,563 | 3,077 | 1.97 | 12 src |
| reconciler | 1,206 | 1,235 | 1.02 | 5 src |
| podmanager | 1,050 | 1,746 | 1.66 | 3 src |
| tui/decision | 1,047 | 610 | 0.58 | 4 src |
| scheduler | 490 | 167 | 0.34 | 2 src |
| poolmanager | 485 | 346 | 0.71 | 1 src |
| subscriber | 473 | 714 | 1.51 | 2 src |
| advice | 421 | 375 | 0.89 | 3 src |
| config | 393 | 211 | 0.54 | 1 src |
| statusreporter | 295 | 933 | 3.16 | 1 src |
| secretreconciler | 219 | 283 | 1.29 | 1 src |

Bridge is 10x larger than the next biggest package and contains 49 source files — more than all other packages combined.

---

## 2. Complexity Hotspots

### Longest Functions (top 10)

| Lines | Location | Function | Risk |
|-------|----------|----------|------|
| 330 | bridge/init.go:67 | `configs()` | Schema drift — massive map literal defining all bead types/views |
| 266 | bridge/bot_decisions.go:39 | `NotifyDecision()` | Deep nesting, mixed concerns (Slack API + bead state + threading) |
| 228 | bridge/init_prompts.go:6 | `configBeadEntries()` | Static config data — low risk but hard to review |
| 221 | reconciler/reconciler.go:109 | `Reconcile()` | Core loop — orphan deletion + drift detection + pod creation in one func |
| 210 | bridge/dashboard.go:160 | dashboard rendering | Complex Slack block construction |
| 190 | bridge/bot_mentions.go:25 | mention handler | Multi-path agent routing with 5+ fallback strategies |
| 155 | bridge/bot_mentions.go:342 | thread reply handler | Nested conditionals for thread management |
| 152 | podmanager/initclone.go:20 | `InitCloneScript()` | Shell script generation via fmt.Sprintf — fragile |
| 141 | bridge/gitlab_sync.go:394 | `GitLabWebhookHandler` | Webhook parsing + sync logic mixed |
| 131 | tui/decision/model.go:233 | `Update()` | Bubbletea state machine |

### Structural Concerns

**Magic strings for bead field access**: Fields like `agent_state`, `pod_phase`, `project` are accessed via `bead.Fields["agent_state"]` across 20+ files. No typed accessor, no compile-time safety.

**Shell script generation in Go**: `initclone.go` builds multi-line bash scripts via `fmt.Sprintf`. Changes to the script require understanding both Go string escaping and bash semantics.

**Reconciler mixes phases**: `Reconcile()` handles orphan deletion, drift detection, and pod creation in one 221-line function. Each phase has different failure modes that get mixed together.

---

## 3. Git Churn Analysis

Most frequently changed files in the last 2 months:

| Changes | File | Why |
|---------|------|-----|
| 96 | helm/gasboat/Chart.yaml | Release bumps (calver) |
| 68 | helm/gasboat/values.yaml | New features, config updates |
| 67 | images/agent/entrypoint.sh | Agent bootstrapping evolution |
| 61 | **bridge/bot.go** | Core bot logic, new features |
| 59 | images/agent/Dockerfile | Tool version updates |
| 47 | beadsapi/client.go | API evolution, new endpoints |
| 38 | bridge/init.go | New config types/views |
| 38 | cmd/slack-bridge/main.go | Startup wiring |
| 37 | cmd/controller/podspec.go | Pod spec changes |
| 37 | .rwx/docker.yml | Build system updates |

**Key insight**: `bridge/bot.go` (61 changes) is the 4th most-churned file and has **zero test coverage**. The files changing most are the ones least tested.

---

## 4. Test Coverage Gaps

### Untested Critical Code (HIGH risk, mission-critical)

| File | Lines | Why It Matters |
|------|-------|---------------|
| bridge/bot.go | 632 | Socket Mode event dispatcher — all Slack interactions route through this |
| bridge/bot_commands.go | 742 | Slash command handlers (/spawn, /decisions, /roster) |
| bridge/bot_decisions.go | 580 | Decision notification creation, resolution, dismissal |
| bridge/bot_agents.go | 674 | Agent card creation, lifecycle tracking, status updates |
| reconciler/upgrade.go | 374 | Pod upgrade strategies (Skip/Rolling/Last), drain orchestration |
| cmd/gb/agent_start.go | 455 | Agent launch orchestration, pod spec generation |
| cmd/gb/agent_k8s_lifecycle.go | 398 | K8s pod lifecycle management |

**Total: 3,855 lines of untested mission-critical code.**

### Untested Important Code (MEDIUM risk)

| File | Lines | Why It Matters |
|------|-------|---------------|
| bridge/state.go | 354 | JSON persistence for Slack thread state across restarts |
| reconciler/imagedigest.go | 288 | Image digest resolution and caching |
| bridge/jira_poller.go | 522 | JIRA change polling infrastructure |
| cmd/gb/decision.go | 547 | CLI decision workflow |
| cmd/gb/hook.go | 433 | Hook processing pipeline |
| bridge/bot_decisions_modal.go | 467 | Slack modal rendering for decisions |

### Packages With Zero Tests

- `cmd/gitlab-bridge/` (238 lines)
- `cmd/jira-bridge/` (292 lines)
- `cmd/wl-bridge/` (278 lines)

### Well-Tested Packages (reference)

| Package | Test:Source Ratio | Notes |
|---------|-------------------|-------|
| statusreporter | 3.16 | Excellent — core status sync well-verified |
| beadsapi | 1.97 | Comprehensive HTTP client tests with mock servers |
| podmanager | 1.66 | Good K8s operation coverage |
| subscriber | 1.51 | SSE stream and edge cases covered |
| secretreconciler | 1.29 | ExternalSecret provisioning tested |

---

## 5. Drift Risks

### CRITICAL: Build System Drift (Dockerfile vs RWX CI)

The Dockerfile pins `COOP_VERSION=2026.66.2` and `BEADS_VERSION=2026.67.2`, but RWX CI overrides both with `latest`:

```yaml
# .rwx/docker.yml — on push to main
init:
  coop-version: latest    # ignores Dockerfile pin
  kd-version: latest      # ignores Dockerfile pin
```

**Impact**: `make image-agent` locally produces a different image than CI for the same commit. CLAUDE.md states "The Dockerfile is the reference spec; RWX CI mirrors it" — this is not true for coop/kbeads versions.

### CRITICAL: COOP_MAX_PODS Default Mismatch

Three different defaults exist:

| Location | Default | Context |
|----------|---------|---------|
| config/config.go:287 | 30 | Go code fallback |
| helm/values.yaml:160 | 4 | Helm chart default |
| templates/controller/deployment.yaml:149 | 4 | Template fallback |

When deployed via Helm, the limit is 4. When running standalone, it's 30. A 7.5x discrepancy.

### HIGH: Inconsistent Image Tag Patterns in Helm

Three different patterns exist for resolving image tags:

1. **Helper function** (agents, coopmux, slack-bridge): `{{ include "gasboat.imageTag" ... }}` — respects `global.latestMode`
2. **Inline if/else** (beads3d): `{{ if .Values.beads3d.image.tag }}...{{ else }}{{ .Chart.AppVersion }}{{ end }}` — ignores latestMode
3. **Chained default** (nats, postgres): `{{ .Values.nats.image.tag | default "2.10-alpine" }}` — ignores latestMode, inline defaults

### MEDIUM: Dynamically-Resolved CLI Versions

kubectl, gh, glab, yq, helm are resolved to "latest" at build time in RWX CI with no version pinning. A breaking upstream release would automatically propagate to all agent images.

---

## 6. Bridge Package — Identity Crisis

The bridge package has 49 source files handling 8+ distinct responsibilities:

| Responsibility | Files | Lines |
|---------------|-------|-------|
| Slack bot (Socket Mode) | bot.go, bot_*.go (14 files) | ~5,500 |
| Decision workflow | decisions.go, api_decisions*.go | ~1,200 |
| GitHub integration | github.go | ~400 |
| GitLab integration | gitlab.go, gitlab_*.go | ~1,200 |
| JIRA integration | jira.go, jira_*.go | ~1,100 |
| Wasteland integration | wasteland*.go | ~900 |
| Config/init | init.go, init_*.go | ~700 |
| SSE/state/dedup | sse.go, state.go, dedup.go | ~700 |
| Dashboard/agents | dashboard.go, agents.go | ~600 |
| IP gating | gate.go | ~200 |

This is a **God Package** — it works because it's all in one directory, but the test coverage suffers (42% of files untested) and changes to any part risk affecting everything else.

The claim "Zero K8s dependencies" is technically true (only gate.go, optional) but obscures the real concern: the package mixes Slack interaction handling, webhook processing, SSE multiplexing, external service integration (GitHub/GitLab/JIRA/Wasteland), config registration, state persistence, and IP management.

---

## 7. Recommendations

### Immediate (block releases)

1. **Align COOP_MAX_PODS default** between config.go and Helm values — pick one value.
2. **Fix Dockerfile/RWX coop-version drift** — either make RWX read Dockerfile ARGs or document the intentional divergence.

### Short-term (next 2 sprints)

3. **Add tests for the 4 most-churned untested files**: bot.go, bot_commands.go, bot_decisions.go, bot_agents.go. These change constantly and have zero test safety net.
4. **Add tests for reconciler/upgrade.go** — pod upgrade strategy is critical and untested.
5. **Standardize Helm image tag patterns** — migrate beads3d and nats to use the `gasboat.imageTag` helper.

### Medium-term (next quarter)

6. **Extract bridge sub-packages**: Consider splitting into `bridge/slack/`, `bridge/github/`, `bridge/gitlab/`, `bridge/jira/`, `bridge/wasteland/`, keeping the SSE/state core in `bridge/`. This would make test boundaries clearer and allow independent evolution.
7. **Add typed bead field accessors** to replace `bead.Fields["agent_state"]` with compile-time safe structs.
8. **Pin dynamic CLI versions** (kubectl, gh, glab, yq) in `.rwx/agent-clis.lock` for reproducible builds.

---

## Appendix: Verification Commands

```bash
# Run all tests
make test

# Check test coverage
cd controller && go test -cover ./...

# Run race detector
cd controller && go test -race ./...

# Lint
quench check

# Helm lint
helm lint helm/gasboat/

# Build both binaries
make build && make build-bridge
```

# Gasboat — Development Guide

## Architecture: Data Instead of Code

Orchestration concepts are expressed as data rather than code. The beads daemon is the control plane. Gasboat is a reactive bridge that:

1. Pushes declarative config to the daemon at startup (`bridge/init.go`) — bead type definitions (`type:agent`, `type:project`, `type:mail`, `type:decision`, `type:advice`), saved views (`view:agents:active`, `view:decisions:pending`, `view:mail:inbox`), and context dashboards (`context:captain`, `context:crew`)
2. Watches NATS for lifecycle events (`subscriber/`) — filters for `type:agent` beads, maps to spawn/done/kill/stop/update events, translates into K8s pod operations
3. Handles decisions and mail via NATS watchers (`bridge/decisions.go`, `bridge/mail.go`) — nudges agents via coop when decisions resolve or urgent mail arrives
4. Posts decisions to Slack/Mobile/etc (`bridge/slack.go`) — interactive notifications for human-in-the-loop responses

Agent-side behavior lives in shell hooks baked into the container image (`images/agent/hooks/`), not in controller Go code. The controller creates pods with the right env vars and lets the hooks do the rest.

When adding new orchestration features, prefer:
- A new bead type + view in `bridge/init.go` over a new Go package
- A new agent hook in `images/agent/hooks/` over controller-side logic
- A NATS subscription in `bridge/` over polling

## Directory Structure

- `controller/` — Go module, the only compiled artifact
  - `cmd/controller/` — entry point, leader election, main event loop
  - `internal/client/` — gRPC client to the beads daemon
  - `internal/bridge/` — config registration, decision + mail watchers, Slack notifier
  - `internal/config/` — env var parsing
  - `internal/podmanager/` — K8s pod CRUD and spec construction
  - `internal/reconciler/` — periodic desired-vs-actual state sync
  - `internal/statusreporter/` — pod phase → bead state updates
  - `internal/subscriber/` — NATS JetStream event listener
- `images/agent/` — agent container Dockerfile, entrypoint, Claude Code hooks
- `helm/gasboat/` — Helm chart (controller, beads daemon, NATS, PostgreSQL, coopmux)
- `deploy/` — example values files

## Commits

Use conventional commit format: `type(scope): description`
Types: feat, fix, chore, docs, test, refactor

## Build & Test

```bash
make build          # compile controller
make test           # run tests
make lint           # lint
make image          # build controller image
make image-agent    # build agent image
```

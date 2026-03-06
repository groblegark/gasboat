# Prewarmed Agent Pool

The prewarmed agent pool maintains a set of idle, fully-initialized agent pods that can be assigned to Slack threads instantly — avoiding the ~60-120s cold-start time of spawning a new pod from scratch.

## How It Works

1. **Pool reconciler** runs in the controller on a configurable interval (default: 30s). It ensures a minimum number of prewarmed agent beads exist with `agent_state=prewarmed`.
2. **Pod creation** is handled by the existing reconciler — it sees the new agent beads and creates pods as usual.
3. **Entrypoint standby** — prewarmed pods get `BOAT_STANDBY=true` injected. The entrypoint completes all initialization (clone repos, install deps, run `gb setup claude`) then blocks, polling the bead's `agent_state` every 5s until it changes from `prewarmed`.
4. **Assignment** — when a Slack thread spawn is requested, the bridge calls `POST /api/v1/pool/assign` on the controller. The pool manager atomically picks the oldest prewarmed agent (FIFO), transitions it to `assigning`, and writes the thread context to the bead. The entrypoint detects the state change, hydrates the thread context, and launches Claude.
5. **TTL recycling** — prewarmed agents that sit idle longer than the TTL (default: 30m) are closed and replaced by fresh ones.

## Enabling the Pool

Set the following environment variable on the **controller** deployment:

```
PREWARMED_POOL_ENABLED=true
```

This is the only required setting. All other config has sensible defaults.

### Helm Chart

The Helm chart does not yet have dedicated values for the prewarmed pool. Set the env vars directly on the controller deployment via `extraEnv` or by adding them to the controller deployment template:

```yaml
# In your values override (e.g. values-production.yaml)
controller:
  extraEnv:
    - name: PREWARMED_POOL_ENABLED
      value: "true"
    - name: PREWARMED_POOL_MIN_SIZE
      value: "2"
    - name: PREWARMED_POOL_MAX_SIZE
      value: "5"
    - name: PREWARMED_POOL_TTL
      value: "30m"
    - name: PREWARMED_POOL_PROJECT
      value: "gasboat"
```

## Configuration Reference

All settings are controller environment variables with defaults:

| Variable | Default | Description |
|---|---|---|
| `PREWARMED_POOL_ENABLED` | `false` | Master switch. Pool reconciler and `/api/v1/pool/assign` endpoint are inactive when false. |
| `PREWARMED_POOL_MIN_SIZE` | `2` | Minimum number of idle prewarmed agents to maintain. The reconciler creates new agents when the count drops below this. |
| `PREWARMED_POOL_MAX_SIZE` | `5` | Maximum prewarmed agents allowed. Caps creation even if min_size is higher. |
| `PREWARMED_POOL_TTL` | `30m` | How long a prewarmed agent can sit idle before being recycled. Prevents stale pods from consuming resources indefinitely. Set to `0` to disable TTL recycling. |
| `PREWARMED_POOL_ROLE` | `thread` | The `role` field set on prewarmed agent beads. Controls which config beads (advice, hooks, instructions) the agent receives. |
| `PREWARMED_POOL_MODE` | `crew` | The `mode` field set on prewarmed agent beads. Controls agent lifecycle behavior (crew = ephemeral worker). |
| `PREWARMED_POOL_PROJECT` | *(empty)* | The project for prewarmed agents. When set, agents are labeled `project:<value>` and only prewarmed agents matching this project are considered for assignment. When empty, uses the first project in the project cache. |
| `PREWARMED_POOL_INTERVAL` | `30s` | How often the pool reconciler runs. Lower values mean faster refill after assignment but more API calls. |

## Architecture

### Components

```
┌──────────────┐     reconcile loop      ┌──────────────────┐
│  Controller  │◄───────────────────────►│  Pool Manager     │
│  (main.go)   │                         │  (poolmanager.go) │
│              │  POST /pool/assign      │                   │
│  HTTP server ├────────────────────────►│  AssignPrewarmed  │
└──────┬───────┘                         └────────┬──────────┘
       │                                          │
       │ creates agent beads                      │ updates agent_state
       ▼                                          ▼
┌──────────────┐                         ┌──────────────────┐
│ Beads Daemon │                         │ Prewarmed Pod     │
│              │                         │ (entrypoint.sh)   │
│              │                         │                   │
│              │◄────── polls ───────────│ BOAT_STANDBY=true │
│              │  agent_state != prewarmed│ waiting...        │
└──────────────┘                         └──────────────────┘
```

### Slack Bridge Integration

When a thread spawn is triggered, the bridge (`internal/bridge/bot_mentions.go`) tries the pool first:

1. Bridge calls `POST http://<controller>/api/v1/pool/assign` with channel, thread_ts, description, and project.
2. If a prewarmed agent is available (HTTP 200), the bridge uses it and posts a "prewarmed agent assigned" message.
3. If the pool is empty (HTTP 404) or the pool is disabled, the bridge falls back to the normal cold-start spawn path.

The bridge needs `CONTROLLER_URL` (or equivalent) set to reach the controller's HTTP server. This is the same health/version server that runs on the controller.

### Agent State Machine

Prewarmed agents go through these states:

```
prewarmed → assigning → spawning → working → done
                                      │
                                      └──► (TTL exceeded) → done (recycled)
```

- **prewarmed** — Pod is running, workspace initialized, Claude not started. Entrypoint is polling.
- **assigning** — Pool manager has picked this agent for a thread. Bead fields updated with thread context.
- **spawning** — Entrypoint detected state change, hydrating thread context, launching Claude.
- **working** — Claude is running and processing the thread.
- **done** — Agent finished or was recycled.

### Status Reporter

The status reporter (`internal/statusreporter/`) is aware of prewarmed pods. When a pod has the `gasboat.io/prewarmed` annotation:
- Running pods are **not** updated to `agent_state=working` (they stay `prewarmed`).
- Failed pods **are** updated to `agent_state=failed` (so the reconciler can clean up).

## Tuning Guide

### Small team (1-3 concurrent threads)
```
PREWARMED_POOL_MIN_SIZE=1
PREWARMED_POOL_MAX_SIZE=3
PREWARMED_POOL_TTL=30m
```

### Medium team (3-10 concurrent threads)
```
PREWARMED_POOL_MIN_SIZE=3
PREWARMED_POOL_MAX_SIZE=8
PREWARMED_POOL_TTL=20m
```

### Cost-sensitive (minimize idle resources)
```
PREWARMED_POOL_MIN_SIZE=1
PREWARMED_POOL_MAX_SIZE=2
PREWARMED_POOL_TTL=15m
PREWARMED_POOL_INTERVAL=60s
```

### Key tradeoffs

- **Higher min_size** = faster response for burst traffic, but more idle resource cost.
- **Lower TTL** = less wasted compute on idle pods, but more churn (recycling and recreating).
- **Lower interval** = faster refill after assignment, but more API calls to the beads daemon.
- Each prewarmed pod consumes the same resources as a regular agent pod (CPU, memory, PVC). Factor this into your `COOP_MAX_PODS` limit.

## Observability

### Check pool status

```bash
# List prewarmed agents
kd list --type agent | grep prewarmed

# Count prewarmed agents
kd list --type agent --json | jq '[.[] | select(.fields.agent_state == "prewarmed")] | length'

# Check pool manager logs
kubectl logs -n gasboat deploy/gasboat-controller | grep poolmanager
```

### Key log messages

| Message | Meaning |
|---|---|
| `pool manager started` | Pool reconciler is running with the configured settings. |
| `pool below minimum, creating prewarmed agents` | Reconciler detected fewer agents than min_size and is creating more. |
| `created prewarmed agent` | A new prewarmed agent bead was created (pod creation follows). |
| `recycling prewarmed agent (TTL exceeded)` | An idle agent exceeded TTL and is being closed. |
| `assigned prewarmed agent` | A prewarmed agent was assigned to a thread (fast path). |
| `pool assign returned non-200` | Bridge tried to use the pool but it was empty or disabled. |

## Troubleshooting

**Pool not filling up**
- Check `PREWARMED_POOL_ENABLED=true` is set on the controller.
- Check controller logs for `pool manager started` — if missing, the pool is disabled.
- Check `COOP_MAX_PODS` — prewarmed pods count toward the total pod limit.

**Prewarmed agents not being assigned**
- Verify the bridge has `CONTROLLER_URL` pointing to the controller's HTTP server.
- Check `PREWARMED_POOL_PROJECT` matches the project used in thread spawns.
- Check controller logs for `pool assign` messages.

**Agents stuck in prewarmed state after assignment**
- Check the entrypoint logs: `kubectl logs -n gasboat <pod-name>`.
- The entrypoint polls every 5s (`BOAT_STANDBY_POLL`). Verify the bead's `agent_state` changed from `prewarmed`.
- Run `kd show <agent-bead-id>` to check the current `agent_state`.

**High pod churn (constant recycling and creation)**
- Increase `PREWARMED_POOL_TTL` to keep agents alive longer.
- Decrease `PREWARMED_POOL_MIN_SIZE` if you don't need as many standby agents.

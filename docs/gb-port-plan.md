# Plan: Port Agent Orchestration from kbeads to gasboat `gb` binary

**Epic**: `bd-fzkui`
**Date**: 2026-02-24
**Status**: Complete (orchestration commands removed from kd in kbeads@20a7aee)

## Motivation

> kbeads should be `psql` for work items; gasboat should be `kubectl` for agents.

kbeads has accumulated significant agent-orchestration responsibility that doesn't belong
in a data/tracking tool. The `kd` binary currently wears two hats:

1. **Data platform** — CRUD on work items, querying, type system, export
2. **Agent control plane** — spawn agents, track presence, enforce gates, route decisions,
   deliver mail, inject advice

These are fundamentally different concerns. An agent running `kd gate status` or
`kd decision create` is doing orchestration, not data management. The `POST /v1/hooks/emit`
endpoint (gate validation on Claude hooks) is pure session control.

## What Moves

| Command group | Current (`kd`) | New (`gb`) |
|---|---|---|
| Agent lifecycle | `kd agent start`, `kd agent roster` | `gb agent start`, `gb agent roster` |
| Decisions | `kd decision create/show/list/respond` | `gb decision create/show/list/respond` |
| Gates | `kd gate status/mark/clear` | `gb gate status/mark/clear` |
| Bus/Hooks | `kd bus emit`, `kd hook check-mail/prime/stop-gate` | `gb bus emit`, `gb hook ...` |
| Prime | `kd prime` | `gb prime` |
| Mail | `kd mail send/inbox/read/list`, `kd inbox`, `kd news` | `gb mail ...`, `gb inbox`, `gb news` |
| Advice | `kd advice add/list/show/remove` | `gb advice add/list/show/remove` |
| Setup | `kd setup claude` | `gb setup claude` |
| Flow control | `kd yield`, `kd ready` | `gb yield`, `gb ready` |

## What Stays in kbeads (`kd`)

| Command group | Purpose |
|---|---|
| `create/show/list/search/update/delete` | Bead CRUD |
| `dep/label/comment` | Relations |
| `claim/close/reopen/defer/yield/done` | Workflow transitions |
| `view/context/tree/watch` | Queries & views |
| `config` | Type system |
| `remote` | Multi-server profiles |
| `serve` | gRPC + HTTP server |
| `jack` | Infrastructure audit trails |

## Key Design Decisions

### 1. Gate state stays in kbeads Postgres

The `session_gates` table stays in the kbeads database. `gb` accesses it via the kbeads
HTTP API. This avoids a second database and keeps gate state coupled to bead IDs where
it belongs.

### 2. `gb` is a client of the kbeads HTTP API

`gb` does not need its own storage. It extends gasboat's existing `internal/beadsapi`
HTTP client to cover gates, hooks, roster, and general bead operations. All orchestration
logic lives in `gb`; all data access goes through `kd serve`.

### 3. Hook enforcement moves to `gb`

Claude Code hooks call `gb bus emit` instead of `kd bus emit`. The hook evaluation logic
(gate checking, decision enforcement, prime injection) lives in `gb`. The kbeads server
just provides data endpoints.

### 4. `POST /v1/hooks/emit` moves conceptually

The endpoint stays on the kbeads HTTP server (it's a data query), but the *client-side
logic* — reading Claude hook JSON from stdin, resolving agent identity, interpreting
block/allow/inject responses — moves to `gb`.

### 5. Env var names preserved

`KD_AGENT_ID`, `KD_ACTOR`, `BEADS_HTTP_URL` etc. keep their names during the transition.
Both `kd` and `gb` read them. Renaming is future work.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Agent Pod                             │
│                                                         │
│  Claude Code ──hooks──► gb bus emit ──HTTP──► kd serve  │
│                           │                     │       │
│                           │ (orchestration)     │(data) │
│                           ▼                     ▼       │
│                     gb prime/gate/        PostgreSQL     │
│                     decision/mail                       │
│                                                         │
│  gb agent start --k8s  (PID 1, replaces entrypoint.sh) │
│  coop (terminal mux)                                    │
│  kd (data queries: show, list, close, create)           │
└─────────────────────────────────────────────────────────┘

┌──────────────────┐  ┌──────────────────┐
│  Controller      │  │  Slack Bridge    │
│  (pod lifecycle) │  │  (notifications) │
│  cmd/controller/ │  │  cmd/slack-bridge/│
└──────────────────┘  └──────────────────┘
         ▲                     ▲
         │ SSE events          │ SSE events
         └─────────┬───────────┘
                   │
              kd serve (HTTP + gRPC + SSE)
                   │
              PostgreSQL + NATS
```

## Gasboat Binary Layout (after port)

```ignore
gasboat/
├── controller/
│   ├── cmd/
│   │   ├── controller/       # K8s controller (existing)
│   │   ├── slack-bridge/     # Slack notifications (existing)
│   │   └── gb/               # NEW: agent orchestration CLI
│   │       ├── main.go       # Root command, client init
│   │       ├── agent.go      # gb agent (parent)
│   │       ├── agent_start.go        # --local, --docker
│   │       ├── agent_start_k8s.go    # --k8s (PID 1 entrypoint)
│   │       ├── agent_k8s_creds.go    # OAuth + credential cascade
│   │       ├── agent_k8s_lifecycle.go # Startup, nudge, exit monitor
│   │       ├── agent_k8s_mux.go      # Coopmux registration
│   │       ├── agent_k8s_workspace.go # PVC, git, session persistence
│   │       ├── agent_roster.go        # gb agent roster
│   │       ├── agent_identity.go      # Shared resolveAgentID()
│   │       ├── decision.go    # gb decision create/show/list/respond
│   │       ├── gate.go        # gb gate status/mark/clear
│   │       ├── bus_emit.go    # gb bus emit --hook=<type>
│   │       ├── hook.go        # gb hook check-mail/prime/stop-gate
│   │       ├── prime.go       # gb prime
│   │       ├── prime_shared.go # outputPrimeForHook() shared function
│   │       ├── mail.go        # gb mail send/inbox/read/list
│   │       ├── inbox.go       # gb inbox (top-level alias)
│   │       ├── news.go        # gb news
│   │       ├── advice.go      # gb advice add/list/show/remove
│   │       ├── setup.go       # gb setup claude
│   │       ├── yield.go       # gb yield
│   │       ├── ready.go       # gb ready
│   │       └── output.go      # Shared output formatters
│   └── internal/
│       ├── beadsapi/          # EXTENDED: full kbeads HTTP client
│       ├── bridge/            # Slack routing (existing)
│       ├── config/            # Env var parsing (existing)
│       ├── podmanager/        # Pod CRUD (existing)
│       ├── reconciler/        # Drift detection (existing)
│       ├── statusreporter/    # Status sync (existing)
│       └── subscriber/        # SSE watcher (existing)
├── helm/gasboat/              # Helm chart
├── images/agent/              # Agent image (updated for gb)
│   ├── Dockerfile             # Add gb build stage
│   ├── entrypoint.sh          # Update kd→gb references
│   └── hooks/                 # Update kd→gb in scripts
└── ...
```

## Execution Plan

### Phase 1: Foundation (sequential)

| Bead | Task | Blocked by |
|---|---|---|
| `bd-fzkui.1` | Scaffold gb cobra CLI skeleton | — |
| `bd-fzkui.2` | Extend gasboat beadsapi client | .1 |

### Phase 2: Port commands (parallelizable)

All 8 tasks depend only on Phase 1 (.1 + .2) and can run concurrently:

| Bead | Task | ~LOC |
|---|---|---|
| `bd-fzkui.3` | Agent start (local/docker) + roster | ~500 |
| `bd-fzkui.4` | Agent start --k8s (pod entrypoint) | ~830 |
| `bd-fzkui.5` | Decision commands | ~450 |
| `bd-fzkui.6` | Gate commands | ~124 |
| `bd-fzkui.7` | Bus emit + hook commands | ~366 |
| `bd-fzkui.8` | Prime command | ~597 |
| `bd-fzkui.9` | Mail/inbox/news + advice | ~590 |
| `bd-fzkui.10` | Setup + yield + ready | ~632 |

### Phase 3: Agent image (sequential)

| Bead | Task | Blocked by |
|---|---|---|
| `bd-fzkui.11` | Add gb to agent Docker image | Phase 2 hooks/bus tasks |
| `bd-fzkui.12` | Update entrypoint.sh + hook scripts | .11 |
| `bd-fzkui.13` | Update helm charts | .12 |

### Phase 4: Cleanup (after Phase 2)

| Bead | Task | Blocked by |
|---|---|---|
| `bd-fzkui.14` | Deprecation warnings on moved kd commands | Phase 2 complete |
| `bd-fzkui.15` | Update docs and CLAUDE.md | .14 |

## Workspace Notes

This is a large change spanning two repos. **Work in fresh worktrees/branches**:

```bash
# gasboat — feature branch for all gb work
cd ~/gasboat
git checkout -b gb-port

# kbeads — feature branch for deprecation warnings
cd ~/kbeads
git checkout -b gb-deprecation
```

## Total Scope

- **~4,100 lines** of command code ported from kbeads to gasboat
- **~300 lines** of beadsapi client extensions
- **~100 lines** of Dockerfile changes
- **~50 lines** of entrypoint.sh updates
- **~200 lines** of documentation updates
- **15 beads** in epic `bd-fzkui`

## Migration Period

During transition:
- Agent image contains **both** `kd` and `gb` binaries
- Hook scripts switch to `gb` for orchestration
- `kd` commands show deprecation warnings but still work
- Data commands (`kd show`, `kd list`, `kd close`, `kd create`) remain in `kd`
- Full removal of deprecated kd commands is future work after gb is proven stable

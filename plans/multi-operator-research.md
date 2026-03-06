# Multi-Operator Support: Research & Design Options

**Status:** Research (2026-03-06)
**Bead:** kd-kW5CTtinae

## Problem Statement

Gasboat is currently single-operator: one deployment, one Slack workspace, one shared pool of agents. Any Slack user can spawn, mention, kill, or interact with any agent. There is no concept of agent ownership, operator identity, or access isolation.

As gasboat scales to multiple humans sharing one instance, several problems emerge:

1. **No ownership** -- agents are global; anyone can `/kill` anyone else's agent
2. **No visibility scoping** -- all agents, decisions, and activity flood one shared Slack channel (or a small set of channels)
3. **No resource isolation** -- one operator can consume all available pod slots
4. **No audit trail** -- spawn events log Slack user IDs but don't persist them as agent metadata
5. **No delegation model** -- no way to say "this agent reports to me, not to everyone"

## Current Architecture (As-Is)

### Identity Model

| Entity | Identity Source | Persisted? | Used for Access Control? |
|--------|----------------|------------|--------------------------|
| Operator (human) | Slack user ID | Logged, not stored on beads | No |
| Agent | Bead ID + agent name | Yes (agent bead) | No |
| Project | Project bead + labels | Yes | Scoping only (no enforcement) |

### Agent Lifecycle (No Operator Binding)

```
Slack User --(/spawn)--> Bridge --> CreateBead(type=agent) --> Reconciler --> K8s Pod
                                    ^^ no user_id field ^^
```

The `cmd.UserID` from Slack is available at spawn time but is **never stored** on the agent bead. Once spawned, the agent is a free-floating resource.

### Interaction Model (Global)

- **Decisions**: Any Slack user can resolve any agent's decision (via button click or web UI)
- **Mentions**: Any user can `@gasboat agent-name do X` to any agent
- **Kill**: Any user can `/kill agent-name`
- **Thread forwarding**: Messages in agent threads are forwarded regardless of sender
- **Squawk**: Agent messages go to a single configured channel (or router-mapped channel)

### What Already Exists That Helps

1. **Projects** -- logical grouping with per-project secrets, repos, resources, and Slack channel mapping
2. **Roles** -- functional categorization (crew, captain, devops) with role-scoped config beads
3. **Router** -- channel routing by `project/role/agent` pattern, can break out agents to dedicated channels
4. **Labels** -- flexible label system on beads (already used for `project:X`, `role:Y`)
5. **Config beads** -- layered config with label scoping (`global < rig < role < agent`)
6. **CreatedBy field** -- exists on beads but set to agent name, not operator identity

## Key Design Principle: Each Agent Belongs to One Operator

The core insight is that **agent-to-operator binding** is the fundamental primitive. Everything else (visibility, permissions, resource limits) flows from knowing who owns an agent.

### What "Belongs To" Means

- The operator who spawned the agent is its **owner**
- The owner receives the agent's decisions, squawks, and status updates
- The owner can kill, reconfigure, or reassign the agent
- Other operators can interact with the agent only if explicitly granted access (or if the agent is in a shared project)

## Design Options

### Option A: Lightweight Owner Field (Minimal Change)

Add an `owner` field to agent beads, populated at spawn time from the Slack user ID.

**Changes required:**
1. `SpawnAgent()` in `client.go` -- accept and store `owner` field
2. `handleSpawnCommand()` in `bot_commands.go` -- pass `cmd.UserID` as owner
3. `handleAppMention()` in `bot_mentions.go` -- thread spawn passes `ev.User` as owner
4. Decision routing -- prefer routing decisions to the owner's DM or owner's thread
5. `/kill` command -- warn (but don't block) when killing another operator's agent

**What this gives you:**
- Audit trail: know who spawned what
- Decision routing: owner gets their agent's decisions
- Soft ownership: warnings when crossing boundaries
- Foundation for harder isolation later

**What this doesn't give you:**
- True access control (anyone can still interact)
- Per-operator resource limits
- Visibility scoping (agents still appear globally)

**Effort:** Small (1-2 days). Mostly field plumbing.

### Option B: Operator Beads + Project Membership (Medium Change)

Introduce `type:operator` beads that represent human users, with project membership.

**New concepts:**
```
type:operator bead
  Fields:
    slack_user_id: "U12345"
    display_name: "alice"
    projects: ["gasboat", "pihealth"]  -- or via labels
    max_agents: 5                      -- resource limit
    notification_channel: "C-alice"    -- personal channel or DM
```

**Changes required:**
1. All of Option A
2. New `type:operator` bead schema in `init.go`
3. Operator resolution at spawn time -- look up or auto-create operator bead from Slack user
4. Agent count enforcement -- check `max_agents` before spawning
5. Decision routing to operator's preferred channel
6. `/roster` shows operators and their agents (not just all agents)
7. Visibility filtering -- agents shown based on operator's project membership

**What this gives you:**
- Persistent operator identity (survives across sessions)
- Per-operator resource limits
- Per-operator notification preferences
- Project-scoped visibility (operators see only their projects' agents)

**What this doesn't give you:**
- Hard access control (enforcement is at the bridge level, not the beads API)
- Cross-deployment federation

**Effort:** Medium (3-5 days). New bead type, spawn flow changes, notification routing.

### Option C: Full Multi-Tenancy with RBAC (Large Change)

True multi-operator isolation with enforced access control.

**New concepts:**
- Operator beads (from Option B)
- Permission model: `owner`, `collaborator`, `viewer` roles per project
- Beads API scoping: operators can only query/modify their own beads
- Slack workspace mapping: multiple Slack workspaces or channels per operator

**Changes required:**
1. All of Options A and B
2. Beads daemon changes (kbeads) -- RLS or query-time filtering by operator
3. Bearer token per operator (or operator-scoped JWT)
4. Bridge permission checks on every command
5. Agent pod environment includes operator context
6. Config bead scoping by operator (not just role/rig/project)

**What this gives you:**
- True isolation between operators
- Enforced access control at the data layer
- Audit trail with accountability
- Safe for untrusted multi-tenant use

**What this doesn't give you:**
- Simplicity (significant complexity increase)
- Quick iteration (requires kbeads changes)

**Effort:** Large (2-4 weeks). Requires kbeads daemon changes, not just gasboat.

## Recommendation

**Start with Option A, plan for Option B.**

Option A is a quick win that establishes the ownership primitive. It requires no schema changes to kbeads (the `owner` field can be stored in the existing `Fields` map on agent beads). It creates the data foundation that Options B and C build on.

Option B should be the medium-term target. Operator beads give you a place to hang per-operator config (notification preferences, resource limits, project membership) without requiring kbeads-level multi-tenancy.

Option C is premature unless gasboat needs to serve truly untrusted operators. For a team of trusted humans sharing one gasboat, soft ownership (A) + operator beads (B) provide sufficient isolation.

## Specific Implementation Notes for Option A

### 1. Store Owner on Agent Bead

```go
// In SpawnAgent(), add owner to fields map:
fields["owner"] = owner  // Slack user ID, e.g. "U12345ABC"
```

The `Fields` map on beads is already a `map[string]string` -- no schema change needed.

### 2. Populate at Spawn Time

```go
// bot_commands.go -- handleSpawnCommand
b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, "", cmd.UserID)
//                                                               ^^^^^^^^^ new param

// bot_mentions.go -- thread spawn
b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, "", ev.User)
```

### 3. Decision Routing

Currently, decisions are posted to the agent's thread in the main channel. With an owner field:

```go
// When posting a decision notification, also DM the owner
if owner := agentBead.Fields["owner"]; owner != "" {
    b.api.PostMessage(owner, /* DM the decision to the owner */)
}
```

### 4. Soft Kill Protection

```go
// In handleKillCommand:
if agentBead.Fields["owner"] != "" && agentBead.Fields["owner"] != cmd.UserID {
    b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
        "Warning: this agent belongs to <@" + agentBead.Fields["owner"] + ">. Proceeding anyway.")
}
```

## Future Considerations

### Multiple Slack Workspaces
If multiple Slack workspaces need to share one gasboat, the operator identity needs to be workspace-scoped (e.g., `workspace:user_id` or a federation layer).

### Agent Handoff
Operators should be able to transfer ownership: `kd update <agent> --owner=@bob`. This is trivial with the field-based approach.

### Shared Agents
Some agents (CI runners, monitoring) may be "unowned" or "shared." The owner field should be optional -- agents without an owner behave as they do today (global).

### Operator Groups / Teams
For larger deployments, operators may form teams. A `type:team` bead with membership could scope visibility and resource limits at the team level rather than individual.

### Per-Operator Budgets
With operator identity established, usage tracking becomes possible: tokens consumed, compute hours, agents spawned. This is a natural extension of Option B.

# Formula Role/Project Transitions

## Research & Design Document

**Epic:** kd-8rRZvjyajQ
**Date:** 2026-03-08
**Status:** Draft

---

## Phase 1: Cross-Project Claim Behavior & Reusable Patterns

### 1.1 Current Cross-Project Safety Mechanisms

The system has four layers of cross-project protection:

#### Layer 1: Server-Side Label Filtering

`gb ready` and `outputAutoAssign()` query beads with `Labels: []string{"project:" + proj}`, so the server only returns beads matching the agent's project.

**File:** `controller/cmd/gb/prime.go:393-400`

```go
ready, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
    Statuses:   []string{"open"},
    Labels:     []string{"project:" + proj},
    Kinds:      []string{"issue"},
    NoOpenDeps: true,
    Sort:       "priority",
    Limit:      1,
})
```

#### Layer 2: Client-Side Belt-and-Suspenders Check

After the server returns candidates, `outputAutoAssign()` double-checks the project label client-side before claiming.

**File:** `controller/cmd/gb/prime.go:405-418`

```go
task := ready.Beads[0]
hasProjectLabel := false
for _, l := range task.Labels {
    if l == "project:"+proj {
        hasProjectLabel = true
        break
    }
}
if !hasProjectLabel {
    return
}
```

#### Layer 3: Project Context Requirement

Auto-assignment refuses to run without a project context (`BOAT_PROJECT` env var), preventing cross-project assignment in unconfigured agents.

**File:** `controller/cmd/gb/prime.go:369-373`

```go
proj := defaultGBProject()
if proj == "" {
    return // no project context — refuse to auto-assign
}
```

#### Layer 4: Secret Isolation

Per-project secrets must be named `{project}-*` to prevent cross-project secret access. The controller validates this prefix before injecting secrets into pod specs.

**File:** `controller/cmd/controller/podspec.go:420-427`

```go
if !strings.HasPrefix(ps.Secret, spec.Project+"-") {
    slog.Warn("skipping secret with invalid prefix",
        "secret", ps.Secret, "project", spec.Project)
    continue
}
```

### 1.2 Cross-Project Links in JIRA

When syncing JIRA issues, cross-project links (issues in different JIRA projects not imported into beads) are handled gracefully:

- Links where the target is not in the local snapshot are stored as `jira_xlinks` field on the source bead
- Format: `"type:JIRA-KEY,type:JIRA-KEY"` (comma-separated `depType:issueKey` pairs)
- This prevents failed dependency creation while preserving link metadata

**File:** `controller/internal/bridge/jira_poller.go:422-472`

### 1.3 Reusable Patterns for Formula Transitions

| Pattern | Current Location | Reusable For |
|---|---|---|
| **Server-side label filtering** | `prime.go` auto-assign | Formula step project scoping — query steps by `project:X` label |
| **Client-side project verification** | `prime.go` belt-and-suspenders | Formula pour — verify step project matches target before claim |
| **Project context requirement** | `prime.go` project guard | Agent spawn — ensure spawned agents have correct project context |
| **Secret prefix isolation** | `podspec.go` secret validation | Cross-project agents — each project's secrets stay isolated |
| **Cross-project link storage** | `jira_poller.go` xlinks | Formula steps — store cross-project step references as metadata |
| **Mode-from-role mapping** | `podspec.go` `modeForRole()` | Role transitions — determine agent mode from target step's role |
| **Per-project overrides** | `podspec.go` `applyProjectDefaults()` | Cross-project spawning — apply correct project config to new agent |

### 1.4 Key Architectural Observations

1. **Project is a pod-level concept.** An agent pod belongs to exactly one project. Cross-project work requires a new agent pod (with different project config, secrets, git repos).

2. **Role is a config resolution concept.** Roles determine which config beads, advice, and instructions an agent receives. A role switch _within_ the same project could potentially be handled by config re-resolution without spawning a new pod.

3. **The formula instantiation is project-scoped.** `handleFormulaPour()` resolves the project from the Slack channel (`projectFromChannel`) and applies it to all steps uniformly. There is no per-step project override.

4. **Steps inherit the molecule's labels.** Step beads get the molecule's project label plus any per-step labels, but there's no mechanism to _replace_ the project label.

5. **Cross-project step execution requires a new agent.** Since agents are bound to a single project (with project-specific secrets, git repos, and config), a step targeting a different project must be picked up by an agent in that project.

6. **Cross-role step execution may or may not need a new agent.** If the roles are in the same project:
   - Same mode (both crew): agent could potentially handle it with re-injected context
   - Different mode (crew vs job): new pod needed (different restart policy, storage)

---

## Phase 2: Design — Role/Project Transition UX for Formula Steps

### 2.1 New Fields on `formulaStep`

Add `role` and `project` fields to enable cross-role and cross-project steps:

```go
type formulaStep struct {
    ID              string   `json:"id"`
    Title           string   `json:"title"`
    Description     string   `json:"description,omitempty"`
    Type            string   `json:"type,omitempty"`
    Priority        *int     `json:"priority,omitempty"`
    Labels          []string `json:"labels,omitempty"`
    DependsOn       []string `json:"depends_on,omitempty"`
    Assignee        string   `json:"assignee,omitempty"`
    Condition       string   `json:"condition,omitempty"`

    // New fields for role/project transitions:
    Role            string   `json:"role,omitempty"`    // target role for this step (empty = inherit molecule's context)
    Project         string   `json:"project,omitempty"` // target project for this step (empty = inherit molecule's project)
    SuggestNewAgent bool     `json:"suggest_new_agent,omitempty"` // hint that this step should be handled by a new agent
}
```

### 2.2 Instantiation Behavior

When pouring a formula, each step is evaluated for role/project transitions:

#### Case 1: Same project, same role (default)
- Step bead inherits the molecule's project label
- No special handling needed
- Existing agents in the project can claim it

#### Case 2: Same project, different role
- Step bead gets the molecule's project label
- Step bead gets a `role:X` label matching the target role
- If `suggest_new_agent` is true, a comment/field is added noting a new agent may be needed
- The step is visible to any agent in the project, but role-matched agents are preferred

#### Case 3: Different project (with or without role change)
- Step bead gets `project:TARGET_PROJECT` label (NOT the molecule's project)
- Step bead optionally gets a `role:X` label if specified
- A cross-project link is stored on the molecule bead (similar to JIRA xlinks pattern)
- The step is only visible to agents in the target project

#### Case 4: `suggest_new_agent` flag
- Adds a `suggest_new_agent: true` field to the step bead
- The bridge or agent tooling can detect this field and suggest (or auto-) spawning a dedicated agent
- This is a hint, not enforcement — any agent can still claim the step

### 2.3 Formula Step Labels

The `role` and `project` fields on a step translate to labels on the created bead:

```go
// During instantiation:
if s.Project != "" {
    // Override molecule project with step-specific project
    stepLabels = replaceProjectLabel(stepLabels, s.Project)
}
if s.Role != "" {
    stepLabels = append(stepLabels, "role:"+s.Role)
}
```

### 2.4 Variable Substitution

Both `role` and `project` fields support `{{variable}}` substitution:

```json
{
    "steps": [
        {
            "id": "deploy",
            "title": "Deploy to {{environment}}",
            "project": "{{target_project}}",
            "role": "crew",
            "suggest_new_agent": true
        }
    ]
}
```

### 2.5 Display in `/formula show`

When showing formula details, indicate role/project transitions:

```
Steps:
  `research` Research the problem [task]
  `implement` Implement the fix [task] (after: research)
  `deploy` Deploy to production [task] (after: implement) → project:infra, role:crew ⚡new agent
```

The `→ project:X` and `role:Y` annotations appear when a step has explicit role/project fields. The `⚡new agent` indicator appears when `suggest_new_agent` is true.

### 2.6 Cross-Project Molecule Tracking

When a molecule has steps in different projects, the molecule bead stores cross-project references:

```json
{
    "formula_id": "kd-ABC",
    "applied_vars": {"env": "prod"},
    "cross_project_steps": {
        "deploy": {"project": "infra", "bead_id": "kd-XYZ"}
    }
}
```

This allows `kd mol show` to display the full molecule status even though some steps live in different project scopes.

### 2.7 Agent Claiming Behavior

When an agent runs `gb ready`, it sees steps matching its project. Cross-project steps are invisible to agents in the wrong project by design (Layer 1: server-side label filtering).

When an agent claims a step with a `role:X` label that doesn't match its own role:
- The claim succeeds (roles are informational, not enforcement)
- The agent receives a warning: "This step was designed for role X; you are role Y"
- Role-specific advice for the target role is injected (Phase 4)

### 2.8 Automatic Agent Spawning (Future Enhancement)

When `suggest_new_agent` is true and no agent in the target project/role exists:
- The bridge could auto-spawn an agent via `gb spawn --project TARGET --role ROLE --task STEP_BEAD_ID`
- This mirrors the existing thread-agent spawning pattern
- Controlled by a project-level setting (`auto_spawn_formula_agents: true/false`)

---

## Phase 3: Implementation Plan — Role Transition Detection & Agent Spawn Suggestions

### 3.1 Changes to `formulaStep` struct

**File:** `controller/internal/bridge/bot_formula.go`

Add three new fields to `formulaStep`:
- `Role string` — target role
- `Project string` — target project
- `SuggestNewAgent bool` — spawn suggestion flag

### 3.2 Changes to `instantiateFormulaSteps()`

**File:** `controller/internal/bridge/bot_formula.go`

Modify step creation to:
1. Apply variable substitution to `role` and `project` fields
2. Replace molecule project label with step-specific project if set
3. Add `role:X` label if step has explicit role
4. Set `suggest_new_agent` field on step bead if flag is true
5. Track cross-project steps in molecule bead fields

### 3.3 Changes to `/formula show`

**File:** `controller/internal/bridge/bot_formula.go`

Update `handleFormulaShow()` to display role/project annotations on steps.

### 3.4 Cross-project step tracking

Store cross-project step references on the molecule bead's fields so the full molecule state can be queried from any project context.

### 3.5 Tests

- Unit tests for formula instantiation with cross-project/cross-role steps
- Unit tests for label generation with role/project overrides
- Unit tests for cross-project step tracking in molecule fields

---

## Phase 4: Implementation Plan — Role Transition Advice Injection

**Status:** Implemented (gb prime integration)

### 4.1 Detection Point

When `gb prime` runs (SessionStart or PreCompact hook), it checks the agent's
currently claimed bead for `role:X` labels that don't match `BOAT_ROLE`. This
catches the case where the same agent continues with a formula step targeting a
different role.

**Detection location:** `outputAdviceRoleDiff()` in `prime_advice_diff.go`.

### 4.2 Advice Diff Delivery

On role mismatch, the system:
1. Loads the agent's current advice (matching `BOAT_ROLE` subscriptions)
2. Computes a hypothetical advice set with the target role added to subscriptions
3. Diffs the two sets to find added and removed advice items
4. Outputs a "Role Transition" section showing the diff inline in the prime output

This is delivered during every `gb prime` invocation (SessionStart, PreCompact),
so the agent always has up-to-date role-specific context even without spawning a
new agent.

### 4.3 Implementation Details

**Files:**
- `controller/internal/advice/diff.go` — `DiffAdviceForRole()` computes added/removed advice
- `controller/cmd/gb/prime_advice_diff.go` — `outputAdviceRoleDiff()` detects role mismatch on claimed bead and renders the diff
- Integration in `prime.go` and `prime_shared.go` — called after `outputAdvice()`

**Output format:**
```
## Role Transition: crew → lead

Your claimed task has `role:lead` — the following advice changes apply:

### New advice for role:lead (2 items)

**[Role: lead]** Review all PRs before merge
  Lead agents must review PRs from crew agents before allowing merge.

### No longer applicable (1 item)

- ~~**[Role: crew]** Always request PR review from a lead~~
```

### 4.4 Config Bead Context

For deeper role transitions, the agent could temporarily extend its subscriptions to include the target role. This would be a runtime config re-resolution triggered by step claim.

**Limitation:** This does not change the agent's pod spec (mode, resources, secrets). For steps requiring a different mode or project-specific secrets, a new agent is still required.

---

## Summary: Transition Decision Matrix

| Step Project | Step Role | Agent Project | Agent Role | Action |
|---|---|---|---|---|
| same | same | — | — | Normal claim, no transition |
| same | different | — | — | Claim + inject target role advice |
| different | any | — | — | Must be claimed by agent in target project |
| any | any | — | — | If `suggest_new_agent`: spawn dedicated agent |

## Files Modified (Full Implementation)

| File | Changes |
|---|---|
| `controller/internal/bridge/bot_formula.go` | Add role/project/suggest_new_agent to formulaStep; update instantiation and show |
| `controller/internal/bridge/bot_formula_test.go` | Tests for cross-project/role step instantiation |
| `controller/internal/bridge/claimed.go` | Detect role mismatch on step claim, inject advice |
| `controller/cmd/gb/hook.go` | Optional: role mismatch warning in claim reminder |
| `docs/formula-role-project-transitions.md` | This document |

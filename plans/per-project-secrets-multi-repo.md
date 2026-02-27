# Per-Project Secrets & Multi-Repo Support

**Status:** Implemented (2026-02-27)
**Commit:** `feat(controller): add per-project secrets and multi-repo support`

## Context

All agent pods currently share the same global secrets (claude-oauth-token, git-credentials, gh-cli-token, gitlab-token, rwx-access-token). Projects can override image, storage class, and service account, but NOT secrets. Reference repos (kbeads, gasboat, pihealth-monorepo) are hardcoded in the entrypoint. This plan adds:

1. **Per-project secrets** -- arbitrary `{env, secret, key}` mappings on project beads, merged with globals
2. **Multi-repo projects** -- projects declare primary + reference repos via a `repos` JSON field
3. **Agent advice bead** -- instructions for agents on managing project secrets

---

## 1. Schema: `type:project` Fields

**File:** `controller/internal/bridge/init.go`

Three new fields added to the `type:project` config:

```go
{Name: "service_account", Type: "string"},  // already used, never declared
{Name: "secrets", Type: "json"},             // per-project secret overrides
{Name: "repos", Type: "json"},              // multi-repo definitions
```

### `secrets` format (array of objects)

```json
[
  {"env": "GITHUB_TOKEN", "secret": "pihealth-gh-token", "key": "token"},
  {"env": "JIRA_API_TOKEN", "secret": "pihealth-jira", "key": "api-token"},
  {"env": "CUSTOM_DB_URL", "secret": "pihealth-db", "key": "url"}
]
```

- Entries override globals when `env` matches an existing global env var name
- New env names are additive (injected alongside globals)
- Git-related overrides (`GIT_TOKEN`, `GIT_USERNAME`, `GITLAB_TOKEN`) also update init container credentials

### `repos` format (array of objects)

```json
[
  {"url": "https://github.com/org/main-repo.git", "branch": "main", "role": "primary"},
  {"url": "https://github.com/org/shared-lib.git", "role": "reference", "name": "shared-lib"}
]
```

- `role`: "primary" (cloned to workspace work dir) or "reference" (cloned to workspace/repos/{name})
- `branch`: defaults to "main"
- `name`: defaults to last path segment of URL (stripped of .git)
- Backward compat: when `repos` is empty, `git_url` + `default_branch` are used as the single primary

---

## 2. Data Model Changes

### `beadsapi/client.go` -- SecretEntry + RepoEntry + ProjectInfo

```go
type SecretEntry struct {
    Env    string `json:"env"`    // env var name in the pod
    Secret string `json:"secret"` // K8s Secret name
    Key    string `json:"key"`    // key within the Secret
}

type RepoEntry struct {
    URL    string `json:"url"`
    Branch string `json:"branch,omitempty"`
    Role   string `json:"role,omitempty"`  // "primary" or "reference"
    Name   string `json:"name,omitempty"`
}
```

`Secrets []SecretEntry` and `Repos []RepoEntry` added to `ProjectInfo`. Parsed from `fields["secrets"]` and `fields["repos"]` via `json.Unmarshal`.

### `config/config.go` -- ProjectCacheEntry

Added to `ProjectCacheEntry`:

```go
Secrets []beadsapi.SecretEntry  // per-project secret overrides
Repos   []beadsapi.RepoEntry    // multi-repo definitions
```

### `podmanager/manager.go` -- AgentPodSpec

Added `RepoRef` type and `ReferenceRepos` field to `AgentPodSpec`:

```go
type RepoRef struct {
    URL    string
    Branch string
    Name   string // directory name under workspace/repos/
}
```

### `cmd/controller/main.go` -- refreshProjectCache

`refreshProjectCache()` populates new `Secrets` and `Repos` fields from `ProjectInfo`.

---

## 3. Per-Project Secret Resolution

**File:** `controller/cmd/controller/podspec.go`

### Core mechanism

In `applyCommonConfig()`, after wiring global secrets, per-project secret overrides are applied:

```go
if entry, ok := cfg.ProjectCache[spec.Project]; ok {
    for _, ps := range entry.Secrets {
        src := podmanager.SecretEnvSource{
            EnvName: ps.Env, SecretName: ps.Secret, SecretKey: ps.Key,
        }
        overrideOrAppendSecretEnv(&spec.SecretEnv, src)

        switch ps.Env {
        case "GIT_TOKEN", "GIT_USERNAME":
            spec.GitCredentialsSecret = ps.Secret
        case "GITLAB_TOKEN":
            spec.GitlabTokenSecret = ps.Secret
        }
    }
}
```

### Helper function

```go
func overrideOrAppendSecretEnv(envs *[]podmanager.SecretEnvSource, src podmanager.SecretEnvSource) {
    for i, e := range *envs {
        if e.EnvName == src.EnvName {
            (*envs)[i] = src
            return
        }
    }
    *envs = append(*envs, src)
}
```

### Order of operations

1. Global secrets wired as today
2. Per-bead metadata overrides
3. Per-project secrets overlay (after globals, so project overrides win)

---

## 4. Multi-Repo Support

### 4A. Repo resolution in `applyCommonConfig()`

**File:** `controller/cmd/controller/podspec.go`

Replaces the previous single-repo wiring with multi-repo-aware logic:

- When `entry.Repos` is non-empty: iterates entries, wires `role=primary` to `spec.GitURL` and others to `spec.ReferenceRepos`
- When `entry.Repos` is empty: legacy single-repo fallback using `git_url` + `default_branch`
- Builds `BOAT_REFERENCE_REPOS` env var in format `name=url:branch,name2=url2:branch2`

### 4B. Init container multi-repo cloning

**File:** `controller/internal/podmanager/initclone.go`

- Guard updated: `if spec.GitURL == "" && len(spec.ReferenceRepos) == 0 { return nil }`
- Primary repo clone is conditional on `spec.GitURL != ""`
- Reference repos cloned after primary, each to `{workspace}/{project}/repos/{name}`

### 4C. Helper: `repoNameFromURL`

```go
func repoNameFromURL(rawURL string) string {
    u := strings.TrimSuffix(rawURL, ".git")
    parts := strings.Split(u, "/")
    if len(parts) > 0 { return parts[len(parts)-1] }
    return "repo"
}
```

---

## 5. Entrypoint Changes

**File:** `images/agent/entrypoint.sh`

Removed hardcoded reference repo clones. Replaced with dynamic cloning from `BOAT_REFERENCE_REPOS`:

```bash
if [ -n "${BOAT_REFERENCE_REPOS:-}" ]; then
    IFS=',' read -ra REPO_ENTRIES <<< "${BOAT_REFERENCE_REPOS}"
    for entry in "${REPO_ENTRIES[@]}"; do
        repo_name="${entry%%=*}"
        repo_url="${entry#*=}"; repo_url="${repo_url%%:*}"
        _clone_repo "${repo_url}" "${REPOS_DIR}/${repo_name}"
    done
fi
```

---

## 6. Advice Bead

Create an advice bead (via `kd create`) that teaches agents how to manage per-project secrets. Content covers:

- What per-project secrets are and how they work
- How to check a project's current secrets: `kd show <project-id>`
- How to create a K8s secret (ExternalSecret in helm chart or kubectl)
- How to wire it to a project: `kd update <project-id> -f 'secrets=[...]'`
- When changes take effect (next pod creation after project cache refresh ~60s)
- How to add reference repos to a project

This is operational content, not code -- created after deployment.

---

## 7. Files Changed (Summary)

| File | Change |
|------|--------|
| `controller/internal/bridge/init.go` | Add `service_account`, `secrets`, `repos` to type:project schema |
| `controller/internal/beadsapi/client.go` | Add `SecretEntry`, `RepoEntry` types; parse new fields in `ListProjectBeads()` |
| `controller/internal/config/config.go` | Add `Secrets`, `Repos` to `ProjectCacheEntry` |
| `controller/internal/podmanager/manager.go` | Add `RepoRef`, `ReferenceRepos` to `AgentPodSpec`; extract init clone to `initclone.go` |
| `controller/internal/podmanager/initclone.go` | Extracted `buildInitCloneContainer()` with reference repo support |
| `controller/cmd/controller/podspec.go` | Add `overrideOrAppendSecretEnv`/`repoNameFromURL` helpers; project secret overlay; multi-repo wiring + `BOAT_REFERENCE_REPOS` |
| `controller/cmd/controller/main.go` | Populate new cache fields in `refreshProjectCache()` |
| `images/agent/entrypoint.sh` | Remove hardcoded repos; add `BOAT_REFERENCE_REPOS` parsing |
| `controller/cmd/controller/podspec_test.go` | 10 tests for secrets/repos logic |
| `controller/internal/beadsapi/client_list_test.go` | 2 tests for parsing secrets/repos JSON |

---

## 8. Testing

### Test coverage

- `TestOverrideOrAppendSecretEnv_OverridesExisting` -- matching env name replaces
- `TestOverrideOrAppendSecretEnv_AppendsNew` -- new env name is additive
- `TestRepoNameFromURL` -- URL parsing with/without .git, nested paths
- `TestApplyCommonConfig_PerProjectSecretOverride` -- project overrides GITHUB_TOKEN
- `TestApplyCommonConfig_PerProjectSecretAdditive` -- project adds JIRA_API_TOKEN alongside globals
- `TestApplyCommonConfig_GitCredentialOverride` -- GIT_TOKEN override updates init container creds
- `TestApplyCommonConfig_NoProjectOverrides` -- empty secrets uses globals (backward compat)
- `TestApplyCommonConfig_MultiRepo` -- primary + reference repos wired correctly
- `TestApplyCommonConfig_LegacySingleRepo` -- repos empty, git_url set works as before
- `TestApplyCommonConfig_ReferenceOnlyRepos` -- no primary, only references
- `TestListProjectBeads_ParsesSecretsAndRepos` -- JSON field parsing
- `TestListProjectBeads_EmptySecretsAndRepos` -- no secrets/repos fields

### Verification

1. `make build && make build-bridge` -- compiles
2. `make test` -- all tests pass
3. `quench check` -- no new failures (pre-existing bot.go issue only)
4. `helm lint helm/gasboat/` -- chart lints
5. Manual: create project bead with secrets + repos, verify pod spec has correct env vars and init script

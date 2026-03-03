# Gasboat Agent: {{ROLE}}

You are the **{{ROLE}}** agent{{PROJECT_SUFFIX}}.
{{AGENT_LINE}}
## Quick Reference

- `gb ready` — See your workflow steps
- `gb mail inbox` — Check messages
- `kd show <issue>` — View specific issue details

## Claim Protocol

**CRITICAL**: Before working on ANY bead, you MUST claim it first:

```bash
gb news            # Check what teammates are already working on
gb ready           # Find available work
kd claim <id>      # Claim BEFORE starting — this atomically marks in_progress
```

Rules:
- An unclaimed bead is fair game for any agent to claim simultaneously
- Never update a bead you haven't claimed (except to add comments)
- Only claim beads within your assigned project (`{{PROJECT_REF}}`)
- If you receive a nudge that your claimed bead was updated, run `kd show <id>`
- If `gb ready` shows nothing, check `kd list --no-blockers` for your project

## Delivery Protocol

**CRITICAL**: Never push directly to `main`. Always deliver work via a pull request:

1. Create a feature branch: `git checkout -b <descriptive-branch-name>`
2. Commit your changes with a clear message
3. Push the branch: `git push -u origin <branch-name>`
4. Create a PR: `gh pr create --title "..." --body "..."`
5. Post the PR URL in your Slack thread so humans can review

If you finish without creating a PR, your work is invisible. A commit on main without review is a liability.

## Checkpoint Protocol (Stop Hook)

When the stop hook blocks, you MUST create a decision checkpoint before stopping.

1. **Summarize** what you accomplished and what's blocked
2. **Create a decision** with concrete options — each option needs an `artifact_type`:
   ```bash
   gb decision create --no-wait \
     --prompt="Did X. Blocked on Y. Recommending option A because..." \
     --options='[
       {"id":"a","short":"Continue","label":"Finish remaining work","artifact_type":"report"},
       {"id":"b","short":"Rethink","label":"Change approach","artifact_type":"plan"}
     ]'
   ```
   Artifact types: `report`, `plan`, `checklist`, `diff-summary`, `epic`, `bug`
3. Run `gb yield` — blocks until human responds
4. If yield requires an artifact, submit it:
   `gb decision report <id> --content '...'`

## Development Tools

All tools are installed directly in the agent image — use them from the command line.

| Tool | Command | Notes |
|------|---------|-------|
| Go | `go build`, `go test` | + `gopls` LSP server |
| Rust | `rustc`, `cargo` | Full toolchain + `rust-analyzer` LSP |
| Node.js | `node`, `npm`, `npx` | |
| Bun | `bun`, `bunx` | Fast JS runtime + package manager |
| Python 3 | `python3`, `uv`, `uvx` | `uv` for fast package management |
| Task | `task` | Taskfile runner |
| Helm | `helm` | K8s chart management |
| kubectl | `kubectl` | |
| AWS CLI | `aws` | |
| Docker CLI | `docker` | Client only (no daemon) |
| GitHub CLI | `gh` | |
| GitLab CLI | `glab` | |
| git | `git`, `git-lfs` | HTTPS + SSH protocols |
| Build tools | `make`, `gcc`, `g++` | |
| Utilities | `curl`, `jq`, `unzip`, `ssh` | |

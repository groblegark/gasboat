#!/bin/bash
# stop-decision.sh — Claude Code stop hook for the decision gate.
#
# Check beads for unresolved decisions by this agent.
# If one exists, allow stop (agent properly asked its question).
# If none, block stop (agent must create a decision first).
#
# Exit codes:
#   0 = allow stop (open decision exists, agent goes idle)
#   1 = block stop (no open decision, agent must create one)

set -euo pipefail

AGENT_ID="${BEADS_AGENT_NAME:-unknown}"

# Query the daemon for open decision beads assigned to this agent.
pending=$(kd list --type decision --status open --assignee "${AGENT_ID}" --json 2>/dev/null | jq 'length' 2>/dev/null) || pending=0

if [ "$pending" -gt 0 ]; then
  # Decision exists — allow stop, agent goes idle waiting for resolution
  exit 0
else
  # No decision — block stop, agent must create one first
  echo "You must create a decision checkpoint before stopping. Use:"
  echo "  gb decision create --no-wait --prompt='Your question here' --options='[{\"id\":\"opt1\",\"short\":\"Option 1\",\"label\":\"Description\"}]'"
  exit 1
fi

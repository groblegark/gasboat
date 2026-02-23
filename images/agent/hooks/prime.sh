#!/bin/bash
# prime.sh — SessionStart hook that outputs role-specific priming context.
#
# Renders the gasboat context template for this agent's role via
# `bd context <role>`, then surfaces any hooked work (hook_bead) and
# instructions from the agent's own bead.
#
# Env:
#   BOAT_ROLE    — agent role (captain, crew, job)
#   BOAT_AGENT   — agent name
#   BOAT_PROJECT — project name
#
# Always exits 0 so hook failures don't block Claude.

set -euo pipefail

ROLE="${BOAT_ROLE:-crew}"
AGENT="${BOAT_AGENT:-}"
PROJECT="${BOAT_PROJECT:-}"

# Render the role's context dashboard from the daemon.
# Jobs have no context config — their entire context is the hook_bead below.
if [ "${ROLE}" != "job" ]; then
    bd context "${ROLE}" 2>/dev/null || true
fi

# Look up this agent's bead to surface hooked work and instructions.
# Any role can have a hook_bead (crew get assigned work too).
if [ -n "${AGENT}" ]; then
    agent_json=$(bd list --type agent --status open,in_progress,blocked --json 2>/dev/null) || exit 0

    bead=$(echo "${agent_json}" | jq --arg agent "${AGENT}" --arg project "${PROJECT}" \
        '[.[] | select(.fields.agent == $agent and .fields.project == $project)] | first // empty' 2>/dev/null) || exit 0

    if [ -z "${bead}" ] || [ "${bead}" = "null" ]; then
        exit 0
    fi

    hook_bead=$(echo "${bead}" | jq -r '.fields.hook_bead // empty' 2>/dev/null) || true
    instructions=$(echo "${bead}" | jq -r '.fields.instructions // empty' 2>/dev/null) || true

    if [ -n "${hook_bead}" ]; then
        echo ""
        echo "## Assigned Work"
        echo ""
        bd show "${hook_bead}" 2>/dev/null || echo "Bead ${hook_bead} not found"
    fi

    if [ -n "${instructions}" ]; then
        echo ""
        echo "## Instructions"
        echo ""
        echo "${instructions}"
    fi
fi

exit 0

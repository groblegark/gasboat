#!/bin/bash
# drain-queue.sh — Drain the inject queue and output content to Claude context.
#
# Called by PostToolUse hook. Reads each JSONL entry's "content" field,
# outputs it to stdout (which Claude Code captures as hook output), then
# removes the queue file.
#
# Flags:
#   --quiet  Suppress "no queue file" messages (used by PostToolUse)
#
# Always exits 0 so hook failures don't block Claude.

set -euo pipefail

QUEUE_FILE="/tmp/inject-queue.jsonl"
QUIET=false

for arg in "$@"; do
    case "${arg}" in
        --quiet) QUIET=true ;;
    esac
done

if [ ! -f "${QUEUE_FILE}" ]; then
    if [ "${QUIET}" = "false" ]; then
        echo "[drain-queue] No queue file" >&2
    fi
    exit 0
fi

# Read and output each entry's content.
while IFS= read -r line; do
    [ -z "${line}" ] && continue
    content=$(echo "${line}" | jq -r '.content // empty' 2>/dev/null) || continue
    if [ -n "${content}" ]; then
        echo "${content}"
    fi
done < "${QUEUE_FILE}"

# Remove the queue file after draining.
rm -f "${QUEUE_FILE}"

exit 0

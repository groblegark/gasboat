#!/bin/bash
# RTK token savings report — runs on Stop hook to capture session metrics.
# Outputs RTK gain stats to stderr (pod logs) for observability.
# Only runs when RTK is enabled and the rtk binary is available.

if [ "${RTK_ENABLED:-}" != "true" ] && [ "${RTK_ENABLED:-}" != "1" ]; then
    exit 0
fi

if ! command -v rtk &>/dev/null; then
    exit 0
fi

# Capture JSON stats for structured logging.
RTK_STATS=$(rtk gain --format json 2>/dev/null) || exit 0

if [ -z "$RTK_STATS" ] || [ "$RTK_STATS" = "null" ]; then
    exit 0
fi

# Log to stderr so it appears in pod logs (observability/Langfuse).
echo "[rtk] session token savings: ${RTK_STATS}" >&2

# Output summary for Claude context.
echo "RTK session stats: ${RTK_STATS}"

exit 0

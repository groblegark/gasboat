#!/bin/bash
# Stop hook gate: calls gb bus emit and handles block by injecting
# checkpoint protocol instructions so the agent knows what to do.
#
# Exit codes (Claude Code hook protocol):
#   0 = allow (agent may stop)
#   2 = block (agent must continue and create a decision checkpoint)

set -uo pipefail

# Read stdin (Claude Code hook JSON) and forward to gb bus emit.
# stderr flows through so Claude Code sees the block reason.
_stdin=$(cat)
echo "$_stdin" | gb bus emit --hook=Stop
_rc=$?

if [ $_rc -eq 2 ]; then
    # Gate blocked — inject checkpoint instructions into the conversation via stdout.
    cat <<'CHECKPOINT'
<system-reminder>
STOP BLOCKED — decision gate unsatisfied. You MUST create a decision checkpoint before you can stop.

Follow these steps:
1. Review what you accomplished this session
2. Create a decision offering next steps:
   gb decision create --no-wait \
     --prompt="<what you did, blockers, and why these options>" \
     --options='[{"id":"opt1","short":"Option 1","label":"Description of option 1"},{"id":"opt2","short":"Option 2","label":"Description of option 2"}]'
3. Run: gb yield
   This blocks until the human responds. When it returns, act on the response.
</system-reminder>
CHECKPOINT
    exit 2
fi

exit $_rc

package main

// prime_shared.go contains the outputPrimeForHook function shared by
// bus_emit.go (SessionStart injection) and hook.go (hook prime command).
//
// The full `gb prime` command (bd-fzkui.8) will add the complete prime
// output with workflow context, advice, jacks, roster, and auto-assign.
// For now, this is a minimal stub that outputs an empty system-reminder.

import (
	"fmt"
	"io"
)

// outputPrimeForHook generates prime output wrapped in a system-reminder tag.
// This is called by bus emit on SessionStart and by hook prime.
func outputPrimeForHook(w io.Writer, agentID string) {
	// Stub: full implementation in bd-fzkui.8 (prime command port).
	// For now, output a minimal system-reminder so the hook protocol works.
	if agentID != "" {
		fmt.Fprintf(w, "<system-reminder>\nAgent: %s\n</system-reminder>\n", agentID)
	}
}

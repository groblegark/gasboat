package main

// prime_shared.go contains the outputPrimeForHook function shared by
// bus_emit.go (SessionStart injection) and hook.go (hook prime command).

import (
	"fmt"
	"io"
)

// outputPrimeForHook generates prime output wrapped in a system-reminder tag.
// This is called by bus emit on SessionStart and by hook prime.
func outputPrimeForHook(w io.Writer, agentID string) {
	fmt.Fprintln(w, "<system-reminder>")
	outputWorkflowContext(w)
	if agentID != "" {
		outputAdvice(w, agentID)
	}
	outputJackSection(w)
	outputRosterSection(w, agentID)
	if agentID != "" {
		outputAutoAssign(w, agentID)
	}
	fmt.Fprintln(w, "</system-reminder>")
}
